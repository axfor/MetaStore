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
	return NewLeaseManagerWithNodeID(store, &cfg.Server.Lease, &cfg.Server.Limits, 1)
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

	// Before LoadLeases, Renew and TimeToLive should fail
	_, err = lm.Renew(100)
	if err == nil {
		t.Fatal("expected Renew to fail before LoadLeases")
	}
	_, err = lm.TimeToLive(100)
	if err == nil {
		t.Fatal("expected TimeToLive to fail before LoadLeases")
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

	// Renew should now succeed
	lease, err := lm.Renew(100)
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

func TestLeaseManager_LoadLeases_CounterInit(t *testing.T) {
	store := memory.NewMemoryEtcd()

	// Create leases with known IDs that have specific counter values
	// Node ID 1 = upper 16 bits, counter in lower 48 bits
	leaseID1 := int64(1<<48) | 50 // node 1, counter 50
	leaseID2 := int64(1<<48) | 42 // node 1, counter 42

	_, err := store.LeaseGrant(context.Background(), leaseID1, 600)
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}
	_, err = store.LeaseGrant(context.Background(), leaseID2, 600)
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}

	// Create new LeaseManager and load leases
	lm := newTestLeaseManager(t, store)
	if err := lm.LoadLeases(); err != nil {
		t.Fatalf("LoadLeases failed: %v", err)
	}

	// Counter should be set to max(50, 42) = 50
	// Next generated ID should have counter > 50
	newID := lm.GenerateLeaseID()
	newCounter := newID & 0x0000FFFFFFFFFFFF
	if newCounter <= 50 {
		t.Fatalf("expected counter > 50, got %d (full ID: %d)", newCounter, newID)
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
