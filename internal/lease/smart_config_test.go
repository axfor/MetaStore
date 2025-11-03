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

// TestSmartLeaseConfig_SingleNode 测试单节点场景
func TestSmartLeaseConfig_SingleNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 单节点集群
	slc.UpdateClusterSize(1)

	// etcd 兼容：单节点也应该启用
	if !slc.IsEnabled() {
		t.Error("Lease Read should be enabled in single-node cluster (etcd-compatible)")
	}

	status := slc.GetStatus()
	if !status.ActualEnabled {
		t.Error("ActualEnabled should be true for single-node (etcd-compatible)")
	}
	if !status.UserEnabled {
		t.Error("UserEnabled should still be true")
	}
}

// TestSmartLeaseConfig_MultiNode 测试多节点场景
func TestSmartLeaseConfig_MultiNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 3 节点集群
	slc.UpdateClusterSize(3)

	// 应该被启用
	if !slc.IsEnabled() {
		t.Error("Lease Read should be enabled in multi-node cluster")
	}

	status := slc.GetStatus()
	if !status.ActualEnabled {
		t.Error("ActualEnabled should be true for multi-node")
	}
	if status.ClusterSize != 3 {
		t.Errorf("ClusterSize should be 3, got %d", status.ClusterSize)
	}
}

// TestSmartLeaseConfig_UserDisabled 测试用户禁用
func TestSmartLeaseConfig_UserDisabled(t *testing.T) {
	slc := NewSmartLeaseConfig(false, zap.NewNop())

	// 即使是多节点集群
	slc.UpdateClusterSize(3)

	// 也应该被禁用（因为用户禁用了）
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled when user disables it")
	}
}

// TestSmartLeaseConfig_DynamicChange 测试动态变化
func TestSmartLeaseConfig_DynamicChange(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 开始时是单节点（etcd 兼容：应该启用）
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// 扩容到 3 节点
	slc.UpdateClusterSize(3)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after scaling to 3 nodes")
	}

	// 缩容回单节点（etcd 兼容：仍然启用）
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should still be enabled after scaling back to 1 node (etcd-compatible)")
	}
}

// TestSmartLeaseConfig_UnknownSize 测试未知集群规模
func TestSmartLeaseConfig_UnknownSize(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 未知集群规模
	slc.UpdateClusterSize(0)

	// 应该被禁用（安全起见）
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled for unknown cluster size")
	}
}

// TestSmartLeaseConfig_UserToggle 测试用户动态切换
func TestSmartLeaseConfig_UserToggle(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 3 节点集群
	slc.UpdateClusterSize(3)
	if !slc.IsEnabled() {
		t.Fatal("Should be enabled initially")
	}

	// 用户禁用
	slc.SetUserEnabled(false)
	if slc.IsEnabled() {
		t.Error("Should be disabled after user disables")
	}

	// 用户重新启用
	slc.SetUserEnabled(true)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after user re-enables (cluster is still multi-node)")
	}
}

// TestDetectClusterSizeFromPeers 测试从 peers 检测集群规模
func TestDetectClusterSizeFromPeers(t *testing.T) {
	tests := []struct {
		name     string
		peers    []string
		expected int
	}{
		{
			name:     "Single node",
			peers:    []string{"http://127.0.0.1:2380"},
			expected: 1,
		},
		{
			name: "Three nodes",
			peers: []string{
				"http://127.0.0.1:2380",
				"http://127.0.0.1:2381",
				"http://127.0.0.1:2382",
			},
			expected: 3,
		},
		{
			name:     "Empty",
			peers:    []string{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := DetectClusterSizeFromPeers(tt.peers)
			if size != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, size)
			}
		})
	}
}

// TestSmartLeaseConfig_AutoDetection 测试自动检测
func TestSmartLeaseConfig_AutoDetection(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 模拟集群规模变化
	clusterSize := 1
	getClusterSize := func() int {
		return clusterSize
	}

	stopC := make(chan struct{})
	defer close(stopC)

	// 启动自动检测（100ms 间隔）
	go slc.StartAutoDetection(getClusterSize, 100*time.Millisecond, stopC)

	// 等待初始检测
	time.Sleep(150 * time.Millisecond)

	// etcd 兼容：单节点也应该启用
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// 模拟扩容到 3 节点
	clusterSize = 3

	// 等待下一次检测
	time.Sleep(150 * time.Millisecond)

	// 应该自动启用
	if !slc.IsEnabled() {
		t.Error("Should be enabled after auto-detecting 3 nodes")
	}

	if slc.GetClusterSize() != 3 {
		t.Errorf("ClusterSize should be 3, got %d", slc.GetClusterSize())
	}
}
