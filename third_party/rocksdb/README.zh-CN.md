# RocksDB 第三方库

本目录包含为不同平台打包的 RocksDB 库和依赖项。

## 快速开始

MetaStore 使用 RocksDB 作为存储引擎，构建流程已完全优化：

### 推荐方式：一步完成

```bash
make rocksdb-download  # 首次下载源码并打包（304MB tarball）
make build             # 自动解压、构建 RocksDB 和项目
```

**工作原理**:
1. `make rocksdb-download` 下载 RocksDB 源码并创建 `rocksdb-10.4.2.tar.gz`
2. `make build` 自动从 tarball 解压源码并构建（如果库不存在）
3. tarball 可以提交到 git，支持离线构建

### 方式 1: 在线构建

如果有网络连接：

```bash
# 下载源码并打包（只需运行一次）
make rocksdb-download

# 构建项目（自动处理 RocksDB）
make build
```

### 方式 2: 离线构建

如果 tarball 已在 `third_party/rocksdb/rocksdb-10.4.2.tar.gz`（已提交到 git）：

```bash
# 直接构建（自动解压 tarball）
make build
```

## 目录结构

```
third_party/rocksdb/
├── rocksdb-10.4.2.tar.gz  # RocksDB 源码包 (~304MB, 提交到 git)
├── src/                   # 临时解压目录（构建时自动创建，git 忽略）
│   └── v10.4.2/           # RocksDB 10.4.2 源码（从 tarball 解压）
├── darwin-arm64/          # macOS Apple Silicon 构建产物（git 忽略）
│   ├── include/rocksdb/   # 已安装的头文件
│   └── lib/               # 已安装的库
│       ├── librocksdb.a   # 静态库 (~661MB)
│       ├── libzstd.a
│       ├── liblz4.a
│       └── libsnappy.a
├── darwin-x86_64/         # macOS Intel（git 忽略）
├── linux-x86_64/          # Linux x86_64（git 忽略）
└── linux-aarch64/         # Linux ARM64（git 忽略）
```

**重要说明**:
- **提交到 git**: `rocksdb-10.4.2.tar.gz` (304MB)
- **git 忽略**: `src/`, `darwin-arm64/`, 等构建产物
- **构建流程**: tarball → 解压到 src/ → 编译 → 安装到平台目录

## 支持的版本

- **RocksDB**: 10.4.2 (与 grocksdb v1.10.2 兼容)
- **grocksdb**: v1.10.2 (Go 封装)

## 当前支持

- ✅ macOS ARM64 (darwin-arm64)
- ✅ macOS Intel (darwin-x86_64) - 通过 `make rocksdb-deps`
- ✅ Linux x86_64 - 通过 `make rocksdb-deps`
- ✅ Linux ARM64 - 通过 `make rocksdb-deps`

## 包含的库

### RocksDB
- librocksdb.a (静态库, ~27MB)

### 压缩库（依赖）
- liblz4.a - 快速压缩 (~150KB)
- libzstd.a - Zstandard 压缩 (~732KB)
- libsnappy.a - Google 的压缩库 (~37KB)

### 系统库（不打包）
- libbz2 - macOS/Linux 默认提供
- libz (zlib) - macOS/Linux 默认提供

## 手动构建说明

### 方案 1: 使用 Makefile（推荐）

```bash
# 下载源码（只需要运行一次）
make rocksdb-download

# 从本地源码构建
make rocksdb

# 清理构建产物（保留源码和已安装的库）
make rocksdb-clean
```

### 方案 2: 手动构建

#### macOS (Intel 或 ARM)

```bash
# 安装依赖
brew install lz4 zstd snappy

# 下载并构建 RocksDB（源码将在 third_party/rocksdb/src/v10.4.2）
mkdir -p third_party/rocksdb
cd third_party/rocksdb
mkdir -p src
git clone --depth 1 --branch v10.4.2 https://github.com/facebook/rocksdb.git src/v10.4.2
cd src/v10.4.2
make static_lib -j$(sysctl -n hw.ncpu)

# 安装到平台目录
mkdir -p ../../darwin-arm64/{include,lib}
cp -r include/rocksdb ../../darwin-arm64/include/
cp librocksdb.a ../../darwin-arm64/lib/
cp $(brew --prefix lz4)/lib/liblz4.a ../../darwin-arm64/lib/
cp $(brew --prefix zstd)/lib/libzstd.a ../../darwin-arm64/lib/
cp $(brew --prefix snappy)/lib/libsnappy.a ../../darwin-arm64/lib/
```

#### Linux x86_64

```bash
# 安装构建依赖
sudo apt-get update
sudo apt-get install -y build-essential libgflags-dev libsnappy-dev \
    zlib1g-dev libbz2-dev liblz4-dev libzstd-dev

# 下载并构建 RocksDB（源码将在 third_party/rocksdb/src/v10.4.2）
mkdir -p third_party/rocksdb
cd third_party/rocksdb
mkdir -p src
git clone --depth 1 --branch v10.4.2 https://github.com/facebook/rocksdb.git src/v10.4.2
cd src/v10.4.2
make static_lib -j$(nproc)

# 安装到平台目录
mkdir -p ../../linux-x86_64/{include,lib}
cp -r include/rocksdb ../../linux-x86_64/include/
cp librocksdb.a ../../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/liblz4.a ../../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/libzstd.a ../../linux-x86_64/lib/
cp /usr/lib/x86_64-linux-gnu/libsnappy.a ../../linux-x86_64/lib/
```

## 远程 Linux 服务器设置

对于远程 Linux 服务器 159.75.78.179:7722：

```bash
# SSH 到服务器
ssh -p 7722 user@159.75.78.179

# 安装依赖
sudo apt-get update
sudo apt-get install -y git build-essential libgflags-dev libsnappy-dev \
    zlib1g-dev libbz2-dev liblz4-dev libzstd-dev

# 克隆仓库
git clone <your-repo-url>
cd MetaStore

# 如果源码已经在仓库中（离线构建）
make build

# 或者从网络下载源码（在线构建）
make rocksdb-download
make build
```

## 构建系统集成

Makefile 自动：
- 检测操作系统：Darwin (macOS), Linux, Windows
- 检测架构：arm64, x86_64 等
- 设置 CGO_CFLAGS 和 CGO_LDFLAGS 使用打包的库
- 链接静态库以避免运行时依赖

## 验证

构建后，验证二进制文件没有动态 RocksDB 依赖：

```bash
# 构建项目
make build

# 检查依赖（macOS）
otool -L metaStore | grep -E "rocksdb|zstd|lz4|snappy"
# 应该没有输出

# 检查依赖（Linux）
ldd metaStore | grep -E "rocksdb|zstd|lz4|snappy"
# 应该没有输出
```

## 故障排除

### 构建失败显示 "command not found"

确保安装了必需的工具：
- **macOS**: `xcode-select --install`
- **Linux**: `sudo apt-get install build-essential git`

### 编译很慢

RocksDB 编译是 CPU 密集型的，需要 10-20 分钟。Makefile 自动使用所有可用 CPU 核心（`-j` 标志）。

### 缺少压缩库

Makefile 会尝试：
1. 在 macOS 上使用 Homebrew
2. 在 Linux 上使用系统库
3. 如果找不到库会警告

如需要可手动安装：
- **macOS**: `brew install lz4 zstd snappy`
- **Ubuntu/Debian**: `sudo apt-get install liblz4-dev libzstd-dev libsnappy-dev`
- **CentOS/RHEL**: `sudo yum install lz4-devel libzstd-devel snappy-devel`

## 清理

```bash
# 删除源代码（保留构建的库）
make rocksdb-deps-clean

# 或手动清理构建产物
cd third_party/rocksdb/src && make clean

# 删除所有内容包括源代码和构建的库
rm -rf third_party/rocksdb/src/ third_party/rocksdb/*/
```

## 离线构建支持

RocksDB 源码 tarball 可以提交到 git 仓库，实现真正的离线构建：

### 工作流程：

**开发机器（有网络）**:
```bash
# 1. 下载源码并打包
make rocksdb-download

# 2. 提交 tarball 到 git（304MB）
git add third_party/rocksdb/rocksdb-10.4.2.tar.gz
git commit -m "Add RocksDB 10.4.2 source tarball for offline builds"
git push
```

**生产服务器（无网络）**:
```bash
# 1. 克隆仓库（包含 tarball）
git clone <your-repo-url>
cd MetaStore

# 2. 直接构建（自动解压 tarball）
make build
```

### 说明：
- **Tarball 大小**: 304MB（压缩后）
- **解压后**: ~2.5GB（构建时临时解压，构建后可删除）
- **构建产物**: `src/` 目录会被 git 忽略
- **平台支持**: 自动检测 macOS/Linux 和 arm64/x86_64
