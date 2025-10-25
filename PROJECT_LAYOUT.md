# 项目结构说明

本项目已按照 [golang-standards/project-layout](https://github.com/golang-standards/project-layout) 标准进行重构。

## 目录结构

```
MetaStore/
├── cmd/                    # 主应用程序入口
│   └── metastore/         # metastore 可执行程序
│       └── main.go        # 应用程序入口点
│
├── internal/              # 私有应用程序和库代码
│   ├── store/             # KV存储层
│   │   ├── store.go       # 存储接口定义和共享类型
│   │   ├── memory.go      # 内存存储实现
│   │   └── rocksdb.go     # RocksDB存储实现
│   │
│   ├── raft/              # Raft共识层
│   │   ├── node.go        # Raft节点(内存模式)
│   │   ├── node_rocksdb.go # Raft节点(RocksDB模式)
│   │   └── listener.go    # 网络监听器
│   │
│   ├── http/              # HTTP服务层
│   │   └── api.go         # HTTP API处理
│   │
│   └── storage/           # 底层存储引擎
│       └── rocksdb.go     # RocksDB存储引擎实现
│
├── test_backup/           # 旧测试文件备份
├── data/                  # 数据目录(运行时创建)
├── Makefile               # 构建配置
├── go.mod                 # Go模块定义
├── go.sum                 # Go依赖校验
└── README.md              # 项目说明文档
```

## 目录说明

### `/cmd`
包含项目的主要应用程序。目录名应该与可执行文件名匹配。
- `cmd/metastore/main.go`: MetaStore 的主入口点,负责命令行参数解析和应用程序初始化

### `/internal`
私有应用程序和库代码。这是不希望其他人在其应用程序或库中导入的代码。

#### `/internal/store` - 存储层
包含KV存储的接口定义和实现:
- `store.go`: 定义 `Store` 接口和共享类型 (`Commit`, `KV`)
- `memory.go`: 基于内存的KV存储实现 (`Memory` 类型)
- `rocksdb.go`: 基于RocksDB的持久化KV存储实现 (`RocksDB` 类型)

#### `/internal/raft` - Raft共识层
实现Raft分布式共识协议:
- `node.go`: 内存模式的Raft节点实现 (`raftNode` 类型)
- `node_rocksdb.go`: RocksDB模式的Raft节点实现 (`raftNodeRocks` 类型)
- `listener.go`: TCP网络监听器,支持优雅停止

#### `/internal/http` - HTTP服务层
提供HTTP REST API:
- `api.go`: HTTP API处理器,实现GET/PUT/POST/DELETE操作

#### `/internal/storage` - 底层存储引擎
低层存储引擎实现:
- `rocksdb.go`: RocksDB存储引擎封装,实现Raft日志和状态的持久化

## 与原结构的对比

### 重构前
```
MetaStore/
├── main.go
├── kvstore.go
├── kvstore_rocks.go
├── raft.go
├── raft_rocks.go
├── rocksdb_storage.go
├── httpapi.go
├── listener.go
└── ...
```

所有代码都在根目录下的 `main` 包中。

### 重构后
```
MetaStore/
├── cmd/metastore/main.go              # 应用入口
└── internal/                           # 核心实现(按功能分层)
    ├── store/                          # 存储层
    │   ├── store.go
    │   ├── memory.go
    │   └── rocksdb.go
    ├── raft/                           # 共识层
    │   ├── node.go
    │   ├── node_rocksdb.go
    │   └── listener.go
    ├── http/                           # API层
    │   └── api.go
    └── storage/                        # 存储引擎层
        └── rocksdb.go
```

- 应用入口和核心实现分离
- **按功能分层的包结构**:更清晰的职责划分
- 符合 Go 社区最佳实践

## 构建和运行

### 构建
```bash
make build
# 或者
go build -o metaStore ./cmd/metastore
```

### 运行
```bash
# 内存模式
./metaStore --storage memory --id 1 --port 9121

# RocksDB模式
./metaStore --storage rocksdb --id 1 --port 9121
```

### 使用 Makefile
```bash
# 构建
make build

# 运行单节点(内存模式)
make run-memory

# 运行单节点(RocksDB模式)
make run-rocksdb

# 启动3节点集群(内存模式)
make cluster-memory

# 启动3节点集群(RocksDB模式)
make cluster-rocksdb

# 停止所有节点
make stop-cluster
```

## 主要改动

1. **包结构重组**
   - 将 `main` 包移动到 `cmd/metastore/`
   - 核心代码按功能分层到 `internal/` 下的多个子包:
     - `store`: 存储接口和实现
     - `raft`: Raft共识协议
     - `http`: HTTP API服务
     - `storage`: 底层存储引擎

2. **接口设计**
   - 定义 `Store` 接口统一存储层API
   - 共享类型 (`Commit`, `KV`) 放在 `store` 包中
   - 避免循环依赖

3. **导出接口**
   - 将需要被其他包使用的类型和函数首字母大写导出
   - 如: `kvstore` → `Memory`, `newKVStore` → `NewMemory`
   - 如: `newRaftNode` → `NewNode`, `NewNodeRocksDB`

4. **构建配置**
   - 更新 Makefile 以使用新的 `cmd/metastore` 路径

5. **保持兼容性**
   - 保持了原有的功能和API不变
   - 二进制文件的使用方式完全相同

## 优势

1. **更好的代码组织**: 清晰的分层结构,按功能职责划分
2. **符合标准**: 遵循 Go 社区广泛采用的项目布局标准
3. **更好的封装**: 使用 `internal` 包限制外部访问
4. **更好的模块化**: 各层之间通过接口交互,降低耦合度
5. **可扩展性**: 为未来添加更多功能提供了良好的基础
6. **易于测试**: 清晰的包边界使单元测试更容易编写
7. **专业性**: 提升项目的专业度和可信度

## 注意事项

- `internal` 目录中的代码只能被项目内部导入
- 如需添加新的可执行程序,在 `cmd/` 下创建新目录
- 共享库代码可以放在根目录的包中或 `pkg/` 目录下
