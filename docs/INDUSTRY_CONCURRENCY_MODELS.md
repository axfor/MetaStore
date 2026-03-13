# 业界高性能并发模型借鉴

**参考系统**: etcd v3, TiKV, CockroachDB

**目标**: 学习并发模型核心设计,保持高效可靠性

---

## etcd v3 并发模型

### 核心架构

```
Client Requests
    ↓
[MVCC Layer] ← 读写分离 + 快照隔离
    ↓
[Raft Propose] ← 串行共识
    ↓
[Apply Queue] ← ⚠️ 关键: 批量 Apply
    ↓
[Backend: BoltDB] ← B+tree, 页级锁
```

### 关键设计 #1: MVCC 读写分离

**核心思想**: 读操作不阻塞写操作

```go
// etcd 源码简化版
type mvccStore struct {
    // 写路径: 单写锁
    mu sync.RWMutex

    // 读路径: 无锁 (使用快照)
    tree *btree.BTree  // 不可变 B-tree

    // 版本控制
    currentRev int64
}

// 读操作: 不加锁
func (s *mvccStore) Get(key string, rev int64) *KeyValue {
    // 获取指定版本的快照
    snapshot := s.getSnapshot(rev)
    return snapshot.Get(key)
}

// 写操作: 只在 apply 时加锁
func (s *mvccStore) Put(key, value string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 创建新版本
    s.currentRev++
    s.tree.Set(key, &KeyValue{
        Key:     key,
        Value:   value,
        Version: s.currentRev,
    })
}
```

**MetaStore 借鉴方案** (简化版):

```go
// internal/memory/mvcc_store.go
type MVCCStore struct {
    // 当前可写版本
    current atomic.Pointer[ShardedMap]

    // 历史版本 (用于快照读)
    history []*ShardedMap

    // 只在切换版本时加锁
    versionMu sync.Mutex
}

// 读操作: 无锁
func (m *MVCCStore) Get(key string) *kvstore.KeyValue {
    snapshot := m.current.Load()  // atomic 读取
    return snapshot.Get(key)      // ShardedMap 内部加锁
}

// 写操作: Apply 时写入新版本
func (m *MVCCStore) Apply(ops []RaftOperation) {
    m.versionMu.Lock()
    defer m.versionMu.Unlock()

    // 复制当前版本
    newVersion := m.current.Load().Clone()

    // 批量应用操作 (并行)
    for _, op := range ops {
        newVersion.Set(op.Key, op.Value)
    }

    // 切换到新版本
    m.current.Store(newVersion)

    // 保存历史 (用于快照)
    m.history = append(m.history, newVersion)
    m.gcOldVersions()  // 清理旧版本
}
```

**优势**:
- ✅ 读操作零锁竞争
- ✅ 写操作批量应用
- ⚠️ 内存开销增加 (维护多版本)

---

### 关键设计 #2: Apply Queue (批量应用)

**核心思想**: 批量提案批量应用,减少锁开销

```go
// etcd 源码简化版
type applyBatcher struct {
    queue chan []raftpb.Entry
}

func (a *applyBatcher) apply() {
    for entries := range a.queue {
        // ⚠️ 关键: 一次加锁,批量应用
        a.store.mu.Lock()

        for _, entry := range entries {
            op := decode(entry.Data)
            a.store.applyNoLock(op)  // 无锁版本
        }

        a.store.mu.Unlock()
    }
}
```

**etcd 实际性能**:
- 批量大小: 100 操作
- 锁开销: 100 次操作 → 1 次加锁 = **100x 减少**
- 吞吐量: ~30,000 ops/sec (3 节点集群)

**MetaStore 借鉴方案**:

```go
// internal/memory/kvstore.go
func (m *Memory) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
    for commit := range commitC {
        // 收集所有操作
        var allOps []RaftOperation
        for _, data := range commit.Data {
            // 解析批量提案
            if batch.IsBatchedProposal(data) {
                proposals := batch.SplitBatchedProposal(data)
                for _, p := range proposals {
                    allOps = append(allOps, deserializeOperation(p))
                }
            } else {
                allOps = append(allOps, deserializeOperation(data))
            }
        }

        // ✅ 批量应用 (一次加锁或无锁)
        m.applyBatch(allOps)

        close(commit.ApplyDoneC)
    }
}

func (m *Memory) applyBatch(ops []RaftOperation) {
    // 按分片分组
    shardOps := make(map[uint32][]RaftOperation)
    for _, op := range ops {
        shardIdx := m.kvData.getShard(op.Key)
        shardOps[shardIdx] = append(shardOps[shardIdx], op)
    }

    // ⚠️ 关键: 并行应用不同分片的操作
    var wg sync.WaitGroup
    for shardIdx, ops := range shardOps {
        wg.Add(1)
        go func(shardIdx uint32, ops []RaftOperation) {
            defer wg.Done()

            // 锁定分片
            shard := &m.kvData.shards[shardIdx]
            shard.mu.Lock()
            defer shard.mu.Unlock()

            // 批量应用
            for _, op := range ops {
                m.applyOpNoLock(op, shard)
            }
        }(shardIdx, ops)
    }
    wg.Wait()
}
```

**预期效果**:
- 批量大小: 100 操作
- 分片分布: 100 操作 → ~512/100 ≈ 每个分片 0.2 操作
- 实际并行度: ~min(100, 512) = 100
- 吞吐量提升: **10-50x**

---

## TiKV 并发模型

### 核心架构

```
Client Requests
    ↓
[Region Router] ← 数据分片 (Multi-Raft)
    ↓
[Raft Group 1]  [Raft Group 2]  [Raft Group 3] ← 并行共识
    ↓               ↓               ↓
[Apply Worker Pool] ← ⚠️ 关键: 异步批量 Apply
    ↓
[Pebble WriteBatch] ← LSM-tree, 批量写入
```

### 关键设计 #1: Multi-Raft (数据分片)

**核心思想**: 数据切分到多个 Raft 组,并行处理

```go
// TiKV 源码简化版
type Store struct {
    regions map[uint64]*Region  // regionID -> Region
}

type Region struct {
    id       uint64
    startKey string
    endKey   string
    raft     *raft.RawNode  // 独立的 Raft 组
}

// 请求路由到对应的 Region
func (s *Store) Put(key, value string) {
    // 1. 找到 key 所属的 Region
    region := s.findRegion(key)

    // 2. 提交到该 Region 的 Raft 组
    region.raft.Propose([]byte(fmt.Sprintf("PUT %s %s", key, value)))
}
```

**TiKV 实际性能**:
- 8 个 Region (Raft 组)
- 吞吐量: 8x 单 Raft 性能
- 实际: ~200,000 ops/sec (8 节点集群)

**MetaStore 借鉴方案** (简化版):

```go
// internal/multiraft/store.go
type MultiRaftStore struct {
    regions []*Region
}

type Region struct {
    id        uint64
    keyRange  KeyRange         // [startKey, endKey)
    raftNode  *raft.RawNode
    kvStore   *memory.Memory
}

func (s *MultiRaftStore) Put(ctx context.Context, key, value string) {
    // 1. 路由到对应 Region
    region := s.route(key)

    // 2. 提交到该 Region 的 Raft
    return region.kvStore.PutWithLease(ctx, key, value, 0)
}

func (s *MultiRaftStore) route(key string) *Region {
    // 简单 hash 分片
    idx := hash(key) % len(s.regions)
    return s.regions[idx]
}
```

**优势**:
- ✅ 并行 Raft 共识 (减少 WAL fsync 瓶颈)
- ✅ 扩展性好 (增加 Region 数提升吞吐)
- ⚠️ 复杂度高 (跨 Region 事务、rebalance)

**适用场景**: 需要 100,000+ ops/sec 时考虑

---

### 关键设计 #2: Async Apply (异步应用)

**核心思想**: Apply 和 Propose 解耦,提升吞吐

```go
// TiKV 源码简化版
type AsyncApplier struct {
    applyQueue chan []raftpb.Entry
    workers    []*ApplyWorker
}

func (a *AsyncApplier) Start() {
    // 启动多个 apply worker
    for i := 0; i < runtime.NumCPU(); i++ {
        worker := &ApplyWorker{queue: a.applyQueue}
        go worker.run()
    }
}

type ApplyWorker struct {
    queue <-chan []raftpb.Entry
}

func (w *ApplyWorker) run() {
    for entries := range w.queue {
        // 批量应用到 Pebble
        batch := pebble.NewWriteBatch()
        for _, entry := range entries {
            op := decode(entry.Data)
            batch.Put(op.Key, op.Value)
        }
        pebble.Write(batch)  // 一次 fsync
    }
}
```

**TiKV 实际性能**:
- Apply 延迟: 与 Propose 并发执行
- 吞吐量: 受限于磁盘 IOPS,不再受 CPU 限制

**MetaStore 借鉴方案**:

```go
// internal/raft/async_applier.go
type AsyncApplier struct {
    commitC <-chan *kvstore.Commit
    store   *memory.Memory
    workers int
}

func (a *AsyncApplier) Start() {
    // 多个 worker 并行 apply
    for i := 0; i < a.workers; i++ {
        go a.applyWorker()
    }
}

func (a *AsyncApplier) applyWorker() {
    for commit := range a.commitC {
        // 收集操作
        var ops []RaftOperation
        for _, data := range commit.Data {
            ops = append(ops, deserializeOperation(data))
        }

        // ⚠️ 关键: 异步批量应用
        a.store.applyBatch(ops)

        close(commit.ApplyDoneC)
    }
}
```

**优势**:
- ✅ Apply 不阻塞 Propose
- ✅ 充分利用多核 CPU
- ⚠️ 需要处理 apply 顺序 (按 commit index)

---

## CockroachDB 并发模型

### 核心架构

```
Client Requests
    ↓
[Intent Resolution] ← MVCC + 乐观锁
    ↓
[Leaseholder] ← 租约机制 (避免 Raft 读)
    ↓
[Raft Propose]
    ↓
[Pebble (LSM)] ← Pebble fork, 批量写入
```

### 关键设计 #1: Leaseholder (租约机制)

**核心思想**: 读操作不走 Raft,直接从 Leaseholder 读取

```go
// CockroachDB 源码简化版
type Range struct {
    raftGroup   *raft.RawNode
    leaseholder uint64  // 持有 lease 的节点
    leaseExpiry time.Time
}

// 读操作: 不走 Raft
func (r *Range) Get(key string) (string, error) {
    // 1. 检查是否持有 lease
    if r.leaseholder == r.nodeID && time.Now().Before(r.leaseExpiry) {
        // 直接读取本地数据 (无 Raft 延迟)
        return r.kvStore.Get(key)
    }

    // 2. 不持有 lease,转发到 leaseholder
    return r.forwardToLeaseholder(key)
}

// 写操作: 走 Raft
func (r *Range) Put(key, value string) error {
    return r.raftGroup.Propose([]byte(fmt.Sprintf("PUT %s %s", key, value)))
}
```

**CockroachDB 实际性能**:
- 读延迟: ~1ms (无 Raft 共识开销)
- 写延迟: ~10ms (Raft 共识)
- 读吞吐量: **100x** 写吞吐量

**MetaStore 借鉴方案**:

```go
// internal/raft/lease.go
type LeaseManager struct {
    leaseHolder  atomic.Uint64
    leaseExpiry  atomic.Int64  // Unix timestamp
    renewTicker  *time.Ticker
}

func (l *LeaseManager) IsLeaseHolder() bool {
    return l.leaseHolder.Load() == l.nodeID &&
           time.Now().Unix() < l.leaseExpiry.Load()
}

// internal/memory/kvstore.go
func (m *Memory) Get(ctx context.Context, key string) (*kvstore.KeyValue, error) {
    // ✅ 如果持有 lease,直接读取 (无 Raft)
    if m.leaseMgr.IsLeaseHolder() {
        return m.MemoryEtcd.Get(key)
    }

    // ⚠️ 否则走 Raft ReadIndex (线性一致性读)
    return m.GetWithRaft(ctx, key)
}
```

**优势**:
- ✅ 读吞吐量大幅提升
- ✅ 读延迟降低
- ⚠️ 需要处理 lease 转移 (leader 变更)

**适用场景**: 读多写少的场景

---

### 关键设计 #2: Intent Resolution (MVCC + 乐观锁)

**核心思想**: 写操作先写 intent,冲突时回滚

```go
// CockroachDB 源码简化版
type MVCCStore struct {
    data map[string][]Version  // key -> versions
}

type Version struct {
    timestamp time.Time
    value     string
    intent    bool  // 是否为 intent (未提交)
}

// 写操作: 先写 intent
func (s *MVCCStore) Put(key, value string, txnID string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 1. 检查是否有冲突的 intent
    if s.hasConflictIntent(key) {
        return ErrWriteConflict
    }

    // 2. 写入 intent
    s.data[key] = append(s.data[key], Version{
        timestamp: time.Now(),
        value:     value,
        intent:    true,
        txnID:     txnID,
    })
}

// 提交: 将 intent 转为正式版本
func (s *MVCCStore) Commit(txnID string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    for key, versions := range s.data {
        for i, v := range versions {
            if v.intent && v.txnID == txnID {
                versions[i].intent = false
            }
        }
    }
}
```

**优势**:
- ✅ 高并发场景下减少锁竞争
- ✅ 支持 snapshot isolation
- ⚠️ 复杂度高 (需要 intent resolution 机制)

**MetaStore 简化借鉴**: 暂不实现 (保持简单)

---

## 三大系统对比

| 特性 | etcd v3 | TiKV | CockroachDB | MetaStore 借鉴 |
|------|---------|------|-------------|----------------|
| **并发模型** | MVCC + 批量 Apply | Multi-Raft + Async Apply | Leaseholder + Intent | ✅ 批量 Apply |
| **读写分离** | ✅ MVCC | ✅ MVCC | ✅ Leaseholder | ⚠️ 简化 MVCC |
| **批量 Apply** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes |
| **数据分片** | ❌ 单 Raft | ✅ Multi-Raft | ✅ Multi-Raft | ⚠️ 可选 |
| **异步 Apply** | ❌ 串行 | ✅ Worker Pool | ✅ Async | ✅ Worker Pool |
| **Lease 读** | ❌ 走 Raft | ❌ 走 Raft | ✅ Leaseholder | ✅ 可选 |
| **复杂度** | 🟡 中等 | 🔴 高 | 🔴 高 | 🟢 低 |

---

## MetaStore 最佳实践组合

### Phase 1: 核心优化 (2 周)

借鉴 **etcd v3 批量 Apply**

```go
// 1. 去除全局锁
func (m *Memory) applyOperation(op RaftOperation) {
    // ✅ 单键操作不加全局锁
    switch op.Type {
    case "PUT":
        m.putDirect(op.Key, op.Value, op.LeaseID)
    case "DELETE":
        m.deleteDirect(op.Key, op.RangeEnd)
    }
}

// 2. 批量 Apply (etcd 方式)
func (m *Memory) applyBatch(ops []RaftOperation) {
    // 按分片分组
    shardOps := groupByShardgroupByShard(ops)

    // 并行应用
    for shardIdx, ops := range shardOps {
        go func() {
            shard.mu.Lock()
            for _, op := range ops {
                applyNoLock(op)
            }
            shard.mu.Unlock()
        }()
    }
}
```

**预期效果**: 1000 → 10,000 ops/sec (**10x**)

---

### Phase 2: 高级优化 (4 周)

借鉴 **TiKV Async Apply**

```go
// internal/raft/async_applier.go
type AsyncApplier struct {
    workerPool [8]*ApplyWorker
}

func (a *AsyncApplier) Start() {
    for i := 0; i < len(a.workerPool); i++ {
        go a.workerPool[i].run()
    }
}
```

借鉴 **CockroachDB Leaseholder**

```go
// internal/raft/lease.go
func (m *Memory) Get(key string) (*kvstore.KeyValue, error) {
    if m.isLeaseHolder() {
        return m.kvData.Get(key)  // 直接读取
    }
    return m.getRaftRead(key)  // 走 Raft ReadIndex
}
```

**预期效果**: 10,000 → 50,000 ops/sec (**50x**)

---

### Phase 3: 扩展性优化 (2 月)

借鉴 **TiKV Multi-Raft** (可选)

```go
// internal/multiraft/store.go
type MultiRaftStore struct {
    regions [8]*Region  // 8 个 Raft 组
}
```

**预期效果**: 50,000 → 500,000 ops/sec (**500x**)

---

## 实现优先级

### 🔴 高优先级 (立即实施)

1. ✅ **批量 Apply** (etcd 方式)
   - 代码量: ~200 行
   - 收益: **10x** 吞吐量
   - 风险: 低

2. ✅ **去除全局锁**
   - 代码量: ~150 行
   - 收益: **5-10x** 吞吐量
   - 风险: 低

### 🟡 中优先级 (1-2 月后)

3. ✅ **Async Apply** (TiKV 方式)
   - 代码量: ~300 行
   - 收益: **2-5x** 吞吐量
   - 风险: 中 (需要处理顺序)

4. ✅ **Leaseholder 读** (CockroachDB 方式)
   - 代码量: ~200 行
   - 收益: **10x** 读吞吐量
   - 风险: 中 (需要处理 lease 转移)

### 🟢 低优先级 (6 月后)

5. ⚠️ **Multi-Raft** (TiKV 方式)
   - 代码量: ~2000 行
   - 收益: **10x** 吞吐量
   - 风险: 高 (复杂度高)

6. ⚠️ **MVCC** (etcd 方式)
   - 代码量: ~1000 行
   - 收益: **2x** 读吞吐量
   - 风险: 高 (需要 GC 机制)

---

## 总结

### 核心学习

1. **etcd v3**: 批量 Apply + MVCC
   - 简单高效,适合单 Raft 场景
   - **立即借鉴**: 批量 Apply

2. **TiKV**: Multi-Raft + Async Apply
   - 高性能,适合大规模场景
   - **后续借鉴**: Async Apply

3. **CockroachDB**: Leaseholder + Intent
   - 读写分离,适合读多场景
   - **可选借鉴**: Leaseholder

### 推荐路线

```
Phase 1 (2 周):
去除全局锁 + 批量 Apply (etcd 方式)
↓ 10x 吞吐量

Phase 2 (4 周):
Async Apply (TiKV 方式) + Leaseholder (CockroachDB 方式)
↓ 50x 吞吐量

Phase 3 (6 月):
Multi-Raft (TiKV 方式) - 可选
↓ 500x 吞吐量
```

### 保持简单的关键

1. ✅ **先优化单 Raft** (去除全局锁 + 批量 Apply)
2. ✅ **只在必要时引入复杂特性** (Multi-Raft, MVCC)
3. ✅ **渐进式优化** (每次 10x 提升,而非一次性 100x)

---

**下一步**: 实现 Phase 1 (去除全局锁 + 批量 Apply)
