# Watch 实现问题分析与修复方案

## 发现的问题

### 1. 缺失的 etcd Watch 选项支持

**问题**: `WatchCreateRequest` 包含以下字段被完全忽略:

```go
type WatchCreateRequest struct {
    PrevKv         bool   // ❌ 未实现：返回事件的前一个值
    ProgressNotify bool   // ❌ 未实现：定期发送进度通知
    Filters        []FilterType // ❌ 未实现：过滤 PUT/DELETE 事件
    WatchId        int64  // ❌ 未实现：客户端指定 watchID
    Fragment       bool   // ❌ 未实现：分片大事件
}
```

**位置**: [pkg/etcdcompat/watch.go:56-62](../pkg/etcdcompat/watch.go#L56-L62)

**影响**:
- PrevKv 不工作导致客户端无法获取删除前的值
- ProgressNotify 缺失导致客户端无法检测连接活性
- Filters 缺失导致带宽浪费
- WatchId 缺失导致多watch场景下顺序无法保证

### 2. startRevision > 0 历史事件重放未实现

**问题**: Watch 从历史 revision 开始时应该重放历史事件

```go
// TODO: 如果 startRevision > 0，需要发送历史事件
```

**位置**:
- [internal/memory/kvstore_etcd_watch_lease.go:42](../internal/memory/kvstore_etcd_watch_lease.go#L42)
- [internal/rocksdb/kvstore_etcd_raft.go:727](../internal/rocksdb/kvstore_etcd_raft.go#L727)

**影响**: 客户端无法从指定 revision 恢复 watch，断线重连后会丢失事件

### 3. CancelWatch 重复关闭导致 panic 风险

**问题**: 多次调用 CancelWatch 会 panic

```go
func (r *RocksDBEtcdRaft) CancelWatch(watchID int64) error {
    // ...
    close(sub.cancel)    // ❌ 重复关闭会 panic
    close(sub.eventCh)   // ❌ 重复关闭会 panic
    // ...
}
```

**位置**:
- [internal/memory/kvstore_etcd_watch_lease.go:58-59](../internal/memory/kvstore_etcd_watch_lease.go#L58-L59)
- [internal/rocksdb/kvstore_etcd_raft.go:744-745](../internal/rocksdb/kvstore_etcd_raft.go#L744-L745)

**影响**:
- 并发场景下可能导致程序崩溃
- 客户端重复取消会导致服务端 panic

### 4. notifyWatches 的并发安全问题

**问题**: notifyWatches 在持有读锁时尝试写入 channel

```go
func (r *RocksDBEtcdRaft) notifyWatches(event kvstore.WatchEvent) {
    r.watchMu.RLock()  // 持有读锁
    defer r.watchMu.RUnlock()

    for _, sub := range r.watches {
        select {
        case sub.eventCh <- event:  // ❌ 可能阻塞，持有锁时间过长
        // ...
        }
    }
}
```

**位置**: [internal/rocksdb/kvstore_etcd_raft.go:948-973](../internal/rocksdb/kvstore_etcd_raft.go#L948-L973)

**影响**:
- 慢客户端会阻塞所有其他 watch
- 持有锁时间过长影响并发性能

### 5. 事件 channel 满时直接丢弃事件

**问题**: 当 channel 满时使用 default 直接跳过

```go
select {
case sub.eventCh <- event:
case <-sub.cancel:
default:
    // 通道已满，跳过  // ❌ 静默丢失事件！
}
```

**位置**: 多处

**影响**:
- 客户端丢失事件且无感知
- 违反 etcd 的事件顺序保证

### 6. Watch 生命周期管理不完整

**问题**:
- 没有实现 watch 超时
- 没有实现 watch 重连
- 服务器关闭时 watch 清理不完整

**影响**: 资源泄漏风险

### 7. PrevKv 总是返回

**问题**: 当前实现总是尝试获取和发送 PrevKv

```go
// 添加前一个键值对（如果有）
if event.PrevKv != nil {  // ❌ 没有检查客户端是否请求了 PrevKv
    watchEvent.PrevKv = &mvccpb.KeyValue{...}
}
```

**位置**: [pkg/etcdcompat/watch.go:151-159](../pkg/etcdcompat/watch.go#L151-L159)

**影响**:
- 浪费带宽和CPU
- 不符合 etcd 规范

## 修复方案

### 方案 1: 添加 WatchOptions 支持

#### 1.1 扩展 watchSubscription 结构

```go
type watchSubscription struct {
    key      string
    rangeEnd string
    startRev int64
    eventCh  chan kvstore.WatchEvent
    cancel   chan struct{}
    closed   atomic.Bool  // 防止重复关闭

    // Options
    prevKV         bool
    progressNotify bool
    filters        []kvstore.WatchFilterType
    fragment       bool
}
```

#### 1.2 修改 handleCreateWatch 读取选项

```go
func (s *WatchServer) handleCreateWatch(stream pb.Watch_WatchServer, req *pb.WatchCreateRequest) error {
    key := string(req.Key)
    rangeEnd := string(req.RangeEnd)
    startRevision := req.StartRevision

    // 解析选项
    opts := &watchOptions{
        prevKV:         req.PrevKv,
        progressNotify: req.ProgressNotify,
        filters:        convertFilters(req.Filters),
        fragment:       req.Fragment,
    }

    // 支持客户端指定 watchID
    watchID := req.WatchId
    if watchID == 0 {
        watchID = s.server.watchMgr.Create(key, rangeEnd, startRevision, opts)
    } else {
        watchID = s.server.watchMgr.CreateWithID(watchID, key, rangeEnd, startRevision, opts)
    }

    // ...
}
```

### 方案 2: 实现历史事件重放

需要存储变更历史。两种方案：

#### 方案 2.1: 使用 WAL（推荐用于 RocksDB）

```go
func (r *RocksDBEtcdRaft) Watch(..., startRevision int64, ...) {
    // ...

    if startRevision > 0 && startRevision < r.CurrentRevision() {
        // 从 WAL 重放历史事件
        go r.replayHistoryEvents(sub, startRevision)
    }

    return eventCh, nil
}

func (r *RocksDBEtcdRaft) replayHistoryEvents(sub *watchSubscription, startRev int64) {
    // 使用 RocksDB TransactionLogIterator
    iter := r.db.GetUpdatesSince(startRev)
    defer iter.Close()

    for iter.Valid() {
        batch := iter.GetBatch()
        // 解析并发送历史事件
        for _, update := range batch.Updates {
            if r.matchWatch(update.Key, sub.key, sub.rangeEnd) {
                event := r.convertToWatchEvent(update)
                select {
                case sub.eventCh <- event:
                case <-sub.cancel:
                    return
                }
            }
        }
        iter.Next()
    }
}
```

#### 方案 2.2: 维护独立的事件历史缓存（适用于内存和 RocksDB）

```go
type EventHistory struct {
    mu      sync.RWMutex
    events  []kvstore.WatchEvent  // 环形缓冲区
    maxSize int
    oldest  int64  // 最旧事件的 revision
}

func (h *EventHistory) Add(event kvstore.WatchEvent) {
    h.mu.Lock()
    defer h.mu.Unlock()

    // 添加到环形缓冲区
    h.events = append(h.events, event)
    if len(h.events) > h.maxSize {
        h.events = h.events[1:]
        h.oldest = h.events[0].Revision
    }
}

func (h *EventHistory) GetEventsFrom(revision int64) []kvstore.WatchEvent {
    h.mu.RLock()
    defer h.mu.RUnlock()

    if revision < h.oldest {
        return nil  // 历史已被压缩
    }

    var result []kvstore.WatchEvent
    for _, e := range h.events {
        if e.Revision >= revision {
            result = append(result, e)
        }
    }
    return result
}
```

### 方案 3: 修复 CancelWatch 重复关闭问题

```go
type watchSubscription struct {
    // ...
    closed   atomic.Bool
    closeOnce sync.Once
}

func (r *RocksDBEtcdRaft) CancelWatch(watchID int64) error {
    r.watchMu.Lock()
    sub, ok := r.watches[watchID]
    if !ok {
        r.watchMu.Unlock()
        return fmt.Errorf("watch not found: %d", watchID)
    }

    // 标记为已关闭
    if !sub.closed.CompareAndSwap(false, true) {
        r.watchMu.Unlock()
        return nil  // 已经关闭过了
    }

    delete(r.watches, watchID)
    r.watchMu.Unlock()

    // 使用 sync.Once 确保只关闭一次
    sub.closeOnce.Do(func() {
        close(sub.cancel)
        close(sub.eventCh)
    })

    return nil
}
```

### 方案 4: 改进 notifyWatches 并发性能

```go
func (r *RocksDBEtcdRaft) notifyWatches(event kvstore.WatchEvent) {
    // 快速复制 watch 列表（减少锁持有时间）
    r.watchMu.RLock()
    watchesCopy := make([]*watchSubscription, 0, len(r.watches))
    for _, sub := range r.watches {
        if r.matchWatch(getEventKey(event), sub.key, sub.rangeEnd) {
            watchesCopy = append(watchesCopy, sub)
        }
    }
    r.watchMu.RUnlock()

    // 在锁外发送事件
    for _, sub := range watchesCopy {
        if sub.closed.Load() {
            continue
        }

        // 应用过滤器
        if r.shouldFilterEvent(event, sub.filters) {
            continue
        }

        // 根据 prevKV 选项决定是否包含前值
        eventToSend := event
        if !sub.prevKV {
            eventToSend.PrevKv = nil
        }

        // 非阻塞发送，满了就异步处理
        select {
        case sub.eventCh <- eventToSend:
        case <-sub.cancel:
        default:
            // Channel 满了，启动异步发送或记录警告
            go r.slowSend(sub, eventToSend)
        }
    }
}

func (r *RocksDBEtcdRaft) slowSend(sub *watchSubscription, event kvstore.WatchEvent) {
    // 慢客户端处理：重试或断开连接
    timer := time.NewTimer(5 * time.Second)
    defer timer.Stop()

    select {
    case sub.eventCh <- event:
    case <-sub.cancel:
    case <-timer.C:
        // 超时，强制关闭该 watch
        log.Printf("Watch %d slow, force closing", sub.watchID)
        r.CancelWatch(sub.watchID)
    }
}
```

### 方案 5: 实现 ProgressNotify

```go
func (s *WatchServer) sendEvents(stream pb.Watch_WatchServer, watchID int64) {
    eventCh, ok := s.server.watchMgr.GetEventChan(watchID)
    if !ok {
        return
    }

    sub := s.server.watchMgr.GetWatch(watchID)
    if sub == nil {
        return
    }

    // ProgressNotify 定时器
    var progressTicker *time.Ticker
    if sub.progressNotify {
        progressTicker = time.NewTicker(10 * time.Second)
        defer progressTicker.Stop()
    }

    for {
        select {
        case event, ok := <-eventCh:
            if !ok {
                return
            }
            // 发送事件...

        case <-progressTicker.C:
            // 发送进度通知
            if err := stream.Send(&pb.WatchResponse{
                Header:  s.server.getResponseHeader(),
                WatchId: watchID,
            }); err != nil {
                return
            }
        }
    }
}
```

## 优先级

### P0 (必须修复)
1. ✅ CancelWatch 重复关闭问题 - 会导致 panic
2. ✅ notifyWatches 并发性能问题 - 影响所有 watch
3. ✅ PrevKv 选项支持 - 核心功能

### P1 (重要)
4. startRevision 历史重放 - 影响断线重连
5. Filters 支持 - 节省带宽
6. WatchId 客户端指定 - 多 watch 场景

### P2 (可选)
7. ProgressNotify 支持 - 连接健康检查
8. Fragment 支持 - 大事件场景
9. 事件满时的慢客户端处理 - 提升健壮性

## 测试计划

### 基础功能测试
- [x] Watch PUT 事件
- [x] Watch DELETE 事件
- [x] Watch 范围查询
- [ ] Watch startRevision > 0
- [ ] Watch PrevKv 选项
- [ ] Watch Filters 选项

### 异常场景测试
- [ ] 重复 CancelWatch
- [ ] 并发创建和取消 watch
- [ ] 客户端断开连接
- [ ] Channel 满时的行为
- [ ] 无效 watchID
- [ ] 无效 startRevision

### 性能测试
- [ ] 1000个并发 watch
- [ ] 高频事件通知
- [ ] 慢客户端影响
- [ ] 内存泄漏检测

## 实施步骤

1. ✅ 定义 WatchOptions 类型
2. ⬜ 修复 CancelWatch 重复关闭
3. ⬜ 实现 PrevKv 选项
4. ⬜ 实现 Filters 选项
5. ⬜ 改进 notifyWatches 并发性能
6. ⬜ 实现历史事件重放
7. ⬜ 实现 WatchId 客户端指定
8. ⬜ 实现 ProgressNotify
9. ⬜ 编写完整测试用例
10. ⬜ 性能测试和优化
