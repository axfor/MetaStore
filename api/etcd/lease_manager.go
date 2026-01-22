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
	stopped atomic.Bool               // whether stopped
	stopCh  chan struct{}             // stop signal

	// Configuration
	checkInterval time.Duration // Lease expiry check interval
	defaultTTL    time.Duration // default TTL
	maxLeaseCount int           // maximum lease count limit (0 means unlimited)

	// Lease ID generator (cluster-safe)
	// ID format: upper 16 bits for node ID, lower 48 bits for counter
	nodeID         uint64
	leaseIDCounter atomic.Int64
}

// NewLeaseManagerWithNodeID creates a new Lease manager (with node ID for cluster)
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
		nodeID:        nodeID,
	}
}

// Start starts the Lease manager (begins expiry checking)
func (lm *LeaseManager) Start() {
	go lm.expiryChecker()
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
