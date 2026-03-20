# MetaStore Reliability Fixes Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix 7 Critical+High reliability issues using 4 unified fix patterns across 8 files.

**Architecture:** Pattern-based approach — TOCTOU race fixes (single critical section), copy-on-write for shared pointers, bounded goroutines via semaphore, and error handling/lifecycle wiring. All fixes are internal — no external API behavior changes.

**Tech Stack:** Go, sync primitives, channel-based semaphores, `pkg/reliability` shutdown manager.

**Spec:** `docs/superpowers/specs/2026-03-19-reliability-fixes-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `api/etcd/watch_manager.go` | Modify | Fix TOCTOU race in `CreateWithID` (Task 1) |
| `api/etcd/watch_manager_test.go` | Create | Tests for WatchManager TOCTOU fix (Task 1) |
| `api/etcd/auth_manager.go` | Modify | Clone-before-mutate for 6 write methods + Enable/Disable write order (Task 2) |
| `api/etcd/auth_manager_test.go` | Modify | Concurrency tests for COW fix (Task 2) |
| `api/etcd/cluster_manager.go` | Modify | Remove optimistic map mutations (Task 3) |
| `api/etcd/cluster_manager_test.go` | Create | Tests for deferred-apply pattern (Task 3) |
| `internal/memory/store.go` | Modify | Add `slowSendSem` field to `watchSubscription` (Task 4) |
| `internal/memory/watch.go` | Modify | Semaphore-gated `slowSendEvent` dispatch (Task 4) |
| `internal/memory/watch_test.go` | Create | Bounded goroutine test (Task 4) |
| `internal/pebbledb/kvstore.go` | Modify | Same semaphore pattern as memory (Task 5) |
| `internal/pebbledb/watch_test.go` | Create | Bounded goroutine test for pebbledb (Task 5) |
| `internal/raft/node_memory.go` | Modify | Error checks for saveSnap and wal.Save (Task 6) |
| `internal/raft/node_memory_test.go` | Create | Verify Fatal on WAL error (Task 6) |
| `cmd/metastore/main.go` | Modify | Wire GracefulShutdown, capture server refs, non-blocking cmux (Task 7) |

---

## Task 1: WatchManager TOCTOU Race Fix

**Issue:** #1 Critical — Triple lock/unlock in `CreateWithID` allows capacity bypass and duplicate watchID.

**Files:**
- Modify: `api/etcd/watch_manager.go:67-121` (CreateWithID), `api/etcd/watch_manager.go:140-149` (GetEventChan)
- Create: `api/etcd/watch_manager_test.go`

### Step 1.1: Write failing test — concurrent CreateWithID exceeds capacity

```go
// api/etcd/watch_manager_test.go
package etcd

import (
	"sync"
	"sync/atomic"
	"testing"

	"metaStore/internal/memory"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
)

func TestCreateWithID_ConcurrentCapacityLimit(t *testing.T) {
	store := memory.NewMemoryEtcd()
	limitsCfg := &config.LimitsConfig{MaxWatchCount: 10}
	wm := NewWatchManager(store, limitsCfg)
	defer wm.Stop()

	var wg sync.WaitGroup
	var successCount atomic.Int64

	// Launch 50 goroutines all trying to create watches concurrently
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			watchID := int64(id + 1)
			result := wm.CreateWithID(watchID, "/test", "", 0, nil)
			if result != -1 {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if successCount.Load() > 10 {
		t.Errorf("capacity exceeded: got %d watches, max is 10", successCount.Load())
	}
}

func TestCreateWithID_DuplicateIDRejected(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	// First creation should succeed
	result1 := wm.CreateWithID(42, "/test", "", 0, nil)
	if result1 == -1 {
		t.Fatal("first CreateWithID should succeed")
	}

	// Duplicate should be rejected
	result2 := wm.CreateWithID(42, "/test", "", 0, nil)
	if result2 != -1 {
		t.Error("duplicate watchID should be rejected")
	}
}

func TestCreateWithID_ConcurrentDuplicateID(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	var wg sync.WaitGroup
	var successCount atomic.Int64

	// 20 goroutines all trying to create the same watchID
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := wm.CreateWithID(99, "/test", "", 0, nil)
			if result != -1 {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if successCount.Load() != 1 {
		t.Errorf("exactly 1 should succeed, got %d", successCount.Load())
	}
}

func TestGetEventChan_NilPlaceholder(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	// Insert nil placeholder directly (simulating in-progress creation)
	wm.mu.Lock()
	wm.watches[999] = nil
	wm.mu.Unlock()

	ch, ok := wm.GetEventChan(999)
	if ok {
		t.Error("GetEventChan should return false for nil placeholder")
	}
	if ch != nil {
		t.Error("channel should be nil for placeholder entry")
	}
}
```

- [ ] **Step 1.1a: Create test file**

Write the test file above to `api/etcd/watch_manager_test.go`.

- [ ] **Step 1.1b: Run tests to verify they fail**

Run: `go test ./api/etcd/ -run "TestCreateWithID_Concurrent|TestGetEventChan_NilPlaceholder" -count=1 -race -v`
Expected: `TestCreateWithID_ConcurrentCapacityLimit` may intermittently fail (race), `TestGetEventChan_NilPlaceholder` will panic or fail (no nil check).

### Step 1.2: Implement CreateWithID fix

- [ ] **Step 1.2a: Fix `CreateWithID` in `api/etcd/watch_manager.go:67-121`**

Replace the entire `CreateWithID` method:

```go
// CreateWithID creates a watch with specified watchID
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

	// Create watch from store (outside lock — may be slow)
	var eventCh <-chan kvstore.WatchEvent
	var err error

	// Try to call WatchWithOptions if available
	type watchWithOptions interface {
		WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error)
	}

	if wwo, ok := wm.store.(watchWithOptions); ok && opts != nil {
		eventCh, err = wwo.WatchWithOptions(key, rangeEnd, startRevision, watchID, opts)
	} else {
		eventCh, err = wm.store.Watch(context.Background(), key, rangeEnd, startRevision, watchID)
	}

	if err != nil {
		wm.mu.Lock()
		delete(wm.watches, watchID) // rollback placeholder
		wm.mu.Unlock()
		return -1
	}

	ws := &watchStream{
		watchID:       watchID,
		key:           key,
		rangeEnd:      rangeEnd,
		startRevision: startRevision,
		eventCh:       eventCh,
	}

	// Replace placeholder with real stream
	wm.mu.Lock()
	wm.watches[watchID] = ws
	wm.mu.Unlock()

	return watchID
}
```

- [ ] **Step 1.2b: Fix `GetEventChan` in `api/etcd/watch_manager.go:140-149`**

Replace the method to handle nil placeholder entries:

```go
// GetEventChan gets the event channel for a watch
func (wm *WatchManager) GetEventChan(watchID int64) (<-chan kvstore.WatchEvent, bool) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	ws, ok := wm.watches[watchID]
	if !ok || ws == nil {
		return nil, false
	}
	return ws.eventCh, true
}
```

- [ ] **Step 1.2c: Run tests to verify they pass**

Run: `go test ./api/etcd/ -run "TestCreateWithID|TestGetEventChan" -count=1 -race -v`
Expected: All PASS, no race conditions detected.

- [ ] **Step 1.2d: Run full etcd package tests**

Run: `go test ./api/etcd/ -count=1 -race -v`
Expected: All existing tests PASS (backward compatibility).

- [ ] **Step 1.2e: Commit**

```bash
git add api/etcd/watch_manager.go api/etcd/watch_manager_test.go
git commit -m "fix: eliminate TOCTOU race in WatchManager.CreateWithID

Use single critical section for capacity check + ID uniqueness + placeholder
insert. Store.Watch runs outside lock with rollback on failure. GetEventChan
now handles nil placeholder entries.

Issue #1 (Critical): capacity bypass and duplicate watchID under concurrency."
```

---

## Task 2: AuthManager Copy-on-Write Fix

**Issue:** #2 Critical — Shared pointer mutation allows concurrent readers to see partially updated state.

**Files:**
- Modify: `api/etcd/auth_manager.go:147-183` (Enable/Disable), `api/etcd/auth_manager.go:427-464` (ChangePassword), `api/etcd/auth_manager.go:468-500` (GrantRole), `api/etcd/auth_manager.go:503-538` (RevokeRole), `api/etcd/auth_manager.go:572-615` (DeleteRole), `api/etcd/auth_manager.go:655-677` (GrantPermission), `api/etcd/auth_manager.go:680-717` (RevokePermission)
- Modify: `api/etcd/auth_test.go` (add concurrency test)

### Step 2.1: Write failing concurrency test

- [ ] **Step 2.1a: Add concurrency test to `api/etcd/auth_test.go`**

Append these test functions to the end of `api/etcd/auth_test.go`:

```go
// TestAuthConcurrentChangePasswordAndCheck verifies that changing a password
// while CheckPermission reads user.Roles does not cause a data race.
func TestAuthConcurrentChangePasswordAndCheck(t *testing.T) {
	store := memory.NewMemoryEtcd()
	cfg := &config.AuthConfig{
		TokenTTL:             5 * time.Minute,
		TokenCleanupInterval: 1 * time.Minute,
		BcryptCost:           4, // low cost for fast tests
		EnableAudit:          false,
	}
	am := NewAuthManager(store, cfg)

	// Setup: create user with a role and permission
	if err := am.AddUser("testuser", "pass123"); err != nil {
		t.Fatal(err)
	}
	if err := am.AddRole("testrole"); err != nil {
		t.Fatal(err)
	}
	if err := am.GrantRole("testuser", "testrole"); err != nil {
		t.Fatal(err)
	}
	if err := am.GrantPermission("testrole", Permission{
		Type: PermissionRead, Key: []byte("/"), RangeEnd: []byte("\x00"),
	}); err != nil {
		t.Fatal(err)
	}

	// Concurrent: ChangePassword vs CheckPermission
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_ = am.ChangePassword("testuser", fmt.Sprintf("newpass%d", n))
		}(i)
		go func() {
			defer wg.Done()
			// Must not panic or observe inconsistent state
			_ = am.CheckPermission("testuser", []byte("/foo"), PermissionRead)
		}()
	}
	wg.Wait()
}

// TestAuthConcurrentGrantRoleAndCheck verifies that GrantRole does not
// cause a data race with concurrent CheckPermission.
func TestAuthConcurrentGrantRoleAndCheck(t *testing.T) {
	store := memory.NewMemoryEtcd()
	cfg := &config.AuthConfig{
		TokenTTL:             5 * time.Minute,
		TokenCleanupInterval: 1 * time.Minute,
		BcryptCost:           4,
		EnableAudit:          false,
	}
	am := NewAuthManager(store, cfg)

	if err := am.AddUser("testuser", "pass123"); err != nil {
		t.Fatal(err)
	}
	// Pre-create many roles
	for i := 0; i < 20; i++ {
		if err := am.AddRole(fmt.Sprintf("role-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_ = am.GrantRole("testuser", fmt.Sprintf("role-%d", n))
		}(i)
		go func() {
			defer wg.Done()
			_ = am.CheckPermission("testuser", []byte("/foo"), PermissionRead)
		}()
	}
	wg.Wait()
}
```

Note: These tests require adding `"fmt"` and `"sync"` to the import block if not already present.

- [ ] **Step 2.1b: Run test with race detector to verify race is detected**

Run: `go test ./api/etcd/ -run "TestAuthConcurrent" -count=1 -race -v`
Expected: Race detector reports data races on `user.Roles` or `user.PasswordHash`.

### Step 2.2: Add clone methods to UserInfo and RoleInfo

- [ ] **Step 2.2a: Add `clone()` methods**

Add after the `UserInfo` and `RoleInfo` struct definitions (find them with grep — they are in `api/etcd/types.go` or similar). They should be placed in `api/etcd/auth_manager.go` near the top, after the struct references. Insert after the closing brace of `NewAuthManager`:

```go
// clone returns a deep copy of UserInfo, safe for mutation.
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

// clone returns a deep copy of RoleInfo, safe for mutation.
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

### Step 2.3: Fix the 6 write methods

- [ ] **Step 2.3a: Fix `ChangePassword` (line 441)**

Replace:
```go
	// 3. Update user info
	user.PasswordHash = passwordHash
```
With:
```go
	// 3. Clone and update (copy-on-write for concurrent safety)
	user = user.clone()
	user.PasswordHash = passwordHash

	// Update cache with cloned copy
	am.users.Store(name, user)
```

Remove no other code — the persist logic at lines 444-451 stays, but now serializes the clone.

- [ ] **Step 2.3b: Fix `GrantRole` (line 487)**

Replace:
```go
	// 2. Add role to user's role list
	user.Roles = append(user.Roles, rolename)
```
With:
```go
	// 2. Clone and add role (copy-on-write for concurrent safety)
	user = user.clone()
	user.Roles = append(user.Roles, rolename)

	// Update cache with cloned copy
	am.users.Store(username, user)
```

- [ ] **Step 2.3c: Fix `RevokeRole` (line 525)**

Replace:
```go
	user.Roles = newRoles
```
With:
```go
	// Clone and update (copy-on-write for concurrent safety)
	user = user.clone()
	user.Roles = newRoles

	// Update cache with cloned copy
	am.users.Store(username, user)
```

Note: The `newRoles` slice was already built from scratch (lines 511-519), so only the pointer swap needs the clone to avoid mutating the shared pointer's other fields while readers access them.

- [ ] **Step 2.3d: Fix `DeleteRole` (lines 585-603)**

Replace the Range callback:
```go
	am.users.Range(func(username string, user *UserInfo) bool {
		newRoles := make([]string, 0, len(user.Roles))
		hasRole := false
		for _, r := range user.Roles {
			if r != name {
				newRoles = append(newRoles, r)
			} else {
				hasRole = true
			}
		}
		if hasRole {
			user.Roles = newRoles
			// Persist user info
			key := authUserPrefix + username
			data, _ := json.Marshal(user)
			_, _, _ = am.store.PutWithLease(context.Background(), key, string(data), 0)
		}
		return true
	})
```
With:
```go
	am.users.Range(func(username string, user *UserInfo) bool {
		newRoles := make([]string, 0, len(user.Roles))
		hasRole := false
		for _, r := range user.Roles {
			if r != name {
				newRoles = append(newRoles, r)
			} else {
				hasRole = true
			}
		}
		if hasRole {
			// Clone and update (copy-on-write for concurrent safety)
			updated := user.clone()
			updated.Roles = newRoles
			am.users.Store(username, updated)
			// Persist user info
			key := authUserPrefix + username
			data, _ := json.Marshal(updated)
			_, _, _ = am.store.PutWithLease(context.Background(), key, string(data), 0)
		}
		return true
	})
```

- [ ] **Step 2.3e: Fix `GrantPermission` (line 664)**

Replace:
```go
	// Add permission
	role.Permissions = append(role.Permissions, perm)
```
With:
```go
	// Clone and add permission (copy-on-write for concurrent safety)
	role = role.clone()
	role.Permissions = append(role.Permissions, perm)

	// Update cache with cloned copy
	am.roles.Store(rolename, role)
```

- [ ] **Step 2.3f: Fix `RevokePermission` (line 704)**

Replace:
```go
	role.Permissions = newPerms
```
With:
```go
	// Clone and update (copy-on-write for concurrent safety)
	role = role.clone()
	role.Permissions = newPerms

	// Update cache with cloned copy
	am.roles.Store(rolename, role)
```

### Step 2.4: Fix Enable/Disable write order

- [ ] **Step 2.4a: Fix `Enable` (lines 158-163)**

Replace:
```go
	// 3. Set enabled = true (atomic operation)
	am.enabled.Store(true)

	// 4. Persist to storage
	_, _, err := am.store.PutWithLease(context.Background(), authEnabledKey, "true", 0)
	return err
```
With:
```go
	// 3. Persist first (store is source of truth)
	if _, _, err := am.store.PutWithLease(context.Background(), authEnabledKey, "true", 0); err != nil {
		return err
	}

	// 4. Update cache on success
	am.enabled.Store(true)
	return nil
```

- [ ] **Step 2.4b: Fix `Disable` (lines 168-173)**

Replace:
```go
	// 1. Set enabled = false (atomic operation)
	am.enabled.Store(false)

	// 2. Persist to storage
	if _, _, err := am.store.PutWithLease(context.Background(), authEnabledKey, "false", 0); err != nil {
		return err
	}
```
With:
```go
	// 1. Persist first (store is source of truth)
	if _, _, err := am.store.PutWithLease(context.Background(), authEnabledKey, "false", 0); err != nil {
		return err
	}

	// 2. Update cache on success
	am.enabled.Store(false)
```

### Step 2.5: Verify

- [ ] **Step 2.5a: Run concurrency tests with race detector**

Run: `go test ./api/etcd/ -run "TestAuthConcurrent" -count=5 -race -v`
Expected: All PASS, no data races.

- [ ] **Step 2.5b: Run full auth test suite**

Run: `go test ./api/etcd/ -run "TestAuth" -count=1 -race -v`
Expected: All existing + new tests PASS.

- [ ] **Step 2.5c: Commit**

```bash
git add api/etcd/auth_manager.go api/etcd/auth_test.go
git commit -m "fix: copy-on-write for AuthManager shared pointers + write order fix

Add clone() to UserInfo/RoleInfo. All 6 write methods now clone before
mutation to prevent concurrent readers from seeing partial updates.
Enable/Disable now persist to store before updating atomic cache.

Issue #2 (Critical): concurrent readers observe partially-updated state."
```

---

## Task 3: ClusterManager Deferred-Apply Fix

**Issue:** #6 High — Optimistic member map mutation before Raft commit creates phantom members.

**Files:**
- Modify: `api/etcd/cluster_manager.go:57-109` (AddMember), `api/etcd/cluster_manager.go:114-160` (AddWitnessMember), `api/etcd/cluster_manager.go:163-192` (RemoveMember), `api/etcd/cluster_manager.go:248-282` (PromoteMember)
- Create: `api/etcd/cluster_manager_test.go`

### Step 3.1: Write failing test

- [ ] **Step 3.1a: Create test file**

```go
// api/etcd/cluster_manager_test.go
package etcd

import (
	"testing"

	"go.etcd.io/raft/v3/raftpb"
)

func TestAddMember_NotVisibleBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	member, err := cm.AddMember([]string{"http://127.0.0.1:9021"}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Member should NOT be in the map yet (Raft hasn't committed)
	members := cm.ListMembers()
	for _, m := range members {
		if m.ID == member.ID {
			t.Errorf("member %d should not be visible before ApplyConfChange", member.ID)
		}
	}

	// Drain the confChange and simulate Raft commit via ApplyConfChange
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now it should be visible
	members = cm.ListMembers()
	found := false
	for _, m := range members {
		if m.ID == member.ID {
			found = true
		}
	}
	if !found {
		t.Error("member should be visible after ApplyConfChange")
	}
}

func TestRemoveMember_NotRemovedBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	// Add a member via ApplyConfChange (committed state)
	cm.ApplyConfChange(raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddNode,
		NodeID: 42,
	}, raftpb.ConfState{})

	// Verify member exists
	_, err := cm.GetMember(42)
	if err != nil {
		t.Fatal("member should exist before removal")
	}

	// Call RemoveMember
	if err := cm.RemoveMember(42); err != nil {
		t.Fatal(err)
	}

	// Member should still be in the map (Raft hasn't committed removal)
	_, err = cm.GetMember(42)
	if err != nil {
		t.Error("member should still be visible before ApplyConfChange removes it")
	}

	// Drain confChange and apply
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now it should be gone
	_, err = cm.GetMember(42)
	if err == nil {
		t.Error("member should be gone after ApplyConfChange")
	}
}

func TestPromoteMember_NotPromotedBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	// Add learner via ApplyConfChange
	cm.ApplyConfChange(raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddLearnerNode,
		NodeID: 77,
	}, raftpb.ConfState{})

	// Verify it's a learner
	member, _ := cm.GetMember(77)
	if !member.IsLearner {
		t.Fatal("should be learner")
	}

	// Promote
	if err := cm.PromoteMember(77); err != nil {
		t.Fatal(err)
	}

	// Should still be learner (Raft hasn't committed)
	member, _ = cm.GetMember(77)
	if !member.IsLearner {
		t.Error("member should still be learner before ApplyConfChange")
	}

	// Apply
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now should be voter
	member, _ = cm.GetMember(77)
	if member.IsLearner {
		t.Error("member should be voter after ApplyConfChange")
	}
}

func TestAddWitnessMember_NotVisibleBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	member, err := cm.AddWitnessMember([]string{"http://127.0.0.1:9031"})
	if err != nil {
		t.Fatal(err)
	}

	// Should NOT be visible yet
	members := cm.ListMembers()
	for _, m := range members {
		if m.ID == member.ID {
			t.Errorf("witness member %d should not be visible before ApplyConfChange", member.ID)
		}
	}
}
```

- [ ] **Step 3.1b: Run tests to verify they fail**

Run: `go test ./api/etcd/ -run "TestAddMember_NotVisible|TestRemoveMember_NotRemoved|TestPromoteMember_NotPromoted|TestAddWitnessMember_NotVisible" -count=1 -v`
Expected: Tests fail because members are currently added/removed immediately.

### Step 3.2: Remove optimistic mutations

- [ ] **Step 3.2a: Fix `AddMember` — remove line 105**

In `api/etcd/cluster_manager.go`, remove line `cm.members[memberID] = member` (line 105) from `AddMember`. The method should end with `return member, nil` after the confChangeC send, without storing into the map.

- [ ] **Step 3.2b: Fix `AddWitnessMember` — remove line 156**

Remove `cm.members[memberID] = member` (line 156) from `AddWitnessMember`.

- [ ] **Step 3.2c: Fix `RemoveMember` — remove line 189**

Remove `delete(cm.members, id)` (line 189) from `RemoveMember`.

- [ ] **Step 3.2d: Fix `PromoteMember` — remove line 279**

Remove `member.IsLearner = false` (line 279) from `PromoteMember`.

### Step 3.3: Verify

- [ ] **Step 3.3a: Run new tests**

Run: `go test ./api/etcd/ -run "TestAddMember_NotVisible|TestRemoveMember_NotRemoved|TestPromoteMember_NotPromoted|TestAddWitnessMember_NotVisible" -count=1 -v`
Expected: All PASS.

- [ ] **Step 3.3b: Run full test suite**

Run: `go test ./api/etcd/ -count=1 -race -v`
Expected: All tests PASS.

- [ ] **Step 3.3c: Commit**

```bash
git add api/etcd/cluster_manager.go api/etcd/cluster_manager_test.go
git commit -m "fix: remove optimistic member map mutations from ClusterManager

AddMember, AddWitnessMember, RemoveMember, PromoteMember no longer mutate
cm.members directly. ApplyConfChange is now the sole writer, ensuring the
map only reflects Raft-committed state.

Issue #6 (High): phantom members visible before Raft commit."
```

---

## Task 4: Memory Backend — Bounded slowSendEvent Goroutines

**Issue:** #3 High — Unbounded `go slowSendEvent` causes goroutine explosion.

**Files:**
- Modify: `internal/memory/store.go:42-57` (watchSubscription struct)
- Modify: `internal/memory/watch.go:56-67` (WatchWithOptions), `internal/memory/watch.go:242-244` (notifyWatches)
- Create: `internal/memory/watch_test.go`

### Step 4.1: Write failing test

- [ ] **Step 4.1a: Create test file**

```go
// internal/memory/watch_test.go
package memory

import (
	"context"
	"runtime"
	"testing"
	"time"

	"metaStore/internal/kvstore"
)

func TestSlowWatcherBoundedGoroutines(t *testing.T) {
	m := NewMemoryEtcd()

	// Create a watch (the eventCh has buffer of 100)
	eventCh, err := m.Watch(context.Background(), "/test", "\x00", 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Record baseline goroutine count
	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Fill the eventCh buffer (100 events) + generate many more events
	// Without semaphore: each excess event spawns a goroutine (unbounded)
	// With semaphore: capped at 8 goroutines per watcher
	for i := 0; i < 500; i++ {
		m.notifyWatches(kvstore.WatchEvent{
			Type: kvstore.EventTypePut,
			Kv: &kvstore.KeyValue{
				Key:         []byte("/test/key"),
				Value:       []byte("value"),
				ModRevision: int64(i + 1),
			},
			Revision: int64(i + 1),
		})
	}

	// Give goroutines a moment to spawn
	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	goroutineIncrease := current - baseline

	// With semaphore cap of 8, increase should be small (< 20 to allow margin)
	// Without fix, increase would be ~400 (500 - 100 buffer)
	if goroutineIncrease > 20 {
		t.Errorf("goroutine explosion: baseline=%d, current=%d, increase=%d (expected < 20)",
			baseline, current, goroutineIncrease)
	}

	// Drain to clean up
	go func() {
		for range eventCh {
		}
	}()
	m.CancelWatch(1)
}
```

- [ ] **Step 4.1b: Run test to verify it fails**

Run: `go test ./internal/memory/ -run TestSlowWatcherBoundedGoroutines -count=1 -v`
Expected: FAIL with "goroutine explosion" — increase will be ~400.

### Step 4.2: Add semaphore field to watchSubscription

- [ ] **Step 4.2a: Modify `internal/memory/store.go:42-57`**

Add `slowSendSem` field to the `watchSubscription` struct. Replace:

```go
type watchSubscription struct {
	watchID      int64
	key          string
	rangeEnd     string
	startRev     int64
	eventCh      chan kvstore.WatchEvent
	cancel       chan struct{}
	closed       atomic.Bool  // duplicateclose
	closeOnce    sync.Once    // close first time

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}
```

With:

```go
type watchSubscription struct {
	watchID      int64
	key          string
	rangeEnd     string
	startRev     int64
	eventCh      chan kvstore.WatchEvent
	cancel       chan struct{}
	closed       atomic.Bool  // duplicateclose
	closeOnce    sync.Once    // close first time
	slowSendSem  chan struct{} // bounds concurrent slowSendEvent goroutines

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}
```

### Step 4.3: Initialize semaphore and gate slowSendEvent

- [ ] **Step 4.3a: Initialize semaphore in `WatchWithOptions` (`internal/memory/watch.go:56-67`)**

Replace the subscription creation block:

```go
	// Create subscription
	sub := &watchSubscription{
		watchID:        watchID,
		key:            key,
		rangeEnd:       rangeEnd,
		startRev:       startRevision,
		eventCh:        eventCh,
		cancel:         make(chan struct{}),
		prevKV:         prevKV,
		progressNotify: progressNotify,
		filters:        filters,
		fragment:       fragment,
	}
```

With:

```go
	// Create subscription
	sub := &watchSubscription{
		watchID:        watchID,
		key:            key,
		rangeEnd:       rangeEnd,
		startRev:       startRevision,
		eventCh:        eventCh,
		cancel:         make(chan struct{}),
		slowSendSem:    make(chan struct{}, 8), // cap concurrent slowSend goroutines per watcher
		prevKV:         prevKV,
		progressNotify: progressNotify,
		filters:        filters,
		fragment:       fragment,
	}
```

- [ ] **Step 4.3b: Gate `slowSendEvent` dispatch in `notifyWatches` (`internal/memory/watch.go:242-244`)**

Replace:

```go
		default:
			// Channel full, send asynchronously (slow client)
			go m.slowSendEvent(sub, eventToSend)
```

With:

```go
		default:
			// Channel full — use semaphore to bound goroutines
			select {
			case sub.slowSendSem <- struct{}{}:
				go func() {
					defer func() { <-sub.slowSendSem }()
					m.slowSendEvent(sub, eventToSend)
				}()
			default:
				// Semaphore full — watcher is severely behind, force cancel
				log.Warn("Watch severely behind, force cancelling",
					zap.Int64("watch_id", sub.watchID),
					zap.String("component", "memory-watch"))
				go m.CancelWatch(sub.watchID)
			}
```

### Step 4.4: Verify

- [ ] **Step 4.4a: Run bounded goroutine test**

Run: `go test ./internal/memory/ -run TestSlowWatcherBoundedGoroutines -count=1 -v`
Expected: PASS — goroutine increase < 20.

- [ ] **Step 4.4b: Run full memory package tests**

Run: `go test ./internal/memory/ -count=1 -race -v`
Expected: All PASS.

- [ ] **Step 4.4c: Commit**

```bash
git add internal/memory/store.go internal/memory/watch.go internal/memory/watch_test.go
git commit -m "fix: bound slowSendEvent goroutines with per-watcher semaphore (memory)

Add slowSendSem channel (capacity 8) to watchSubscription. notifyWatches
now uses semaphore-gated dispatch. When semaphore is full, watcher is
force-cancelled as severely behind.

Issue #3 (High): unbounded goroutine explosion from slow watchers."
```

---

## Task 5: PebbleDB Backend — Same Bounded slowSendEvent Fix

**Issue:** #3 High — Same unbounded goroutine pattern in pebbledb.

**Files:**
- Modify: `internal/pebbledb/kvstore.go:83-98` (watchSubscription struct), `internal/pebbledb/kvstore.go:1562-1573` (WatchWithOptions), `internal/pebbledb/kvstore.go:2138-2141` (notifyWatches)
- Create: `internal/pebbledb/watch_test.go`

### Step 5.1: Write test (same pattern as memory)

- [ ] **Step 5.1a: Create test file**

```go
// internal/pebbledb/watch_test.go
package pebbledb

import (
	"runtime"
	"testing"
	"time"

	"metaStore/internal/kvstore"
)

func TestSlowWatcherBoundedGoroutines(t *testing.T) {
	// Create a temporary PebbleDB for testing
	dir := t.TempDir()
	db, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := newPebbleDBForTest(db)
	if r == nil {
		t.Skip("newPebbleDBForTest not available")
	}

	// Create a watch
	eventCh, err := r.WatchWithOptions("/test", "\x00", 0, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Record baseline
	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Generate many events to overflow the channel buffer
	for i := 0; i < 500; i++ {
		r.notifyWatches(kvstore.WatchEvent{
			Type: kvstore.EventTypePut,
			Kv: &kvstore.KeyValue{
				Key:         []byte("/test/key"),
				Value:       []byte("value"),
				ModRevision: int64(i + 1),
			},
			Revision: int64(i + 1),
		})
	}

	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	goroutineIncrease := current - baseline

	if goroutineIncrease > 20 {
		t.Errorf("goroutine explosion: baseline=%d, current=%d, increase=%d (expected < 20)",
			baseline, current, goroutineIncrease)
	}

	// Cleanup
	go func() {
		for range eventCh {
		}
	}()
	r.CancelWatch(1)
}
```

Note: This test may need a helper `newPebbleDBForTest` that creates a minimal PebbleDB with initialized watches map. If PebbleDB's `WatchWithOptions` and `notifyWatches` are available on the PebbleDB struct directly, this works. If the test helper doesn't exist, adapt the test to use the public API. The implementer should check the existing test patterns in `internal/pebbledb/` and adjust accordingly.

- [ ] **Step 5.1b: Run test to verify failure**

Run: `go test ./internal/pebbledb/ -run TestSlowWatcherBoundedGoroutines -count=1 -v`
Expected: FAIL or goroutine explosion detected.

### Step 5.2: Apply identical changes to pebbledb

- [ ] **Step 5.2a: Add `slowSendSem` to `watchSubscription` in `internal/pebbledb/kvstore.go:83-98`**

Add `slowSendSem chan struct{}` field after `closeOnce sync.Once`:

```go
type watchSubscription struct {
	watchID   int64
	key       string
	rangeEnd  string
	startRev  int64
	eventCh   chan kvstore.WatchEvent
	cancel    chan struct{}
	closed    atomic.Bool // duplicateclose
	closeOnce sync.Once   // close first time
	slowSendSem chan struct{} // bounds concurrent slowSendEvent goroutines

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}
```

- [ ] **Step 5.2b: Initialize semaphore in `WatchWithOptions` (`internal/pebbledb/kvstore.go:1562-1573`)**

Add `slowSendSem: make(chan struct{}, 8),` to the subscription creation.

- [ ] **Step 5.2c: Gate dispatch in `notifyWatches` (`internal/pebbledb/kvstore.go:2138-2141`)**

Replace:
```go
		default:
			// Channelfull，asynchronoussend(slowclient)
			go r.slowSendEvent(sub, eventToSend)
```
With:
```go
		default:
			// Channel full — use semaphore to bound goroutines
			select {
			case sub.slowSendSem <- struct{}{}:
				go func() {
					defer func() { <-sub.slowSendSem }()
					r.slowSendEvent(sub, eventToSend)
				}()
			default:
				// Semaphore full — watcher is severely behind, force cancel
				log.Warn("Watch severely behind, force cancelling",
					zap.Int64("watch_id", sub.watchID),
					zap.String("component", "pebble-watch"))
				go r.CancelWatch(sub.watchID)
			}
```

### Step 5.3: Verify

- [ ] **Step 5.3a: Run test**

Run: `go test ./internal/pebbledb/ -run TestSlowWatcherBoundedGoroutines -count=1 -v`
Expected: PASS.

- [ ] **Step 5.3b: Run full pebbledb tests**

Run: `go test ./internal/pebbledb/ -count=1 -race -v`
Expected: All PASS.

- [ ] **Step 5.3c: Commit**

```bash
git add internal/pebbledb/kvstore.go internal/pebbledb/watch_test.go
git commit -m "fix: bound slowSendEvent goroutines with per-watcher semaphore (pebbledb)

Same semaphore pattern as memory backend. Adds slowSendSem (capacity 8)
to pebbledb watchSubscription.

Issue #3 (High): unbounded goroutine explosion from slow watchers."
```

---

## Task 6: Raft WAL/Snapshot Error Handling

**Issue:** #9 High — `saveSnap` and `wal.Save` return values silently discarded in `node_memory.go`.

**Files:**
- Modify: `internal/raft/node_memory.go:805-808`
- Create: `internal/raft/node_memory_test.go` (if testable — see note below)

**Reference:** `internal/raft/node_pebble.go:820-834` already handles these correctly.

### Step 6.1: Implement the fix directly (TDD not practical for log.Fatal)

Testing `log.Fatal` requires mocking the logger or using `exec.Command` to spawn a subprocess, which is complex and fragile for this simple fix. The pattern is already proven in `node_pebble.go`. We'll apply the same fix and verify compilation + existing tests.

- [ ] **Step 6.1a: Fix error handling in `internal/raft/node_memory.go:805-808`**

Replace:
```go
			if !raft.IsEmptySnap(rd.Snapshot) {
				rc.saveSnap(rd.Snapshot)
			}
			rc.wal.Save(rd.HardState, rd.Entries)
```

With:
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

This aligns exactly with `node_pebble.go:820-834`.

- [ ] **Step 6.1b: Verify compilation**

Run: `go build ./internal/raft/`
Expected: Compiles successfully.

- [ ] **Step 6.1c: Run raft package tests**

Run: `go test ./internal/raft/ -count=1 -v`
Expected: All existing tests PASS.

- [ ] **Step 6.1d: Commit**

```bash
git add internal/raft/node_memory.go
git commit -m "fix: check WAL save and snapshot errors in raft memory node

saveSnap and wal.Save return values were silently discarded. Now log.Fatal
on failure, matching the established pattern in node_pebble.go. WAL write
failure means durability is compromised; fast-fail is the correct behavior.

Issue #9 (High): silent data loss on WAL/snapshot write failure."
```

---

## Task 7: GracefulShutdown Wiring in main.go

**Issue:** #8 High — `pkg/reliability/shutdown.go` GracefulShutdown manager exists but is not wired into `cmd/metastore/main.go`.

**Files:**
- Modify: `cmd/metastore/main.go:17-48` (imports), `cmd/metastore/main.go:335-367` (main function body after store creation)

**Key insight:** The etcd server already has its own `GracefulShutdown` internally (registered hooks for gRPC GracefulStop, lease manager stop, etc.). The main.go process-level shutdown should call `etcdServer.Stop()` (which triggers etcd's internal shutdown), `mysqlServer.Stop()`, `closeStore()`, and `closeListener()`.

### Step 7.1: Implement the wiring

- [ ] **Step 7.1a: Add `time` import to `cmd/metastore/main.go`**

Add `"time"` and `"metaStore/pkg/reliability"` to the import block. The `reliability` import may already be transitively available but needs to be explicit. Check existing imports first.

- [ ] **Step 7.1b: Modify the main function body**

Replace the section from line 343 (`defer closeStore()`) through line 367 (end of `m.Serve()`):

```go
	defer closeStore()

	if _, err := starter.startMySQL(store); err != nil {
		log.Fatal("Failed to create MySQL server", zap.Error(err), zap.String("component", "main"))
	}

	_, m, mux, closeListener, err := starter.startMux()
	if err != nil {
		log.Fatal("Failed to listen",
			zap.String("address", cfg.Server.Etcd.Address),
			zap.Error(err),
			zap.String("component", "main"))
	}
	defer closeListener()

	_ = starter.startGateway(m, mux)
	if _, err := starter.startEtcd(store, raftNode, m); err != nil {
		log.Fatal("Failed to create etcd server", zap.Error(err), zap.String("component", "main"))
	}

	log.Info("Starting cmux multiplexing", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
	if err := m.Serve(); err != nil {
		log.Fatal("cmux failed", zap.Error(err), zap.String("component", "main"))
	}
```

With:

```go
	// Create process-level graceful shutdown manager
	gs := reliability.NewGracefulShutdown(30 * time.Second)

	mysqlServer, err := starter.startMySQL(store)
	if err != nil {
		log.Fatal("Failed to create MySQL server", zap.Error(err), zap.String("component", "main"))
	}

	_, m, mux, closeListener, err := starter.startMux()
	if err != nil {
		log.Fatal("Failed to listen",
			zap.String("address", cfg.Server.Etcd.Address),
			zap.Error(err),
			zap.String("component", "main"))
	}

	_ = starter.startGateway(m, mux)
	etcdServer, err := starter.startEtcd(store, raftNode, m)
	if err != nil {
		log.Fatal("Failed to create etcd server", zap.Error(err), zap.String("component", "main"))
	}

	// Phase 1: Stop accepting new requests
	gs.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
		etcdServer.Stop() // triggers etcd's internal graceful shutdown
		return nil
	})
	gs.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
		return mysqlServer.Stop()
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
		log.Info("Starting cmux multiplexing", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
		if err := m.Serve(); err != nil && !gs.IsShuttingDown() {
			log.Fatal("cmux failed", zap.Error(err), zap.String("component", "main"))
		}
	}()

	// Block until shutdown signal (SIGTERM/SIGINT)
	gs.Wait()
```

Also remove the two `defer` statements for `closeStore()` and `closeListener()` since they are now managed by shutdown hooks.

- [ ] **Step 7.1c: Verify compilation**

Run: `go build ./cmd/metastore/`
Expected: Compiles successfully.

- [ ] **Step 7.1d: Commit**

```bash
git add cmd/metastore/main.go
git commit -m "fix: wire GracefulShutdown manager into main.go lifecycle

Create process-level GracefulShutdown that calls etcdServer.Stop() and
mysqlServer.Stop() on SIGTERM/SIGINT, then closes store and listener.
cmux.Serve() runs in a goroutine; main blocks on gs.Wait().

Issue #8 (High): no signal handling, process termination skips cleanup."
```

---

## Task 8: Final Verification

- [ ] **Step 8.1: Run full test suite with race detector**

Run: `go test ./... -count=1 -race -timeout 5m`
Expected: All tests PASS, no data races.

- [ ] **Step 8.2: Run build**

Run: `go build ./...`
Expected: All packages compile.

- [ ] **Step 8.3: Run vet**

Run: `go vet ./...`
Expected: No issues.
