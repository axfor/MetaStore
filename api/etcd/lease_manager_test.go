// Copyright 2025 The axfor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package etcd

import (
	"context"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/pkg/config"
)

// newTestLeaseManager creates a LeaseManager with a memory store for testing.
func newTestLeaseManager(t *testing.T, store *memory.MemoryEtcd) *LeaseManager {
	t.Helper()
	cfg := config.DefaultConfig(1, 1, ":2379")
	return NewLeaseManagerWithMemberID(store, &cfg.Server.Lease, &cfg.Server.Limits, &cfg.Server.Raft, 1)
}

func TestLeaseManager_LoadLeases_Empty(t *testing.T) {
	store := memory.NewMemoryEtcd()
	lm := newTestLeaseManager(t, store)

	// LoadLeases on empty store should succeed
	if err := lm.LoadLeases(); err != nil {
		t.Fatalf("LoadLeases failed on empty store: %v", err)
	}

	lm.mu.RLock()
	count := len(lm.leases)
	lm.mu.RUnlock()

	if count != 0 {
		t.Fatalf("expected 0 leases, got %d", count)
	}
}

func TestLeaseManager_LoadLeases_Recovery(t *testing.T) {
	store := memory.NewMemoryEtcd()

	// Simulate pre-restart state: create leases directly in the store
	_, err := store.LeaseGrant(context.Background(), 100, 600)
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}
	_, err = store.LeaseGrant(context.Background(), 200, 300)
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}

	// Create a NEW LeaseManager (simulates server restart)
	lm := newTestLeaseManager(t, store)

	// Before LoadLeases, cross-node/cache-miss operations should still work by
	// consulting the underlying store and repairing the cache.
	lease, err := lm.Renew(100)
	if err != nil {
		t.Fatalf("Renew failed on cache miss: %v", err)
	}
	if lease.ID != 100 {
		t.Fatalf("expected lease ID 100, got %d", lease.ID)
	}

	lease, err = lm.TimeToLive(200)
	if err != nil {
		t.Fatalf("TimeToLive failed on cache miss: %v", err)
	}
	if lease.ID != 200 {
		t.Fatalf("expected lease ID 200, got %d", lease.ID)
	}

	// Load persisted leases
	if err := lm.LoadLeases(); err != nil {
		t.Fatalf("LoadLeases failed: %v", err)
	}

	// Verify leases are recovered
	lm.mu.RLock()
	count := len(lm.leases)
	lm.mu.RUnlock()
	if count != 2 {
		t.Fatalf("expected 2 leases, got %d", count)
	}

	// Renew should continue to succeed after an explicit cache reload
	lease, err = lm.Renew(100)
	if err != nil {
		t.Fatalf("Renew failed after LoadLeases: %v", err)
	}
	if lease.ID != 100 {
		t.Fatalf("expected lease ID 100, got %d", lease.ID)
	}

	// TimeToLive should now succeed
	lease, err = lm.TimeToLive(200)
	if err != nil {
		t.Fatalf("TimeToLive failed after LoadLeases: %v", err)
	}
	if lease.ID != 200 {
		t.Fatalf("expected lease ID 200, got %d", lease.ID)
	}
}

func TestLeaseManager_LoadLeases_ExpiredHandling(t *testing.T) {
	store := memory.NewMemoryEtcd()

	// Create a lease with a very short TTL
	_, err := store.LeaseGrant(context.Background(), 100, 1) // 1 second TTL
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}

	// Wait for it to expire
	time.Sleep(1100 * time.Millisecond)

	// Create new LeaseManager and load leases
	lm := newTestLeaseManager(t, store)
	if err := lm.LoadLeases(); err != nil {
		t.Fatalf("LoadLeases failed: %v", err)
	}

	// The expired lease should be loaded into the map
	lm.mu.RLock()
	count := len(lm.leases)
	lm.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected 1 lease loaded (even if expired), got %d", count)
	}

	// checkExpiredLeases should detect and revoke it
	lm.checkExpiredLeases()

	// After expiry check, lease should be removed
	lm.mu.RLock()
	count = len(lm.leases)
	lm.mu.RUnlock()
	if count != 0 {
		t.Fatalf("expected 0 leases after expiry check, got %d", count)
	}
}
