# BatchProposer 性能分析报告

**日期**: 2025-11-01
**状态**: ⚠️ 性能回归分析

---

## 问题概述

BatchProposer 集成后，性能测试显示**性能下降**而非预期的 5-10x 提升：

### 性能对比

| 测试场景 | 预期性能 | 实际性能 | 差异 |
|---------|---------|---------|------|
| Memory LargeScale | ~3,386 ops/sec | 982 ops/sec | ⬇️ **-71%** |
| Memory Sustained | ~1,500 ops/sec | 453 ops/sec | ⬇️ **-70%** |
| Memory MixedWorkload | ~1,455 ops/sec | 6,476 ops/sec | ⬆️ **+345%** (主要是 GET) |

### 根本原因分析

#### 问题 1: 批量延迟 vs. 操作延迟的冲突

**当前实现**:
```go
func (bp *BatchProposer) Propose(ctx context.Context, proposal string) error {
    bp.mu.Lock()
    bp.buffer = append(bp.buffer, proposal)

    if len(bp.buffer) >= bp.maxBatchSize {
        bp.flushLocked()  // ✅ 立即刷新
    } else {
        // ❌ 返回，但批次尚未发送到 Raft
        // 实际刷新要等到 ticker 触发 (最多 5ms)
    }
    bp.mu.Unlock()
    return nil
}
```

**问题**:
1. 操作被添加到 buffer 后，`Propose()` 立即返回
2. 调用者认为操作已提交，但实际上**还在 buffer 中**
3. 批次刷新要等到:
   - 批次满 (100 个操作) **或**
   - 超时触发 (最多 5ms)
4. 在低并发场景下，批次很少填满，所以每个操作都要等待 5ms

#### 问题 2: 测试场景不匹配

BatchProposer 的设计目标是**高吞吐量场景**:
- 每秒数万个并发操作
- 批次快速填满（100 个操作 < 5ms）
- WAL fsync 频率从 10,000+/sec 降至 100/sec

**当前测试场景**:
- LargeScale: 50 个客户端，每个 1000 次操作
- Sustained: 20 个客户端，30 秒内持续
- 并发度不足以让批次快速填满

**结果**:
- 批次平均大小可能只有 1-10 个
- 大部分刷新都是超时触发 (TimeoutFlushes)
- **5ms 延迟成为瓶颈**，而非优化点

---

## 性能回归原因总结

### 1. 延迟增加

**优化前**:
```
操作 → Raft Propose → WAL fsync (5ms) → Apply → 响应
总延迟: ~5-10ms
```

**优化后（低并发）**:
```
操作 → Buffer → 等待刷新 (0-5ms) → Raft Propose → WAL fsync (5ms) → Apply → 响应
总延迟: ~5-15ms (增加了 0-5ms 批量等待)
```

### 2. 吞吐量受限

在低并发下：
- 批次填充慢
- 超时刷新频繁
- 每次刷新只有少量操作
- **没有减少 WAL fsync 次数**

---

## 解决方案

### 方案 A: 自适应批量策略 ⭐ **推荐**

根据操作到达速率动态调整：

```go
// 高并发：使用批量（100 ops, 5ms）
// 低并发：立即刷新 或 使用小批量（10 ops, 100μs）

type AdaptiveBatchProposer struct {
    recentOpsRate float64  // 最近的操作速率

    // 高并发阈值: > 1000 ops/sec
    // 中并发阈值: 100-1000 ops/sec
    // 低并发: < 100 ops/sec
}

func (abp *AdaptiveBatchProposer) Propose(ctx context.Context, proposal string) error {
    if abp.recentOpsRate > 1000 {
        // 高并发：大批量 (100, 5ms)
        return abp.largeBatchProposer.Propose(ctx, proposal)
    } else if abp.recentOpsRate > 100 {
        // 中并发：小批量 (10, 500μs)
        return abp.smallBatchProposer.Propose(ctx, proposal)
    } else {
        // 低并发：立即发送
        return abp.directPropose(ctx, proposal)
    }
}
```

### 方案 B: 减小批量参数

将批量参数从 (100 ops, 5ms) 调整为 (10 ops, 100μs):

```go
// main.go
const (
    batchProposerMaxBatchSize = 10                    // 100 → 10
    batchProposerMaxBatchTime = 100 * time.Microsecond // 5ms → 100μs
)
```

**优点**:
- 减少延迟：0-100μs vs 0-5ms
- 仍能减少部分 WAL fsync

**缺点**:
- 批量效果减弱（理论提升从 100x 降至 10x）

### 方案 C: 立即刷新模式

添加 `ImmediateFlush` 配置选项：

```go
type BatchProposer struct {
    immediateMode bool  // 立即刷新模式（禁用批量）
}

func (bp *BatchProposer) Propose(ctx context.Context, proposal string) error {
    bp.mu.Lock()
    bp.buffer = append(bp.buffer, proposal)

    if bp.immediateMode || len(bp.buffer) >= bp.maxBatchSize {
        bp.flushLocked()
    }
    bp.mu.Unlock()
    return nil
}
```

### 方案 D: 等待式批量 (ProposeSync)

使用 `ProposeSync` 确保批次刷新完成：

```go
// memory/kvstore.go
func (m *Memory) propose(ctx context.Context, data string) error {
    if m.batchProposer != nil {
        // 等待批次刷新完成
        return m.batchProposer.ProposeSync(ctx, data)
    }
    // ...
}
```

**问题**:
- 降低批量效率
- 增加锁竞争

---

## 下一步行动

### 短期修复（今天）

1. **实施方案 B**: 减小批量参数
   ```bash
   # 修改 cmd/metastore/main.go
   batchProposerMaxBatchSize = 10
   batchProposerMaxBatchTime = 100 * time.Microsecond
   ```

2. **添加性能日志**: 记录批量统计
   ```go
   // 每次测试后打印
   batchProposer.PrintStats()
   ```

3. **重新运行性能测试**: 验证改进

### 中期优化（本周）

4. **实施方案 A**: 自适应批量策略
   - 监控操作速率
   - 动态调整批量参数
   - 测试验证

### 长期优化（Phase 2+）

5. **综合优化**:
   - Protobuf 序列化 (替代 JSON)
   - WriteBatch (RocksDB 原生批量)
   - gRPC 并发优化
   - 目标：**100,000+ QPS**

---

## 性能提升预期（修复后）

### 方案 B 预期效果

| 参数 | 当前 | 优化后 | 说明 |
|-----|------|-------|------|
| 最大批量延迟 | 5ms | 100μs | ⬇️ **50x 减少** |
| 批量大小 | 100 | 10 | 仍能减少 WAL fsync |
| 理论性能提升 | 100x → **-71%** ⬇️ | 10x → **~3-5x** ⬆️ | 预期达标 |

### 目标性能

| 测试场景 | 当前 | 目标 | 预期方案 B |
|---------|------|------|-----------|
| Memory LargeScale | 982 ops/sec | 15,000+ ops/sec | ~3,000 ops/sec |
| Memory Sustained | 453 ops/sec | 10,000+ ops/sec | ~2,000 ops/sec |

---

## 教训与启示

### 关键教训

1. **批量优化不是银弹**
   - 适用场景：高并发、高吞吐量
   - 不适用：低并发、延迟敏感

2. **参数调优至关重要**
   - 批量大小和超时需要根据实际负载调整
   - 一刀切的配置可能适得其反

3. **性能测试的重要性**
   - 理论分析 ≠ 实际效果
   - 必须在真实场景下验证

### 正确的优化路径

1. ✅ **先测量，后优化** (Measure before optimize)
2. ✅ **针对瓶颈优化** (Optimize bottlenecks)
3. ⚠️ **验证优化效果** (Validate improvements) ← **我们在这里**
4. ⭐ **根据反馈调整** (Adjust based on feedback) ← **下一步**

---

**结论**: BatchProposer 的设计思路是正确的，但参数配置需要根据实际负载调整。通过减小批量参数（方案 B）或实施自适应策略（方案 A），可以解决当前的性能回归问题。

