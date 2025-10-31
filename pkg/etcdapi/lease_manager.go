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

package etcdapi

import (
	"context"
	"metaStore/internal/kvstore"
	"metaStore/pkg/log"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// LeaseManager 管理所有的 lease
type LeaseManager struct {
	mu      sync.RWMutex
	store   kvstore.Store
	leases  map[int64]*kvstore.Lease // leaseID -> Lease
	stopped atomic.Bool               // 是否已停止
	stopCh  chan struct{}             // 停止信号
}

// NewLeaseManager 创建新的 Lease 管理器
func NewLeaseManager(store kvstore.Store) *LeaseManager {
	return &LeaseManager{
		store:  store,
		leases: make(map[int64]*kvstore.Lease),
		stopCh: make(chan struct{}),
	}
}

// Start 启动 Lease 管理器（开始过期检查）
func (lm *LeaseManager) Start() {
	go lm.expiryChecker()
}

// Stop 停止 Lease 管理器
func (lm *LeaseManager) Stop() {
	if !lm.stopped.CompareAndSwap(false, true) {
		return
	}
	close(lm.stopCh)
}

// Grant 创建一个新的 lease
func (lm *LeaseManager) Grant(id int64, ttl int64) (*kvstore.Lease, error) {
	if lm.stopped.Load() {
		return nil, ErrLeaseNotFound
	}

	// 委托给 store
	lease, err := lm.store.LeaseGrant(context.Background(), id, ttl)
	if err != nil {
		return nil, err
	}

	lm.mu.Lock()
	lm.leases[id] = lease
	lm.mu.Unlock()

	return lease, nil
}

// Revoke 撤销一个 lease（删除所有关联的键）
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

	// 委托给 store（会删除所有关联的键）
	return lm.store.LeaseRevoke(context.Background(), id)
}

// Renew 续约一个 lease
func (lm *LeaseManager) Renew(id int64) (*kvstore.Lease, error) {
	lm.mu.RLock()
	_, ok := lm.leases[id]
	lm.mu.RUnlock()

	if !ok {
		return nil, ErrLeaseNotFound
	}

	// 委托给 store
	lease, err := lm.store.LeaseRenew(context.Background(), id)
	if err != nil {
		return nil, err
	}

	lm.mu.Lock()
	lm.leases[id] = lease
	lm.mu.Unlock()

	return lease, nil
}

// TimeToLive 获取 lease 的剩余时间
func (lm *LeaseManager) TimeToLive(id int64) (*kvstore.Lease, error) {
	lm.mu.RLock()
	_, ok := lm.leases[id]
	lm.mu.RUnlock()

	if !ok {
		return nil, ErrLeaseNotFound
	}

	// 委托给 store
	return lm.store.LeaseTimeToLive(context.Background(), id)
}

// Leases 返回所有 lease
func (lm *LeaseManager) Leases() ([]*kvstore.Lease, error) {
	return lm.store.Leases(context.Background())
}

// expiryChecker 定期检查并清理过期的 lease
func (lm *LeaseManager) expiryChecker() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lm.checkExpiredLeases()
		case <-lm.stopCh:
			return
		}
	}
}

// checkExpiredLeases 检查并清理过期的 lease
func (lm *LeaseManager) checkExpiredLeases() {
	lm.mu.RLock()
	expiredIDs := make([]int64, 0)
	for id, lease := range lm.leases {
		if lease.IsExpired() {
			expiredIDs = append(expiredIDs, id)
		}
	}
	lm.mu.RUnlock()

	// 撤销过期的 lease
	for _, id := range expiredIDs {
		if err := lm.Revoke(id); err != nil {
			log.Error("Failed to revoke expired lease", zap.Int64("lease_id", id), zap.Error(err), zap.String("component", "lease-manager"))
		} else {
			log.Info("Revoked expired lease", zap.Int64("lease_id", id), zap.String("component", "lease-manager"))
		}
	}
}
