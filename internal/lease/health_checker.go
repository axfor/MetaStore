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
	"fmt"
	"time"

	"go.uber.org/zap"
)

// HealthChecker 自动检测 Lease Read 系统健康状态
type HealthChecker struct {
	leaseManager     *LeaseManager
	readIndexManager *ReadIndexManager
	logger           *zap.Logger

	// 配置
	checkInterval    time.Duration
	alertThreshold   int // 连续失败次数阈值

	// 状态
	consecutiveFailures int
	lastCheckTime       time.Time
	lastAlertTime       time.Time
}

// HealthStatus Lease Read 健康状态
type HealthStatus struct {
	Healthy           bool
	Issues            []string
	LeaseEstablished  bool
	LeaseRenewRate    float64 // 续期成功率
	FastPathRate      float64 // 快速路径使用率
	Timestamp         time.Time
}

// NewHealthChecker 创建健康检查器
func NewHealthChecker(lm *LeaseManager, rim *ReadIndexManager, logger *zap.Logger) *HealthChecker {
	return &HealthChecker{
		leaseManager:     lm,
		readIndexManager: rim,
		logger:           logger,
		checkInterval:    30 * time.Second, // 默认 30 秒检查一次
		alertThreshold:   3,                 // 连续 3 次失败才报警
	}
}

// Check 执行健康检查
func (hc *HealthChecker) Check() HealthStatus {
	hc.lastCheckTime = time.Now()

	status := HealthStatus{
		Healthy:   true,
		Issues:    make([]string, 0),
		Timestamp: hc.lastCheckTime,
	}

	// 检查 1: Lease 是否建立（仅对 Leader）
	if hc.leaseManager != nil && hc.leaseManager.IsLeader() {
		leaseStats := hc.leaseManager.Stats()
		status.LeaseEstablished = leaseStats.HasValidLease

		if !status.LeaseEstablished {
			issue := fmt.Sprintf("Leader lease not established (RenewCount=%d, ExpireCount=%d)",
				leaseStats.LeaseRenewCount, leaseStats.LeaseExpireCount)
			status.Issues = append(status.Issues, issue)
			status.Healthy = false

			// 特殊情况：单节点集群
			// 如果是单节点，这是已知限制，降级为警告
			if hc.isSingleNodeScenario() {
				hc.logger.Warn("Lease not established in single-node scenario (known limitation)",
					zap.Int64("renew_count", leaseStats.LeaseRenewCount))
				status.Healthy = true // 标记为健康，但保留 issue
			}
		}

		// 计算续期成功率
		totalAttempts := leaseStats.LeaseRenewCount + leaseStats.LeaseExpireCount
		if totalAttempts > 0 {
			status.LeaseRenewRate = float64(leaseStats.LeaseRenewCount) / float64(totalAttempts)

			// 检查续期成功率是否过低
			if status.LeaseRenewRate < 0.8 && totalAttempts > 10 {
				issue := fmt.Sprintf("Low lease renew rate: %.2f%% (expected >80%%)",
					status.LeaseRenewRate*100)
				status.Issues = append(status.Issues, issue)
				status.Healthy = false
			}
		}
	}

	// 检查 2: ReadIndex 使用情况
	if hc.readIndexManager != nil {
		readStats := hc.readIndexManager.Stats()
		status.FastPathRate = readStats.FastPathRate

		// 如果有读请求但快速路径使用率为 0，可能有问题
		if readStats.TotalRequests > 100 && readStats.FastPathRate == 0 {
			// 但如果不是 Leader 或租约未建立，这是正常的
			if hc.leaseManager != nil && hc.leaseManager.IsLeader() && hc.leaseManager.HasValidLease() {
				issue := fmt.Sprintf("Fast path not used despite valid lease (Total=%d, FastPath=%d)",
					readStats.TotalRequests, readStats.FastPathReads)
				status.Issues = append(status.Issues, issue)
				status.Healthy = false
			}
		}

		// 检查待处理读请求是否过多
		if readStats.PendingReads > 1000 {
			issue := fmt.Sprintf("Too many pending reads: %d (may indicate performance issue)",
				readStats.PendingReads)
			status.Issues = append(status.Issues, issue)
			status.Healthy = false
		}
	}

	// 更新连续失败计数
	if !status.Healthy {
		hc.consecutiveFailures++

		// 达到阈值时发出警报
		if hc.consecutiveFailures >= hc.alertThreshold {
			hc.alert(status)
			hc.consecutiveFailures = 0 // 重置计数
		}
	} else {
		hc.consecutiveFailures = 0
	}

	return status
}

// isSingleNodeScenario 检测是否是单节点场景
func (hc *HealthChecker) isSingleNodeScenario() bool {
	// TODO: 实现实际的节点数检测
	// 这需要访问 Raft 集群配置信息
	// 当前简化实现：如果续期次数很少但没有过期，可能是单节点
	stats := hc.leaseManager.Stats()
	return stats.LeaseRenewCount > 0 && stats.LeaseExpireCount == 0 && stats.LeaseRenewCount < 10
}

// alert 发出健康警报
func (hc *HealthChecker) alert(status HealthStatus) {
	// 避免频繁报警（至少间隔 5 分钟）
	if time.Since(hc.lastAlertTime) < 5*time.Minute {
		return
	}

	hc.lastAlertTime = time.Now()

	hc.logger.Error("Lease Read health check failed",
		zap.Bool("healthy", status.Healthy),
		zap.Strings("issues", status.Issues),
		zap.Bool("lease_established", status.LeaseEstablished),
		zap.Float64("lease_renew_rate", status.LeaseRenewRate),
		zap.Float64("fast_path_rate", status.FastPathRate),
		zap.Int("consecutive_failures", hc.consecutiveFailures))

	// TODO: 集成告警系统
	// - 发送 Prometheus Alert
	// - 发送邮件/短信
	// - 记录到监控系统
}

// StartMonitoring 启动后台健康监控
func (hc *HealthChecker) StartMonitoring(stopC <-chan struct{}) {
	ticker := time.NewTicker(hc.checkInterval)
	defer ticker.Stop()

	hc.logger.Info("Lease Read health monitoring started",
		zap.Duration("check_interval", hc.checkInterval),
		zap.Int("alert_threshold", hc.alertThreshold))

	for {
		select {
		case <-ticker.C:
			status := hc.Check()

			// 定期记录健康状态
			if status.Healthy {
				hc.logger.Debug("Lease Read health check passed",
					zap.Bool("lease_established", status.LeaseEstablished),
					zap.Float64("fast_path_rate", status.FastPathRate))
			} else {
				hc.logger.Warn("Lease Read health check issues detected",
					zap.Strings("issues", status.Issues))
			}

		case <-stopC:
			hc.logger.Info("Lease Read health monitoring stopped")
			return
		}
	}
}

// GetMetrics 获取 Prometheus 格式的指标
func (hc *HealthChecker) GetMetrics() map[string]float64 {
	status := hc.Check()

	metrics := make(map[string]float64)

	// 健康状态（0 = 不健康，1 = 健康）
	if status.Healthy {
		metrics["lease_read_healthy"] = 1.0
	} else {
		metrics["lease_read_healthy"] = 0.0
	}

	// 租约建立状态
	if status.LeaseEstablished {
		metrics["lease_established"] = 1.0
	} else {
		metrics["lease_established"] = 0.0
	}

	// 续期成功率
	metrics["lease_renew_rate"] = status.LeaseRenewRate

	// 快速路径使用率
	metrics["lease_fast_path_rate"] = status.FastPathRate

	// 问题数量
	metrics["lease_issues_count"] = float64(len(status.Issues))

	// 连续失败次数
	metrics["lease_consecutive_failures"] = float64(hc.consecutiveFailures)

	return metrics
}
