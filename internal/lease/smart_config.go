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

// SmartLeaseConfig can Lease Read configmanager
// clusterenvironmentandcanenabled/disabled Lease Read
type SmartLeaseConfig struct {
	// userconfig
	userEnabled atomic.Bool // userisnoenabled Lease Read

	// runningwhenstatus
	actualEnabled   atomic.Bool // isnoenabled(cluster)
	clusterSize     atomic.Int32
	lastUpdateTime  atomic.Int64 // Unix nano

	// 
	logger *zap.Logger
}

// NewSmartLeaseConfig createcanconfigmanager
func NewSmartLeaseConfig(userEnabled bool, logger *zap.Logger) *SmartLeaseConfig {
	slc := &SmartLeaseConfig{
		logger: logger,
	}
	slc.userEnabled.Store(userEnabled)
	slc.actualEnabled.Store(false) // initialdisabled，waitclustertest
	slc.clusterSize.Store(0)
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	return slc
}

// UpdateClusterSize updateclusterandnewisnoenabled Lease Read
//
// canenabledpolicy：
//   - singlenode (size=1): disabled Lease Read(knownlimit)
//   - manynode (size>=2): rootuserconfig
//   - unknown (size=0): disabled(safe)
func (slc *SmartLeaseConfig) UpdateClusterSize(size int) {
	oldSize := slc.clusterSize.Swap(int32(size))
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	// isnoshouldenabled
	shouldEnable := slc.shouldEnableLeaseRead(size)
	oldEnabled := slc.actualEnabled.Swap(shouldEnable)

	// ifstatusoccurchangetransform，recordlog
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

// IsEnabled return Lease Read isnoenabled
func (slc *SmartLeaseConfig) IsEnabled() bool {
	return slc.actualEnabled.Load()
}

// GetClusterSize getcurrentcluster
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

		// newisnoshouldenabled
		size := int(slc.clusterSize.Load())
		shouldEnable := slc.shouldEnableLeaseRead(size)
		slc.actualEnabled.Store(shouldEnable)
	}
}

// GetStatus getfinestatus info
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

// SmartConfigStatus canconfigstatus
type SmartConfigStatus struct {
	UserEnabled    bool
	ActualEnabled  bool
	ClusterSize    int
	LastUpdateTime time.Time
	Reason         string
}

// shouldEnableLeaseRead isnoshouldenabled Lease Read
func (slc *SmartLeaseConfig) shouldEnableLeaseRead(clusterSize int) bool {
	// ifusernoneenabled，return false
	if !slc.userEnabled.Load() {
		return false
	}

	// rootcluster
	switch {
	case clusterSize == 0:
		// unknowncluster，disabled
		return false

	case clusterSize >= 1:
		// singlenode/manynodecluster，enabled(reference etcd implement)
		// singlenodewhenis quorum，previouscan
		return true

	default:
		// exception，disabled
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

// DetectClusterSizeFromPeers from peer URLs listtestcluster
func DetectClusterSizeFromPeers(peers []string) int {
	return len(peers)
}

// StartAutoDetection starttest(period)
//
// argument:
//   - getClusterSize: getcurrentclusterfunction
//   - interval: testinterval
//   - stopC: stopped
func (slc *SmartLeaseConfig) StartAutoDetection(
	getClusterSize func() int,
	interval time.Duration,
	stopC <-chan struct{},
) {
	// executefirst timetest
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
