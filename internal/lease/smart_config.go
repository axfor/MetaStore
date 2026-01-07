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

// SmartLeaseConfig 智能 Lease Read configmanager
// 自动感知clusterenvironment并智能enabled/disabled Lease Read
type SmartLeaseConfig struct {
	// userconfig
	userEnabled atomic.Bool // userisnoenabled Lease Read

	// running时status
	actualEnabled   atomic.Bool // 实际isnoenabled（考虑cluster规模）
	clusterSize     atomic.Int32
	lastUpdateTime  atomic.Int64 // Unix nano

	// 依赖
	logger *zap.Logger
}

// NewSmartLeaseConfig create智能configmanager
func NewSmartLeaseConfig(userEnabled bool, logger *zap.Logger) *SmartLeaseConfig {
	slc := &SmartLeaseConfig{
		logger: logger,
	}
	slc.userEnabled.Store(userEnabled)
	slc.actualEnabled.Store(false) // initialdisabled，waitcluster规模检测
	slc.clusterSize.Store(0)
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	return slc
}

// UpdateClusterSize updatecluster规模并重new评估isnoenabled Lease Read
//
// 智能enabledpolicy：
//   - 单node (size=1): disabled Lease Read（knownlimit）
//   - 多node (size>=2): root据userconfig决定
//   - unknown (size=0): disabled（safe起见）
func (slc *SmartLeaseConfig) UpdateClusterSize(size int) {
	oldSize := slc.clusterSize.Swap(int32(size))
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	// 评估isnoshouldenabled
	shouldEnable := slc.shouldEnableLeaseRead(size)
	oldEnabled := slc.actualEnabled.Swap(shouldEnable)

	// ifstatus发生变化，recordlog
	if oldEnabled != shouldEnable || oldSize != int32(size) {
		slc.logger.Info("Lease Read smart config updated",
			zap.Int("old_cluster_size", int(oldSize)),
			zap.Int("new_cluster_size", size),
			zap.Bool("old_enabled", oldEnabled),
			zap.Bool("new_enabled", shouldEnable),
			zap.Bool("user_enabled", slc.userEnabled.Load()),
			zap.String("reason", slc.getEnableReason(size)))
	}
}

// IsEnabled return Lease Read isno实际enabled
func (slc *SmartLeaseConfig) IsEnabled() bool {
	return slc.actualEnabled.Load()
}

// GetClusterSize getcurrentcluster规模
func (slc *SmartLeaseConfig) GetClusterSize() int {
	return int(slc.clusterSize.Load())
}

// SetUserEnabled setuserconfig
func (slc *SmartLeaseConfig) SetUserEnabled(enabled bool) {
	oldEnabled := slc.userEnabled.Swap(enabled)

	if oldEnabled != enabled {
		slc.logger.Info("User changed Lease Read configuration",
			zap.Bool("old_enabled", oldEnabled),
			zap.Bool("new_enabled", enabled))

		// 重new评估isnoshouldenabled
		size := int(slc.clusterSize.Load())
		shouldEnable := slc.shouldEnableLeaseRead(size)
		slc.actualEnabled.Store(shouldEnable)
	}
}

// GetStatus get详finestatusinfo
func (slc *SmartLeaseConfig) GetStatus() SmartConfigStatus {
	lastUpdate := time.Unix(0, slc.lastUpdateTime.Load())

	return SmartConfigStatus{
		UserEnabled:    slc.userEnabled.Load(),
		ActualEnabled:  slc.actualEnabled.Load(),
		ClusterSize:    int(slc.clusterSize.Load()),
		LastUpdateTime: lastUpdate,
		Reason:         slc.getEnableReason(int(slc.clusterSize.Load())),
	}
}

// SmartConfigStatus 智能configstatus
type SmartConfigStatus struct {
	UserEnabled    bool
	ActualEnabled  bool
	ClusterSize    int
	LastUpdateTime time.Time
	Reason         string
}

// shouldEnableLeaseRead 判断isnoshouldenabled Lease Read
func (slc *SmartLeaseConfig) shouldEnableLeaseRead(clusterSize int) bool {
	// ifusernoneenabled，直接return false
	if !slc.userEnabled.Load() {
		return false
	}

	// root据cluster规模判断
	switch {
	case clusterSize == 0:
		// unknowncluster规模，保守disabled
		return false

	case clusterSize >= 1:
		// 单node/多nodecluster，enabled（reference etcd implement）
		// 单node时自己就is quorum，理论上can工作
		return true

	default:
		// exception情况，disabled
		return false
	}
}

// getEnableReason getenabled/disabledreasondescription
func (slc *SmartLeaseConfig) getEnableReason(clusterSize int) string {
	if !slc.userEnabled.Load() {
		return "User disabled Lease Read in configuration"
	}

	switch {
	case clusterSize == 0:
		return "Unknown cluster size, disabled for safety"

	case clusterSize == 1:
		return "Single-node cluster detected, enabled with special handling (following etcd behavior)"

	case clusterSize >= 2:
		return "Multi-node cluster detected, enabled"

	default:
		return "Invalid cluster size"
	}
}

// DetectClusterSizeFromPeers from peer URLs list检测cluster规模
func DetectClusterSizeFromPeers(peers []string) int {
	return len(peers)
}

// StartAutoDetection start自动检测（period性）
//
// argument:
//   - getClusterSize: getcurrentcluster规模function
//   - interval: 检测interval
//   - stopC: stopped信号
func (slc *SmartLeaseConfig) StartAutoDetection(
	getClusterSize func() int,
	interval time.Duration,
	stopC <-chan struct{},
) {
	// 立即execute一次检测
	size := getClusterSize()
	slc.UpdateClusterSize(size)

	slc.logger.Info("Lease Read auto-detection started",
		zap.Int("initial_cluster_size", size),
		zap.Duration("check_interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			size := getClusterSize()
			slc.UpdateClusterSize(size)

		case <-stopC:
			slc.logger.Info("Lease Read auto-detection stopped")
			return
		}
	}
}
