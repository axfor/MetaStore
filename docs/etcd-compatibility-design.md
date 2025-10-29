# etcd 兼容层设计文档

## 1. 概述

本文档描述如何将 MetaStore 改造为在接口层 100% 兼容 etcd v3 API 的分布式键值存储系统。

### 1.1 目标

- 实现与 etcd v3 gRPC API 完全兼容的接口层
- 支持使用官方 etcd client SDK（如 `go.etcd.io/etcd/client/v3`）直接对接
- 保留现有 HTTP API 作为额外访问通道
- 保持代码结构遵循 `golang-standards/project-layout` 规范

### 1.2 范围

**必须实现的功能（最高优先级）：**
- KV 操作：Range（Get）、Put、Delete（单键/范围）
- Watch：创建、取消、事件类型（PUT, DELETE）、历史事件
- Lease：grant、revoke、keepalive（单次/流式）、租约绑定、过期行为
- Transaction（Txn）：Compare-Then-Else 语义
- Maintenance：status、health、snapshot、defragment
- 错误码和语义与 etcd 客户端期望一致

**可选实现：**
- Authentication/Authorization（用户/角色管理）
- Compact（压缩）和 Revision 语义
- Lock/Concurrency 高层接口

## 2. 架构设计

### 2.1 总体架构

```
┌─────────────────────────────────────────────────┐
│          etcd Client SDK (clientv3)             │
└────────────────┬────────────────────────────────┘
                 │ gRPC (etcd v3 protocol)
                 │
┌────────────────▼────────────────────────────────┐
│           pkg/etcdcompat                        │
│  ┌──────────────────────────────────────────┐  │
│  │  gRPC Server (etcd v3 API)               │  │
│  ├──────────────────────────────────────────┤  │
│  │  KV Service    │  Watch Service          │  │
│  │  Lease Service │  Txn Service            │  │
│  │  Maintenance   │  Auth Service (optional)│  │
│  └──────────────────────────────────────────┘  │
└────────────────┬────────────────────────────────┘
                 │
┌────────────────▼────────────────────────────────┐
│          Enhanced KV Store Interface            │
│  (支持 revision, lease, watch 等语义)            │
└────────────────┬────────────────────────────────┘
                 │
    ┌────────────┴────────────┐
    │                         │
┌───▼─────────┐      ┌────────▼─────────┐
│  Memory KV  │      │  RocksDB KV      │
│  + Raft     │      │  + Raft          │
└─────────────┘      └──────────────────┘
```

### 2.2 包结构设计

#### pkg/etcdcompat - etcd 兼容层核心
```
pkg/etcdcompat/
├── server.go              # gRPC server 初始化和管理
├── kv.go                  # KV service 实现
├── watch.go               # Watch service 实现
├── lease.go               # Lease service 实现
├── txn.go                 # Transaction service 实现
├── maintenance.go         # Maintenance service 实现
├── auth.go                # Auth service 实现（可选）
├── revision.go            # Revision 管理
└── errors.go              # 错误码映射
```

#### pkg/httpapi - HTTP API 独立包
```
pkg/httpapi/
├── server.go              # HTTP server
├── handler.go             # HTTP 请求处理器
└── middleware.go          # 中间件（日志、认证等）
```

### 2.3 实现方案选择

#### 方案 1：完整 etcd v3 gRPC 实现（推荐）

**描述：**
- 在 `pkg/etcdcompat` 中实现完整的 etcd v3 gRPC server
- 使用 etcd 官方的 proto 定义（`go.etcd.io/etcd/api/v3`）
- 实现所有核心服务：KV、Watch、Lease、Txn、Maintenance

**优点：**
- ✅ 100% API 兼容，客户端透明
- ✅ 支持所有 etcd 客户端 SDK
- ✅ 语义一致，行为可预测
- ✅ 易于扩展和维护

**缺点：**
- ❌ 实现复杂度高
- ❌ 开发和测试工作量大
- ❌ 需要深入理解 etcd 语义

**实现复杂度：** ⭐⭐⭐⭐ (4/5)

**推荐采用方案 1**，理由：满足 100% 兼容的核心需求，虽然实现复杂，但语义清晰，长期维护成本低。

## 3. 数据模型扩展

### 3.1 扩展 KeyValue 结构
```go
type KeyValue struct {
    Key            []byte
    Value          []byte
    CreateRevision int64  // 创建时的 revision
    ModRevision    int64  // 最后修改的 revision
    Version        int64  // 修改次数
    Lease          int64  // 关联的 lease ID
}
```

### 3.2 Revision 管理
- 全局 revision 计数器，每次写操作递增
- 需要持久化以支持快照恢复
- Watch 基于 revision 实现历史事件回放

### 3.3 Lease 数据结构
```go
type Lease struct {
    ID         int64
    TTL        int64           // 秒
    Remaining  int64           // 剩余时间
    GrantTime  time.Time       // 授予时间
    Keys       map[string]bool // 关联的 keys
}
```

### 3.4 扩展 Store 接口

现有接口需要扩展以支持 etcd 语义：

```go
type Store interface {
    // 原有方法
    Lookup(key string) (string, bool)
    Propose(k string, v string)
    GetSnapshot() ([]byte, error)

    // 新增：Range 查询支持前缀和范围
    Range(key, rangeEnd string, limit int64, revision int64) ([]*KeyValue, int64, error)

    // 新增：Put 返回 revision 和旧值
    PutWithLease(key, value string, leaseID int64) (revision int64, prevKV *KeyValue, error)

    // 新增：Delete 支持范围删除
    DeleteRange(key, rangeEnd string) (deleted int64, revision int64, error)

    // 新增：Transaction 支持原子操作
    Txn(cmps []Compare, thenOps []Op, elseOps []Op) (*TxnResponse, error)

    // 新增：Watch 支持
    Watch(key string, revision int64, watchID int64) (<-chan WatchEvent, error)
    CancelWatch(watchID int64) error

    // 新增：Compact
    Compact(revision int64) error
}
```

## 4. 关键实现细节

### 4.1 KV Service

```go
func (s *KVServer) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
    // 1. 验证参数
    // 2. 根据 key/rangeEnd 确定查询范围
    // 3. 从存储层获取数据
    // 4. 根据 revision 过滤（如果指定）
    // 5. 应用 limit
    // 6. 返回结果和当前 revision
}

func (s *KVServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
    // 1. 验证 lease ID（如果指定）
    // 2. 通过 Raft propose 写操作
    // 3. 等待 commit
    // 4. 递增 revision
    // 5. 触发相关 watch
    // 6. 返回新 revision 和旧值（如果请求）
}
```

### 4.2 Watch Service

Watch 使用流式 gRPC：

```go
func (s *WatchServer) Watch(stream pb.Watch_WatchServer) error {
    for {
        req, err := stream.Recv()

        if createReq := req.GetCreateRequest(); createReq != nil {
            watchID := s.watchManager.Create(createReq.Key, createReq.StartRevision)
            go s.sendEvents(stream, watchID)
        }

        if cancelReq := req.GetCancelRequest(); cancelReq != nil {
            s.watchManager.Cancel(cancelReq.WatchId)
        }
    }
}
```

### 4.3 Lease Service

```go
func (s *LeaseServer) LeaseGrant(ctx context.Context, req *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
    lease := &Lease{
        ID:  req.ID,
        TTL: req.TTL,
        GrantTime: time.Now(),
        Keys: make(map[string]bool),
    }
    s.leaseManager.Add(lease)
    go s.leaseManager.MonitorExpiry(lease.ID)
    return &pb.LeaseGrantResponse{ID: lease.ID, TTL: lease.TTL}, nil
}

func (s *LeaseServer) LeaseKeepAlive(stream pb.Lease_LeaseKeepAliveServer) error {
    for {
        req, err := stream.Recv()
        lease := s.leaseManager.Renew(req.ID)
        stream.Send(&pb.LeaseKeepAliveResponse{ID: lease.ID, TTL: lease.TTL})
    }
}
```

### 4.4 Transaction Service

```go
func (s *KVServer) Txn(ctx context.Context, req *pb.TxnRequest) (*pb.TxnResponse, error) {
    // 1. 评估所有 compare 条件
    success := true
    for _, cmp := range req.Compare {
        if !s.evaluateCompare(cmp) {
            success = false
            break
        }
    }

    // 2. 执行 then 或 else 分支
    var responses []*pb.ResponseOp
    if success {
        responses = s.executeOps(req.Success)
    } else {
        responses = s.executeOps(req.Failure)
    }

    return &pb.TxnResponse{
        Succeeded: success,
        Responses: responses,
    }, nil
}
```

### 4.5 错误处理

正确映射到 gRPC 状态码：

```go
var errorCodeMap = map[error]codes.Code{
    ErrKeyNotFound:      codes.NotFound,
    ErrCompacted:        codes.OutOfRange,
    ErrFutureRev:        codes.OutOfRange,
    ErrLeaseNotFound:    codes.NotFound,
    ErrTxnConflict:      codes.FailedPrecondition,
}

func mapError(err error) error {
    if code, ok := errorCodeMap[err]; ok {
        return status.Error(code, err.Error())
    }
    return status.Error(codes.Internal, err.Error())
}
```

## 5. 与 etcd 的差异和限制

### 5.1 已知限制

1. **MVCC 完整性**
   - **限制**：不支持完整的 MVCC 历史查询
   - **影响**：无法查询任意历史 revision 的数据
   - **替代方案**：未来扩展存储层支持多版本

2. **Compact 行为**
   - **限制**：压缩操作可能不会立即释放空间
   - **影响**：数据库大小可能持续增长
   - **替代方案**：定期快照+清理

3. **Lease 精度**
   - **限制**：过期检查基于轮询（1秒）
   - **影响**：实际过期时间可能有 ±1 秒误差
   - **替代方案**：可接受的误差范围

4. **Auth 功能**
   - **限制**：初期不实现完整的 Auth/RBAC
   - **影响**：没有细粒度权限控制
   - **替代方案**：使用网络层安全（TLS）

### 5.2 兼容性声明

- ✅ 完整的 KV 操作语义
- ✅ Watch 事件流
- ✅ Lease 生命周期管理
- ✅ Transaction 原子性
- ✅ Maintenance 基本操作
- ⚠️ 部分 MVCC 功能（仅当前版本）
- ❌ 完整的 Auth/RBAC（可选功能）

## 6. 测试策略

### 6.1 集成测试（使用官方 clientv3）

```go
func TestEtcdCompatibility(t *testing.T) {
    server := startMetaStoreServer()
    defer server.Stop()

    cli, err := clientv3.New(clientv3.Config{
        Endpoints: []string{"localhost:2379"},
    })
    require.NoError(t, err)
    defer cli.Close()

    t.Run("Put and Get", func(t *testing.T) {
        _, err := cli.Put(context.Background(), "foo", "bar")
        assert.NoError(t, err)

        resp, err := cli.Get(context.Background(), "foo")
        assert.NoError(t, err)
        assert.Equal(t, "bar", string(resp.Kvs[0].Value))
    })
}
```

### 6.2 符合性测试

10 个核心 API 用例：
1. Put/Get 基本操作
2. Range 查询（前缀、范围）
3. Delete 操作
4. Transaction（Compare-Then-Else）
5. Watch 事件订阅
6. Lease Grant 和 KeepAlive
7. Lease 过期触发删除
8. Watch 历史事件回放
9. Maintenance Status
10. Snapshot 操作

## 7. 实施计划

### Phase 1: 基础架构（1-2周）
- [ ] 创建 pkg/etcdcompat 和 pkg/httpapi 包结构
- [ ] 集成 etcd v3 proto 定义
- [ ] 实现 gRPC server 框架
- [ ] 扩展 kvstore 接口

### Phase 2: 核心功能（2-3周）
- [ ] 实现 KV Service（Range, Put, Delete）
- [ ] 实现 Revision 管理
- [ ] 实现 Watch Service
- [ ] 实现 Lease Service

### Phase 3: 高级功能（1-2周）
- [ ] 实现 Transaction Service
- [ ] 实现 Maintenance Service
- [ ] 错误处理和映射
- [ ] 迁移 HTTP API

### Phase 4: 测试和文档（1-2周）
- [ ] 单元测试（覆盖率 > 80%）
- [ ] 集成测试（使用 clientv3）
- [ ] 创建示例程序
- [ ] 完善文档

**总计：5-9 周**

## 8. 参考资料

- [etcd 官方文档](https://etcd.io/docs/)
- [etcd v3 API 参考](https://etcd.io/docs/v3.6/learning/api/)
- [etcd proto 定义](https://github.com/etcd-io/etcd/tree/main/api/etcdserverpb)
- [etcd clientv3 包](https://pkg.go.dev/go.etcd.io/etcd/client/v3)
