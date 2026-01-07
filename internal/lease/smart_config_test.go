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

// TestSmartLeaseConfig_SingleNode test单node场景
func TestSmartLeaseConfig_SingleNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 单nodecluster
	slc.UpdateClusterSize(1)

	// etcd compatible：单node也shouldenabled
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

// TestSmartLeaseConfig_MultiNode test多node场景
func TestSmartLeaseConfig_MultiNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 3 nodecluster
	slc.UpdateClusterSize(3)

	// should被enabled
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

// TestSmartLeaseConfig_UserDisabled testuserdisabled
func TestSmartLeaseConfig_UserDisabled(t *testing.T) {
	slc := NewSmartLeaseConfig(false, zap.NewNop())

	// 即使is多nodecluster
	slc.UpdateClusterSize(3)

	// 也should被disabled（因asuserdisabled）
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled when user disables it")
	}
}

// TestSmartLeaseConfig_DynamicChange testdynamic变化
func TestSmartLeaseConfig_DynamicChange(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// start时is单node（etcd compatible：shouldenabled）
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// 扩容to 3 node
	slc.UpdateClusterSize(3)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after scaling to 3 nodes")
	}

	// 缩容回单node（etcd compatible：仍然enabled）
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should still be enabled after scaling back to 1 node (etcd-compatible)")
	}
}

// TestSmartLeaseConfig_UnknownSize testunknowncluster规模
func TestSmartLeaseConfig_UnknownSize(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// unknowncluster规模
	slc.UpdateClusterSize(0)

	// should被disabled（safe起见）
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled for unknown cluster size")
	}
}

// TestSmartLeaseConfig_UserToggle testuserdynamic切换
func TestSmartLeaseConfig_UserToggle(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 3 nodecluster
	slc.UpdateClusterSize(3)
	if !slc.IsEnabled() {
		t.Fatal("Should be enabled initially")
	}

	// userdisabled
	slc.SetUserEnabled(false)
	if slc.IsEnabled() {
		t.Error("Should be disabled after user disables")
	}

	// user重newenabled
	slc.SetUserEnabled(true)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after user re-enables (cluster is still multi-node)")
	}
}

// TestDetectClusterSizeFromPeers testfrom peers 检测cluster规模
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

// TestSmartLeaseConfig_AutoDetection test自动检测
func TestSmartLeaseConfig_AutoDetection(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 模拟cluster规模变化
	clusterSize := 1
	getClusterSize := func() int {
		return clusterSize
	}

	stopC := make(chan struct{})
	defer close(stopC)

	// start自动检测（100ms interval）
	go slc.StartAutoDetection(getClusterSize, 100*time.Millisecond, stopC)

	// waitinitial检测
	time.Sleep(150 * time.Millisecond)

	// etcd compatible：单node也shouldenabled
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// 模拟扩容to 3 node
	clusterSize = 3

	// wait下一次检测
	time.Sleep(150 * time.Millisecond)

	// should自动enabled
	if !slc.IsEnabled() {
		t.Error("Should be enabled after auto-detecting 3 nodes")
	}

	if slc.GetClusterSize() != 3 {
		t.Errorf("ClusterSize should be 3, got %d", slc.GetClusterSize())
	}
}
