# Building RocksDB from Source

This guide shows how to build RocksDB 10.4.2 (compatible with grocksdb v1.10.2) from source.

## Quick Build

```bash
make rocksdb-deps
```

This single command will:
1. Download RocksDB 10.4.2 source code
2. Compile the static library (10-20 minutes)
3. Install headers and libraries to `third_party/rocksdb/<os-arch>/`
4. Copy compression library dependencies (lz4, zstd, snappy)

## What Gets Built

- **RocksDB**: v10.4.2 static library (~27MB)
- **Platform**: Automatically detected (darwin-arm64, linux-x86_64, etc.)
- **Dependencies**: lz4, zstd, snappy static libraries

## Platform-Specific Notes

### macOS
- Requires: Xcode Command Line Tools (`xcode-select --install`)
- Compression libs: Automatically fetched from Homebrew

### Linux
- Requires: build-essential, git
- Compression libs: Uses system libraries from `/usr/lib/`

## Example Usage

### First-time setup on a new machine:

```bash
# Clone repository
git clone <your-repo>
cd MetaStore

# Build RocksDB dependencies
make rocksdb-deps

# Build the project
make build

# Verify no dynamic dependencies
otool -L metaStore | grep rocksdb  # macOS (should be empty)
ldd metaStore | grep rocksdb       # Linux (should be empty)
```

### On a Linux server (e.g., 159.75.78.179:7722):

```bash
# Install build dependencies first
sudo apt-get update
sudo apt-get install -y git build-essential libgflags-dev \
    libsnappy-dev zlib1g-dev libbz2-dev liblz4-dev libzstd-dev

# Clone and build
git clone <your-repo>
cd MetaStore
make rocksdb-deps
make build
```

## Cleaning Up

```bash
# Remove only source code (keep built libraries)
make rocksdb-deps-clean

# Remove everything
rm -rf third_party/src/ third_party/rocksdb/
```

## Troubleshooting

**Build fails**: Make sure you have build tools installed
- macOS: `xcode-select --install`
- Linux: `sudo apt-get install build-essential git`

**Slow compilation**: Normal - RocksDB takes 10-20 minutes to compile

**Missing libraries**: Install compression libraries
- macOS: `brew install lz4 zstd snappy`
- Linux: `sudo apt-get install liblz4-dev libzstd-dev libsnappy-dev`

For more details, see [third_party/rocksdb/README.md](third_party/rocksdb/README.md)
