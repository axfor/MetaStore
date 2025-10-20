# Distributed KV Store with RocksDB Support

A lightweight distributed key-value store based on the Raft consensus algorithm, with support for both in-memory and RocksDB persistent storage.

This is an enhanced version of etcd's [raft](https://github.com/etcd-io/raft) with added RocksDB backend support for full persistence.

[raft]: http://raftconsensus.github.io/

## Features

- **Raft Consensus**: Built on etcd's battle-tested raft library
- **High Availability**: Tolerates up to (N-1)/2 node failures in an N-node cluster
- **Dual Storage Modes**:
  - **Memory + WAL**: Default mode with WAL-based persistence (fast, suitable for most use cases)
  - **RocksDB**: Full persistent storage backend (requires RocksDB C++ library)
- **HTTP API**: Simple REST API for key-value operations
- **Dynamic Membership**: Add/remove nodes without downtime
- **Snapshots**: Automatic log compaction via snapshots
- **Single Binary**: Lightweight, easy to deploy

## Building

### Default Build (Memory + WAL)

```sh
go build -o store
```

This builds the store with memory + WAL storage (no external dependencies required).

### RocksDB Build (Optional - Requires RocksDB C++ Library)

**Prerequisites:**
- Go 1.23 or higher
- CGO enabled
- RocksDB C++ library installed

**Linux:**
```sh
# Install RocksDB
sudo apt-get install librocksdb-dev  # Debian/Ubuntu
# or
sudo yum install rocksdb-devel       # RHEL/CentOS

# Build with RocksDB support
CGO_ENABLED=1 go build -tags=rocksdb -o store
```

**macOS:**
```sh
# Install RocksDB
brew install rocksdb

# Build with RocksDB support
CGO_ENABLED=1 go build -tags=rocksdb -o store
```

> **Note for macOS users**: If you encounter linking errors with Go 1.25+ on older SDK versions, see [ROCKSDB_BUILD_MACOS.md](ROCKSDB_BUILD_MACOS.md) for detailed troubleshooting steps.

**Windows:**
```sh
# Install RocksDB (use vcpkg or build from source)
vcpkg install rocksdb:x64-windows

# Build with RocksDB support
$env:CGO_ENABLED=1
go build -tags=rocksdb -o store.exe
```

## Getting Started

### Running single node store

#### Memory + WAL Mode (Default)

```sh
metaStore --id 1 --cluster http://127.0.0.1:12379 --port 12380
```

#### RocksDB Mode (if built with -tags=rocksdb)

```sh
# Create data directory first
mkdir -p data

# Start the node
metaStore --id 1 --cluster http://127.0.0.1:12379 --port 12380 --rocksdb
```

Each store process maintains a single raft instance and a key-value server.
The process's list of comma separated peers (--cluster), its raft ID index into the peer list (--id), and HTTP key-value server port (--port) are passed through the command line.

Next, store a value ("hello") to a key ("my-key"):

```
curl -L http://127.0.0.1:12380/my-key -XPUT -d hello
```

Finally, retrieve the stored key:

```
curl -L http://127.0.0.1:12380/my-key
```

### Running a local cluster

First install [goreman](https://github.com/mattn/goreman), which manages Procfile-based applications.

The [Procfile script](./Procfile) will set up a local example cluster. Start it with:

```sh
goreman start
```

This will bring up three store instances.

Now it's possible to write a key-value pair to any member of the cluster and likewise retrieve it from any member.

### Fault Tolerance

To test cluster recovery, first start a cluster and write a value "foo":
```sh
goreman start
curl -L http://127.0.0.1:12380/my-key -XPUT -d foo
```

Next, remove a node and replace the value with "bar" to check cluster availability:

```sh
goreman run stop store2
curl -L http://127.0.0.1:12380/my-key -XPUT -d bar
curl -L http://127.0.0.1:32380/my-key
```

Finally, bring the node back up and verify it recovers with the updated value "bar":
```sh
goreman run start store2
curl -L http://127.0.0.1:22380/my-key
```

### Dynamic cluster reconfiguration

Nodes can be added to or removed from a running cluster using requests to the REST API.

For example, suppose we have a 3-node cluster that was started with the commands:
```sh
metaStore --id 1 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 12380
metaStore --id 2 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 22380
metaStore --id 3 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 32380
```

A fourth node with ID 4 can be added by issuing a POST:
```sh
curl -L http://127.0.0.1:12380/4 -XPOST -d http://127.0.0.1:42379
```

Then the new node can be started as the others were, using the --join option:
```sh
metaStore --id 4 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379,http://127.0.0.1:42379 --port 42380 --join
```

The new node should join the cluster and be able to service key/value requests.

We can remove a node using a DELETE request:
```sh
curl -L http://127.0.0.1:12380/3 -XDELETE
```

Node 3 should shut itself down once the cluster has processed this request.

## Design

The store consists of three components: a raft-backed key-value store, a REST API server, and a raft consensus server based on etcd's raft implementation.

The raft-backed key-value store is a key-value map that holds all committed key-values.
The store bridges communication between the raft server and the REST server.
Key-value updates are issued through the store to the raft server.
The store updates its map once raft reports the updates are committed.

The REST server exposes the current raft consensus by accessing the raft-backed key-value store.
A GET command looks up a key in the store and returns the value, if any.
A key-value PUT command issues an update proposal to the store.

The raft server participates in consensus with its cluster peers.
When the REST server submits a proposal, the raft server transmits the proposal to its peers.
When raft reaches a consensus, the server publishes all committed updates over a commit channel.
For store, this commit channel is consumed by the key-value store.

## Storage Modes

### Memory + WAL (Default)

- **Persistence**: Write-Ahead Log (WAL) + periodic snapshots
- **Storage Location**: `./metaStore-{id}/` (WAL), `./metaStore-{id}-snap/` (snapshots)
- **Use Case**: Fast performance, suitable for most scenarios
- **Data Loss**: Minimal (only uncommitted entries on crash)
- **Recovery**: Fast snapshot + WAL replay

### RocksDB Mode (Optional)

- **Persistence**: Full persistent storage with RocksDB backend
- **Storage Location**: `./data/{id}/` (RocksDB + snapshots in `./data/{id}/snap/`)
- **Use Case**: When you need guaranteed persistence of all data
- **Data Loss**: None (all data persisted to disk atomically)
- **Recovery**: Direct from RocksDB (faster for large datasets)
- **Requirements**: RocksDB C++ library, CGO enabled, built with `-tags=rocksdb`
- **Note**: The `data/` parent directory must exist before starting the node

## Performance Considerations

### Memory + WAL Mode
- **Pros**: Faster reads/writes, lower latency, no external dependencies
- **Cons**: Limited by available RAM for large datasets, slower recovery with large WAL

### RocksDB Mode
- **Pros**: Handles TB-scale datasets, faster recovery, guaranteed persistence, efficient compaction
- **Cons**: Slightly higher latency due to disk I/O, requires RocksDB library and CGO

## Command Line Options

```
--id int            Node ID (default: 1)
--cluster string    Comma-separated list of cluster peer URLs (default: "http://127.0.0.1:9021")
--port int          HTTP API port for key-value operations (default: 9121)
--join              Join an existing cluster (default: false)
--rocksdb           Use RocksDB storage (only available when built with -tags=rocksdb)
```

## Testing

### Run All Tests (Default Build)

```sh
go test -v
```

### Run RocksDB Tests (Requires RocksDB)

```sh
CGO_ENABLED=1 go test -v -tags=rocksdb
```

> **macOS users**: See [ROCKSDB_BUILD_MACOS.md](ROCKSDB_BUILD_MACOS.md) for SDK compatibility issues.

### Run Specific Tests

```sh
# Single node test
go test -v -run TestPutAndGetKeyValue

# Cluster test
go test -v -run TestProposeOnCommit

# Snapshot test
go test -v -run TestSnapshot

# RocksDB storage test (requires -tags=rocksdb)
CGO_ENABLED=1 go test -v -tags=rocksdb -run TestRocksDBStorage
```

## Fault Tolerance

A cluster of **N** nodes can tolerate up to **(N-1)/2** failures:

- 1 node: 0 failures (no fault tolerance)
- 3 nodes: 1 failure
- 5 nodes: 2 failures
- 7 nodes: 3 failures

## License

Apache License 2.0 (inherited from etcd)

## Documentation

- [ROCKSDB_BUILD_MACOS.md](ROCKSDB_BUILD_MACOS.md) - macOS RocksDB Build Guide (includes SDK compatibility solutions)
- [ROCKSDB_BUILD_MACOS_EN.md](ROCKSDB_BUILD_MACOS_EN.md) - macOS RocksDB Build Guide (English version)
- [ROCKSDB_TEST_GUIDE.md](ROCKSDB_TEST_GUIDE.md) - RocksDB Testing Guide
- [ROCKSDB_TEST_REPORT.md](ROCKSDB_TEST_REPORT.md) - RocksDB Test Report
- [ROCKSDB_3NODE_TEST_REPORT.md](ROCKSDB_3NODE_TEST_REPORT.md) - 3-Node Cluster Test Report
- [DIRECTORY_STRUCTURE_CHANGE_REPORT.md](DIRECTORY_STRUCTURE_CHANGE_REPORT.md) - Data Directory Structure Change Report
- [QUICKSTART.md](QUICKSTART.md) - Quick Start Guide
- [IMPLEMENTATION.md](IMPLEMENTATION.md) - Implementation Details



