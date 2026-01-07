# 分布式锁测试修复总结

## 问题描述

在 RocksDB 存储引擎的分布式锁测试中,有两个测试失败:

1. **TestRocksDB_MutexFIFOOrder**: FIFO 顺序测试失败,锁获取顺序不符合预期
2. **TestRocksDB_MutexReleaseOnSessionClose**: Session 关闭后锁释放测试失败,Session2 无法获取锁

## 根本原因

### 问题 1: FIFO 顺序问题
- **原因**: 测试代码并发创建 Session,导致 CreateRevision 根据 Raft 提交顺序分配,而非启动顺序
- **关键洞察**: 用户指出 "从 API 语义来说,写入的 API 返回后,我立马读取应该可以读到",这是线性一致性保证
- **解决方案**: 实现两阶段信号机制,确保 Session 按顺序创建:
  1. `startSignals`: 通知 goroutine 开始
  2. `sessionReady`: Session 创建完成(Lease 已通过 Raft 共识)
  3. 主线程等待每个 Session 创建完成后再启动下一个

### 问题 2: Session 关闭后锁无法释放
- **根本原因**: RocksDB 批量处理模式下,`LEASE_REVOKE` 操作没有触发 Watch 事件
- **症状**:
  - Session1 的租约成功撤销 (TTL = -1) ✅
  - Session1 的锁键成功删除 (exists = false) ✅
  - 但 Session2 的 Watch 没有收到删除事件通知 ❌

## 修复细节

### 修复 1: TestRocksDB_MutexFIFOOrder

```go
// 两阶段信号机制
startSignals := make([]chan struct{}, numClients)
sessionReady := make([]chan struct{}, numClients)

// 按顺序启动客户端
for i := 0; i < numClients; i++ {
    close(startSignals[i])
    <-sessionReady[i] // 等待 Session 创建完成
    time.Sleep(10 * time.Millisecond)
}
```

**结果**: 锁获取顺序 [0,1,2,3,4] ✅

### 修复 2: LEASE_REVOKE Watch 事件触发

**问题代码** ([internal/rocksdb/kvstore.go:365-371](internal/rocksdb/kvstore.go#L365-L371)):
```go
case "LEASE_REVOKE":
    if err := r.prepareLeaseRevokeBatch(batch, op.LeaseID); err != nil {
        // ... 错误处理,没有收集 Watch 事件
    }
```

**修复后** ([internal/rocksdb/kvstore.go:365-374](internal/rocksdb/kvstore.go#L365-L374)):
```go
case "LEASE_REVOKE":
    events, err := r.prepareLeaseRevokeBatch(batch, op.LeaseID)
    if err != nil {
        // ... 错误处理
        continue
    }
    watchEvents = append(watchEvents, events...) // 收集 Watch 事件
```

**修改 prepareLeaseRevokeBatch** ([internal/rocksdb/kvstore.go:896-950](internal/rocksdb/kvstore.go#L896-L950)):
```go
// 修改前: func prepareLeaseRevokeBatch(...) error
// 修改后: func prepareLeaseRevokeBatch(...) ([]kvstore.WatchEvent, error)

func (r *RocksDB) prepareLeaseRevokeBatch(batch *grocksdb.WriteBatch, leaseID int64) ([]kvstore.WatchEvent, error) {
    // ... 获取 lease

    var events []kvstore.WatchEvent

    // 删除所有关联的键,并为每个键准备 Watch 事件
    for key := range lease.Keys {
        prevKv, _ := r.getKeyValue(key)
        batch.Delete(dbKey)

        if prevKv != nil {
            newRevision, _ := r.incrementRevision()
            deletedKv := &kvstore.KeyValue{
                Key:            prevKv.Key,
                Value:          nil,
                CreateRevision: prevKv.CreateRevision,
                ModRevision:    newRevision,
                Version:        0,
                Lease:          0,
            }
            events = append(events, kvstore.WatchEvent{
                Type:     kvstore.EventTypeDelete,
                Kv:       deletedKv,
                PrevKv:   prevKv,
                Revision: newRevision,
            })
        }
    }

    return events, nil
}
```

## 测试结果

### 修复前
- ❌ TestRocksDB_MutexFIFOOrder: 顺序 [1,0,3,2,4] - 失败
- ❌ TestRocksDB_MutexReleaseOnSessionClose: 10秒超时 - 失败

### 修复后
- ✅ TestRocksDB_MutexFIFOOrder: 顺序 [0,1,2,3,4] - 通过 (33.08s)
- ✅ TestRocksDB_MutexReleaseOnSessionClose: Session2 成功获取锁 - 通过 (32.82s)

## 关键技术点

1. **线性一致性 (Linearizability)**:
   - etcd API 保证写入返回后,立即读取能看到数据
   - Session 创建通过 Raft 共识,返回时 Lease 已持久化

2. **Watch 事件机制**:
   - 所有键删除操作必须触发 Watch 事件
   - 批量操作需要收集并发送所有 Watch 事件
   - PUT/DELETE/LEASE_REVOKE 在批量模式下必须一致处理

3. **Raft 共识**:
   - LEASE_REVOKE 通过 Raft 提交保证强一致性
   - 批量操作原子性: 一次 fsync 写入所有操作

## 修改的文件

1. [test/distributed_lock_rocksdb_test.go](test/distributed_lock_rocksdb_test.go):
   - TestRocksDB_MutexFIFOOrder: 实现两阶段信号机制
   - TestRocksDB_MutexReleaseOnSessionClose: 增强日志,验证租约和键状态

2. [internal/rocksdb/kvstore.go](internal/rocksdb/kvstore.go):
   - prepareLeaseRevokeBatch: 返回 Watch 事件
   - 批量处理 LEASE_REVOKE: 收集并触发 Watch 事件

## 对比: Memory vs RocksDB

| 特性 | Memory 引擎 | RocksDB 引擎 |
|------|------------|-------------|
| 测试通过率 | 40/40 (100%) | 24/24 (100%) ✅ |
| FIFO 顺序 | ✅ 正确 | ✅ 修复后正确 |
| Session 清理 | ✅ 立即生效 | ✅ 修复后立即生效 |
| Watch 事件 | ✅ 同步触发 | ✅ 修复后正确触发 |

## 下一步

用户提到的新需求:
> "分布式锁需要保障 MetaStore 节点故障场景,尤其是主节点故障,导致主切换。整个过程客户端应该是无感的。"

建议实现:
1. 客户端自动重连机制
2. Raft leader 切换时的锁状态迁移
3. 故障恢复测试用例
4. 客户端无感知主切换测试
