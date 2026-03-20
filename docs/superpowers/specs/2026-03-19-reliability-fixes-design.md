# MetaStore Reliability Fixes Design

**Date**: 2026-03-19
**Scope**: Critical + High severity issues (7 issues, 8 files)
**Constraint**: Strict backward compatibility — no external API behavior changes
**Approach**: Pattern-based unified fixes (Approach B)

## Context

A reliability audit of the MetaStore codebase identified 22 issues across 4 severity levels. This design covers the 7 Critical + High issues that pose the greatest risk to production stability. Two issues originally classified as High (#4 pendingOps leak, #7 LeaseManager cache divergence) were removed after deeper code review confirmed they are already handled by existing mechanisms.

## Issues Addressed

| # | Severity | Component | Problem |
|---|----------|-----------|---------|
| 1 | Critical | WatchManager | TOCTOU race in `CreateWithID` — triple lock/unlock allows capacity bypass and duplicate watchID |
| 2 | Critical | AuthManager | Shared pointer mutation — concurrent readers see partially updated `UserInfo`/`RoleInfo` |
| 3 | High | memory/watch + pebbledb | Unbounded goroutines in `slowSendEvent` — slow watchers cause goroutine explosion |
| 5 | High | LeaseManager | TOCTOU in `Grant` — RLock capacity check separated from Lock insert |
| 6 | High | ClusterManager | Optimistic member map update before Raft commit — phantom members visible to readers |
| 8 | High | main.go | GracefulShutdown manager exists but is not wired into the application lifecycle |
| 9 | High | raft/node_memory | WAL Save and saveSnap return values silently discarded |

## Issues Removed After Review

| # | Original Severity | Reason for Removal |
|---|-------------------|--------------------|
| 4 | High | pendingOps already cleaned up on all paths (context cancel + 30s timeout). No leak. |
| 7 | High | LeaseManager rebuilds cache from store on startup (`LoadLeases`) and on every expiry check (`SyncFromStore`). Crash recovery is covered. |

---

## Pattern 1: TOCTOU Race Condition Fixes

**Principle**: Check and mutate must happen within a single critical section. Never release a lock between a guard check and the guarded operation.

### 1.1 WatchManager `CreateWithID`

**File**: `api/etcd/watch_manager.go`

**Current code** (lines 67-121): Three separate lock/unlock cycles with gaps between them. Between the capacity check (RLock) and the map insert (Lock), other goroutines can exceed `maxWatchCount`. Between the ID uniqueness check and the store Watch call, another goroutine can claim the same watchID.

**Fix**: Merge capacity check and ID uniqueness check into a single Lock. Use a nil placeholder entry to reserve the watchID while calling `store.Watch` outside the lock (to avoid holding a lock during a potentially blocking store call).

```go
func (wm *WatchManager) CreateWithID(watchID int64, key, rangeEnd string, startRevision int64, opts *kvstore.WatchOptions) int64 {
    if wm.stopped.Load() {
        return -1
    }

    // Single lock: capacity check + ID uniqueness + placeholder insert
    wm.mu.Lock()
    if wm.maxWatchCount > 0 && len(wm.watches) >= wm.maxWatchCount {
        wm.mu.Unlock()
        return -1
    }
    if _, exists := wm.watches[watchID]; exists {
        wm.mu.Unlock()
        return -1
    }
    wm.watches[watchID] = nil // placeholder prevents concurrent duplicate
    wm.mu.Unlock()

    // Store.Watch outside lock (may be slow)
    eventCh, err := createWatch(wm.store, key, rangeEnd, startRevision, watchID, opts)
    if err != nil {
        wm.mu.Lock()
        delete(wm.watches, watchID) // rollback placeholder
        wm.mu.Unlock()
        return -1
    }

    // Replace placeholder with real stream
    ws := &watchStream{
        watchID: watchID, key: key, rangeEnd: rangeEnd,
        startRevision: startRevision, eventCh: eventCh,
    }
    wm.mu.Lock()
    wm.watches[watchID] = ws
    wm.mu.Unlock()

    return watchID
}
```

**Companion change**: `GetEventChan` must handle nil placeholder entries (return `nil, false` if `ws == nil`).

### 1.2 LeaseManager `Grant`

**File**: `api/etcd/lease_manager.go`

**Current code** (lines 120-144): RLock for count → RUnlock → store.LeaseGrant → Lock for cache insert. The count check and insert are in separate critical sections.

**Fix**: Keep the capacity check as an approximate guard (RLock). The store is the source of truth — `store.LeaseGrant` is a Raft proposal that enforces correctness. The cache is only an acceleration layer. The current structure (check → store → cache) is acceptable as a pragmatic tradeoff: strict enforcement would require holding a write lock across a Raft proposal, which risks deadlock.

The TOCTOU here has limited blast radius because the store-level LeaseGrant is idempotent and authoritative. The fix from Pattern 1 design review: no structural change needed beyond the existing flow. The capacity check serves as a fast-reject, not a hard guarantee.

**No code change required** — the existing flow is correct for a cache-over-store architecture. The original audit over-classified this based on the lock pattern without accounting for the store being the source of truth.

### 1.3 ClusterManager `AddMember` / `AddWitnessMember` / `RemoveMember` / `PromoteMember`

**File**: `api/etcd/cluster_manager.go`

**Current code**: Methods add/remove/update members in the local map immediately after sending a ConfChange to the channel, before Raft has committed the change. `ApplyConfChange` (called on Raft commit) also updates the map.

**Fix**: Remove the local map mutation from `AddMember`, `AddWitnessMember`, `RemoveMember`, and `PromoteMember`. Let `ApplyConfChange` be the sole writer to `cm.members`. The returned `*MemberInfo` from Add methods is constructed for the gRPC response but not cached locally.

```go
func (cm *ClusterManager) AddMember(peerURLs []string, isLearner bool) (*MemberInfo, error) {
    cm.mu.Lock()
    defer cm.mu.Unlock()

    memberID := generateMemberID()
    member := &MemberInfo{
        ID: memberID, Name: fmt.Sprintf("node-%d", memberID),
        PeerURLs: peerURLs, ClientURLs: []string{}, IsLearner: isLearner,
    }

    // Build and send ConfChange
    cc := raftpb.ConfChange{Type: ccType, NodeID: memberID, Context: context}
    select {
    case cm.confChangeC <- cc:
    default:
        return nil, fmt.Errorf("confChange channel full")
    }

    // Do NOT add to cm.members here. ApplyConfChange handles it after Raft commit.
    return member, nil
}
```

Same pattern for `AddWitnessMember`. For `RemoveMember`: remove the `delete(cm.members, id)` line. For `PromoteMember`: remove the `member.IsLearner = false` line.

**Note on `RemoveMember` existence check**: The current code checks `cm.members[id]` before sending the ConfChange. After this fix, the check still works because `ApplyConfChange` populates the map — the member must have been committed by Raft to be in the map. This is consistent.

---

## Pattern 2: Copy-on-Write for Shared Pointers

**Principle**: Never mutate a pointer loaded from `sync.Map` in place. Clone first, modify the clone, then Store the clone back.

### AuthManager Shared Pointer Mutation

**File**: `api/etcd/auth_manager.go`

**Problem**: Six methods load a `*UserInfo` or `*RoleInfo` pointer from `syncmap.Map`, mutate fields directly, then Store the same pointer back. Concurrent readers (e.g., `CheckPermission` reading `user.Roles`) can observe partially-updated state.

**Fix**: Add `clone()` methods to `UserInfo` and `RoleInfo`. All write methods clone before mutation.

#### Helper Functions

```go
func (u *UserInfo) clone() *UserInfo {
    c := &UserInfo{
        Name:         u.Name,
        PasswordHash: u.PasswordHash,
        Roles:        make([]string, len(u.Roles)),
        CreatedAt:    u.CreatedAt,
    }
    copy(c.Roles, u.Roles)
    return c
}

func (r *RoleInfo) clone() *RoleInfo {
    c := &RoleInfo{
        Name:        r.Name,
        Permissions: make([]Permission, len(r.Permissions)),
        CreatedAt:   r.CreatedAt,
    }
    copy(c.Permissions, r.Permissions)
    return c
}
```

#### Affected Methods

| Method | Field Mutated | Fix |
|--------|--------------|-----|
| `ChangePassword` | `user.PasswordHash` | `clone()` -> modify clone -> `Store` clone |
| `GrantRole` | `user.Roles` (append) | `clone()` -> append to clone -> `Store` clone |
| `RevokeRole` | `user.Roles` (filter) | `clone()` -> filter clone -> `Store` clone |
| `GrantPermission` | `role.Permissions` (append) | `clone()` -> append to clone -> `Store` clone |
| `RevokePermission` | `role.Permissions` (filter) | `clone()` -> filter clone -> `Store` clone |
| `DeleteRole` | Each user's `Roles` in Range callback | `clone()` each affected user -> `Store` clone |

#### Write Order Fix

`Enable` and `Disable` currently set the atomic bool before persisting to store. If the persist fails, the in-memory state is wrong. Fix: persist first, then update cache on success. Same pattern as `AddUser`/`AddRole` which already do this correctly.

```go
func (am *AuthManager) Enable() error {
    if _, exists := am.users.Load("root"); !exists {
        return fmt.Errorf("root user does not exist")
    }
    if am.enabled.Load() {
        return nil
    }

    // Persist first
    if _, _, err := am.store.PutWithLease(ctx, authEnabledKey, "true", 0); err != nil {
        return err
    }
    // Then update cache
    am.enabled.Store(true)
    return nil
}
```

---

## Pattern 3: Bounded Goroutines for Slow Watchers

**Principle**: Never spawn unbounded goroutines. Use a per-resource semaphore to cap concurrency.

### slowSendEvent Goroutine Explosion

**Files**: `internal/memory/watch.go`, `internal/pebbledb/kvstore.go`

**Current code**: When a watcher's eventCh is full, `notifyWatches` spawns `go slowSendEvent(sub, event)` with no limit. Each goroutine waits up to 5 seconds. Under sustained write load with a slow consumer, thousands of goroutines can accumulate per watcher in the 5-second window.

**Fix**: Add a channel-based semaphore (`slowSendSem`) to each `watchSubscription`, limiting concurrent slowSend goroutines per watcher.

#### Subscription Change

```go
type watchSubscription struct {
    // ... existing fields
    slowSendSem chan struct{} // capacity = max concurrent slowSend goroutines
}

// On creation:
sub := &watchSubscription{
    // ...
    slowSendSem: make(chan struct{}, 8),
}
```

#### Notification Change

```go
// In notifyWatches, replace the bare `go slowSendEvent(...)`:
default:
    select {
    case sub.slowSendSem <- struct{}{}:
        go func() {
            defer func() { <-sub.slowSendSem }()
            m.slowSendEvent(sub, eventToSend)
        }()
    default:
        // Semaphore full — watcher is severely behind, force cancel
        log.Warn("Watch severely behind, force cancelling",
            zap.Int64("watch_id", sub.watchID))
        go m.CancelWatch(sub.watchID)
    }
```

**Goroutine cap**: With semaphore capacity 8, maximum goroutines per watcher is 8 (down from unbounded). Total system goroutines for slow watchers = 8 * number_of_slow_watchers, which is manageable.

**Backward compatibility**: Slow watchers were already force-cancelled after 5 seconds. This change makes the cancellation trigger sooner when a watcher is severely behind, which is strictly better behavior.

**Both files get identical changes**: The `watchSubscription` struct and `notifyWatches` pattern are the same in memory and pebbledb backends.

---

## Pattern 4: Error Handling and Lifecycle Management

### 4.1 Raft WAL/Snapshot Error Handling

**File**: `internal/raft/node_memory.go`

**Current code** (lines 805-808):
```go
if !raft.IsEmptySnap(rd.Snapshot) {
    rc.saveSnap(rd.Snapshot)        // return value discarded
}
rc.wal.Save(rd.HardState, rd.Entries) // return value discarded
```

`node_pebble.go` (lines 820-834) already handles these errors correctly with `log.Fatal`.

**Fix**: Align with pebble node — check errors and Fatal on failure:

```go
if !raft.IsEmptySnap(rd.Snapshot) {
    if err := rc.saveSnap(rd.Snapshot); err != nil {
        log.Fatal("failed to save snapshot",
            zap.Error(err),
            zap.String("component", "raft-memory"))
    }
}
if err := rc.wal.Save(rd.HardState, rd.Entries); err != nil {
    log.Fatal("failed to save WAL",
        zap.Error(err),
        zap.String("component", "raft-memory"))
}
```

**Why Fatal**: WAL/Snapshot write failure means durability is compromised. Continuing would cause silent data loss. In a distributed system, fast-fail is safer — other nodes take over. This matches etcd's own behavior.

### 4.2 GracefulShutdown Integration

**File**: `cmd/metastore/main.go`

**Current code**: `m.Serve()` blocks the main goroutine. No signal handling. Process termination skips all cleanup — Raft state is not persisted, connections are not drained, lease manager is not stopped.

`pkg/reliability/shutdown.go` implements a complete 4-phase shutdown manager that is never used.

**Fix**: Create a `GracefulShutdown` instance in main, register hooks for each component, run `m.Serve()` in a goroutine, and block on `gs.Wait()`.

```go
func main() {
    // ... existing initialization ...

    gs := reliability.NewGracefulShutdown(30 * time.Second)

    // Phase 1: Stop accepting new requests
    gs.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
        etcdServer.GracefulStop()
        return nil
    })
    gs.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
        mysqlServer.Stop()
        return nil
    })

    // Phase 3: Persist state
    gs.RegisterHook(reliability.PhasePersistState, func(ctx context.Context) error {
        leaseManager.Stop()
        return nil
    })

    // Phase 4: Close resources
    gs.RegisterHook(reliability.PhaseCloseResources, func(ctx context.Context) error {
        closeStore()
        return nil
    })
    gs.RegisterHook(reliability.PhaseCloseResources, func(ctx context.Context) error {
        closeListener()
        return nil
    })

    // Run cmux non-blocking
    go func() {
        if err := m.Serve(); err != nil && !gs.IsShuttingDown() {
            log.Fatal("cmux failed", zap.Error(err))
        }
    }()

    // Block until shutdown signal
    gs.Wait()
}
```

**Companion changes**:
- Remove `defer closeStore()` and `defer closeListener()` (managed by shutdown hooks)
- Retain references to `etcdServer`, `mysqlServer`, `leaseManager` in main scope for hook registration
- `startMySQL` and `startEtcd` return values are already captured; ensure they are accessible

---

## Files Changed Summary

| File | Changes |
|------|---------|
| `api/etcd/watch_manager.go` | Single-lock + placeholder pattern in `CreateWithID`; nil check in `GetEventChan` |
| `api/etcd/auth_manager.go` | Add `clone()` to `UserInfo`/`RoleInfo`; fix 6 write methods; fix `Enable`/`Disable` write order |
| `api/etcd/cluster_manager.go` | Remove local map mutations from `AddMember`, `AddWitnessMember`, `RemoveMember`, `PromoteMember` |
| `api/etcd/lease_manager.go` | No change (re-evaluated: current flow is correct for cache-over-store) |
| `internal/memory/watch.go` | Add `slowSendSem` to `watchSubscription`; semaphore check in `notifyWatches` |
| `internal/pebbledb/kvstore.go` | Same semaphore pattern as memory |
| `internal/raft/node_memory.go` | Add error checks for `saveSnap` and `wal.Save` |
| `cmd/metastore/main.go` | Wire `GracefulShutdown`; register hooks; non-blocking cmux |

## Testing Strategy

1. **Unit tests**: Each pattern fix should have targeted tests:
   - TOCTOU: Concurrent `CreateWithID` calls testing capacity limits and duplicate ID rejection
   - COW: Concurrent `ChangePassword` + `CheckPermission` verifying no partial reads
   - Semaphore: Test that slow watcher goroutine count stays bounded
   - Raft errors: Verify Fatal is called on WAL save failure (mock WAL)

2. **Integration tests**: Existing test suite in `test/` must pass without modification (backward compatibility)

3. **Race detector**: Run full test suite with `-race` flag to verify no data races remain in fixed code
