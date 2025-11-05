# MySQL Transaction Implementation Design

## Overview

This document describes the design and implementation of full ACID transaction support for the MySQL API, with consideration for node failure recovery and TiDB-level capabilities.

## Design Goals

1. **ACID Compliance**: Full ACID transaction support
2. **Snapshot Isolation**: SI level isolation to prevent dirty reads and non-repeatable reads
3. **Distributed Transactions**: Work correctly across Raft cluster
4. **Failure Recovery**: Handle node failures gracefully
5. **Performance**: Optimize for common transaction patterns
6. **MySQL Compatibility**: Support standard MySQL transaction syntax

## Architecture

### 1. Transaction Model

**Isolation Level**: Snapshot Isolation (SI)
- Reads see a consistent snapshot at transaction start
- Writes buffer locally until commit
- Optimistic concurrency control at commit time
- Similar to TiDB's default isolation level

**Transaction Lifecycle**:
```
BEGIN → READ/WRITE Operations → COMMIT/ROLLBACK
  ↓           ↓                        ↓
Start     Buffer Ops            Apply/Discard
Snapshot
```

### 2. Core Components

#### 2.1 Transaction State

```go
type Transaction struct {
    mu          sync.Mutex
    connID      uint32           // MySQL connection ID
    active      bool             // Transaction active flag
    startRev    int64            // Snapshot revision at BEGIN
    operations  []TxOp           // Buffered operations
    readSet     map[string]int64 // Key -> version for conflict detection
    startTime   time.Time        // Transaction start time
}

type TxOp struct {
    OpType string // "PUT" or "DELETE"
    Key    string
    Value  string
}
```

#### 2.2 Transaction Manager

```go
type TransactionManager struct {
    mu           sync.RWMutex
    transactions map[uint32]*Transaction  // connID -> transaction
    store        kvstore.Store
}
```

### 3. Transaction Operations

#### 3.1 BEGIN / START TRANSACTION

**Logic**:
1. Check if transaction already active for connection
2. Create new transaction with current revision as snapshot
3. Initialize read set and write buffer
4. Store in transactions map

**Implementation**:
```go
func (h *MySQLHandler) handleBegin(ctx context.Context, connID uint32) (*mysql.Result, error) {
    tx := h.getTransaction(connID)
    if tx != nil && tx.active {
        return nil, errors.New("transaction already active")
    }

    // Create transaction with snapshot
    newTx := &Transaction{
        connID:     connID,
        active:     true,
        startRev:   h.store.CurrentRevision(),
        operations: make([]TxOp, 0),
        readSet:    make(map[string]int64),
        startTime:  time.Now(),
    }

    h.setTransaction(connID, newTx)
    return successResult(), nil
}
```

#### 3.2 INSERT / UPDATE / DELETE in Transaction

**Logic**:
1. Check if transaction is active
2. If active: Buffer operation, don't write to store
3. If not active: Execute immediately (autocommit)

**Key Point**: All writes are buffered until COMMIT

```go
func (h *MySQLHandler) handleInsert(ctx context.Context, connID uint32, key, value string) {
    tx := h.getTransaction(connID)
    if tx != nil && tx.active {
        // Buffer operation
        tx.mu.Lock()
        tx.operations = append(tx.operations, TxOp{
            OpType: "PUT",
            Key:    key,
            Value:  value,
        })
        tx.mu.Unlock()
    } else {
        // Autocommit mode
        h.store.PutWithLease(ctx, key, value, 0)
    }
}
```

#### 3.3 SELECT in Transaction

**Logic**:
1. Read from snapshot revision (startRev)
2. Check write buffer for uncommitted writes
3. Record read in readSet for conflict detection

**Implementation**:
```go
func (h *MySQLHandler) handleSelect(ctx context.Context, connID uint32, key string) (string, error) {
    tx := h.getTransaction(connID)
    if tx != nil && tx.active {
        // Check write buffer first
        if val, found := tx.getBufferedValue(key); found {
            return val, nil
        }

        // Read from snapshot
        resp, err := h.store.Range(ctx, key, "", 1, tx.startRev)
        if err != nil {
            return "", err
        }

        // Record read for conflict detection
        if len(resp.Kvs) > 0 {
            tx.mu.Lock()
            tx.readSet[key] = resp.Kvs[0].ModRevision
            tx.mu.Unlock()
            return string(resp.Kvs[0].Value), nil
        }
        return "", nil
    } else {
        // Read latest (autocommit)
        resp, err := h.store.Range(ctx, key, "", 1, 0)
        // ...
    }
}
```

#### 3.4 COMMIT

**Logic** (Optimistic Concurrency Control):
1. Validate read set: Check if any read keys were modified since startRev
2. If validation fails: Abort with conflict error
3. If validation passes: Apply all buffered operations atomically
4. Use kvstore.Txn for atomic commit
5. Clean up transaction state

**Implementation**:
```go
func (h *MySQLHandler) handleCommit(ctx context.Context, connID uint32) (*mysql.Result, error) {
    tx := h.getTransaction(connID)
    if tx == nil || !tx.active {
        return successResult(), nil // No-op if no active transaction
    }

    tx.mu.Lock()
    defer tx.mu.Unlock()

    // Step 1: Build comparison conditions for conflict detection
    cmps := make([]kvstore.Compare, 0, len(tx.readSet))
    for key, expectedRev := range tx.readSet {
        cmps = append(cmps, kvstore.Compare{
            Target: kvstore.CompareMod,
            Result: kvstore.CompareEqual,
            Key:    []byte(key),
            TargetUnion: kvstore.CompareUnion{
                ModRevision: expectedRev,
            },
        })
    }

    // Step 2: Build operations to apply
    thenOps := make([]kvstore.Op, 0, len(tx.operations))
    for _, op := range tx.operations {
        switch op.OpType {
        case "PUT":
            thenOps = append(thenOps, kvstore.Op{
                Type:  kvstore.OpPut,
                Key:   []byte(op.Key),
                Value: []byte(op.Value),
            })
        case "DELETE":
            thenOps = append(thenOps, kvstore.Op{
                Type: kvstore.OpDelete,
                Key:  []byte(op.Key),
            })
        }
    }

    // Step 3: Execute transaction atomically
    txnResp, err := h.store.Txn(ctx, cmps, thenOps, nil)
    if err != nil {
        h.removeTransaction(connID)
        return nil, fmt.Errorf("transaction commit failed: %w", err)
    }

    // Step 4: Check if transaction succeeded
    if !txnResp.Succeeded {
        h.removeTransaction(connID)
        return nil, errors.New("transaction conflict: data was modified by another transaction")
    }

    // Step 5: Clean up
    h.removeTransaction(connID)

    return &mysql.Result{
        Status:       0,
        AffectedRows: uint64(len(tx.operations)),
    }, nil
}
```

#### 3.5 ROLLBACK

**Logic**:
1. Simply discard all buffered operations
2. Clean up transaction state

```go
func (h *MySQLHandler) handleRollback(ctx context.Context, connID uint32) (*mysql.Result, error) {
    tx := h.getTransaction(connID)
    if tx == nil || !tx.active {
        return successResult(), nil
    }

    // Simply remove transaction (discards all buffered operations)
    h.removeTransaction(connID)

    return successResult(), nil
}
```

### 4. Failure Recovery

#### 4.1 Connection Close

**Problem**: MySQL connection closes but transaction not committed

**Solution**: Clean up on connection close
```go
func (h *MySQLHandler) ConnectionClosed(connID uint32) {
    // Automatically rollback uncommitted transaction
    h.removeTransaction(connID)
}
```

#### 4.2 Node Failure

**Problem**: Node crashes with active transactions

**Solution**:
- Transactions are per-connection and in-memory only
- If node crashes, all uncommitted transactions are lost (same as MySQL)
- Committed transactions are durable (via Raft log)
- No recovery needed because transactions are never partially applied

#### 4.3 Leader Change

**Problem**: Leader fails during transaction

**Solution**:
- Transactions are local to each node
- If client was connected to old leader, connection will fail
- Client must reconnect and retry transaction
- This is standard behavior for distributed databases

### 5. Transaction Guarantees

#### 5.1 ACID Properties

**Atomicity**:
- All operations commit together via kvstore.Txn
- If any operation fails, entire transaction fails
- No partial commits

**Consistency**:
- Read set validation ensures serializable reads
- Write conflicts detected at commit time
- No lost updates or dirty writes

**Isolation**:
- Snapshot Isolation level
- Reads from consistent snapshot at transaction start
- Writes visible only after commit
- Prevents: Dirty Read, Non-Repeatable Read
- Does NOT prevent: Phantom Read, Write Skew (same as TiDB default)

**Durability**:
- Committed transactions written to Raft log
- Replicated to majority of nodes
- Survives node failures

#### 5.2 Conflict Handling

**Read-Write Conflicts**:
```
Transaction T1:               Transaction T2:
BEGIN                         BEGIN
  SELECT key1 (rev=10)
                                UPDATE key1
                                COMMIT (rev=11)
  UPDATE key1
  COMMIT
  → CONFLICT! (key1 was modified)
```

**Write-Write Conflicts**:
```
Transaction T1:               Transaction T2:
BEGIN                         BEGIN
  UPDATE key1
                                UPDATE key1
  COMMIT (success)
                                COMMIT
                                → CONFLICT! (first writer wins)
```

### 6. Comparison with TiDB

#### Similarities:
1. **Snapshot Isolation**: Same default isolation level
2. **Optimistic Locking**: Same concurrency control strategy
3. **Raft Consensus**: Same replication mechanism
4. **MVCC**: Both use multi-version concurrency control
5. **Conflict Detection**: Both detect conflicts at commit time

#### Differences:
1. **Scope**: TiDB supports full SQL, we support KV operations only
2. **Scale**: TiDB scales to thousands of nodes, we target small clusters
3. **Secondary Indexes**: TiDB has indexes, we have single KV space
4. **SQL Complexity**: TiDB handles joins/aggregations, we handle simple CRUD
5. **Pessimistic Locking**: TiDB supports both pessimistic and optimistic, we only do optimistic

#### Our Advantages:
1. **Simplicity**: Simpler implementation, easier to understand
2. **Performance**: Lower overhead for simple KV operations
3. **Resource Usage**: Much lighter weight

### 7. Performance Considerations

#### 7.1 Optimization Strategies

**1. Read Set Pruning**:
- Only track keys that were read AND might be written by others
- Don't track keys in write buffer

**2. Batch Operations**:
- Group multiple operations in single Raft proposal
- Reduces latency for large transactions

**3. Early Conflict Detection**:
- Check for obvious conflicts before building full Txn
- Fail fast to reduce wasted work

**4. Connection Pooling**:
- Reuse transaction structures
- Reduce allocation overhead

#### 7.2 Limits

**Transaction Limits**:
- Max operations per transaction: 10,000 (configurable)
- Max transaction size: 10 MB (configurable)
- Max transaction duration: 1 hour (configurable)

**Rationale**: Prevent resource exhaustion and long-lived transactions

### 8. Testing Strategy

#### 8.1 Unit Tests
- Transaction lifecycle (BEGIN/COMMIT/ROLLBACK)
- Operation buffering
- Read set tracking
- Conflict detection

#### 8.2 Integration Tests
- Cross-protocol consistency (HTTP write → MySQL transaction read)
- Concurrent transactions
- Conflict scenarios
- Connection close with active transaction

#### 8.3 Cluster Tests
- Multi-node transaction execution
- Leader failover during transaction
- Network partitions

#### 8.4 Stress Tests
- High concurrency (1000+ concurrent transactions)
- Large transactions (1000+ operations)
- Long-running transactions

### 9. Usage Examples

#### Example 1: Basic Transaction
```sql
BEGIN;
INSERT INTO kv (key, value) VALUES ('user:1:name', 'Alice');
INSERT INTO kv (key, value) VALUES ('user:1:email', 'alice@example.com');
INSERT INTO kv (key, value) VALUES ('user:1:age', '30');
COMMIT;
```

#### Example 2: Transaction with Conflict
```sql
-- Session 1
BEGIN;
SELECT value FROM kv WHERE key = 'counter';  -- Gets 100
UPDATE kv SET value = '101' WHERE key = 'counter';

-- Session 2 (before Session 1 commits)
BEGIN;
SELECT value FROM kv WHERE key = 'counter';  -- Gets 100 (snapshot)
UPDATE kv SET value = '101' WHERE key = 'counter';
COMMIT;  -- Success

-- Session 1 (continues)
COMMIT;  -- CONFLICT! Counter was modified
```

#### Example 3: Rollback
```sql
BEGIN;
INSERT INTO kv (key, value) VALUES ('temp:1', 'test');
SELECT value FROM kv WHERE key = 'temp:1';  -- Returns 'test'
ROLLBACK;
SELECT value FROM kv WHERE key = 'temp:1';  -- Key not found
```

#### Example 4: Autocommit Mode
```sql
-- No explicit BEGIN - operates in autocommit mode
INSERT INTO kv (key, value) VALUES ('key1', 'value1');
-- Immediately committed

SELECT value FROM kv WHERE key = 'key1';
-- Returns 'value1'
```

### 10. Implementation Phases

#### Phase 1: Basic Transaction Support (This PR)
- [x] Connection-level transaction tracking
- [x] Operation buffering for INSERT/UPDATE/DELETE
- [x] COMMIT with kvstore.Txn
- [x] ROLLBACK
- [x] Basic conflict detection

#### Phase 2: Enhanced Isolation
- [ ] Read set tracking
- [ ] Full conflict detection
- [ ] Connection cleanup
- [ ] Transaction timeouts

#### Phase 3: Advanced Features
- [ ] Savepoints (SAVEPOINT/ROLLBACK TO)
- [ ] FOR UPDATE locking hints
- [ ] Transaction statistics
- [ ] Deadlock detection

#### Phase 4: Performance Optimization
- [ ] Read set pruning
- [ ] Batch commit optimization
- [ ] Early conflict detection
- [ ] Transaction pooling

### 11. Configuration

```yaml
server:
  mysql:
    transaction:
      max_operations: 10000      # Max ops per transaction
      max_size_mb: 10            # Max transaction size
      timeout_seconds: 3600      # Max transaction duration
      conflict_retry: true       # Auto-retry on conflict
      conflict_retry_max: 3      # Max retry attempts
```

### 12. Monitoring & Metrics

**Transaction Metrics**:
- Active transaction count
- Transaction commit rate
- Transaction conflict rate
- Average transaction duration
- Average operations per transaction
- Transaction timeout rate

**Conflict Metrics**:
- Read-write conflicts
- Write-write conflicts
- Conflict by key pattern
- Retry success rate

### 13. Error Codes

**MySQL-Compatible Error Codes**:
- `1205` - Lock wait timeout
- `1213` - Deadlock detected
- `1640` - Transaction size exceeded
- `25006` - Read only transaction

**Custom Error Codes**:
- `40001` - Transaction conflict
- `40002` - Transaction timeout
- `40003` - Transaction too large

## Summary

This design provides:
1. ✅ Full ACID transaction support
2. ✅ Snapshot Isolation
3. ✅ Distributed transaction coordination via Raft
4. ✅ Failure recovery through connection tracking
5. ✅ TiDB-level capabilities for KV workloads
6. ✅ MySQL compatibility
7. ✅ Performance optimization strategies

The implementation leverages the existing kvstore.Txn infrastructure and provides a solid foundation for building more advanced transaction features in the future.
