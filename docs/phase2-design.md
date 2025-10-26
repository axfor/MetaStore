# Phase 2: etcd 兼容层 + Raft 集成设计

## 目标

将 Phase 1 的 etcd 兼容层与现有的 Raft 实现集成，实现：
- 分布式共识（多节点集群）
- 数据持久化（RocksDB）
- 完整的 etcd 兼容存储系统

## 架构设计

### 整体架构

```
┌─────────────────────────────────────────────────┐
│           etcd Client SDK                       │
└────────────────┬────────────────────────────────┘
                 │ gRPC
                 │
┌────────────────▼────────────────────────────────┐
│         pkg/etcdcompat (gRPC Server)            │
│  - KV/Watch/Lease/Maintenance Services          │
└────────────────┬────────────────────────────────┘
                 │
┌────────────────▼────────────────────────────────┐
│       internal/memory (Raft 集成版)             │
│  ┌──────────────────────────────────────────┐  │
│  │  proposeC ────▶ Raft ────▶ commitC      │  │
│  │       │                       │          │  │
│  │       ▼                       ▼          │  │
│  │   序列化操作            Apply & Notify   │  │
│  └──────────────────────────────────────────┘  │
└────────────────┬────────────────────────────────┘
                 │
         ┌───────┴────────┐
         │                │
    ┌────▼────┐      ┌────▼─────┐
    │  WAL    │      │ Snapshot │
    └─────────┘      └──────────┘
```

## 关键设计决策

### 1. 操作序列化

将 etcd 操作编码为 Raft log entries：

```go
type RaftOperation struct {
    Type     string            // "PUT", "DELETE", "TXN", "LEASE_GRANT", etc.
    Key      string
    Value    string
    LeaseID  int64
    RangeEnd string

    // For transaction
    Compares []Compare
    ThenOps  []Op
    ElseOps  []Op

    // Response channel (不序列化)
    ResponseC chan<- OpResponse
}
```

### 2. Raft 集成流程

#### Put 操作流程

```
1. Client -> gRPC Server: Put(key, value)
2. gRPC Server -> Store: PutWithLease()
3. Store: 序列化操作 -> proposeC
4. Raft: 共识达成 -> commitC
5. Store: 应用操作 + 更新 revision
6. Store: 触发 Watch 事件
7. Store: 返回响应
```

#### Watch 流程

```
1. Client -> gRPC Server: Watch(key)
2. Server: 创建 watch 订阅
3. 当 Put/Delete 通过 Raft commit:
   - Store 应用操作
   - Store 触发所有匹配的 watch
4. Watch event -> gRPC stream -> Client
```

### 3. Lease 集成

Lease 过期需要通过 Raft 共识：

```go
// LeaseManager 检测到过期
func (lm *LeaseManager) checkExpiredLeases() {
    for id, lease := range expiredLeases {
        // 通过 Raft propose 删除操作
        op := RaftOperation{
            Type: "LEASE_REVOKE",
            LeaseID: id,
        }
        store.Propose(op)
    }
}
```

### 4. Snapshot 支持

快照需要包含：
```go
type EtcdSnapshot struct {
    CurrentRevision int64
    KVData          map[string]*KeyValue
    Leases          map[int64]*Lease
    NextWatchID     int64
}
```

## 实现计划

### Step 1: 创建 MemoryEtcdRaft

基于 Phase 1 的 MemoryEtcd，添加 Raft 集成：

```go
type MemoryEtcdRaft struct {
    *MemoryEtcd                  // 嵌入 Phase 1 实现

    proposeC    chan<- string    // 发送 Raft 提案
    commitC     <-chan *Commit   // 接收 Raft 提交
    errorC      <-chan error
    snapshotter *snap.Snapshotter
}
```

关键方法：
- `readCommits()` - 从 Raft commit channel 读取并应用
- `propose()` - 序列化操作并发送到 proposeC
- `apply()` - 应用已提交的操作

### Step 2: 操作编码/解码

```go
// 编码操作为 Raft entry
func encodeOperation(op RaftOperation) string {
    // JSON 序列化
}

// 从 Raft entry 解码
func decodeOperation(data string) RaftOperation {
    // JSON 反序列化
}
```

### Step 3: 同步 vs 异步

**选项 A**：同步等待 Raft commit（推荐）
```go
func (m *MemoryEtcdRaft) PutWithLease(...) (int64, *KeyValue, error) {
    respC := make(chan OpResponse)
    op := RaftOperation{Type: "PUT", ResponseC: respC}

    m.propose(op)

    // 等待 commit 完成
    resp := <-respC
    return resp.Revision, resp.PrevKv, resp.Error
}
```

**选项 B**：乐观返回（当前 HTTP API 方式）
- 立即返回
- 不等待 commit
- 可能读到旧值

**决策**：使用选项 A，确保线性化一致性。

### Step 4: 更新 main.go

同时启动 HTTP 和 gRPC 服务器：

```go
func main() {
    // 创建 Raft 支持的存储
    store := memory.NewMemoryEtcdRaft(snapshotter, proposeC, commitC, errorC)

    // 启动 HTTP API (保持向后兼容)
    go httpapi.ServeHTTPKVAPI(store, httpPort, confChangeC, errorC)

    // 启动 etcd gRPC server
    etcdServer, _ := etcdcompat.NewServer(etcdcompat.ServerConfig{
        Store:   store,
        Address: grpcAddr,
    })
    go etcdServer.Start()

    select {}
}
```

### Step 5: RocksDB 实现

创建 `RocksDBEtcdRaft`，类似 MemoryEtcdRaft。

## 测试策略

### 单节点测试

```bash
# 启动单节点
./metastore --id=1 --port=9121 --grpc-addr=:2379 --cluster=http://localhost:9021

# 测试 etcd API
go test ./test/etcd_compatibility_test.go
```

### 3节点集群测试

```bash
# 启动 3 节点
./metastore --id=1 --port=9121 --grpc-addr=:2379 --cluster=http://localhost:9021,http://localhost:9022,http://localhost:9023
./metastore --id=2 --port=9122 --grpc-addr=:2380 --cluster=http://localhost:9021,http://localhost:9022,http://localhost:9023 --join
./metastore --id=3 --port=9123 --grpc-addr=:2381 --cluster=http://localhost:9021,http://localhost:9022,http://localhost:9023 --join

# 测试故障转移
etcdctl --endpoints=localhost:2379,localhost:2380,localhost:2381 put foo bar
# Kill leader
etcdctl --endpoints=localhost:2379,localhost:2380,localhost:2381 get foo
```

## 性能考虑

### 批量操作

Raft 可以批量提交，优化吞吐量：

```go
// 在 Raft tick 时批量处理
func (m *MemoryEtcdRaft) batchPropose() {
    ops := collectOps(100)  // 收集最多 100 个操作
    batchData := encodeOps(ops)
    m.proposeC <- batchData
}
```

### Watch 性能

- 使用缓冲 channel 避免阻塞
- 异步发送 watch 事件
- 限制每个 watch 的缓冲大小

## 兼容性

### 保持 HTTP API

现有的 HTTP API 继续工作：
- PUT /key -> 通过 Raft -> commitC -> 应用
- GET /key -> 直接读取（leader read）

### etcd 客户端

官方 etcd clientv3 可以直接使用：
```go
cli, _ := clientv3.New(clientv3.Config{
    Endpoints: []string{"localhost:2379", "localhost:2380", "localhost:2381"},
})
```

## 限制和未来工作

### Phase 2 不包括

- Auth/RBAC（Phase 3）
- Cluster 管理 API（Phase 3）
- 完整 MVCC 历史（保持简化）

### 已知限制

- Lease 过期仍然基于单节点检测（不是分布式协调）
- Watch 不支持历史事件回放
- 无 ReadIndex 优化（所有读都走 leader）

## 验收标准

Phase 2 完成标准：

1. ✅ 3 节点集群可以启动
2. ✅ etcd clientv3 可以连接到集群
3. ✅ Put/Get 操作正常
4. ✅ Watch 在所有节点上工作
5. ✅ Lease 在集群中正常工作
6. ✅ 节点故障后自动故障转移
7. ✅ 数据在重启后持久化（RocksDB）
8. ✅ 所有 Phase 1 测试在集群模式下通过

## 时间估算

- Step 1-2: MemoryEtcdRaft 实现 - 2-3 小时
- Step 3: 同步机制 - 1-2 小时
- Step 4: main.go 集成 - 1 小时
- Step 5: RocksDB 实现 - 2-3 小时
- 测试和调试 - 2-3 小时

**总计**: 8-12 小时

## 参考

- 现有 Raft 实现：`internal/raft/node.go`
- Phase 1 存储：`internal/memory/kvstore_etcd.go`
- 序列化参考：现有 `readCommits()` 方法
