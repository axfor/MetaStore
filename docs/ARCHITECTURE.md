# MetaStore Architecture Design

## Table of Contents

- [Overview](#overview)
- [Package Structure](#package-structure)
- [Dual Storage Engine](#dual-storage-engine)
- [Raft Storage Layer Deep Dive](#raft-storage-layer-deep-dive)
- [Data Flow](#data-flow)
- [Key Component Relationships](#key-component-relationships)

---

## Overview

MetaStore is a lightweight distributed KV storage system based on the etcd Raft consensus protocol. It supports two storage engines:

1. **Memory Mode** (Memory + WAL) - Default mode, fast and lightweight
2. **RocksDB Mode** - Full persistence, suitable for large datasets

```
┌─────────────────────────────────────────────────┐
│              HTTP REST API                      │
│         GET/PUT/POST/DELETE /key                │
└──────────────────┬──────────────────────────────┘
                   │
                   ↓
┌─────────────────────────────────────────────────┐
│       KV Store Layer (Application Layer)        │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ Memory KV Store  │  │ RocksDB KV Store     │ │
│  │ (Memory Mode)    │  │ (RocksDB Mode)       │ │
│  └──────────────────┘  └──────────────────────┘ │
└──────────────────┬──────────────────────────────┘
                   │
                   ↓ Committed via Raft
┌─────────────────────────────────────────────────┐
│      Raft Consensus Layer (Consensus Layer)     │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ raftNode         │  │ raftNodeRocks        │ │
│  │ (Memory Node)    │  │ (RocksDB Node)       │ │
│  └──────────────────┘  └──────────────────────┘ │
└──────────────────┬──────────────────────────────┘
                   │
                   ↓ Raft Log Storage
┌─────────────────────────────────────────────────┐
│      Raft Storage Layer (Raft Storage)          │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ MemoryStorage    │  │ RocksDBStorage       │ │
│  │ + WAL            │  │ (raftlog.go)         │ │
│  └──────────────────┘  └──────────────────────┘ │
└─────────────────────────────────────────────────┘
```

---

## Package Structure

```
internal/
├── kvstore/              # Interface Definition Layer
│   └── store.go          # Store interface + Commit/KV types
│
├── memory/               # Memory Implementation Layer
│   ├── kvstore.go        # Memory KV store implementation
│   └── kvstore_test.go   # Unit tests
│
├── rocksdb/              # RocksDB Implementation Layer
│   ├── kvstore.go        # RocksDB KV store (application data)
│   ├── raftlog.go        # RocksDB Raft storage (Raft internal data) ⭐
│   └── raftlog_test.go   # Raft storage tests
│
├── raft/                 # Raft Consensus Layer
│   ├── node.go           # Memory mode Raft node
│   ├── node_rocksdb.go   # RocksDB mode Raft node
│   ├── node_test.go      # Raft tests
│   └── listener.go       # Network listener
│
└── http/                 # HTTP API Layer
    └── api.go            # REST API handler
```

### Package Responsibility Matrix

| Package | Responsibility | Dependencies | Key Types |
|---------|---------------|--------------|-----------|
| `kvstore` | Define KV store interface | None | `Store`, `Commit`, `KV` |
| `memory` | Implement memory KV store | `kvstore` | `Memory` |
| `rocksdb` | Implement RocksDB KV + Raft storage | `kvstore` | `RocksDB`, `RocksDBStorage` |
| `raft` | Implement Raft consensus protocol | `kvstore`, `rocksdb` | `raftNode`, `raftNodeRocks` |
| `http` | Provide HTTP REST API | `kvstore` | `httpKVAPI` |

---

## Dual Storage Engine

### Mode Comparison

| Feature | Memory Mode (Memory + WAL) | RocksDB Mode |
|---------|---------------------------|--------------|
| **Application KV Storage** | `internal/memory/kvstore.go` | `internal/rocksdb/kvstore.go` |
| **Raft Node** | `internal/raft/node.go` | `internal/raft/node_rocksdb.go` |
| **Raft Log Storage** | `raft.MemoryStorage` (etcd) | `rocksdb.RocksDBStorage` ⭐ |
| **WAL Persistence** | `wal.WAL` (etcd) | ✅ Built-in RocksDB |
| **Snapshot Storage** | Filesystem | RocksDB |
| **Data Location** | Memory + WAL files | All in RocksDB |
| **CLI Flag** | `--storage=memory` | `--storage=rocksdb` |
| **Use Case** | Fast, lightweight deployment | Large datasets, full persistence |

### Memory Mode Architecture

```
┌─────────────────────────────────────────────────┐
│            internal/memory/kvstore.go           │
│                  Memory                         │
│        (User KV data stored in memory)          │
└──────────────────┬──────────────────────────────┘
                   ↓ Propose to Raft
┌─────────────────────────────────────────────────┐
│           internal/raft/node.go                 │
│                raftNode                         │
│          (Raft consensus node)                  │
└──────────────────┬──────────────────────────────┘
                   ↓ Raft log storage
┌─────────────────────────────────────────────────┐
│     raft.MemoryStorage (etcd built-in)          │
│       (Raft logs stored in memory)              │
│                    +                            │
│           wal.WAL (etcd built-in)               │
│         (WAL file persistence)                  │
└──────────────────┬──────────────────────────────┘
                   ↓
┌─────────────────────────────────────────────────┐
│    Memory + WAL files + Snapshot files          │
│    Directory: ./metaStore-{id}/                 │
└─────────────────────────────────────────────────┘
```

### RocksDB Mode Architecture

```
┌─────────────────────────────────────────────────┐
│         internal/rocksdb/kvstore.go             │
│                 RocksDB                         │
│    (User KV data, key prefix: kv_data_)         │
└──────────────────┬──────────────────────────────┘
                   ↓ Propose to Raft
┌─────────────────────────────────────────────────┐
│        internal/raft/node_rocksdb.go            │
│             raftNodeRocks                       │
│          (Raft consensus node)                  │
└──────────────────┬──────────────────────────────┘
                   ↓ Raft log storage
┌─────────────────────────────────────────────────┐
│       internal/rocksdb/raftlog.go ⭐            │
│           RocksDBStorage                        │
│  (Raft log data, key prefix: raft_log_, etc.)  │
│  Replaces MemoryStorage + WAL combination       │
└──────────────────┬──────────────────────────────┘
                   ↓
┌─────────────────────────────────────────────────┐
│       RocksDB Database (all data)               │
│         Directory: ./data/{id}/                 │
│                                                 │
│  Contains:                                      │
│  - User KV data (kv_data_*)                     │
│  - Raft logs (raft_log_*)                      │
│  - Raft HardState (hard_state)                 │
│  - Raft ConfState (conf_state)                 │
│  - Snapshot metadata (snapshot_meta)            │
└─────────────────────────────────────────────────┘
```

---

## Raft Storage Layer Deep Dive

### ⭐ The Role of `internal/rocksdb/raftlog.go`

**This is the most confusing part of the project!**

`raftlog.go` implements the `raft.Storage` interface, providing **Raft log storage for RocksDB mode**.

#### Why is this file needed?

1. **etcd Raft Library Requirement**
   - etcd Raft library requires a storage backend that implements `raft.Storage` interface
   - etcd provides `raft.MemoryStorage` (in-memory implementation)
   - But the project needs RocksDB persistence, so we must implement it ourselves

2. **Different from kvstore.go**
   - `kvstore.go` = **Application layer** KV storage (stores user data)
   - `raftlog.go` = **Raft layer** log storage (stores Raft internal state)

3. **Replaces MemoryStorage + WAL**
   - Memory mode needs `raft.MemoryStorage` + `wal.WAL` combination
   - RocksDB mode uses `RocksDBStorage` to replace the entire combination
   - All data is in RocksDB, no separate WAL files needed

#### Data Types Stored

```go
const (
    raftLogPrefix = "raft_log_"     // Raft log entries
    hardStateKey  = "hard_state"    // Raft HardState (Term, Vote, Commit)
    confStateKey  = "conf_state"    // Cluster configuration state
    snapshotKey   = "snapshot_meta" // Snapshot metadata
    firstIndexKey = "first_index"   // First log index
    lastIndexKey  = "last_index"    // Last log index
)
```

These are all **Raft consensus protocol internal states**, not user data!

#### Implemented Interface Methods

```go
type RocksDBStorage struct {
    db     *grocksdb.DB
    nodeID string
    // ...
}

// Required by raft.Storage interface:
func (s *RocksDBStorage) InitialState() (HardState, ConfState, error)
func (s *RocksDBStorage) Entries(lo, hi, maxSize uint64) ([]Entry, error)
func (s *RocksDBStorage) Term(index uint64) (uint64, error)
func (s *RocksDBStorage) FirstIndex() (uint64, error)
func (s *RocksDBStorage) LastIndex() (uint64, error)
func (s *RocksDBStorage) Snapshot() (Snapshot, error)

// Additional persistence methods:
func (s *RocksDBStorage) Append(entries []Entry) error
func (s *RocksDBStorage) SetHardState(st HardState) error
func (s *RocksDBStorage) CreateSnapshot(...) (Snapshot, error)
func (s *RocksDBStorage) ApplySnapshot(snap Snapshot) error
func (s *RocksDBStorage) Compact(compactIndex uint64) error
```

### How Raft Nodes Use Storage

#### Memory Mode (node.go)

```go
type raftNode struct {
    node        raft.Node
    raftStorage *raft.MemoryStorage    // ← etcd built-in
    wal         *wal.WAL               // ← etcd WAL
    // ...
}

// Initialization
func NewNode(...) {
    rc.raftStorage = raft.NewMemoryStorage()
    rc.wal = wal.Create(waldir, nil)

    // Start Raft
    raft.NewRawNode(&raft.Config{
        Storage: rc.raftStorage,  // ← Use MemoryStorage
    })
}
```

#### RocksDB Mode (node_rocksdb.go)

```go
type raftNodeRocks struct {
    node        raft.Node
    raftStorage *rocksdb.RocksDBStorage  // ← raftlog.go implementation!
    rocksDB     *grocksdb.DB
    // No WAL needed!
}

// Initialization
func NewNodeRocksDB(..., rocksDB *grocksdb.DB) {
    // Create RocksDBStorage
    rc.raftStorage = rocksdb.NewRocksDBStorage(rocksDB, "node_1")

    // Start Raft
    raft.NewRawNode(&raft.Config{
        Storage: rc.raftStorage,  // ← Use RocksDBStorage
    })
}
```

---

## Data Flow

### Write Flow (PUT /key → value)

```
1. HTTP API receives request
   ↓
   internal/http/api.go:ServeHTTP()

2. Call KV Store's Propose method
   ↓
   Memory:  internal/memory/kvstore.go:Propose()
   RocksDB: internal/rocksdb/kvstore.go:Propose()

3. Send to Raft proposal channel
   ↓
   proposeC <- encodedKV

4. Raft node receives proposal
   ↓
   Memory:  internal/raft/node.go:serveChannels()
   RocksDB: internal/raft/node_rocksdb.go:serveChannels()

5. Raft reaches consensus, writes to log
   ↓
   Memory:  raftStorage.Append() → MemoryStorage + WAL
   RocksDB: raftStorage.Append() → RocksDBStorage (raftlog.go)

6. Commit applied entries
   ↓
   commitC <- &Commit{Data: [...]string, ApplyDoneC: ...}

7. KV Store applies committed entries
   ↓
   Memory:  internal/memory/kvstore.go:readCommits()
            → Write to memory map
   RocksDB: internal/rocksdb/kvstore.go:readCommits()
            → Write to RocksDB (kv_data_ prefix)

8. Return success response
```

### Read Flow (GET /key)

```
1. HTTP API receives request
   ↓
   internal/http/api.go:ServeHTTP()

2. Call KV Store's Lookup method
   ↓
   Memory:  internal/memory/kvstore.go:Lookup()
            → Read from memory map
   RocksDB: internal/rocksdb/kvstore.go:Lookup()
            → Read from RocksDB (kv_data_ prefix)

3. Return result
```

### Node Restart Recovery Flow

#### Memory Mode Recovery

```
1. Start node
   ↓
   internal/raft/node.go:NewNode()

2. Replay WAL
   ↓
   wal.OpenForRead(waldir)
   raftStorage.Append(entries from WAL)

3. Load snapshot (if exists)
   ↓
   snapshotter.Load()
   raftStorage.ApplySnapshot(snapshot)

4. KV Store recovers from snapshot
   ↓
   internal/memory/kvstore.go:recoverFromSnapshot()
   → Rebuild memory map

5. Continue processing new requests
```

#### RocksDB Mode Recovery

```
1. Start node
   ↓
   internal/raft/node_rocksdb.go:NewNodeRocksDB()

2. Open RocksDB
   ↓
   rocksdb.Open("data/1")

3. Create RocksDBStorage
   ↓
   internal/rocksdb/raftlog.go:NewRocksDBStorage()
   → Automatically load firstIndex, lastIndex from RocksDB

4. Load snapshot (if exists)
   ↓
   snapshotter.Load()
   raftStorage.ApplySnapshot(snapshot)

5. KV Store recovers from RocksDB
   ↓
   internal/rocksdb/kvstore.go:recoverFromSnapshot()
   → All data already in RocksDB, no additional recovery needed

6. Continue processing new requests
```

---

## Key Component Relationships

### 1. Same RocksDB, Two Purposes

In RocksDB mode, **the same RocksDB database instance** is shared by two components:

```go
// cmd/metastore/main.go
db := rocksdb.Open("data/1")

// Purpose 1: Application layer KV storage
kvs := rocksdb.NewRocksDB(db, "node_1", ...)
// Writes key: "kv_data_mykey" → value: "myvalue"

// Purpose 2: Raft log storage
raftStorage := rocksdb.NewRocksDBStorage(db, "node_1")
// Writes key: "raft_log_123" → value: <raft entry>
// Writes key: "hard_state" → value: <term, vote, commit>
```

Data types are distinguished by **different key prefixes**:

| Prefix | Purpose | Defined In |
|--------|---------|------------|
| `kv_data_*` | User KV data | `internal/rocksdb/kvstore.go` |
| `raft_log_*` | Raft log entries | `internal/rocksdb/raftlog.go` |
| `hard_state` | Raft HardState | `internal/rocksdb/raftlog.go` |
| `conf_state` | Raft ConfState | `internal/rocksdb/raftlog.go` |
| `snapshot_meta` | Snapshot metadata | `internal/rocksdb/raftlog.go` |

### 2. Raft Node and Storage Binding

```
┌──────────────────────────────────────┐
│     etcd Raft Library (go.etcd.io)   │
│                                      │
│  Requires: raft.Storage interface    │
└──────────────┬───────────────────────┘
               │
               ↓ Provide implementation
┌──────────────────────────────────────┐
│          Memory Mode                 │
│  ┌────────────────────────────┐     │
│  │ raft.MemoryStorage         │     │
│  │ (etcd built-in impl)       │     │
│  └────────────────────────────┘     │
│              +                       │
│  ┌────────────────────────────┐     │
│  │ wal.WAL                    │     │
│  │ (etcd built-in WAL)        │     │
│  └────────────────────────────┘     │
└──────────────────────────────────────┘

               OR

┌──────────────────────────────────────┐
│        RocksDB Mode                  │
│  ┌────────────────────────────┐     │
│  │ rocksdb.RocksDBStorage     │     │
│  │ (raftlog.go custom impl)   │     │
│  │                            │     │
│  │ Replaces MemoryStorage+WAL │     │
│  └────────────────────────────┘     │
└──────────────────────────────────────┘
```

### 3. Interface Implementation Relationships

```
kvstore.Store interface
    ↑ implemented by
    ├── internal/memory/Memory
    └── internal/rocksdb/RocksDB

raft.Storage interface (defined by etcd)
    ↑ implemented by
    ├── raft.MemoryStorage (etcd built-in)
    └── rocksdb.RocksDBStorage (raftlog.go custom)
```

---

## Summary

### Core Design Principles

1. **Layered Architecture**: HTTP → KV Store → Raft → Storage
2. **Dual Mode Support**: Memory mode (fast) vs RocksDB mode (persistent)
3. **Interface Abstraction**: Pluggable storage engines through interfaces
4. **Shared Storage**: In RocksDB mode, user data and Raft data share the same database

### Key File Responsibilities

| File | Responsibility | Interface |
|------|---------------|-----------|
| `internal/memory/kvstore.go` | Memory mode user KV storage | `kvstore.Store` |
| `internal/rocksdb/kvstore.go` | RocksDB mode user KV storage | `kvstore.Store` |
| `internal/rocksdb/raftlog.go` | RocksDB mode Raft log storage | `raft.Storage` |
| `internal/raft/node.go` | Memory mode Raft node | - |
| `internal/raft/node_rocksdb.go` | RocksDB mode Raft node | - |

### Why It's Not Confusing

Although package and file names appear to have duplicates (memory, rocksdb), each file has **a clear and unique responsibility**:

- **Application Layer Storage** vs **Raft Layer Storage** - Completely different layers
- **Memory Mode** vs **RocksDB Mode** - Two optional implementation approaches
- **Interface Definition** vs **Interface Implementation** - Clear abstraction levels

This is a well-designed, distributed system architecture that follows Go best practices!
