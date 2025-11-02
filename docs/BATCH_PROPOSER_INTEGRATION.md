# BatchProposer 集成指南

**创建日期**: 2025-11-01
**状态**: 实施中

---

## 概述

BatchProposer 是 Phase 1 性能优化的核心组件，通过批量合并 Raft 提案来减少 WAL fsync 次数，预期带来 **5-10x 性能提升**。

---

## 已完成工作

### 1. BatchProposer 核心实现 ✅

**文件**: `internal/raft/batch_proposer.go`

**核心功能**:
- 批量收集提案（默认 100 个/批）
- 超时自动刷新（默认 5ms）
- 优雅关闭支持
- 统计指标追踪

### 2. main.go 集成 ✅

**文件**: `cmd/metastore/main.go`

**关键修改**:
```go
// 创建 BatchProposer
batchProposer := raft.NewBatchProposer(proposeC, 100, 5*time.Millisecond)
defer batchProposer.Stop()

// Memory 引擎
kvs = memory.NewMemoryWithBatchProposer(<-snapshotterReady, batchProposer, commitC, errorC)

// RocksDB 引擎
kvs = rocksdb.NewRocksDBWithBatchProposer(db, <-snapshotterReady, batchProposer, commitC, errorC)
```

---

## 待完成工作

### 3. Memory 引擎集成 🚧

**文件**: `internal/memory/kvstore.go`

**需要修改**:

#### 3.1 添加 BatchProposer 字段
```go
type Memory struct {
    *MemoryEtcd

    proposeC      chan<- string        // 原始 proposal channel (向后兼容)
    batchProposer *raft.BatchProposer  // ✅ 新增：批量提案器
    snapshotter   *snap.Snapshotter
    // ...
}
```

#### 3.2 新增构造函数
```go
// NewMemoryWithBatchProposer 使用 BatchProposer 创建 Memory 存储
func NewMemoryWithBatchProposer(
    snapshotter *snap.Snapshotter,
    batchProposer *raft.BatchProposer,
    commitC <-chan *kvstore.Commit,
    errorC <-chan error,
) *Memory {
    m := &Memory{
        MemoryEtcd:        NewMemoryEtcd(),
        batchProposer:     batchProposer,  // ✅ 使用 BatchProposer
        proposeC:          nil,             // 不使用原始 channel
        snapshotter:       snapshotter,
        pendingOps:        make(map[string]chan struct{}),
        pendingTxnResults: make(map[string]*kvstore.TxnResponse),
    }

    // ... 其余初始化逻辑相同

    return m
}
```

#### 3.3 修改 propose 方法（添加辅助函数）
```go
// propose 发送提案到 Raft（使用 BatchProposer 如果可用）
func (m *Memory) propose(ctx context.Context, data string) error {
    if m.batchProposer != nil {
        // ✅ 使用 BatchProposer（性能优化）
        return m.batchProposer.Propose(ctx, data)
    }

    // 向后兼容：使用原始 proposeC
    select {
    case m.proposeC <- data:
        return nil
    case <-time.After(30 * time.Second):
        return fmt.Errorf("timeout proposing operation")
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

#### 3.4 修改所有 propose 调用点

**PutWithLease** (line 296):
```go
// 旧代码
select {
case m.proposeC <- string(data):
case <-time.After(30 * time.Second):
    // ...
}

// ✅ 新代码
if err := m.propose(ctx, string(data)); err != nil {
    m.pendingMu.Lock()
    delete(m.pendingOps, seqNum)
    m.pendingMu.Unlock()
    return 0, nil, err
}
```

**DeleteRange** (类似修改)
**Txn** (类似修改)
**LeaseGrant** (类似修改)
**LeaseRevoke** (类似修改)

#### 3.5 修改 readCommits 支持批量拆分

**核心修改** (line 110-150):
```go
func (m *Memory) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
    for commit := range commitC {
        if commit == nil {
            return
        }

        for _, data := range commit.Data {
            // ✅ 检查是否为批量提案
            if raft.IsBatchedProposal(data) {
                // 拆分批量提案
                proposals := raft.SplitBatchedProposal(data)
                for _, proposal := range proposals {
                    var op RaftOperation
                    if err := json.Unmarshal([]byte(proposal), &op); err != nil {
                        log.Error("Failed to unmarshal operation",
                            zap.Error(err),
                            zap.String("component", "storage-memory"))
                        continue
                    }
                    m.applyOperation(op)
                }
            } else {
                // 单个提案（向后兼容）
                var op RaftOperation
                if err := json.Unmarshal([]byte(data), &op); err != nil {
                    log.Error("Failed to unmarshal operation",
                        zap.Error(err),
                        zap.String("component", "storage-memory"))
                    continue
                }
                m.applyOperation(op)
            }
        }

        if commit.ApplyDoneC != nil {
            close(commit.ApplyDoneC)
        }
    }
}
```

---

### 4. RocksDB 引擎集成 🚧

**文件**: `internal/rocksdb/kvstore.go`

**类似 Memory 的修改**:
1. 添加 `batchProposer *raft.BatchProposer` 字段
2. 创建 `NewRocksDBWithBatchProposer` 构造函数
3. 添加 `propose()` 辅助方法
4. 修改所有 propose 调用点
5. 修改 readCommits 支持批量拆分

---

## 测试验证

### 编译测试
```bash
# 构建项目
make build

# 预期：编译成功
```

### 单元测试
```bash
# 运行所有测试
make test

# 预期：所有测试通过
```

### 性能测试
```bash
# Memory 性能测试
make test-perf-memory

# RocksDB 性能测试
make test-perf-rocksdb

# 预期性能提升
# Memory:  3,386 → 20,000+ ops/sec (6x)
# RocksDB: 4,921 → 25,000+ ops/sec (5x)
```

---

## 向后兼容性

### 设计考虑

1. **保留原始 NewMemory 和 NewRocksDB**
   - 不破坏现有代码
   - 测试代码无需修改

2. **新增 *WithBatchProposer 构造函数**
   - 仅 main.go 使用新构造函数
   - 明确标识性能优化版本

3. **propose 辅助方法**
   - 检查 batchProposer 是否为 nil
   - 自动回退到原始 proposeC
   - 确保两种模式都能工作

---

## 性能提升原理

### 优化前
```
操作 1 → JSON 序列化 → proposeC → Raft → WAL fsync (5ms)
操作 2 → JSON 序列化 → proposeC → Raft → WAL fsync (5ms)
操作 3 → JSON 序列化 → proposeC → Raft → WAL fsync (5ms)

总耗时: 15ms
吞吐量: 200 ops/sec
```

### 优化后
```
操作 1-100 → JSON 序列化 → BatchProposer 收集
           ↓
操作 1-100 合并 → proposeC → Raft → WAL fsync (5ms)

总耗时: 5ms (100 个操作)
吞吐量: 20,000 ops/sec
提升: 100x (理论) / 5-10x (实际，受其他瓶颈限制)
```

---

## 下一步

1. ✅ 完成 Memory 引擎集成
2. ✅ 完成 RocksDB 引擎集成
3. ✅ 运行性能测试验证
4. ✅ 分析性能测试结果
5. 📝 更新性能报告文档

---

**预计完成时间**: 今天
**风险等级**: 低（向后兼容设计）
**预期收益**: 5-10x 性能提升
