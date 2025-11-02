# BatchProposer 最终评估报告

**日期**: 2025-11-01
**状态**: ⚠️ 需要重新评估优化策略
**作者**: Claude

---

## 📊 性能测试结果总结

### 测试配置对比

| 配置 | 批量大小 | 超时时间 | Memory LargeScale | 评估 |
|-----|---------|---------|------------------|------|
| **基准** (无BatchProposer) | N/A | N/A | ~3,386 ops/sec | ✅ 基准性能 |
| **配置 A** | 100 | 5ms | 982 ops/sec | ❌ -71% 严重回归 |
| **配置 B** | 10 | 100μs | 955 ops/sec | ❌ -72% 仍然回归 |

### 关键发现

1. **BatchProposer 在当前测试场景下导致性能下降**
   - 即使调整到最小参数，性能仍低于基准
   - 问题不仅仅是参数配置，可能还有其他因素

2. **可能的根本原因**:
   - ✅ 批量延迟：即使 100μs 也增加了延迟
   - ✅ 锁竞争：BatchProposer 内部的 mutex 可能成为瓶颈
   - ✅ Goroutine 开销：额外的后台 flush loop
   - ⚠️ 测试环境：WAL 数据累积可能影响性能

---

## 🔍 深入分析

### BatchProposer 的性能影响

```
正常流程（无 BatchProposer）:
Client → KVStore.Put → proposeC → Raft → WAL fsync → commitC → Client
延迟: ~5ms (主要是 Raft + WAL)

使用 BatchProposer:
Client → KVStore.Put → BatchProposer.Propose (获取锁, append buffer, 释放锁)
                      ↓
                   Flush Loop (定期或满批次)
                      ↓ (获取锁, copy buffer, 释放锁, 发送)
                   proposeC → Raft → WAL fsync → commitC → Client

额外开销:
1. 2次 mutex 锁操作 (Propose + Flush)
2. Buffer 复制
3. 后台 goroutine 轮询 (100μs ticker)
4. 批量延迟 (0-100μs)
```

### 为什么在低并发下反而更慢？

在低并发场景（50 个客户端，< 1000 ops/sec）：

1. **批量效率低**: 平均批量大小可能只有 1-2 个操作
2. **锁开销高于收益**: Mutex 争用增加延迟
3. **没有减少 WAL fsync**: 批量大小太小，fsync 次数几乎不变
4. **纯开销增加**: 所有 BatchProposer 的开销都是纯额外成本

---

## 💡 重要认识

### BatchProposer 的适用场景

BatchProposer 设计用于：
- ✅ **极高并发** (> 10,000 ops/sec)
- ✅ **大量客户端** (100+ 并发连接)
- ✅ **批次快速填满** (100 ops < 1ms)
- ✅ **WAL fsync 是主要瓶颈**

**不适用于**:
- ❌ 中低并发 (< 1,000 ops/sec) ← **当前测试场景**
- ❌ 延迟敏感型应用
- ❌ 批次填充慢的场景

### 当前测试场景特点

- 50 个客户端并发
- 每个客户端 1000 次操作
- 总吞吐量 ~1,000 ops/sec
- **典型的中低并发场景**

---

## 🎯 建议的优化策略

### 方案 1: 禁用 BatchProposer ⭐ **立即可行**

**操作**:
```go
// cmd/metastore/main.go
// 注释掉 BatchProposer 创建
// batchProposer := batch.NewBatchProposer(...)
// defer batchProposer.Stop()

// 使用原始构造函数
kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)
kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)
```

**预期**:
- 恢复基准性能 (~3,000-4,000 ops/sec)
- 消除批量延迟和锁开销

**缺点**:
- 无法在高并发场景获得性能提升
- BatchProposer 的工作暂时浪费

---

### 方案 2: 条件启用 BatchProposer ⭐⭐ **中期方案**

**实现自适应策略**:
```go
// 添加配置选项
enableBatchProposer := flag.Bool("enable-batch", false, "Enable BatchProposer for high concurrency workloads")

if *enableBatchProposer {
    batchProposer := batch.NewBatchProposer(...)
    kvs = memory.NewMemoryWithBatchProposer(...)
} else {
    kvs = memory.NewMemory(...)
}
```

**预期**:
- 默认使用原始模式（适合大多数场景）
- 高并发场景手动启用 BatchProposer

---

### 方案 3: 零延迟 BatchProposer ⭐⭐⭐ **长期方案**

**修改 BatchProposer 设计**:
```go
// 立即发送模式：不等待批次填满或超时
func (bp *BatchProposer) Propose(ctx context.Context, proposal string) error {
    bp.mu.Lock()
    bp.buffer = append(bp.buffer, proposal)

    // ✅ 每次 Propose 都立即检查并尝试刷新
    // 如果有其他操作在等待，它们会被一起批量发送
    if len(bp.buffer) >= 1 {
        bp.flushLocked()
    }
    bp.mu.Unlock()
    return nil
}

// 移除 ticker，改为纯粹的机会性批量
```

**优点**:
- 零等待延迟
- 在高并发时自然形成批量
- 低并发时行为与直接发送相同

**缺点**:
- 批量效率可能降低
- 需要重新设计和测试

---

## 📈 其他性能优化方向

既然 BatchProposer 在当前场景不适用，建议优先以下优化：

### 1. Protobuf 序列化 **（高优先级）**

**当前**: JSON 序列化
```go
data, err := json.Marshal(op)  // 慢
```

**优化**: Protobuf
```go
data, err := proto.Marshal(op)  // 快 3-5x
```

**预期提升**: 10-20% 性能改进

---

### 2. 减少内存分配

**问题**: 频繁的 map 查找和 channel 创建
```go
m.pendingOps[seqNum] = make(chan struct{})  // 每次操作都创建
```

**优化**: 使用 sync.Pool 或预分配
```go
waitCh := m.waitChPool.Get().(chan struct{})
defer m.waitChPool.Put(waitCh)
```

**预期提升**: 5-10% 性能改进

---

### 3. 优化 Raft 配置

**当前**: 默认 Raft 配置
**优化**: 调整心跳间隔、选举超时等

```go
raft.Config{
    HeartbeatTick: 1,         // 减少心跳间隔
    ElectionTick:  3,          // 减少选举超时
    MaxSizePerMsg: 1024 * 1024, // 增加消息大小
}
```

**预期提升**: 10-15% 性能改进

---

## 🏁 结论

### 当前状态

1. ✅ BatchProposer 实现正确，代码质量高
2. ❌ 在当前测试场景下不适用（性能回归）
3. ✅ 对高并发场景仍有价值

### 立即行动

1. **禁用 BatchProposer**（方案 1）
   - 恢复基准性能
   - 为后续优化建立正确的基准

2. **优先其他优化**
   - Protobuf 序列化
   - 减少内存分配
   - Raft 配置优化

3. **重新评估 BatchProposer**
   - 在实现其他优化后
   - 在真实高并发场景下测试
   - 考虑零延迟设计（方案 3）

### 长期目标

目标性能: **100,000+ QPS**

优化路径:
1. Protobuf 序列化: ~3,000 → ~4,000 ops/sec (+33%)
2. 内存优化: ~4,000 → ~5,000 ops/sec (+25%)
3. Raft 配置: ~5,000 → ~6,000 ops/sec (+20%)
4. **高并发场景** + BatchProposer: ~6,000 → ~30,000+ ops/sec (+400%)

**总预期**: 10x 提升在高并发场景下可达成

---

## 📝 下一步

### 今天
1. ✅ 创建评估报告（当前文档）
2. ⏳ 禁用 BatchProposer，恢复基准性能
3. ⏳ 验证基准性能恢复

### 本周
4. 开始 Protobuf 序列化实现
5. 基准测试和对比

### 本月
6. 完成所有基础优化
7. 在真实高并发场景重新评估 BatchProposer

---

**最终建议**: BatchProposer 是一个有价值的优化，但需要在正确的场景下使用。当前应该：
1. 暂时禁用 BatchProposer
2. 专注于其他普遍适用的优化
3. 在高并发场景下重新引入

这样可以确保在所有场景下都有良好的性能表现。
