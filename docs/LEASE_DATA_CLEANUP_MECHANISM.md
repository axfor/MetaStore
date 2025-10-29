# Lease 数据清理机制详解

## 概述

Lease（租约）是 etcd 的重要特性，用于为 key-value 对提供自动过期清理功能。本文档详细说明了 MetaStore 中 Lease 的数据清理机制实现。

## 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                     etcd Client                              │
│          (创建 Lease, 关联 Key, KeepAlive)                    │
└───────────────────────┬─────────────────────────────────────┘
                        │ gRPC API
                        ▼
┌─────────────────────────────────────────────────────────────┐
│              LeaseManager (过期检查与清理)                     │
│  ┌──────────────────────────────────────────────────┐       │
│  │  expiryChecker (后台 Goroutine)                  │       │
│  │  ├─ Ticker: 每 1 秒检查一次                       │       │
│  │  ├─ 检查所有 Lease 是否过期                       │       │
│  │  └─ 调用 Revoke() 清理过期 Lease                 │       │
│  └──────────────────────────────────────────────────┘       │
└───────────────────────┬─────────────────────────────────────┘
                        │ Store Interface
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                Storage Layer (内存/RocksDB)                   │
│  ┌──────────────────────────────────────────────────┐       │
│  │  Lease 数据结构:                                  │       │
│  │  ├─ ID: Lease ID                                 │       │
│  │  ├─ TTL: 生存时间（秒）                           │       │
│  │  ├─ GrantTime: 授予时间                          │       │
│  │  └─ Keys: map[string]bool (关联的键集合)         │       │
│  └──────────────────────────────────────────────────┘       │
│                                                              │
│  LeaseRevoke() 执行:                                         │
│  1. 读取 Lease，获取所有关联的 Keys                          │
│  2. 删除所有关联的 Key-Value 对                              │
│  3. 删除 Lease 本身                                          │
└─────────────────────────────────────────────────────────────┘
```

## 核心组件

### 1. Lease 数据结构

**位置**: `internal/kvstore/types.go:139-177`

```go
type Lease struct {
    ID        int64              // Lease ID
    TTL       int64              // 生存时间（秒）
    GrantTime time.Time          // 授予时间
    Keys      map[string]bool    // 关联的键集合（追踪所有使用此 lease 的 key）
}

// IsExpired 检查租约是否已过期
func (l *Lease) IsExpired() bool {
    if l == nil {
        return true
    }
    elapsed := time.Since(l.GrantTime).Seconds()
    return elapsed >= float64(l.TTL)
}
```

**关键点**:
- `GrantTime` 记录 Lease 创建或最后续约的时间
- `IsExpired()` 通过比较当前时间与 GrantTime + TTL 来判断是否过期
- `Keys` map 追踪所有关联此 lease 的键（这是数据清理的关键）

### 2. LeaseManager (过期检查引擎)

**位置**: `pkg/etcdcompat/lease_manager.go`

#### 2.1 启动机制

```go
func (lm *LeaseManager) Start() {
    go lm.expiryChecker()  // 启动后台 goroutine
}
```

#### 2.2 定期检查循环

```go
func (lm *LeaseManager) expiryChecker() {
    ticker := time.NewTicker(1 * time.Second)  // 每秒检查一次
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            lm.checkExpiredLeases()  // 检查并清理过期 lease
        case <-lm.stopCh:
            return  // 优雅停止
        }
    }
}
```

**设计要点**:
- ⏰ **检查频率**: 每 1 秒检查一次
- 🔄 **独立 Goroutine**: 不阻塞主流程
- 🛑 **优雅退出**: 支持通过 stopCh 停止

#### 2.3 过期检查与清理

```go
func (lm *LeaseManager) checkExpiredLeases() {
    // 第一步：找出所有过期的 Lease ID（只读锁）
    lm.mu.RLock()
    expiredIDs := make([]int64, 0)
    for id, lease := range lm.leases {
        if lease.IsExpired() {
            expiredIDs = append(expiredIDs, id)
        }
    }
    lm.mu.RUnlock()

    // 第二步：撤销过期的 Lease（写操作）
    for _, id := range expiredIDs {
        if err := lm.Revoke(id); err != nil {
            log.Printf("Failed to revoke expired lease %d: %v", id, err)
        } else {
            log.Printf("Revoked expired lease %d", id)
        }
    }
}
```

**优化设计**:
- ✅ **读写分离**: 先用读锁收集过期 ID，再逐个撤销
- ✅ **错误容忍**: 单个 Lease 撤销失败不影响其他
- ✅ **日志记录**: 记录所有清理操作

### 3. LeaseRevoke (数据清理执行)

#### 3.1 LeaseManager 层

```go
func (lm *LeaseManager) Revoke(id int64) error {
    // 从内存缓存中删除
    lm.mu.Lock()
    _, ok := lm.leases[id]
    if ok {
        delete(lm.leases, id)
    }
    lm.mu.Unlock()

    if !ok {
        return ErrLeaseNotFound
    }

    // 委托给底层存储执行实际删除
    return lm.store.LeaseRevoke(id)
}
```

#### 3.2 内存存储实现

**位置**: `internal/memory/kvstore_etcd_watch_lease.go`

```go
func (m *MemoryEtcd) LeaseRevoke(id int64) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    // 获取 Lease
    lease, ok := m.leases[id]
    if !ok {
        return nil  // 已经被删除
    }

    // 删除所有关联的键
    for key := range lease.Keys {
        delete(m.kvData, key)  // 直接从 map 中删除
    }

    // 删除 Lease 本身
    delete(m.leases, id)

    return nil
}
```

#### 3.3 RocksDB 存储实现

**位置**: `internal/rocksdb/kvstore_etcd_raft.go:590-611`

```go
func (r *RocksDBEtcdRaft) leaseRevokeUnlocked(id int64) error {
    // 1. 获取 Lease 以找到关联的键
    lease, err := r.getLease(id)
    if err != nil {
        return err
    }
    if lease == nil {
        return nil  // 已删除
    }

    // 2. 删除所有关联的键
    for key := range lease.Keys {
        if err := r.deleteUnlocked(key, ""); err != nil {
            log.Printf("Failed to delete key %s during lease revoke: %v", key, err)
        }
    }

    // 3. 删除 Lease 本身
    dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
    return r.db.Delete(r.wo, dbKey)
}
```

**RocksDB 特殊处理**:
- 通过 Raft 提交保证分布式一致性
- 持久化到磁盘，崩溃恢复后仍有效

### 4. Lease 键追踪机制

当 key 与 lease 关联时，必须更新 lease 的 Keys map。

#### 4.1 内存版本

```go
func (m *MemoryEtcd) PutWithLease(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
    // ... 创建 KeyValue ...

    m.kvData[key] = kv

    // 关联到 lease
    if leaseID != 0 {
        if lease, ok := m.leases[leaseID]; ok {
            lease.Keys[key] = true  // 追踪这个 key
        }
    }

    return newRevision, prevKv, nil
}
```

#### 4.2 RocksDB 版本（本次修复）

**位置**: `internal/rocksdb/kvstore_etcd_raft.go:384-410`

```go
func (r *RocksDBEtcdRaft) putUnlocked(key, value string, leaseID int64) error {
    // ... 保存 KeyValue ...

    // 更新 lease 的键追踪（关键修复！）
    if leaseID != 0 {
        lease, err := r.getLease(leaseID)
        if err != nil {
            return fmt.Errorf("failed to get lease %d: %v", leaseID, err)
        }
        if lease != nil {
            // 添加 key 到 lease 的键集合
            if lease.Keys == nil {
                lease.Keys = make(map[string]bool)
            }
            lease.Keys[key] = true

            // 保存更新后的 lease
            // ... 序列化并写入数据库 ...
        }
    }

    return nil
}
```

## 完整的数据清理流程

### 场景：客户端创建一个 2 秒 TTL 的 Lease

```
时间线：
T=0s    客户端：cli.Grant(ctx, 2)
        └─> LeaseManager.Grant(id, 2)
            └─> Store.LeaseGrant(id, 2)
                创建 Lease{ID: 1, TTL: 2, GrantTime: T0, Keys: {}}

T=0.5s  客户端：cli.Put(ctx, "key1", "value1", WithLease(1))
        └─> Store.PutWithLease("key1", "value1", 1)
            ├─> 保存 KeyValue{Key: "key1", Value: "value1", Lease: 1}
            └─> 更新 Lease{Keys: {"key1": true}}

T=1s    LeaseManager.expiryChecker() 检查
        └─> checkExpiredLeases()
            └─> Lease.IsExpired() = false  (elapsed 1s < TTL 2s)
            └─> 无操作

T=2s    LeaseManager.expiryChecker() 检查
        └─> checkExpiredLeases()
            └─> Lease.IsExpired() = false  (elapsed 2s = TTL 2s, 还未超过)
            └─> 无操作

T=3s    LeaseManager.expiryChecker() 检查
        └─> checkExpiredLeases()
            └─> Lease.IsExpired() = true  (elapsed 3s > TTL 2s) ✓
            └─> LeaseManager.Revoke(1)
                └─> Store.LeaseRevoke(1)
                    ├─> 读取 Lease.Keys = {"key1": true}
                    ├─> 删除 key1 的 KeyValue
                    └─> 删除 Lease 本身

        结果：key1 被自动删除！
```

### 客户端验证

```go
// T=0s
leaseResp, _ := cli.Grant(ctx, 2)  // leaseID = 1

// T=0.5s
cli.Put(ctx, "key1", "value1", clientv3.WithLease(leaseResp.ID))

// T=1s
resp, _ := cli.Get(ctx, "key1")
// resp.Kvs[0].Value = "value1"  ✓ 存在

// T=3s (等待 lease 过期)
time.Sleep(3 * time.Second)

resp, _ = cli.Get(ctx, "key1")
// len(resp.Kvs) = 0  ✓ 已被自动删除
```

## 设计优势

### 1. 自动化清理

✅ **零手动干预**: 客户端无需主动删除，到期自动清理
✅ **资源释放**: 防止过期数据占用存储空间
✅ **一致性保证**: 所有关联键同时删除

### 2. 性能优化

✅ **批量检查**: 每秒检查所有 lease，批量清理
✅ **读写分离**: 使用读锁收集，减少锁竞争
✅ **异步执行**: 后台 goroutine，不阻塞主线程

### 3. 可靠性

✅ **错误容忍**: 单个 lease 清理失败不影响其他
✅ **持久化**: RocksDB 版本通过 Raft 保证数据一致性
✅ **日志记录**: 完整的操作日志便于调试

## 关键配置参数

| 参数 | 位置 | 默认值 | 说明 |
|------|------|--------|------|
| 检查间隔 | lease_manager.go:136 | 1 秒 | Ticker 间隔，控制检查频率 |
| TTL 精度 | - | 秒级 | 最小 TTL 单位为秒 |

## 潜在改进方向

### 短期优化

1. **动态检查间隔**: 根据最近 lease 的 TTL 动态调整检查频率
2. **最小堆优化**: 使用优先队列，只检查即将过期的 lease
3. **指标监控**: 添加过期清理的 metrics

### 长期优化

```go
// 优化示例：使用最小堆
type leaseHeap []*Lease

func (lm *LeaseManager) expiryChecker() {
    heap := leaseHeap(lm.leases)

    for {
        next := heap.Peek()
        if next == nil {
            time.Sleep(1 * time.Second)
            continue
        }

        waitTime := next.ExpiryTime().Sub(time.Now())
        time.Sleep(waitTime)

        lm.Revoke(next.ID)
    }
}
```

## 测试验证

### 单元测试

```bash
# 测试 Lease 过期自动清理
go test -v ./test -run TestLeaseExpiry
```

### 性能测试

```bash
# 创建 10000 个 lease，测试清理性能
go test -v ./test -run TestLeaseExpiryPerformance -count=1
```

## 总结

MetaStore 的 Lease 数据清理机制通过以下层次实现：

1. **Lease 结构**: 追踪关联的键集合（Keys map）
2. **LeaseManager**: 每秒检查并清理过期 lease
3. **LeaseRevoke**: 删除所有关联键 + 删除 lease 本身
4. **存储层**: 内存直接删除，RocksDB 通过 Raft 保证一致性

这种设计确保了：
- ✅ 自动化、无需人工干预
- ✅ 可靠、支持崩溃恢复
- ✅ 高效、后台异步处理
- ✅ 兼容 etcd 语义

---

**文档版本**: 1.0
**最后更新**: 2025-10-26
