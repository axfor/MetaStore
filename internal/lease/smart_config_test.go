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

// TestSmartLeaseConfig_SingleNode testsinglenodescenarioscene
func TestSmartLeaseConfig_SingleNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// singlenodecluster
	slc.UpdateClusterSize(1)

	// etcd compatible：singlenodeshouldenabled
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

// TestSmartLeaseConfig_MultiNode testmanynodescenarioscene
func TestSmartLeaseConfig_MultiNode(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// 3 nodecluster
	slc.UpdateClusterSize(3)

	// shouldbeenabled
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

	// ismanynodecluster
	slc.UpdateClusterSize(3)

	// shouldbedisabled(asuserdisabled)
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled when user disables it")
	}
}

// TestSmartLeaseConfig_DynamicChange testdynamicchangetransform
func TestSmartLeaseConfig_DynamicChange(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// startwhenissinglenode(etcd compatible：shouldenabled)
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// to 3 node
	slc.UpdateClusterSize(3)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after scaling to 3 nodes")
	}

	// singlenode(etcd compatible：stillenabled)
	slc.UpdateClusterSize(1)
	if !slc.IsEnabled() {
		t.Error("Should still be enabled after scaling back to 1 node (etcd-compatible)")
	}
}

// TestSmartLeaseConfig_UnknownSize testunknowncluster
func TestSmartLeaseConfig_UnknownSize(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// unknowncluster
	slc.UpdateClusterSize(0)

	// shouldbedisabled(safe)
	if slc.IsEnabled() {
		t.Error("Lease Read should be disabled for unknown cluster size")
	}
}

// TestSmartLeaseConfig_UserToggle testuserdynamic
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

	// usernewenabled
	slc.SetUserEnabled(true)
	if !slc.IsEnabled() {
		t.Error("Should be enabled after user re-enables (cluster is still multi-node)")
	}
}

// TestDetectClusterSizeFromPeers testfrom peers testcluster
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

// TestSmartLeaseConfig_AutoDetection testtest
func TestSmartLeaseConfig_AutoDetection(t *testing.T) {
	slc := NewSmartLeaseConfig(true, zap.NewNop())

	// clusterchangetransform
	clusterSize := 1
	getClusterSize := func() int {
		return clusterSize
	}

	stopC := make(chan struct{})
	defer close(stopC)

	// starttest(100ms interval)
	go slc.StartAutoDetection(getClusterSize, 100*time.Millisecond, stopC)

	// waitinitialtest
	time.Sleep(150 * time.Millisecond)

	// etcd compatible：singlenodeshouldenabled
	if !slc.IsEnabled() {
		t.Error("Should be enabled for single-node (etcd-compatible)")
	}

	// to 3 node
	clusterSize = 3

	// waitnextfirst timetest
	time.Sleep(150 * time.Millisecond)

	// shouldenabled
	if !slc.IsEnabled() {
		t.Error("Should be enabled after auto-detecting 3 nodes")
	}

	if slc.GetClusterSize() != 3 {
		t.Errorf("ClusterSize should be 3, got %d", slc.GetClusterSize())
	}
}
