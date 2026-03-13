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
2. **Pebble Mode** - Full persistence, suitable for large datasets

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
│  │ Memory KV Store  │  │ Pebble KV Store     │ │
│  │ (Memory Mode)    │  │ (Pebble Mode)       │ │
│  └──────────────────┘  └──────────────────────┘ │
└──────────────────┬──────────────────────────────┘
                   │
                   ↓ Committed via Raft
┌─────────────────────────────────────────────────┐
│      Raft Consensus Layer (Consensus Layer)     │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ raftNode         │  │ raftNodePebble        │ │
│  │ (Memory Node)    │  │ (Pebble Node)       │ │
│  └──────────────────┘  └──────────────────────┘ │
└──────────────────┬──────────────────────────────┘
                   │
                   ↓ Raft Log Storage
┌─────────────────────────────────────────────────┐
│      Raft Storage Layer (Raft Storage)          │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ MemoryStorage    │  │ PebbleStorage       │ │
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
├── pebble/              # Pebble Implementation Layer
│   ├── kvstore.go        # Pebble KV store (application data)
│   ├── raftlog.go        # Pebble Raft storage (Raft internal data) ⭐
│   └── raftlog_test.go   # Raft storage tests
│
├── raft/                 # Raft Consensus Layer
│   ├── node.go           # Memory mode Raft node
│   ├── node_pebble.go   # Pebble mode Raft node
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
| `pebble` | Implement Pebble KV + Raft storage | `kvstore` | `Pebble`, `PebbleStorage` |
| `raft` | Implement Raft consensus protocol | `kvstore`, `pebble` | `raftNode`, `raftNodePebble` |
| `http` | Provide HTTP REST API | `kvstore` | `httpKVAPI` |

---

## Dual Storage Engine

### Mode Comparison

| Feature | Memory Mode (Memory + WAL) | Pebble Mode |
|---------|---------------------------|--------------|
| **Application KV Storage** | `internal/memory/kvstore.go` | `internal/pebble/kvstore.go` |
| **Raft Node** | `internal/raft/node.go` | `internal/raft/node_pebble.go` |
| **Raft Log Storage** | `raft.MemoryStorage` (etcd) | `pebble.PebbleStorage` ⭐ |
| **WAL Persistence** | `wal.WAL` (etcd) | ✅ Built-in Pebble |
| **Snapshot Storage** | Filesystem | Pebble |
| **Data Location** | Memory + WAL files | All in Pebble |
| **CLI Flag** | `--storage=memory` | `--storage=pebble` |
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

### Pebble Mode Architecture

```
┌─────────────────────────────────────────────────┐
│         internal/pebble/kvstore.go             │
│                 Pebble                         │
│    (User KV data, key prefix: kv_data_)         │
└──────────────────┬──────────────────────────────┘
                   ↓ Propose to Raft
┌─────────────────────────────────────────────────┐
│        internal/raft/node_pebble.go            │
│             raftNodePebble                       │
│          (Raft consensus node)                  │
└──────────────────┬──────────────────────────────┘
                   ↓ Raft log storage
┌─────────────────────────────────────────────────┐
│       internal/pebble/raftlog.go ⭐            │
│           PebbleStorage                        │
│  (Raft log data, key prefix: raft_log_, etc.)  │
│  Replaces MemoryStorage + WAL combination       │
└──────────────────┬──────────────────────────────┘
                   ↓
┌─────────────────────────────────────────────────┐
│       Pebble Database (all data)               │
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

### ⭐ The Role of `internal/pebble/raftlog.go`

**This is the most confusing part of the project!**

`raftlog.go` implements the `raft.Storage` interface, providing **Raft log storage for Pebble mode**.

#### Why is this file needed?

1. **etcd Raft Library Requirement**
   - etcd Raft library requires a storage backend that implements `raft.Storage` interface
   - etcd provides `raft.MemoryStorage` (in-memory implementation)
   - But the project needs Pebble persistence, so we must implement it ourselves

2. **Different from kvstore.go**
   - `kvstore.go` = **Application layer** KV storage (stores user data)
   - `raftlog.go` = **Raft layer** log storage (stores Raft internal state)

3. **Replaces MemoryStorage + WAL**
   - Memory mode needs `raft.MemoryStorage` + `wal.WAL` combination
   - Pebble mode uses `PebbleStorage` to replace the entire combination
   - All data is in Pebble, no separate WAL files needed

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
type PebbleStorage struct {
    db     *gpebble.DB
    nodeID string
    // ...
}

// Required by raft.Storage interface:
func (s *PebbleStorage) InitialState() (HardState, ConfState, error)
func (s *PebbleStorage) Entries(lo, hi, maxSize uint64) ([]Entry, error)
func (s *PebbleStorage) Term(index uint64) (uint64, error)
func (s *PebbleStorage) FirstIndex() (uint64, error)
func (s *PebbleStorage) LastIndex() (uint64, error)
func (s *PebbleStorage) Snapshot() (Snapshot, error)

// Additional persistence methods:
func (s *PebbleStorage) Append(entries []Entry) error
func (s *PebbleStorage) SetHardState(st HardState) error
func (s *PebbleStorage) CreateSnapshot(...) (Snapshot, error)
func (s *PebbleStorage) ApplySnapshot(snap Snapshot) error
func (s *PebbleStorage) Compact(compactIndex uint64) error
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

#### Pebble Mode (node_pebble.go)

```go
type raftNodePebble struct {
    node        raft.Node
    raftStorage *pebble.PebbleStorage  // ← raftlog.go implementation!
    pebble     *gpebble.DB
    // No WAL needed!
}

// Initialization
func NewNodePebble(..., pebble *gpebble.DB) {
    // Create PebbleStorage
    rc.raftStorage = pebble.NewPebbleStorage(pebble, "node_1")

    // Start Raft
    raft.NewRawNode(&raft.Config{
        Storage: rc.raftStorage,  // ← Use PebbleStorage
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
   Pebble: internal/pebble/kvstore.go:Propose()

3. Send to Raft proposal channel
   ↓
   proposeC <- encodedKV

4. Raft node receives proposal
   ↓
   Memory:  internal/raft/node.go:serveChannels()
   Pebble: internal/raft/node_pebble.go:serveChannels()

5. Raft reaches consensus, writes to log
   ↓
   Memory:  raftStorage.Append() → MemoryStorage + WAL
   Pebble: raftStorage.Append() → PebbleStorage (raftlog.go)

6. Commit applied entries
   ↓
   commitC <- &Commit{Data: [...]string, ApplyDoneC: ...}

7. KV Store applies committed entries
   ↓
   Memory:  internal/memory/kvstore.go:readCommits()
            → Write to memory map
   Pebble: internal/pebble/kvstore.go:readCommits()
            → Write to Pebble (kv_data_ prefix)

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
   Pebble: internal/pebble/kvstore.go:Lookup()
            → Read from Pebble (kv_data_ prefix)

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

#### Pebble Mode Recovery

```
1. Start node
   ↓
   internal/raft/node_pebble.go:NewNodePebble()

2. Open Pebble
   ↓
   pebble.Open("data/1")

3. Create PebbleStorage
   ↓
   internal/pebble/raftlog.go:NewPebbleStorage()
   → Automatically load firstIndex, lastIndex from Pebble

4. Load snapshot (if exists)
   ↓
   snapshotter.Load()
   raftStorage.ApplySnapshot(snapshot)

5. KV Store recovers from Pebble
   ↓
   internal/pebble/kvstore.go:recoverFromSnapshot()
   → All data already in Pebble, no additional recovery needed

6. Continue processing new requests
```

---

## Key Component Relationships

### 1. Same Pebble, Two Purposes

In Pebble mode, **the same Pebble database instance** is shared by two components:

```go
// cmd/metastore/main.go
db := pebble.Open("data/1")

// Purpose 1: Application layer KV storage
kvs := pebble.NewPebble(db, "node_1", ...)
// Writes key: "kv_data_mykey" → value: "myvalue"

// Purpose 2: Raft log storage
raftStorage := pebble.NewPebbleStorage(db, "node_1")
// Writes key: "raft_log_123" → value: <raft entry>
// Writes key: "hard_state" → value: <term, vote, commit>
```

Data types are distinguished by **different key prefixes**:

| Prefix | Purpose | Defined In |
|--------|---------|------------|
| `kv_data_*` | User KV data | `internal/pebble/kvstore.go` |
| `raft_log_*` | Raft log entries | `internal/pebble/raftlog.go` |
| `hard_state` | Raft HardState | `internal/pebble/raftlog.go` |
| `conf_state` | Raft ConfState | `internal/pebble/raftlog.go` |
| `snapshot_meta` | Snapshot metadata | `internal/pebble/raftlog.go` |

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
│        Pebble Mode                  │
│  ┌────────────────────────────┐     │
│  │ pebble.PebbleStorage     │     │
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
    └── internal/pebble/Pebble

raft.Storage interface (defined by etcd)
    ↑ implemented by
    ├── raft.MemoryStorage (etcd built-in)
    └── pebble.PebbleStorage (raftlog.go custom)
```

---

## Summary

### Core Design Principles

1. **Layered Architecture**: HTTP → KV Store → Raft → Storage
2. **Dual Mode Support**: Memory mode (fast) vs Pebble mode (persistent)
3. **Interface Abstraction**: Pluggable storage engines through interfaces
4. **Shared Storage**: In Pebble mode, user data and Raft data share the same database

### Key File Responsibilities

| File | Responsibility | Interface |
|------|---------------|-----------|
| `internal/memory/kvstore.go` | Memory mode user KV storage | `kvstore.Store` |
| `internal/pebble/kvstore.go` | Pebble mode user KV storage | `kvstore.Store` |
| `internal/pebble/raftlog.go` | Pebble mode Raft log storage | `raft.Storage` |
| `internal/raft/node.go` | Memory mode Raft node | - |
| `internal/raft/node_pebble.go` | Pebble mode Raft node | - |

### Why It's Not Confusing

Although package and file names appear to have duplicates (memory, pebble), each file has **a clear and unique responsibility**:

- **Application Layer Storage** vs **Raft Layer Storage** - Completely different layers
- **Memory Mode** vs **Pebble Mode** - Two optional implementation approaches
- **Interface Definition** vs **Interface Implementation** - Clear abstraction levels

This is a well-designed, distributed system architecture that follows Go best practices!
