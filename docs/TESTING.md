# 测试指南

本文档说明如何运行 MetaStore 的各种测试。MetaStore 支持两种存储引擎（内存和RocksDB），所有测试都会覆盖这两种模式。

## 测试结构

```
MetaStore/
├── internal/
│   ├── store/
│   │   └── memory_test.go           # 内存存储单元测试
│   ├── raft/
│   │   └── node_test.go             # Raft节点单元测试
│   └── storage/
│       └── rocksdb_test.go          # RocksDB存储单元测试
└── test/
    └── integration_test.go           # 集成测试（多节点集群、HTTP API）
```

## 快速开始

### 运行所有测试（推荐）

```bash
make test
```

这会自动运行：
1. 内存存储层测试
2. Raft共识层测试
3. 集成测试（使用内存存储）
4. RocksDB存储层测试

## 测试命令说明

### 1. 运行所有测试

```bash
make test
```

### 2. 只运行单元测试

```bash
make test-unit
```

### 3. 只运行集成测试

```bash
make test-integration
```

### 4. 只运行RocksDB存储测试

```bash
make test-storage
```

### 5. 运行测试并生成覆盖率报告

```bash
make test-coverage
```

这会生成 `coverage.html` 文件，可以在浏览器中查看。

## 各层测试说明

### 1. Store 层测试 (`internal/store/memory_test.go`)

测试内存KV存储的基本功能：
- 快照创建和恢复
- 数据查找

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2" \
  go test ./internal/store/ -v -run TestMemory
```

### 2. Raft 层测试 (`internal/raft/node_test.go`)

测试Raft共识协议的消息处理：
- Snapshot消息处理
- ConfState更新

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2" \
  go test ./internal/raft/ -v
```

### 3. Storage 层测试 (`internal/storage/rocksdb_test.go`)

测试RocksDB存储引擎的低层操作：
- 基本操作（初始化、索引）
- 日志条目追加和检索
- Term查询
- HardState持久化
- 快照创建和应用
- 日志压缩
- 数据持久化

```bash
make test-storage
```

### 4. 集成测试 (`test/integration_test.go`)

测试多个组件的集成：
- **TestProposeOnCommit**: 3节点集群的提议和提交流程
- **TestCloseProposerBeforeReplay**: 在 WAL 重放前关闭节点
- **TestCloseProposerInflight**: 在提交进行中关闭节点
- **TestPutAndGetKeyValue**: HTTP API 的 PUT/GET 操作
- **TestAddNewNode**: 动态添加新节点到集群
- **TestSnapshot**: 快照触发机制

```bash
make test-integration
```

## 手动运行测试（高级）

如果需要手动控制测试执行，可以使用 `go test` 命令。由于项目依赖 RocksDB C++ 库，需要设置正确的 CGO 链接标志：

```bash
# 设置环境变量（方便多次使用）
export CGO_ENABLED=1
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2"

# 运行所有测试
go test ./internal/... ./test/ -v

# 运行RocksDB存储测试
go test -tags=rocksdb ./internal/storage/ -v

# 运行特定测试
go test ./internal/raft/ -v -run TestProcessMessages

# 运行测试并显示详细输出
go test ./test/ -v -timeout=60s
```

## 测试覆盖的存储引擎

MetaStore 支持两种存储引擎，测试会覆盖所有模式：

| 存储引擎 | 用途 | 测试覆盖 |
|---------|------|---------|
| **Memory + WAL** | 默认模式，快速启动，WAL持久化 | ✅ 单元测试 + 集成测试 |
| **RocksDB** | 完全持久化，适合生产环境 | ✅ 专门的存储层测试 |

### 内存存储引擎测试

- **测试文件**: `internal/store/memory_test.go`
- **集成测试**: `test/integration_test.go` (使用内存存储)
- **覆盖功能**:
  - KV存储
  - 快照创建和恢复
  - Raft日志持久化（WAL）

### RocksDB存储引擎测试

- **测试文件**: `internal/storage/rocksdb_test.go`
- **覆盖功能**:
  - Raft日志存储
  - HardState持久化
  - 快照管理
  - 日志压缩
  - 多会话持久化验证

所有测试确保两种存储引擎在运行时都能正常工作。

## 测试最佳实践

1. **运行所有测试**: 在每次提交前运行 `make test` 确保没有回归
2. **单元测试**: 优先编写单元测试，测试单个组件的功能
3. **集成测试**: 编写集成测试验证组件间的交互
4. **测试两种存储模式**: 确保功能在内存和RocksDB模式下都能工作

## 添加新测试

### 单元测试示例

在对应的包目录下创建 `*_test.go` 文件：

```go
package store

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestMemory_NewFeature(t *testing.T) {
    // Setup
    s := &Memory{kvStore: make(map[string]string)}

    // Test
    s.kvStore["key"] = "value"

    // Assert
    val, ok := s.Lookup("key")
    require.True(t, ok)
    require.Equal(t, "value", val)
}
```

### 集成测试示例

在 `test/` 目录下添加测试：

```go
package test

import (
    "testing"
    "metaStore/internal/raft"
    "metaStore/internal/store"
)

func TestNewIntegration(t *testing.T) {
    // 创建集群
    clus := newCluster(3)
    defer clus.closeNoErrors(t)

    // 测试逻辑
    // ...
}
```

## 故障排查

#### 问题：链接错误 (undefined reference to BZ2_*)

**原因**: RocksDB 编译时启用了 bz2 压缩，但链接时缺少 bz2 库。

**解决方案**:
1. 确保已安装 bz2 开发库：
   ```bash
   # Ubuntu/Debian
   sudo apt-get install libbz2-dev

   # CentOS/RHEL
   sudo yum install bzip2-devel

   # macOS
   brew install bzip2
   ```

2. 使用 Makefile（已包含正确的链接标志）：
   ```bash
   make test
   ```

#### 问题：测试超时

**解决方案**: 集成测试可能需要更长时间，使用 `-timeout` 标志：

```bash
make test-integration  # 已设置 60s 超时
```

#### 问题：端口冲突

**原因**: 集成测试使用固定端口（10000-10004），可能与其他进程冲突。

**解决方案**:
```bash
# 检查端口占用
lsof -i :10000-10004

# 停止冲突进程或修改测试代码中的端口
```

## 持续集成

在 CI 环境中运行测试的示例配置（GitHub Actions）：

```yaml
- name: Run tests
  run: |
    make test
```

## 测试数据清理

测试会在 `data/` 目录和 `/tmp/` 下创建临时文件，使用以下命令清理：

```bash
make clean
```
