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

// SmartLeaseConfig 智能 Lease Read 配置管理器
// 自动感知集群环境并智能启用/禁用 Lease Read
type SmartLeaseConfig struct {
	// 用户配置
	userEnabled atomic.Bool // 用户是否启用了 Lease Read

	// 运行时状态
	actualEnabled   atomic.Bool // 实际是否启用（考虑集群规模）
	clusterSize     atomic.Int32
	lastUpdateTime  atomic.Int64 // Unix nano

	// 依赖
	logger *zap.Logger
}

// NewSmartLeaseConfig 创建智能配置管理器
func NewSmartLeaseConfig(userEnabled bool, logger *zap.Logger) *SmartLeaseConfig {
	slc := &SmartLeaseConfig{
		logger: logger,
	}
	slc.userEnabled.Store(userEnabled)
	slc.actualEnabled.Store(false) // 初始禁用，等待集群规模检测
	slc.clusterSize.Store(0)
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	return slc
}

// UpdateClusterSize 更新集群规模并重新评估是否启用 Lease Read
//
// 智能启用策略：
//   - 单节点 (size=1): 禁用 Lease Read（已知限制）
//   - 多节点 (size>=2): 根据用户配置决定
//   - 未知 (size=0): 禁用（安全起见）
func (slc *SmartLeaseConfig) UpdateClusterSize(size int) {
	oldSize := slc.clusterSize.Swap(int32(size))
	slc.lastUpdateTime.Store(time.Now().UnixNano())

	// 评估是否应该启用
	shouldEnable := slc.shouldEnableLeaseRead(size)
	oldEnabled := slc.actualEnabled.Swap(shouldEnable)

	// 如果状态发生变化，记录日志
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

// IsEnabled 返回 Lease Read 是否实际启用
func (slc *SmartLeaseConfig) IsEnabled() bool {
	return slc.actualEnabled.Load()
}

// GetClusterSize 获取当前集群规模
func (slc *SmartLeaseConfig) GetClusterSize() int {
	return int(slc.clusterSize.Load())
}

// SetUserEnabled 设置用户配置
func (slc *SmartLeaseConfig) SetUserEnabled(enabled bool) {
	oldEnabled := slc.userEnabled.Swap(enabled)

	if oldEnabled != enabled {
		slc.logger.Info("User changed Lease Read configuration",
			zap.Bool("old_enabled", oldEnabled),
			zap.Bool("new_enabled", enabled))

		// 重新评估是否应该启用
		size := int(slc.clusterSize.Load())
		shouldEnable := slc.shouldEnableLeaseRead(size)
		slc.actualEnabled.Store(shouldEnable)
	}
}

// GetStatus 获取详细状态信息
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

// SmartConfigStatus 智能配置状态
type SmartConfigStatus struct {
	UserEnabled    bool
	ActualEnabled  bool
	ClusterSize    int
	LastUpdateTime time.Time
	Reason         string
}

// shouldEnableLeaseRead 判断是否应该启用 Lease Read
func (slc *SmartLeaseConfig) shouldEnableLeaseRead(clusterSize int) bool {
	// 如果用户没有启用，直接返回 false
	if !slc.userEnabled.Load() {
		return false
	}

	// 根据集群规模判断
	switch {
	case clusterSize == 0:
		// 未知集群规模，保守禁用
		return false

	case clusterSize >= 1:
		// 单节点/多节点集群，启用（参考 etcd 实现）
		// 单节点时自己就是 quorum，理论上可以工作
		return true

	default:
		// 异常情况，禁用
		return false
	}
}

// getEnableReason 获取启用/禁用的原因说明
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

// DetectClusterSizeFromPeers 从 peer URLs 列表检测集群规模
func DetectClusterSizeFromPeers(peers []string) int {
	return len(peers)
}

// StartAutoDetection 启动自动检测（周期性）
//
// 参数:
//   - getClusterSize: 获取当前集群规模的函数
//   - interval: 检测间隔
//   - stopC: 停止信号
func (slc *SmartLeaseConfig) StartAutoDetection(
	getClusterSize func() int,
	interval time.Duration,
	stopC <-chan struct{},
) {
	// 立即执行一次检测
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
