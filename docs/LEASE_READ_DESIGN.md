# Lease Read 优化设计文档

**日期**: 2025-11-02
**状态**: 设计中
**预期性能提升**: 10-100x（读操作）

---

## 背景和动机

### 当前读请求的性能瓶颈

在标准 Raft 实现中，所有读请求都需要通过 Raft 共识：
1. Client 发送读请求到 Leader
2. Leader 将读请求作为 Proposal 提交到 Raft 日志
3. 等待日志复制到多数节点
4. Apply 到状态机后返回结果

**问题**：
- ❌ 每次读都需要磁盘 I/O（写 Raft 日志）
- ❌ 每次读都需要网络往返（复制到多数节点）
- ❌ 读多写少场景性能极差

### Lease Read 优化原理

**核心思想**：Leader 在租约期内可以直接服务读请求，无需 Raft 共识

**安全性保证**：
- ✅ Leader 在租约期内保证自己是合法 Leader
- ✅ 租约时间 < 选举超时，确保不会有新 Leader 产生
- ✅ 读取的数据是 committed 状态

**性能提升**：
- 🚀 无磁盘 I/O（不写 Raft 日志）
- 🚀 无网络往返（不需要复制）
- 🚀 延迟降低 10-100x

---

## 设计方案

### 方案选择：ReadIndex + Leader Lease

我们采用 **etcd/TiKV 的 Lease Read** 方案，结合两个机制：

#### 1. Leader Lease（租约机制）

**原理**：
```
Leader 维护一个租约时间窗口：
- 租约有效期 = min(选举超时 / 2, 心跳间隔 × 3)
- 收到多数节点心跳响应时续约
- 租约期内保证没有新 Leader 产生
```

**实现要点**：
- 租约续期条件：收到 > n/2 节点的心跳响应
- 时钟偏移容忍：租约时间 = 实际时间 - 时钟偏移（默认 500ms）
- 租约过期处理：降级为 ReadIndex 模式

#### 2. ReadIndex（读索引）

**原理**：
```
1. Leader 收到读请求
2. 记录当前 committedIndex（称为 readIndex）
3. 确认自己仍是 Leader（通过心跳广播）
4. 等待 appliedIndex >= readIndex
5. 从状态机读取数据返回
```

**优势**：
- ✅ 无需写 Raft 日志
- ✅ 只需一次心跳确认（比完整共识快）
- ✅ 保证线性一致性读

#### 3. 组合模式（性能最优）

```
if Leader 有有效租约:
    // Fast Path: 直接读（最快）
    return readFromStateMachine(committedIndex)
else if 是 Leader:
    // Slow Path: ReadIndex 模式（较快）
    readIndex = confirmLeadershipAndGetCommittedIndex()
    waitUntil(appliedIndex >= readIndex)
    return readFromStateMachine(readIndex)
else:
    // 转发到 Leader（最慢，但仍比原始方案快）
    forward to Leader
```

---

## 核心组件设计

### 1. LeaseManager（租约管理器）

**职责**：管理 Leader 租约的生命周期

```go
type LeaseManager struct {
    // 配置
    electionTimeout time.Duration  // 选举超时
    heartbeatTick   time.Duration  // 心跳间隔
    clockDrift      time.Duration  // 时钟偏移容忍（默认 500ms）

    // 租约状态
    leaseExpireTime atomic.Int64   // 租约过期时间（Unix nano）
    isLeader        atomic.Bool    // 是否是 Leader

    // 统计
    leaseRenewCount atomic.Int64   // 租约续期次数
    leaseExpireCount atomic.Int64  // 租约过期次数

    mu     sync.RWMutex
    logger *zap.Logger
}

// 核心方法
func (lm *LeaseManager) RenewLease(receivedAcks int, totalNodes int) bool
func (lm *LeaseManager) HasValidLease() bool
func (lm *LeaseManager) GetLeaseRemaining() time.Duration
func (lm *LeaseManager) OnBecomeLeader()
func (lm *LeaseManager) OnBecomeFollower()
```

**租约续期算法**：
```go
func (lm *LeaseManager) RenewLease(receivedAcks int, totalNodes int) bool {
    // 1. 检查是否是 Leader
    if !lm.isLeader.Load() {
        return false
    }

    // 2. 检查是否收到多数节点响应
    if receivedAcks < (totalNodes/2 + 1) {
        return false
    }

    // 3. 计算新的租约过期时间
    // 租约时间 = min(选举超时/2, 心跳间隔×3) - 时钟偏移
    leaseDuration := min(
        lm.electionTimeout / 2,
        lm.heartbeatTick * 3,
    ) - lm.clockDrift

    newExpireTime := time.Now().Add(leaseDuration)
    lm.leaseExpireTime.Store(newExpireTime.UnixNano())
    lm.leaseRenewCount.Add(1)

    return true
}

func (lm *LeaseManager) HasValidLease() bool {
    if !lm.isLeader.Load() {
        return false
    }

    now := time.Now().UnixNano()
    expireTime := lm.leaseExpireTime.Load()

    return now < expireTime
}
```

### 2. ReadIndexManager（读索引管理器）

**职责**：管理 ReadIndex 请求和响应

```go
type ReadIndexRequest struct {
    RequestID  string          // 请求 ID
    ReadIndex  uint64          // 读索引（committedIndex）
    RecvTime   time.Time       // 收到时间
    ResponseC  chan ReadResult // 响应通道
}

type ReadResult struct {
    ReadIndex uint64
    Err       error
}

type ReadIndexManager struct {
    // 待处理的 ReadIndex 请求
    pendingReads sync.Map  // map[string]*ReadIndexRequest

    // 统计
    totalReadIndexReqs atomic.Int64
    fastPathReads      atomic.Int64  // 租约读（fast path）
    slowPathReads      atomic.Int64  // ReadIndex 读（slow path）

    logger *zap.Logger
}

// 核心方法
func (rm *ReadIndexManager) AddReadRequest(ctx context.Context, committedIndex uint64) (uint64, error)
func (rm *ReadIndexManager) ConfirmReadIndex(readIndex uint64) error
func (rm *ReadIndexManager) NotifyApplied(appliedIndex uint64)
```

**ReadIndex 流程**：
```go
func (rm *ReadIndexManager) AddReadRequest(ctx context.Context, committedIndex uint64) (uint64, error) {
    req := &ReadIndexRequest{
        RequestID: generateRequestID(),
        ReadIndex: committedIndex,
        RecvTime:  time.Now(),
        ResponseC: make(chan ReadResult, 1),
    }

    rm.pendingReads.Store(req.RequestID, req)

    // 等待 ReadIndex 确认或超时
    select {
    case result := <-req.ResponseC:
        return result.ReadIndex, result.Err
    case <-ctx.Done():
        rm.pendingReads.Delete(req.RequestID)
        return 0, ctx.Err()
    }
}

func (rm *ReadIndexManager) NotifyApplied(appliedIndex uint64) {
    // 通知所有 readIndex <= appliedIndex 的请求
    rm.pendingReads.Range(func(key, value interface{}) bool {
        req := value.(*ReadIndexRequest)
        if req.ReadIndex <= appliedIndex {
            req.ResponseC <- ReadResult{
                ReadIndex: req.ReadIndex,
                Err:       nil,
            }
            rm.pendingReads.Delete(key)
        }
        return true
    })
}
```

### 3. 集成到 Raft 节点

#### 读请求路由

```go
func (rc *raftNode) handleReadRequest(ctx context.Context, key string) (string, error) {
    // 1. Fast Path: 租约读（最快）
    if rc.leaseManager.HasValidLease() {
        rc.readIndexManager.fastPathReads.Add(1)

        // 等待 appliedIndex >= committedIndex
        committedIndex := rc.getCommittedIndex()
        rc.waitUntilApplied(ctx, committedIndex)

        // 直接从状态机读取
        return rc.kvstore.Get(key)
    }

    // 2. Slow Path: ReadIndex 模式（较快）
    if rc.isLeader() {
        rc.readIndexManager.slowPathReads.Add(1)

        // ReadIndex 流程
        committedIndex := rc.getCommittedIndex()
        readIndex, err := rc.readIndexManager.AddReadRequest(ctx, committedIndex)
        if err != nil {
            return "", err
        }

        // 等待应用到状态机
        rc.waitUntilApplied(ctx, readIndex)

        // 从状态机读取
        return rc.kvstore.Get(key)
    }

    // 3. Follower: 转发到 Leader（最慢）
    return rc.forwardToLeader(ctx, key)
}
```

#### 租约续期时机

```go
// 在心跳响应处理中续约
func (rc *raftNode) handleHeartbeatResponse(responses int, totalNodes int) {
    if rc.leaseManager.RenewLease(responses, totalNodes) {
        rc.logger.Debug("Lease renewed",
            zap.Int("acks", responses),
            zap.Duration("remaining", rc.leaseManager.GetLeaseRemaining()),
        )
    }
}

// 在角色变更时更新租约状态
func (rc *raftNode) onStateChange(newState raft.StateType) {
    switch newState {
    case raft.StateLeader:
        rc.leaseManager.OnBecomeLeader()
    case raft.StateFollower, raft.StateCandidate:
        rc.leaseManager.OnBecomeFollower()
    }
}
```

---

## 配置设计

### 配置结构

```go
type LeaseReadConfig struct {
    Enable       bool          // 是否启用 Lease Read（默认 true）
    ClockDrift   time.Duration // 时钟偏移容忍（默认 500ms）
    ReadTimeout  time.Duration // 读超时（默认 5s）
}
```

### 配置文件

```yaml
# configs/config.yaml
server:
  raft:
    lease_read:
      enable: true          # 启用 Lease Read
      clock_drift: 500ms    # 时钟偏移容忍（保守值）
      read_timeout: 5s      # 读超时
```

---

## 安全性分析

### 1. 线性一致性保证

**问题**：如何保证读到的是最新的 committed 数据？

**解决**：
- Lease Read：租约期内 Leader 不会变更，appliedIndex >= committedIndex 保证读到最新数据
- ReadIndex：显式确认 Leader 身份并读取 committedIndex 时刻的数据

### 2. 时钟偏移处理

**问题**：不同节点的时钟可能不一致，如何避免安全问题？

**解决**：
- 租约时间 = 理论租约时间 - 时钟偏移容忍（默认 500ms）
- 保守策略：租约时间 < 选举超时 / 2

### 3. 脑裂场景

**问题**：网络分区导致两个 Leader？

**解决**：
- 租约续期需要多数节点响应（> n/2）
- 分区的少数派无法续约，租约自动过期
- 租约过期后降级为 ReadIndex 模式（需要心跳确认）

---

## 性能预期

### 理论分析

| 读模式 | 磁盘 I/O | 网络往返 | 延迟（理论） | 吞吐提升 |
|--------|---------|---------|------------|---------|
| **原始（Raft 共识）** | 1 次写 | 1-2 次 | 10-50ms | 1x |
| **ReadIndex** | 无 | 1 次心跳 | 2-10ms | 5-10x |
| **Lease Read** | 无 | 无 | 0.5-2ms | **10-100x** |

### 实际场景预期

#### 低延迟场景（本地网络）
- 原始 Raft 读：10ms
- ReadIndex 读：2ms（**5x 提升**）
- Lease Read：0.5ms（**20x 提升**）

#### 高延迟场景（跨区域）
- 原始 Raft 读：100ms
- ReadIndex 读：20ms（**5x 提升**）
- Lease Read：2ms（**50x 提升**）

#### 读多写少场景（90% 读）
- 吞吐提升：**10-50x**（大部分读走 Lease Read fast path）

---

## 实现计划

### Phase 1: 核心组件（2-3 小时）
1. ✅ 设计文档完成
2. ⏳ 实现 `LeaseManager`（租约管理器）
3. ⏳ 实现 `ReadIndexManager`（读索引管理器）
4. ⏳ 单元测试

### Phase 2: Raft 集成（2-3 小时）
1. 集成到 Memory Raft 节点
2. 集成到 RocksDB Raft 节点
3. 添加配置系统
4. 集成测试

### Phase 3: 性能测试（1-2 小时）
1. 读性能对比测试（Lease Read vs 原始）
2. 读写混合场景测试
3. 租约续期和过期测试

---

## 监控指标

建议收集的指标：

```go
type LeaseReadStats struct {
    // 租约统计
    LeaseRenewCount  int64  // 租约续期次数
    LeaseExpireCount int64  // 租约过期次数
    LeaseHitRate     float64 // 租约命中率

    // 读模式统计
    FastPathReads    int64  // Lease Read 次数
    SlowPathReads    int64  // ReadIndex 次数
    ForwardedReads   int64  // 转发读次数

    // 性能指标
    AvgReadLatency   time.Duration // 平均读延迟
    P99ReadLatency   time.Duration // P99 读延迟
}
```

---

## 参考资料

- [etcd Lease Read Implementation](https://etcd.io/docs/v3.5/learning/design-learner/)
- [TiKV Lease Read](https://tikv.org/deep-dive/distributed-transaction/read/)
- [Raft Dissertation - Section 6.4 (Processing read-only queries)](https://raft.github.io/raft.pdf)
- [CockroachDB Lease-based Reads](https://www.cockroachlabs.com/docs/stable/architecture/reads-and-writes-overview.html)

---

## 总结

**Lease Read 优化**是提升读性能的关键技术：

**核心优势**：
- 🚀 **10-100x 读性能提升**（取决于场景）
- ✅ 保持线性一致性读
- ✅ 向后兼容（可随时禁用）
- ✅ 业界验证（etcd、TiKV、CockroachDB 都在使用）

**实现策略**：
- Lease Read（fast path）：租约期内直接读
- ReadIndex（slow path）：租约过期时心跳确认后读
- 转发（fallback）：Follower 转发到 Leader

**下一步**：开始实现 `LeaseManager` 和 `ReadIndexManager`

---

**设计完成时间**: 2025-11-02
**预计实现时间**: 5-8 小时
**预期性能提升**: 10-100x 🚀
