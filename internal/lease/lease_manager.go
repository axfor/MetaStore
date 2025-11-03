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

package lease

import (
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// LeaseManager manages the Leader lease lifecycle.
// A Leader can serve reads directly without Raft consensus while the lease is valid.
type LeaseManager struct {
	// Configuration
	electionTimeout time.Duration // Election timeout from Raft
	heartbeatTick   time.Duration // Heartbeat interval
	clockDrift      time.Duration // Clock drift tolerance (default 500ms)

	// Lease state
	leaseExpireTime atomic.Int64 // Lease expiration time (Unix nano)
	isLeader        atomic.Bool  // Whether this node is Leader

	// Statistics
	leaseRenewCount  atomic.Int64 // Lease renewal count
	leaseExpireCount atomic.Int64 // Lease expiration count

	// Smart configuration (支持动态扩缩容)
	smartConfig *SmartLeaseConfig // nil 表示总是启用

	logger *zap.Logger
}

// LeaseConfig contains configuration for lease management
type LeaseConfig struct {
	ElectionTimeout time.Duration // Election timeout from Raft
	HeartbeatTick   time.Duration // Heartbeat interval
	ClockDrift      time.Duration // Clock drift tolerance (default 500ms)
}

// NewLeaseManager creates a new lease manager
// smartConfig: 传入 nil 表示总是启用，传入非 nil 则根据智能配置决定
func NewLeaseManager(config LeaseConfig, smartConfig *SmartLeaseConfig, logger *zap.Logger) *LeaseManager {
	// Default clock drift: 500ms
	clockDrift := config.ClockDrift
	if clockDrift == 0 {
		clockDrift = 500 * time.Millisecond
	}

	lm := &LeaseManager{
		electionTimeout: config.ElectionTimeout,
		heartbeatTick:   config.HeartbeatTick,
		clockDrift:      clockDrift,
		smartConfig:     smartConfig,
		logger:          logger,
	}

	lm.isLeader.Store(false)
	lm.leaseExpireTime.Store(0)

	return lm
}

// RenewLease attempts to renew the lease after receiving heartbeat acknowledgments
// Returns true if the lease was successfully renewed
func (lm *LeaseManager) RenewLease(receivedAcks int, totalNodes int) bool {
	// 0. 运行时检查：智能配置是否允许启用
	if lm.smartConfig != nil && !lm.smartConfig.IsEnabled() {
		// 单节点或用户禁用，跳过续期
		return false
	}

	// 1. Check if this node is Leader
	if !lm.isLeader.Load() {
		return false
	}

	// 2. 单节点特殊处理（参考 etcd）
	// 防御性处理：Progress 为空或单节点时的边界情况
	if totalNodes <= 1 {
		// 单节点场景：自己就是 quorum
		totalNodes = 1
		receivedAcks = max(receivedAcks, 1) // 确保至少算上自己
	}

	// 3. Check if we received majority acknowledgments
	majority := totalNodes/2 + 1
	if receivedAcks < majority {
		lm.logger.Debug("Insufficient acks for lease renewal",
			zap.Int("received", receivedAcks),
			zap.Int("required", majority),
			zap.Int("total_nodes", totalNodes))
		return false
	}

	// 3. Calculate new lease expiration time
	// Lease duration = min(electionTimeout/2, heartbeatTick*3) - clockDrift
	leaseDuration := minDuration(
		lm.electionTimeout/2,
		lm.heartbeatTick*3,
	) - lm.clockDrift

	// Ensure lease duration is positive with a minimum floor value
	// 最小兜底值：50ms (仅用于配置严重不合理的情况)
	// 注意：
	// - 业界推荐：etcd 500ms+, Consul 500ms, Chubby 5s+
	// - 生产环境建议配置：electionTimeout >= 1000ms, clockDrift <= 200ms
	// - 此最小值仅在配置导致负数或接近0时触发，避免系统完全无法工作
	const minLeaseDuration = 50 * time.Millisecond
	if leaseDuration <= 0 {
		lm.logger.Warn("Invalid lease duration (<=0), using minimum fallback",
			zap.Duration("electionTimeout", lm.electionTimeout),
			zap.Duration("heartbeatTick", lm.heartbeatTick),
			zap.Duration("clockDrift", lm.clockDrift),
			zap.Duration("calculated", leaseDuration),
			zap.Duration("fallback", minLeaseDuration))
		leaseDuration = minLeaseDuration
	} else if leaseDuration < minLeaseDuration {
		lm.logger.Warn("Lease duration too small, using minimum fallback",
			zap.Duration("calculated", leaseDuration),
			zap.Duration("fallback", minLeaseDuration))
		leaseDuration = minLeaseDuration
	}

	newExpireTime := time.Now().Add(leaseDuration)
	lm.leaseExpireTime.Store(newExpireTime.UnixNano())
	lm.leaseRenewCount.Add(1)

	lm.logger.Debug("Lease renewed",
		zap.Int("acks", receivedAcks),
		zap.Duration("duration", leaseDuration),
		zap.Time("expireTime", newExpireTime))

	return true
}

// HasValidLease checks if the current lease is still valid
func (lm *LeaseManager) HasValidLease() bool {
	// Must be Leader
	if !lm.isLeader.Load() {
		return false
	}

	now := time.Now().UnixNano()
	expireTime := lm.leaseExpireTime.Load()

	// Check if lease is still valid
	if now >= expireTime {
		// Lease expired
		lm.leaseExpireCount.Add(1)
		return false
	}

	return true
}

// GetLeaseRemaining returns the remaining time for the current lease
// Returns 0 if no valid lease
func (lm *LeaseManager) GetLeaseRemaining() time.Duration {
	if !lm.isLeader.Load() {
		return 0
	}

	now := time.Now().UnixNano()
	expireTime := lm.leaseExpireTime.Load()

	if now >= expireTime {
		return 0
	}

	return time.Duration(expireTime - now)
}

// OnBecomeLeader should be called when this node becomes Leader
func (lm *LeaseManager) OnBecomeLeader() {
	lm.isLeader.Store(true)
	// Reset lease expiration time
	lm.leaseExpireTime.Store(0)

	lm.logger.Info("Node became Leader, lease initialized")
}

// OnBecomeFollower should be called when this node becomes Follower/Candidate
func (lm *LeaseManager) OnBecomeFollower() {
	wasLeader := lm.isLeader.Swap(false)
	if wasLeader {
		// Invalidate lease immediately
		lm.leaseExpireTime.Store(0)
		lm.logger.Info("Node stepped down from Leader, lease invalidated")
	}
}

// IsLeader returns whether this node is currently Leader
func (lm *LeaseManager) IsLeader() bool {
	return lm.isLeader.Load()
}

// Stats returns lease statistics
func (lm *LeaseManager) Stats() LeaseStats {
	return LeaseStats{
		IsLeader:         lm.isLeader.Load(),
		HasValidLease:    lm.HasValidLease(),
		LeaseRemaining:   lm.GetLeaseRemaining(),
		LeaseRenewCount:  lm.leaseRenewCount.Load(),
		LeaseExpireCount: lm.leaseExpireCount.Load(),
	}
}

// LeaseStats contains lease statistics
type LeaseStats struct {
	IsLeader         bool
	HasValidLease    bool
	LeaseRemaining   time.Duration
	LeaseRenewCount  int64
	LeaseExpireCount int64
}

// minDuration returns the minimum of two durations
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
