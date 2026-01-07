# RocksDB Third-Party Libraries

This directory contains RocksDB libraries and dependencies bundled for different platforms.

## Quick Start

The easiest way to build RocksDB dependencies is using the Makefile:

```bash
# Build RocksDB 10.4.2 and dependencies from source
make rocksdb-deps

# This will:
# 1. Download RocksDB 10.4.2 source code
# 2. Compile static library (takes 10-20 minutes)
# 3. Copy compression libraries (lz4, zstd, snappy)
# 4. Install everything to third_party/rocksdb/<os-arch>/
```

After running `make rocksdb-deps`, you can build the project:

```bash
make build
```

## Directory Structure

```
third_party/rocksdb/
├── src/                   # RocksDB source code (v10.4.2)
│   ├── include/           # Source headers
│   ├── *.cc               # Source files
│   └── librocksdb.a       # Built static library
├── darwin-arm64/          # macOS Apple Silicon (M1/M2/M3)
│   ├── include/
│   │   └── rocksdb/       # Installed headers
│   └── lib/               # Installed libraries
│       ├── librocksdb.a   # Static library (~27MB)
│       ├── libzstd.a
│       ├── liblz4.a
│       └── libsnappy.a
├── darwin-x86_64/         # macOS Intel
├── linux-x86_64/          # Linux x86_64
└── linux-aarch64/         # Linux ARM64
```

**Source Code**: The `src/` directory contains the RocksDB source code (v10.4.2) and can be committed to git for offline builds.

**Build Artifacts**: Compiled `.o` files and intermediate build products are git-ignored.

## Supported Versions

- **RocksDB**: 10.4.2 (compatible with grocksdb v1.10.2)
- **grocksdb**: v1.10.2 (Go wrapper)

## Current Support

- ✅ macOS ARM64 (darwin-arm64)
- ✅ macOS Intel (darwin-x86_64) - via `make rocksdb-deps`
- ✅ Linux x86_64 - via `make rocksdb-deps`
- ✅ Linux ARM64 - via `make rocksdb-deps`

## Libraries Included

### RocksDB
- librocksdb.a (static library, ~27MB)

### Compression Libraries (Dependencies)
- liblz4.a - Fast compression (~150KB)
- libzstd.a - Zstandard compression (~732KB)
- libsnappy.a - Google's compression library (~37KB)

### System Libraries (not bundled)
- libbz2 - Available on macOS/Linux by default
- libz (zlib) - Available on macOS/Linux by default

## Manual Build Instructions

### Option 1: Using Makefile (Recommended)

```bash
# Build for your current platform
make rocksdb-deps

# Clean source code (keeps built libraries)
make rocksdb-deps-clean
```

### Option 2: Manual Build

#### For macOS (Intel or ARM)

```bash
# Install dependencies
brew install lz4 zstd snappy

# Download and build RocksDB (source will be at third_party/rocksdb/src)
mkdir -p third_party/rocksdb
cd third_party/rocksdb
git clone --depth 1 --branch v10.4.2 https://github.com/facebook/rocksdb.git src
cd src
make static_lib -j$(sysctl -n hw.ncpu)

# Install to platform directory
mkdir -p ../darwin-arm64/{include,lib}
cp -r include/rocksdb ../darwin-arm64/include/
cp librocksdb.a ../darwin-arm64/lib/
cp $(brew --prefix lz4)/lib/liblz4.a ../darwin-arm64/lib/
cp $(brew --prefix zstd)/lib/libzstd.a ../darwin-arm64/lib/
cp $(brew --prefix snappy)/lib/libsnappy.a ../darwin-arm64/lib/
```

#### For Linux x86_64

```bash
# Install build dependencies
sudo apt-get update
sudo apt-get install -y build-essential libgflags-dev libsnappy-dev \
    zlib1g-dev libbz2-dev liblz4-dev libzstd-dev

# Download and build RocksDB (source will be at third_party/rocksdb/src)
mkdir -p third_party/rocksdb
cd third_party/rocksdb
git clone --depth 1 --branch v10.4.2 https://github.com/facebook/rocksdb.git src
cd src
make static_lib -j$(nproc)

# Install to platform directory
mkdir -p ../linux-x86_64/{include,lib}
cp -r include/rocksdb ../linux-x86_64/include/
cp librocksdb.a ../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/liblz4.a ../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/libzstd.a ../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/libsnappy.a ../linux-x86_64/lib/
```

## Remote Linux Server Setup

For the remote Linux server at 159.75.78.179:7722:

```bash
# SSH to server
ssh -p 7722 user@159.75.78.179

# Install dependencies
sudo apt-get update
sudo apt-get install -y git build-essential libgflags-dev libsnappy-dev \
    zlib1g-dev libbz2-dev liblz4-dev libzstd-dev

# Clone the repository
git clone <your-repo-url>
cd MetaStore

# Build RocksDB dependencies
make rocksdb-deps

# Build the project
make build
```

## Build System Integration

The Makefile automatically:
- Detects OS: Darwin (macOS), Linux, Windows
- Detects Architecture: arm64, x86_64, etc.
- Sets CGO_CFLAGS and CGO_LDFLAGS to use bundled libraries
- Links static libraries to avoid runtime dependencies

## Verification

After building, verify the binary has no dynamic RocksDB dependencies:

```bash
# Build the project
make build

# Check dependencies (macOS)
otool -L metaStore | grep -E "rocksdb|zstd|lz4|snappy"
# Should return nothing

# Check dependencies (Linux)
ldd metaStore | grep -E "rocksdb|zstd|lz4|snappy"
# Should return nothing
```

## Troubleshooting

### Build fails with "command not found"

Make sure you have required tools:
- **macOS**: `xcode-select --install`
- **Linux**: `sudo apt-get install build-essential git`

### Compilation is slow

RocksDB compilation is CPU-intensive and takes 10-20 minutes. The Makefile automatically uses all available CPU cores (`-j` flag).

### Missing compression libraries

The Makefile will try to:
1. Use Homebrew on macOS
2. Use system libraries on Linux
3. Warn if libraries are not found

Install manually if needed:
- **macOS**: `brew install lz4 zstd snappy`
- **Ubuntu/Debian**: `sudo apt-get install liblz4-dev libzstd-dev libsnappy-dev`
- **CentOS/RHEL**: `sudo yum install lz4-devel libzstd-devel snappy-devel`

## Clean Up

```bash
# Remove source code (keeps built libraries)
make rocksdb-deps-clean

# Or manually clean the build artifacts
cd third_party/rocksdb/src && make clean

# Remove everything including source code and built libraries
rm -rf third_party/rocksdb/src/ third_party/rocksdb/*/
```

## Offline Build Support

The source code in `third_party/rocksdb/src/` can be committed to your git repository, enabling offline builds:

1. **First time (online)**: Run `make rocksdb-deps` to download and compile
2. **Commit**: The source code will be in `third_party/rocksdb/src/`
3. **Offline builds**: On other machines, just run `make rocksdb-deps` (no download needed)

Build artifacts (`.o`, `.a` files in `src/`) are automatically git-ignored.
