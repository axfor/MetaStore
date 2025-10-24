# MetaStore is a Lightweight Distributed KV Store   
A Lightweight, distributed, high-performance Metadata management component that can replace heavy-resource systems like Zookeeper and ETCD. Supports integration as a library (so/lib) or as a single-process solution.

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

### Unified Build (Supports Both Storage Modes)

**Prerequisites:**
- Go 1.23 or higher
- CGO enabled
- RocksDB C++ library installed

**Linux:**

#### Option 1: Install from Package Manager (Recommended for Quick Setup)

```sh
# Debian/Ubuntu
sudo apt-get install librocksdb-dev

# RHEL/CentOS/Fedora
sudo yum install rocksdb-devel

# Build MetaStore
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
export CGO_ENABLED=1
go build -ldflags="-s -w" -o metaStore
```

#### Option 2: Build RocksDB from Source (Recommended for Latest Version)

```sh
# Install build dependencies
sudo yum install -y gcc-c++ make cmake git \
  snappy snappy-devel \
  zlib zlib-devel \
  bzip2 bzip2-devel \
  lz4-devel \
  zstd libzstd-devel \
  gflags-devel

# Install GCC 11 toolset (required for RocksDB)
sudo dnf install -y gcc-toolset-11
scl enable gcc-toolset-11 bash
echo "source /opt/rh/gcc-toolset-11/enable" >> ~/.bashrc
source ~/.bashrc

# Clone and build RocksDB v10.7.5
git clone --branch v10.7.5 https://github.com/facebook/rocksdb.git
cd rocksdb
make clean
make static_lib -j$(nproc)
sudo make install

# Return to MetaStore directory and build
cd /path/to/MetaStore
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
export CGO_ENABLED=1
go build -ldflags="-s -w" -o metaStore
```

> **Note**: Building RocksDB from source gives you the latest stable version (v10.7.5) with better performance and bug fixes. The package manager version may be older.

**macOS:**

#### Option 1: Install from Homebrew (Recommended for Quick Setup)

```sh
# Install RocksDB
brew install rocksdb

# Build MetaStore
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
export CGO_ENABLED=1
go build -ldflags="-s -w" -o metaStore
```

#### Option 2: Build RocksDB from Source

```sh
# Install build dependencies
brew install cmake snappy zlib bzip2 lz4 zstd gflags

# Clone and build RocksDB v10.7.5
git clone --branch v10.7.5 https://github.com/facebook/rocksdb.git
cd rocksdb
make clean
make static_lib -j$(sysctl -n hw.ncpu)
sudo make install

# Return to MetaStore directory and build
cd /path/to/MetaStore
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
export CGO_ENABLED=1
go build -ldflags="-s -w" -o metaStore
```

> **Note for macOS users**: If you encounter linking errors with Go 1.25+ on older SDK versions, see [ROCKSDB_BUILD_MACOS.md](ROCKSDB_BUILD_MACOS.md) for detailed troubleshooting steps.

**Windows:**

#### Option 1: Install from vcpkg (Recommended for Quick Setup)

```sh
# Install RocksDB using vcpkg
vcpkg install rocksdb:x64-windows

# Build MetaStore
$env:CGO_ENABLED=1
$env:CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
go build -ldflags="-s -w" -o metaStore.exe
```

#### Option 2: Build RocksDB from Source

```powershell
# Install dependencies (requires Visual Studio 2019 or later)
# Install CMake, Git, and required compression libraries via vcpkg
vcpkg install snappy:x64-windows zlib:x64-windows bzip2:x64-windows lz4:x64-windows zstd:x64-windows

# Clone and build RocksDB
git clone --branch v10.7.5 https://github.com/facebook/rocksdb.git
cd rocksdb
mkdir build
cd build
cmake .. -G "Visual Studio 16 2019" -A x64 -DCMAKE_BUILD_TYPE=Release
cmake --build . --config Release
cmake --install . --prefix "C:\rocksdb"

# Build MetaStore (adjust paths to match your installation)
cd \path\to\MetaStore
$env:CGO_ENABLED=1
$env:CGO_CFLAGS="-IC:\rocksdb\include"
$env:CGO_LDFLAGS="-LC:\rocksdb\lib -lrocksdb"
go build -ldflags="-s -w" -o metaStore.exe
```

The unified build produces a single binary that supports **both** memory and RocksDB storage modes. You can switch between storage engines at runtime using the `--storage` flag.

## Getting Started

### Running single node store

#### Memory + WAL Mode

```sh
# Start with memory + WAL storage (default)
metaStore --id 1 --cluster http://127.0.0.1:12379 --port 12380 --storage memory
```

#### RocksDB Mode

```sh
# Create data directory first
mkdir -p data

# Start with RocksDB storage
metaStore --id 1 --cluster http://127.0.0.1:12379 --port 12380 --storage rocksdb
```

The unified binary allows you to choose the storage engine at runtime using the `--storage` flag.

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


### Fault Tolerance

To test cluster recovery, first start a cluster and write a value "foo":
```sh
curl -L http://127.0.0.1:12380/my-key -XPUT -d foo
```

Next, remove a node and replace the value with "bar" to check cluster availability:

```sh
curl -L http://127.0.0.1:12380/my-key -XPUT -d bar
curl -L http://127.0.0.1:32380/my-key
```

Finally, bring the node back up and verify it recovers with the updated value "bar":
```sh
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
--storage string    Storage engine: "memory" or "rocksdb" (default: "memory")
```

The unified binary supports runtime storage engine selection. Both memory and RocksDB modes are always available.

## Testing

### Run All Tests

```sh
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"
export CGO_ENABLED=1
go test -v
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

# RocksDB storage test
go test -v -run TestRocksDBStorage
```

## Fault Tolerance

A cluster of **N** nodes can tolerate up to **(N-1)/2** failures:

- 1 node: 0 failures (no fault tolerance)
- 3 nodes: 1 failure
- 5 nodes: 2 failures
- 7 nodes: 3 failures

## Changelog

### v2.0.0 - Unified Storage Engine Architecture (2025-10-24)

**Breaking Changes:**
- Removed build tag separation between memory and RocksDB modes
- Single unified binary now supports both storage engines at runtime
- Simplified build process: no need for `-tags=rocksdb` flag

**New Features:**
- Runtime storage engine selection via `--storage` flag
- Unified `main.go` entry point for both memory and RocksDB modes
- Consistent command-line interface across all storage modes

**Improvements:**
- Simplified build process: single `go build` command
- No more separate binaries for different storage modes
- Easier maintenance with unified codebase
- Both storage engines always available in single binary

**Migration Guide:**
- Old: `go build -tags=rocksdb` → New: `go build` (with CGO and RocksDB libraries)
- Old: Binary selection at compile time → New: Runtime selection with `--storage` flag
- Default storage mode remains `memory` for backward compatibility

## License

Apache License 2.0 (inherited from etcd)

## Documentation

### User Guides
- [Quick Start Guide](docs/QUICKSTART.md) - 10-step tutorial to get started
- This README - Complete feature overview and API reference

### Technical Documentation
- [Implementation Details](docs/IMPLEMENTATION.md) - Architecture and design decisions
- [Project Summary](docs/PROJECT_SUMMARY.md) - Complete project overview
- [Files Checklist](docs/FILES_CHECKLIST.md) - Complete file inventory

### RocksDB Documentation
- [RocksDB Test Guide](docs/ROCKSDB_TEST_GUIDE.md) - How to run RocksDB tests in different environments
- [RocksDB Test Report](docs/ROCKSDB_TEST_REPORT.md) - Expected test results and performance benchmarks

### Development Guides
- [Git Commit Guide](docs/GIT_COMMIT.md) - How to commit changes to the project




