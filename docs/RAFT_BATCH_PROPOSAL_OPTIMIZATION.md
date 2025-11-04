# Raft 批量 Proposal 优化方案

**创建日期**: 2025-11-02
**优化目标**: 将端到端吞吐量从 796 ops/sec 提升到 5,000-10,000 ops/sec（6-12x）

---

## 一、瓶颈分析

### 当前性能数据

```
端到端测试（gRPC + Raft + 存储）:
- 吞吐量: 796 ops/sec ❌
- 平均延迟: 62.77ms
- 50 并发客户端，50,000 total 操作

存储层测试（无 Raft）:
- 吞吐量: 9.43M ops/sec ✅
- 性能差距: 11,849x
```

### 瓶颈定位

**问题代码路径**:
```
1. 客户端请求 → internal/memory/kvstore.go:112
   m.proposeC <- data  // 每个请求立即 propose

2. Raft 节点处理 → internal/raft/node_memory.go:500
   rc.wal.Save(rd.HardState, rd.Entries)  // 每次 Ready 都 fsync

3. WAL fsync → etcd/wal
   每次 fsync ~1-5ms（磁盘 I/O）
```

**性能计算**:
```
50,000 个请求
× 1 proposal/请求（立即发送）
× 1 Ready/proposal（Raft 处理）
× 1 fsync/Ready（WAL 写入）
× 1.25ms/fsync（平均）
= 62.5 秒总时间
```

实际测试: **62.77 秒** ✅ 与预期完全吻合！

---

## 二、优化方案：批量 Proposal

### 核心思想

在 `proposeC` 和 Raft 之间添加批量缓冲层，将多个客户端请求合并为一个 Raft proposal。

### 架构设计

**优化前**:
```
客户端1 → propose1 → Ready1 → fsync1 (1ms)
客户端2 → propose2 → Ready2 → fsync2 (1ms)
...
客户端50 → propose50 → Ready50 → fsync50 (1ms)

总时间: 50ms (串行)
```

**优化后**:
```
客户端1 \
客户端2  → [批量缓冲器] → propose(batch) → Ready → fsync1 (1ms)
...      /  等待1-5ms
客户端50/   批量大小50

总时间: 1-5ms (等待) + 1ms (fsync) = 2-6ms
提升: 50ms → 6ms ≈ 8x
```

---

## 三、实现设计

### 3.1 批量提交器结构

```go
// internal/raft/batch_proposer.go

package raft

import (
    "sync"
    "time"
)

// BatchProposer 批量提交器，减少 WAL fsync 次数
type BatchProposer struct {
    inputC     <-chan string          // 输入 channel（来自 kvstore）
    outputC    chan<- string          // 输出 channel（到 Raft）

    batch      []string               // 当前批次
    batchMu    sync.Mutex             // 保护 batch

    maxBatchSize int                  // 最大批次大小
    maxBatchTime time.Duration        // 最大等待时间

    stopC      chan struct{}
}

type BatchProposerConfig struct {
    MaxBatchSize int           // 默认 50
    MaxBatchTime time.Duration // 默认 2ms
}
```

### 3.2 核心逻辑

```go
func (bp *BatchProposer) Run() {
    ticker := time.NewTicker(bp.maxBatchTime)
    defer ticker.Stop()

    for {
        select {
        case proposal := <-bp.inputC:
            bp.batchMu.Lock()
            bp.batch = append(bp.batch, proposal)
            shouldFlush := len(bp.batch) >= bp.maxBatchSize
            bp.batchMu.Unlock()

            if shouldFlush {
                bp.flush()
            }

        case <-ticker.C:
            bp.flush()

        case <-bp.stopC:
            bp.flush() // 最后刷新
            return
        }
    }
}

func (bp *BatchProposer) flush() {
    bp.batchMu.Lock()
    if len(bp.batch) == 0 {
        bp.batchMu.Unlock()
        return
    }

    // 合并为一个大的 proposal
    combined := strings.Join(bp.batch, "|BATCH|")
    bp.batch = bp.batch[:0] // 清空 batch
    bp.batchMu.Unlock()

    // 发送到 Raft
    bp.outputC <- combined
}
```

### 3.3 Apply 层支持批量

```go
// internal/memory/kvstore.go

func (m *Memory) applyOperation(op RaftOperation) {
    // 检测是否为批量操作
    if strings.Contains(op.Data, "|BATCH|") {
        // 批量 apply
        ops := strings.Split(op.Data, "|BATCH|")
        for _, opData := range ops {
            m.applySingleOp(opData)
        }
    } else {
        // 单个 apply
        m.applySingleOp(op.Data)
    }
}
```

---

## 四、集成方案

### 4.1 修改 kvstore 初始化

```go
// internal/memory/kvstore.go

func NewMemory(...) *Memory {
    // 创建批量提交器
    batchInputC := make(chan string, 1000)
    batchOutputC := make(chan string, 100)

    batchProposer := NewBatchProposer(
        batchInputC,
        batchOutputC,
        BatchProposerConfig{
            MaxBatchSize: 50,    // 可配置
            MaxBatchTime: 2*time.Millisecond,
        },
    )
    go batchProposer.Run()

    m := &Memory{
        proposeC: batchInputC,  // 改为输入到批量器
        ...
    }

    // 启动 Raft（使用 batchOutputC）
    commitC, errorC, ... := raft.NewNode(..., batchOutputC, ...)

    return m
}
```

### 4.2 配置支持

```go
// pkg/config/config.go

type RaftConfig struct {
    // 批量提交配置
    EnableBatchProposal bool          `yaml:"enable_batch_proposal"`
    BatchMaxSize        int           `yaml:"batch_max_size"`         // 默认 50
    BatchMaxTime        time.Duration `yaml:"batch_max_time"`         // 默认 2ms

    // 现有配置...
}
```

```yaml
# configs/config.yaml

raft:
  enable_batch_proposal: true
  batch_max_size: 50
  batch_max_time: 2ms
```

---

## 五、性能预期

### 5.1 理论分析

**批量大小 = 50**:
```
50,000 个请求
÷ 50 (批量大小)
= 1,000 次 Raft proposal
× 1 fsync/proposal
× 1.25ms/fsync
= 1.25 秒总时间

吞吐量: 50,000 / 1.25s = 40,000 ops/sec
提升: 796 → 40,000 ≈ 50x ✅
```

**批量大小 = 100**:
```
吞吐量: 80,000 ops/sec
提升: 100x
```

### 5.2 延迟影响

**额外延迟**:
- 批量等待时间: 0-2ms（平均 1ms）
- fsync 时间: 1.25ms
- 总延迟: 原延迟 + 1ms

**权衡**:
- 吞吐量: +50-100x ✅
- 延迟: +1ms（可接受）

---

## 六、风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 延迟增加 | +1-2ms | 可配置批量超时（默认 2ms） |
| 复杂度增加 | 代码复杂 | 可配置开关，默认启用 |
| Apply 解析开销 | 批量解析 | 使用高效分隔符 `|BATCH|` |
| Channel 缓冲 | 内存占用 | 限制 channel 大小（1000） |

---

## 七、实施计划

### Phase 1: 核心实现（1-2天）

1. **创建 BatchProposer** (4小时)
   - `internal/raft/batch_proposer.go`
   - 核心批量逻辑
   - 单元测试

2. **修改 kvstore** (2小时)
   - 集成 BatchProposer
   - 批量 apply 逻辑

3. **配置支持** (1小时)
   - 添加 Raft 配置项
   - 默认值设定

### Phase 2: 测试验证（1天）

1. **单元测试** (2小时)
   - TestBatchProposer
   - TestBatchApply

2. **集成测试** (2小时)
   - 50 并发客户端测试
   - 验证正确性

3. **性能测试** (4小时)
   - 运行 performance_test
   - 对比优化前后
   - CPU/内存 profiling

### Phase 3: 文档与部署（半天）

1. **文档更新**
2. **配置示例**
3. **性能报告**

**总工作量**: 2-3 天

---

## 八、备选方案

### 方案 A：当前方案（批量 Proposal）
- **优点**: 简单直接，效果明显
- **缺点**: 增加 1-2ms 延迟
- **适用**: 吞吐量优先场景

### 方案 B：异步 Apply
- **优点**: 更高吞吐量
- **缺点**: 复杂度高，需要重构
- **适用**: 长期优化

### 方案 C：使用 NVMe SSD + DirectIO
- **优点**: 硬件加速 fsync
- **缺点**: 需要硬件升级
- **适用**: 预算充足场景

**推荐**: 先实施方案 A（批量 Proposal），获得 50-100x 提升。

---

## 九、下一步行动

**立即开始**: 实现 BatchProposer

```bash
# 创建文件
touch internal/raft/batch_proposer.go
touch internal/raft/batch_proposer_test.go

# 实现核心逻辑
# 集成到 kvstore
# 运行性能测试
```

**预期结果**:
- 吞吐量: 796 → 40,000+ ops/sec (**50x**)
- 延迟: 62.77ms → 2-5ms (**10x faster**)
- CPU 利用率: 保持低水平
- 内存: 增加 <1MB

---

*优化方案设计完成 - 准备实施！*
