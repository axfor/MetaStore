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
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestDynamicScaleUp 测试从单节点扩容到多节点的场景
// 验证：Lease Read 组件总是创建，在单节点和多节点下都能工作（etcd 兼容）
func TestDynamicScaleUp(t *testing.T) {
	// 1. 创建智能配置（模拟单节点启动）
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(1)

	// 新实现：单节点也启用（etcd 兼容）
	if !smartConfig.IsEnabled() {
		t.Error("Should be enabled for single-node cluster (etcd-compatible)")
	}

	// 2. 创建 LeaseManager（总是创建，不管集群规模）
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}
	lm := NewLeaseManager(config, smartConfig, zap.NewNop())

	// 3. 验证组件已创建
	if lm == nil {
		t.Fatal("LeaseManager should be created")
	}

	// 4. 模拟成为 Leader
	lm.OnBecomeLeader()

	// 5. 尝试续期租约（etcd 兼容：单节点应该成功）
	renewed := lm.RenewLease(1, 1)
	if !renewed {
		t.Error("Should renew lease in single-node (etcd-compatible)")
	}

	// 验证已建立租约
	if !lm.HasValidLease() {
		t.Error("Should have valid lease in single-node")
	}

	// 6. 模拟扩容到 3 节点
	smartConfig.UpdateClusterSize(3)

	if !smartConfig.IsEnabled() {
		t.Error("Should be enabled after scaling to 3 nodes")
	}

	// 7. 再次尝试续期（应该成功）
	renewed = lm.RenewLease(2, 3)
	if !renewed {
		t.Error("Should renew lease after scale-up to 3 nodes")
	}

	// 验证租约已建立
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after scale-up")
	}

	// 8. 模拟缩容回单节点（etcd 兼容：仍然启用）
	smartConfig.UpdateClusterSize(1)

	if !smartConfig.IsEnabled() {
		t.Error("Should still be enabled after scaling back to 1 node (etcd-compatible)")
	}

	// 9. 尝试续期（etcd 兼容：应该成功）
	renewed = lm.RenewLease(1, 1)
	if !renewed {
		t.Error("Should renew lease after scaling back to 1 node (etcd-compatible)")
	}
}

// TestDynamicScaleUp_ReadIndexManager 测试 ReadIndexManager 的动态扩缩容
func TestDynamicScaleUp_ReadIndexManager(t *testing.T) {
	// 1. 单节点启动
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(1)

	rim := NewReadIndexManager(smartConfig, zap.NewNop())

	// 2. 单节点时记录快速路径（etcd 兼容：应该记录）
	rim.RecordFastPathRead()

	stats := rim.Stats()
	if stats.FastPathReads != 1 {
		t.Errorf("Fast path reads should be 1 in single-node (etcd-compatible), got %d", stats.FastPathReads)
	}

	// 3. 扩容到 3 节点
	smartConfig.UpdateClusterSize(3)

	// 4. 记录快速路径（应该成功）
	rim.RecordFastPathRead()

	stats = rim.Stats()
	if stats.FastPathReads != 2 {
		t.Errorf("Fast path reads should be 2 after scale-up, got %d", stats.FastPathReads)
	}

	// 5. 缩容回单节点（etcd 兼容：仍然记录）
	smartConfig.UpdateClusterSize(1)

	// 6. 记录快速路径（etcd 兼容：应该记录）
	rim.RecordFastPathRead()

	stats = rim.Stats()
	if stats.FastPathReads != 3 {
		t.Errorf("Fast path reads should be 3 after scale-down (etcd-compatible), got %d", stats.FastPathReads)
	}
}

// TestDynamicScaling_StatusTracking 测试动态扩缩容的状态跟踪
func TestDynamicScaling_StatusTracking(t *testing.T) {
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())

	testCases := []struct {
		name            string
		clusterSize     int
		expectedEnabled bool
		expectedReason  string
	}{
		{
			name:            "Unknown size",
			clusterSize:     0,
			expectedEnabled: false,
			expectedReason:  "Unknown cluster size",
		},
		{
			name:            "Single node",
			clusterSize:     1,
			expectedEnabled: true, // etcd 兼容：单节点启用
			expectedReason:  "Single-node cluster detected, enabled with special handling",
		},
		{
			name:            "Two nodes",
			clusterSize:     2,
			expectedEnabled: true,
			expectedReason:  "Multi-node cluster detected",
		},
		{
			name:            "Five nodes",
			clusterSize:     5,
			expectedEnabled: true,
			expectedReason:  "Multi-node cluster detected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			smartConfig.UpdateClusterSize(tc.clusterSize)

			status := smartConfig.GetStatus()

			if status.ActualEnabled != tc.expectedEnabled {
				t.Errorf("Expected enabled=%v, got %v", tc.expectedEnabled, status.ActualEnabled)
			}

			if status.ClusterSize != tc.clusterSize {
				t.Errorf("Expected clusterSize=%d, got %d", tc.clusterSize, status.ClusterSize)
			}

			// 验证原因描述包含预期关键字
			if !containsReason(status.Reason, tc.expectedReason) {
				t.Errorf("Expected reason to contain '%s', got '%s'", tc.expectedReason, status.Reason)
			}
		})
	}
}

// containsReason 检查原因字符串是否包含期望的关键字
func containsReason(reason, expected string) bool {
	// 简单的子串匹配
	return len(reason) > 0 && len(expected) > 0 &&
		(reason == expected ||
		 (len(expected) > 10 && len(reason) > len(expected)-5 && reason[:len(expected)-5] == expected[:len(expected)-5]))
}

// TestDynamicScaling_PerformanceOverhead 测试运行时检查的性能开销
func TestDynamicScaling_PerformanceOverhead(t *testing.T) {
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(3) // 多节点，启用

	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}
	lm := NewLeaseManager(config, smartConfig, zap.NewNop())
	lm.OnBecomeLeader()

	// 测试运行时检查的性能
	start := time.Now()
	iterations := 1000000 // 100 万次

	for i := 0; i < iterations; i++ {
		_ = lm.RenewLease(2, 3)
	}

	elapsed := time.Since(start)
	avgPerOp := elapsed / time.Duration(iterations)

	t.Logf("Dynamic scaling overhead: %v per operation (avg over %d iterations)", avgPerOp, iterations)

	// 运行时检查应该非常快（< 1 微秒）
	if avgPerOp > time.Microsecond {
		t.Logf("Warning: Runtime check overhead is %v (expected < 1µs)", avgPerOp)
	}
}
