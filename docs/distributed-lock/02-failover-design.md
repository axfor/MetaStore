# 分布式锁在主节点故障场景下的设计分析

## 用户需求

> "分布式锁需要保障 MetaStore 节点故障场景,尤其是主节点故障,导致主切换。整个过程客户端应该是无感的。"

## 当前架构分析

### 1. 服务端 - Raft 共识保证

#### Lease 状态持久化
- **当前实现**: ✅ Lease 通过 Raft 共识持久化到所有节点
  - `LeaseGrant` → Raft 提交 → 所有节点应用
  - `LeaseRevoke` → Raft 提交 → 所有节点应用

- **主节点故障时**:
  ```
  时间轴:
  t0: 主节点 (Node1) 持有 Lease {ID: 123, Keys: ["/lock/abc"]}
  t1: Node1 故障,Raft 开始选举
  t2: Node2 成为新 Leader
  t3: Node2 继承所有 Lease 状态 (已通过 Raft 同步) ✅
  ```

- **结论**: **服务端 Lease 状态在主切换时是连续的** ✅

#### Watch 机制
- **当前实现**: Watch 在每个节点独立维护
  - 客户端与 Node1 建立 Watch
  - Node1 故障,Watch 连接断开
  - **问题**: Watch 状态没有通过 Raft 同步 ❌

- **影响**:
  - 客户端需要重新创建 Watch
  - 可能错过主切换期间的事件

### 2. 客户端 - 故障感知与恢复

#### etcd 官方客户端的支持

**etcd client-go** 已经内置主切换支持:

```go
// 客户端配置多个 endpoints
cli, err := clientv3.New(clientv3.Config{
    Endpoints:   []string{"node1:2379", "node2:2379", "node3:2379"},
    DialTimeout: 5 * time.Second,
})
```

**自动重连机制**:
1. 客户端检测到连接断开
2. 自动尝试连接其他 endpoints
3. 新连接建立后,操作继续

**当前测试的问题**:
```go
// 测试只使用单个 endpoint
cli, err := clientv3.New(clientv3.Config{
    Endpoints:   []string{node.clientAddr}, // ❌ 单点
    DialTimeout: 5 * time.Second,
})
```

#### Session 在主切换时的行为

**场景 1: Session KeepAlive 期间主节点故障**

```
时间轴:
t0: Client 持有 Session (Lease 123) 连接到 Node1
t1: Client 发送 KeepAlive 请求
t2: Node1 故障,请求失败
t3: Client 自动重连到 Node2 (新 Leader)
t4: Client 重新发送 KeepAlive 到 Node2
t5: Node2 续约 Lease 123 ✅ (状态已同步)
```

**结论**: **Session KeepAlive 可以无缝切换** ✅ (如果客户端配置了多个 endpoints)

**场景 2: Mutex Lock 等待期间主节点故障**

```
时间轴:
t0: Client1 持有锁 (连接到 Node1)
t1: Client2 发起 Lock 请求,进入等待 (Watch Node1)
t2: Node1 故障
t3: Client2 的 Watch 连接断开
t4: Client2 Watch 错误: "connection closed"
t5: ❌ Client2 Lock 失败,返回错误
```

**问题**: **Watch 断开会导致 Lock 失败** ❌

## 需要改进的地方

### 1. Mutex 实现增强 - Watch 自动重连

当前 `Mutex.Lock` 的 Watch 代码:

```go
// pkg/concurrency/mutex.go:164
wch := client.Watch(watchCtx, lastKey, clientv3.WithRev(resp.Header.Revision))

for wresp := range wch {
    if wresp.Canceled {
        watchCancel()
        return errors.New("watch canceled") // ❌ 直接返回错误
    }
    // ...
}
```

**改进建议**:

```go
func (m *Mutex) waitDeletes(ctx context.Context, myKey string, myRev int64) error {
    client := m.s.client
    getOpts := append(clientv3.WithLastCreate(), clientv3.WithMaxCreateRev(myRev-1))

    for {
        // 检查 session 和 context
        select {
        case <-m.s.Done():
            return errors.New("session expired")
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        resp, err := client.Get(ctx, m.pfx, getOpts...)
        if err != nil {
            // ✅ 处理网络错误,重试
            if isNetworkError(err) {
                time.Sleep(100 * time.Millisecond)
                continue
            }
            return err
        }

        if len(resp.Kvs) == 0 {
            return nil
        }

        lastKey := string(resp.Kvs[0].Key)

        // ✅ Watch 失败后自动重试
        err = m.watchKeyDeletion(ctx, lastKey, resp.Header.Revision)
        if err != nil {
            if isNetworkError(err) || isWatchCanceled(err) {
                // 网络错误或 Watch 被取消,重新检查锁状态
                continue
            }
            return err
        }
    }
}

func (m *Mutex) watchKeyDeletion(ctx context.Context, key string, revision int64) error {
    watchCtx, watchCancel := context.WithCancel(ctx)
    defer watchCancel()

    wch := m.s.client.Watch(watchCtx, key, clientv3.WithRev(revision))

    for wresp := range wch {
        if wresp.Canceled {
            // ✅ Watch 被取消,但不立即失败,让外层循环重试
            if wresp.Err() != nil {
                return wresp.Err()
            }
            return errors.New("watch canceled")
        }
        for _, ev := range wresp.Events {
            if ev.Type == clientv3.EventTypeDelete {
                return nil // ✅ 键被删除
            }
        }
    }
    return errors.New("watch channel closed")
}
```

### 2. 测试改进 - 多节点集群测试

**当前测试**:
```go
func startRocksDBLockTestServer(t *testing.T) (*clientv3.Client, func()) {
    node, cleanup := startRocksDBNode(t, 1) // ❌ 单节点

    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   []string{node.clientAddr}, // ❌ 单 endpoint
        DialTimeout: 5 * time.Second,
    })
    // ...
}
```

**改进建议**:

```go
// 新增: 启动 3 节点集群用于故障测试
func startRocksDBClusterForLockTest(t *testing.T) ([]*testNode, *clientv3.Client, func()) {
    nodes := make([]*testNode, 3)
    endpoints := make([]string, 3)

    for i := 0; i < 3; i++ {
        node, _ := startRocksDBNode(t, i+1)
        nodes[i] = node
        endpoints[i] = node.clientAddr
    }

    // ✅ 配置多个 endpoints
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   endpoints,
        DialTimeout: 5 * time.Second,
    })
    require.NoError(t, err)

    cleanup := func() {
        for _, node := range nodes {
            node.cleanup()
        }
        cli.Close()
    }

    return nodes, cli, cleanup
}

// 新测试: 主节点故障时的锁行为
func TestRocksDB_MutexDuringLeaderFailover(t *testing.T) {
    nodes, cli, cleanup := startRocksDBClusterForLockTest(t)
    defer cleanup()
    ctx := context.Background()

    // Session1 获取锁
    session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
    require.NoError(t, err)
    defer session1.Close()

    mutex1 := concurrency.NewMutex(session1, "/test/failover")
    err = mutex1.Lock(ctx)
    require.NoError(t, err)

    // Session2 等待锁
    session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
    require.NoError(t, err)
    defer session2.Close()

    mutex2 := concurrency.NewMutex(session2, "/test/failover")

    acquired := make(chan struct{})
    go func() {
        err := mutex2.Lock(ctx)
        if err == nil {
            close(acquired)
        }
    }()

    time.Sleep(500 * time.Millisecond)

    // ✅ 模拟 Leader 故障
    leaderNode := findLeaderNode(nodes)
    t.Logf("Stopping leader node: %d", leaderNode.id)
    stopNode(leaderNode)

    // ✅ 等待新 Leader 选举
    time.Sleep(3 * time.Second)

    // ✅ Session1 释放锁
    err = mutex1.Unlock(ctx)
    require.NoError(t, err)

    // ✅ Session2 应该能获取锁 (即使 Leader 已切换)
    select {
    case <-acquired:
        t.Log("Session2 acquired lock after leader failover")
        assert.True(t, mutex2.IsOwner())
    case <-time.After(10 * time.Second):
        t.Fatal("Session2 should acquire lock after leader failover")
    }

    mutex2.Unlock(ctx)
}
```

## 实现路线图

### 阶段 1: 增强客户端容错 (推荐优先实现)

1. ✅ 修改 Mutex.waitDeletes 支持 Watch 自动重试
2. ✅ 添加网络错误检测和重试逻辑
3. ✅ 测试客户端配置多个 endpoints

### 阶段 2: 多节点集群测试

1. ✅ 实现 startRocksDBClusterForLockTest
2. ✅ 添加 TestRocksDB_MutexDuringLeaderFailover
3. ✅ 验证主节点故障时客户端无感知

### 阶段 3: 高级功能 (可选)

1. ⚠️ Watch 状态通过 Raft 同步 (复杂度高,收益有限)
2. ✅ Session 自动重连到新 Leader
3. ✅ Lease 过期检测在新 Leader 继续工作

## 结论

**当前状态评估**:

| 场景 | 是否支持 | 说明 |
|------|---------|------|
| Lease 状态持久化 | ✅ 完全支持 | 通过 Raft 共识 |
| Session KeepAlive 重连 | ✅ etcd 客户端支持 | 需配置多 endpoints |
| Mutex Lock 主切换 | ⚠️ 部分支持 | Watch 断开会导致失败 |
| 客户端无感知 | ⚠️ 需改进 | 需增强 Watch 重试逻辑 |

**推荐方案**:
1. **优先**: 增强 `Mutex.waitDeletes` 的 Watch 重试逻辑 ← **最有效**
2. **其次**: 添加多节点故障恢复测试
3. **可选**: 实现更复杂的 Watch 状态同步

**预期效果**:
- ✅ 主节点故障时,客户端自动重连
- ✅ Lock 等待过程中主切换,Watch 自动重建
- ✅ Session KeepAlive 无缝切换到新 Leader
- ✅ 锁状态保持一致性
