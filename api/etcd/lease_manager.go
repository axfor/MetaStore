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
	"fmt"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	"metaStore/pkg/log"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// LeaseManager manages all leases
type LeaseManager struct {
	mu      sync.RWMutex
	store   kvstore.Store
	leases  map[int64]*kvstore.Lease // leaseID -> Lease
	stopped atomic.Bool              // whether stopped
	stopCh  chan struct{}            // stop signal

	// Configuration
	checkInterval time.Duration // Lease expiry check interval
	defaultTTL    time.Duration // default TTL
	maxLeaseCount int           // maximum lease count limit (0 means unlimited)

	lastLeader atomic.Bool // track leader state for cleanup synchronization
}

// NewLeaseManagerWithMemberID creates a new Lease manager (with member ID for cluster)
func NewLeaseManagerWithMemberID(store kvstore.Store, leaseCfg *config.LeaseConfig, limitsCfg *config.LimitsConfig, raftCfg *config.RaftConfig, nodeID uint64) *LeaseManager {
	// Use configuration or defaults
	if leaseCfg == nil {
		defaultCfg := config.DefaultConfig(1, 1, ":2379")
		leaseCfg = &defaultCfg.Server.Lease
	}

	maxLeases := 0 // default unlimited
	if limitsCfg != nil {
		maxLeases = limitsCfg.MaxLeaseCount
	}

	return &LeaseManager{
		store:         store,
		leases:        make(map[int64]*kvstore.Lease),
		stopCh:        make(chan struct{}),
		checkInterval: leaseCfg.CheckInterval,
		defaultTTL:    leaseCfg.DefaultTTL,
		maxLeaseCount: maxLeases,
	}
}

// Start starts the Lease manager (begins expiry checking)
func (lm *LeaseManager) Start() {
	// Load persisted leases from store before starting expiry checker
	if err := lm.LoadLeases(); err != nil {
		log.Error("Failed to load persisted leases, continuing with empty state",
			zap.Error(err),
			zap.String("component", "lease-manager"))
	}
	// Immediate cleanup on startup (leader-only)
	lm.checkExpiredLeases()
	lm.lastLeader.Store(lm.isLeaderForCleanup())
	go lm.expiryChecker()
}

// LoadLeases loads all persisted leases from the store into the in-memory map.
// This must be called during startup to recover leases that existed before a server restart.
func (lm *LeaseManager) LoadLeases() error {
	leases, err := lm.store.Leases(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load leases from store: %w", err)
	}

	if len(leases) == 0 {
		return nil
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()

	for _, lease := range leases {
		lm.leases[lease.ID] = lease
	}

	log.Info("Loaded persisted leases",
		zap.Int("count", len(leases)),
		zap.String("component", "lease-manager"))

	return nil
}

// Stop stops the Lease manager
func (lm *LeaseManager) Stop() {
	if !lm.stopped.CompareAndSwap(false, true) {
		return
	}
	close(lm.stopCh)
}

// Grant creates a new lease
func (lm *LeaseManager) Grant(id int64, ttl int64) (*kvstore.Lease, error) {
	if lm.stopped.Load() {
		return nil, ErrLeaseNotFound
	}

	// Check lease count limit
	lm.mu.RLock()
	currentCount := len(lm.leases)
	lm.mu.RUnlock()

	if lm.maxLeaseCount > 0 && currentCount >= lm.maxLeaseCount {
		return nil, ErrTooManyLeases
	}

	// Delegate to store
	lease, err := lm.store.LeaseGrant(context.Background(), id, ttl)
	if err != nil {
		return nil, err
	}

	lm.mu.Lock()
	lm.leases[id] = lease
	lm.mu.Unlock()

	return lease, nil
}

// Revoke revokes a lease (deletes all associated keys)
func (lm *LeaseManager) Revoke(id int64) error {
	lm.mu.Lock()
	_, ok := lm.leases[id]
	if ok {
		delete(lm.leases, id)
	}
	lm.mu.Unlock()

	if !ok {
		return ErrLeaseNotFound
	}

	// Delegate to store (will delete all associated keys)
	return lm.store.LeaseRevoke(context.Background(), id)
}

// Renew renews a lease
func (lm *LeaseManager) Renew(id int64) (*kvstore.Lease, error) {
	lm.mu.RLock()
	_, ok := lm.leases[id]
	lm.mu.RUnlock()

	if !ok {
		return nil, ErrLeaseNotFound
	}

	// Delegate to store
	lease, err := lm.store.LeaseRenew(context.Background(), id)
	if err != nil {
		return nil, err
	}

	lm.mu.Lock()
	lm.leases[id] = lease
	lm.mu.Unlock()

	return lease, nil
}

// TimeToLive gets the remaining time for a lease
func (lm *LeaseManager) TimeToLive(id int64) (*kvstore.Lease, error) {
	lm.mu.RLock()
	_, ok := lm.leases[id]
	lm.mu.RUnlock()

	if !ok {
		return nil, ErrLeaseNotFound
	}

	// Delegate to store
	return lm.store.LeaseTimeToLive(context.Background(), id)
}

// Leases returns all leases
func (lm *LeaseManager) Leases() ([]*kvstore.Lease, error) {
	return lm.store.Leases(context.Background())
}

// SyncFromStore rebuilds lease cache from storage
func (lm *LeaseManager) SyncFromStore(ctx context.Context) error {
	leases, err := lm.store.Leases(ctx)
	if err != nil {
		return err
	}

	leaseMap := make(map[int64]*kvstore.Lease, len(leases))
	for _, lease := range leases {
		if lease == nil {
			continue
		}
		leaseMap[lease.ID] = lease
	}

	lm.mu.Lock()
	lm.leases = leaseMap
	lm.mu.Unlock()

	if len(leaseMap) > 0 {
		log.Debug("Lease cache synced",
			zap.Int("lease_count", len(leaseMap)),
			zap.String("component", "lease-manager"))
	}

	return nil
}

// expiryChecker periodically checks and cleans up expired leases
func (lm *LeaseManager) expiryChecker() {
	ticker := time.NewTicker(lm.checkInterval) // Use configured check interval
	defer ticker.Stop()

	log.Info("Lease expiry checker started",
		zap.Duration("check_interval", lm.checkInterval),
		zap.String("component", "lease-manager"))

	for {
		select {
		case <-ticker.C:
			lm.checkExpiredLeases()
		case <-lm.stopCh:
			log.Info("Lease expiry checker stopped", zap.String("component", "lease-manager"))
			return
		}
	}
}

// checkExpiredLeases checks and cleans up expired leases
func (lm *LeaseManager) checkExpiredLeases() {
	if !lm.isLeaderForCleanup() {
		return
	}

	// Sync from store to pick up leases created on other nodes in the cluster.
	// Without this, the leader's in-memory cache would miss leases granted
	// through follower nodes, preventing them from being expired.
	if err := lm.SyncFromStore(context.Background()); err != nil {
		log.Error("Failed to sync lease cache before expiry check",
			zap.Error(err),
			zap.String("component", "lease-manager"))
	}

	lm.mu.RLock()
	expiredIDs := make([]int64, 0)
	for id, lease := range lm.leases {
		if lease.IsExpired() {
			expiredIDs = append(expiredIDs, id)
		}
	}
	lm.mu.RUnlock()

	// Revoke expired leases
	for _, id := range expiredIDs {
		if err := lm.Revoke(id); err != nil {
			log.Error("Failed to revoke expired lease", zap.Int64("lease_id", id), zap.Error(err), zap.String("component", "lease-manager"))
		} else {
			log.Info("Revoked expired lease", zap.Int64("lease_id", id), zap.String("component", "lease-manager"))
		}
	}
}

func (lm *LeaseManager) isLeaderForCleanup() bool {
	status := lm.store.GetRaftStatus()
	if status.State == "standalone" {
		return true
	}
	if status.LeaderID == 0 {
		return false
	}
	return status.NodeID == status.LeaderID
}

// OnLeaderChange handles leader transitions (event-driven cleanup)
func (lm *LeaseManager) OnLeaderChange(status kvstore.RaftStatus) {
	if status.State == "standalone" {
		if err := lm.SyncFromStore(context.Background()); err != nil {
			log.Error("Failed to sync lease cache on leader change",
				zap.Error(err),
				zap.String("component", "lease-manager"))
			return
		}
		lm.checkExpiredLeases()
		lm.lastLeader.Store(true)
		return
	}
	if status.LeaderID == 0 || status.NodeID != status.LeaderID {
		lm.lastLeader.Store(false)
		return
	}
	if !lm.lastLeader.Swap(true) {
		if err := lm.SyncFromStore(context.Background()); err != nil {
			log.Error("Failed to sync lease cache on leader change",
				zap.Error(err),
				zap.String("component", "lease-manager"))
			return
		}
		lm.checkExpiredLeases()
	}
}
