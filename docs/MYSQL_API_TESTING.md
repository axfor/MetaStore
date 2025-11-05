# MySQL API Testing Guide

## Overview

This document describes the comprehensive test suite for the MySQL protocol access layer, including cross-protocol data consistency tests.

## Test Files

### 1. Basic Integration Tests

#### Memory Engine Tests
**File**: `test/mysql_api_memory_integration_test.go`

Tests basic MySQL operations with memory storage engine:
- ✅ Insert and Select operations
- ✅ Update operations
- ✅ Delete operations
- ✅ SHOW DATABASES/TABLES commands
- ✅ Transaction support (BEGIN/COMMIT/ROLLBACK)
- ✅ Connection lifecycle

**Run Command**:
```bash
go test -v ./test -run TestMySQLMemorySingleNodeOperations
```

#### RocksDB Engine Tests
**File**: `test/mysql_api_rocksdb_integration_test.go`

Tests MySQL operations with RocksDB persistent storage:
- ✅ All memory engine tests
- ✅ Large value handling (1KB, 10KB)
- ✅ Special characters in keys
- ✅ Data persistence

**Run Command**:
```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
go test -v ./test -run TestMySQLRocksDB
```

### 2. Cross-Protocol Integration Tests

**File**: `test/mysql_cross_protocol_test.go`

Tests data consistency across HTTP, etcd, and MySQL protocols:

#### Test Scenarios

| Test | Description | Validates |
|------|-------------|-----------|
| `HTTP_Write_MySQL_Read` | Write via HTTP API, read via MySQL | Cross-protocol read consistency |
| `Etcd_Write_MySQL_Read` | Write via etcd gRPC, read via MySQL | Cross-protocol read consistency |
| `MySQL_Write_HTTP_Read` | Write via MySQL, read via HTTP API | Cross-protocol read consistency |
| `MySQL_Write_Etcd_Read` | Write via MySQL, read via etcd gRPC | Cross-protocol read consistency |
| `MySQL_Update_HTTP_Read` | Update via MySQL, verify via HTTP | Update propagation |
| `MySQL_Update_Etcd_Read` | Update via MySQL, verify via etcd | Update propagation |
| `MySQL_Delete_HTTP_Verify` | Delete via MySQL, verify via HTTP | Delete propagation |
| `MySQL_Delete_Etcd_Verify` | Delete via MySQL, verify via etcd | Delete propagation |
| `Batch_Interleaved_Operations` | Mixed protocol batch writes | Multi-protocol consistency |
| `Concurrent_Multi_Protocol_Writes` | Concurrent writes from all protocols | Concurrent consistency |

**Run Command**:
```bash
go test -v ./test -run TestMySQLCrossProtocol
```

**Expected Results**:
- All data written via any protocol should be readable from all other protocols
- No data loss or corruption
- Consistent ordering and atomicity

### 3. Cluster Integration Tests

**File**: `test/mysql_cluster_integration_test.go`

Tests MySQL in a multi-node Raft cluster:

#### Test Scenarios

| Test | Description | Validates |
|------|-------------|-----------|
| `Write_Node1_Read_All_MySQL` | Write to one node, read from all | Cluster replication |
| `HTTP_Write_Node2_MySQL_Read_All` | HTTP write on node 2, MySQL read all | Cross-protocol cluster consistency |
| `Etcd_Write_Node3_MySQL_Read_All` | etcd write on node 3, MySQL read all | Cross-protocol cluster consistency |
| `MySQL_Update_Different_Nodes` | Update from different nodes | Update replication |
| `MySQL_Delete_Verify_All` | Delete from one, verify on all | Delete replication |
| `Concurrent_MySQL_Writes` | Concurrent writes from all nodes | Concurrent cluster writes |
| `Mixed_Protocol_Cluster_Writes` | Mixed protocol writes in cluster | Full cluster consistency |

**Run Command**:
```bash
go test -v ./test -run TestMySQLCluster -timeout 5m
```

**Note**: Cluster tests require more time and resources (3 nodes).

### 4. Protocol Commands Tests

Tests MySQL-specific commands:
- ✅ SHOW DATABASES
- ✅ SHOW TABLES
- ✅ DESCRIBE/DESC table
- ✅ USE database
- ✅ SET variables

**Run Command**:
```bash
go test -v ./test -run TestMySQLProtocolShowCommands
```

## Running All Tests

### Quick Test (Memory Only)
```bash
go test -v ./test -run TestMySQL.*Memory
```

### Full Test Suite (Requires RocksDB)
```bash
# Set CGO flags for RocksDB
export CGO_ENABLED=1
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain"

# Run all MySQL tests
go test -v ./test -run TestMySQL -timeout 10m
```

### Cross-Protocol Tests Only
```bash
go test -v ./test -run TestMySQLCrossProtocol -timeout 5m
```

### Cluster Tests Only
```bash
go test -v ./test -run TestMySQLCluster -timeout 5m
```

## Test Environment Requirements

### Software Dependencies
- Go 1.21+
- MySQL CLI client (for manual testing)
- RocksDB library (for RocksDB tests)
- Available ports: 13306-13311, 12379-12382, 19200-19300

### System Resources
- Memory: 2GB+ recommended
- Disk: 1GB+ free space for RocksDB
- Network: Localhost access
- Ports: Must be available (not in use)

## Test Coverage

### Protocol Operations
- ✅ INSERT - Data insertion via SQL
- ✅ SELECT - Query operations with WHERE clause
- ✅ UPDATE - Data modification
- ✅ DELETE - Data removal
- ✅ BEGIN/COMMIT/ROLLBACK - Transaction control
- ✅ SHOW commands - Schema inspection
- ✅ Connection management

### Cross-Protocol Scenarios
- ✅ HTTP → MySQL
- ✅ etcd → MySQL
- ✅ MySQL → HTTP
- ✅ MySQL → etcd
- ✅ Concurrent multi-protocol writes
- ✅ Batch operations

### Storage Engines
- ✅ Memory engine (in-memory with WAL)
- ✅ RocksDB engine (persistent storage)

### Cluster Scenarios
- ✅ 3-node cluster replication
- ✅ Write to any node, read from all
- ✅ Cross-protocol in cluster
- ✅ Concurrent cluster operations

## Manual Testing

### 1. Start MetaStore with MySQL Enabled

```bash
# Create config file
cat > config.yaml <<EOF
server:
  cluster_id: 1
  member_id: 1
  listen_address: ":2379"
  mysql:
    enable: true
    address: ":3306"
    username: "root"
    password: ""
EOF

# Start server
./metastore -config config.yaml -storage memory
```

### 2. Test with MySQL CLI

```bash
# Connect
mysql -h 127.0.0.1 -P 3306 -u root

# Test commands
USE metastore;
SHOW TABLES;
INSERT INTO kv (key, value) VALUES ('test', 'works');
SELECT * FROM kv WHERE key = 'test';
UPDATE kv SET value = 'updated' WHERE key = 'test';
DELETE FROM kv WHERE key = 'test';
```

### 3. Test Cross-Protocol

```bash
# Terminal 1: Start server
./metastore -config config.yaml -storage memory

# Terminal 2: Write via HTTP
curl -X PUT http://localhost:9121/mykey -d "myvalue"

# Terminal 3: Read via MySQL
mysql -h 127.0.0.1 -P 3306 -u root -e \
  "SELECT * FROM metastore.kv WHERE key = 'mykey'"

# Terminal 4: Read via etcd
etcdctl --endpoints=localhost:2379 get mykey
```

## Debugging Failed Tests

### Common Issues

#### 1. Port Already in Use
```
Error: bind: address already in use
```

**Solution**: Kill processes using the ports or change test ports
```bash
lsof -i :3306
kill <PID>
```

#### 2. RocksDB Not Found
```
Error: ld: library not found for -lrocksdb
```

**Solution**: Install RocksDB or skip RocksDB tests
```bash
# macOS
brew install rocksdb

# Or skip RocksDB tests
go test -v ./test -run TestMySQL.*Memory
```

#### 3. Raft Commit Timeout
```
Error: Condition not met after waiting
```

**Solution**: Increase wait times or check Raft logs
- Increase `time.Sleep()` durations in tests
- Check data directory permissions
- Ensure no network issues

#### 4. Connection Refused
```
Error: dial tcp 127.0.0.1:3306: connect: connection refused
```

**Solution**: Wait longer for server startup
- Increase initial sleep time
- Add retry logic with longer timeout
- Check server logs for startup errors

### Enable Debug Logging

Modify test config to enable debug logs:
```go
cfg := NewTestConfig(1, 1, ":2379")
cfg.Server.Log.Level = "debug"
cfg.Server.Log.OutputPaths = []string{"test.log"}
```

## Continuous Integration

### GitHub Actions Example

```yaml
name: MySQL API Tests

on: [push, pull_request]

jobs:
  test-memory:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - name: Run Memory Tests
        run: go test -v ./test -run TestMySQL.*Memory

  test-rocksdb:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - name: Install RocksDB
        run: |
          sudo apt-get update
          sudo apt-get install -y librocksdb-dev
      - name: Run RocksDB Tests
        run: |
          export CGO_ENABLED=1
          go test -v ./test -run TestMySQLRocksDB

  test-cross-protocol:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - name: Run Cross-Protocol Tests
        run: go test -v ./test -run TestMySQLCrossProtocol -timeout 5m
```

## Performance Benchmarks

### Running Benchmarks

```bash
# Run all benchmarks
go test -bench=. -benchmem ./test -run=^$

# Run specific benchmark
go test -bench=BenchmarkMySQLInsert -benchmem ./test
```

### Expected Performance

| Operation | Throughput | Latency |
|-----------|-----------|---------|
| INSERT | ~5,000 ops/sec | ~0.2ms |
| SELECT | ~10,000 ops/sec | ~0.1ms |
| UPDATE | ~4,000 ops/sec | ~0.25ms |
| DELETE | ~4,000 ops/sec | ~0.25ms |

*Note: Performance depends on hardware and configuration*

## Test Maintenance

### Adding New Tests

1. Create test function in appropriate file
2. Follow naming convention: `Test<Feature><Engine>`
3. Add cleanup: `defer os.RemoveAll(dataDir)`
4. Wait for Raft commits: `time.Sleep()`
5. Use `require` for critical checks, `assert` for non-critical

### Example Test Template

```go
func TestMySQLNewFeature(t *testing.T) {
    t.Parallel()

    // Setup
    dataDir := "data/memory/test_feature"
    os.RemoveAll(dataDir)
    defer os.RemoveAll(dataDir)

    // Create storage and server
    // ... setup code ...

    // Test logic
    t.Run("SubTest1", func(t *testing.T) {
        // ... test code ...
    })

    // Cleanup
    // ... cleanup code ...
}
```

## Conclusion

The MySQL API test suite provides comprehensive coverage of:
- Basic MySQL protocol operations
- Cross-protocol data consistency
- Multi-node cluster replication
- Both storage engines (Memory and RocksDB)

All tests validate that the MySQL protocol layer maintains data consistency with HTTP and etcd APIs while providing standard MySQL compatibility.
