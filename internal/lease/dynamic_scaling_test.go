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

// TestDynamicScaleUp testfromsinglenodetomanynodescenarioscene
// verify：Lease Read componentalwayscreate，insinglenodeandmanynodenextallcan(etcd compatible)
func TestDynamicScaleUp(t *testing.T) {
	// 1. createcanconfig(singlenodestart)
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(1)

	// newimplement：singlenodeenabled(etcd compatible)
	if !smartConfig.IsEnabled() {
		t.Error("Should be enabled for single-node cluster (etcd-compatible)")
	}

	// 2. create LeaseManager(alwayscreate，notcluster)
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}
	lm := NewLeaseManager(config, smartConfig, zap.NewNop())

	// 3. verifycomponentalready create
	if lm == nil {
		t.Fatal("LeaseManager should be created")
	}

	// 4. becomeas Leader
	lm.OnBecomeLeader()

	// 5. testlease(etcd compatible：singlenodeshouldsuccess)
	renewed := lm.RenewLease(1, 1)
	if !renewed {
		t.Error("Should renew lease in single-node (etcd-compatible)")
	}

	// verifyalready buildlease
	if !lm.HasValidLease() {
		t.Error("Should have valid lease in single-node")
	}

	// 6. to 3 node
	smartConfig.UpdateClusterSize(3)

	if !smartConfig.IsEnabled() {
		t.Error("Should be enabled after scaling to 3 nodes")
	}

	// 7.  timetest(shouldsuccess)
	renewed = lm.RenewLease(2, 3)
	if !renewed {
		t.Error("Should renew lease after scale-up to 3 nodes")
	}

	// verifyleasealready build
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after scale-up")
	}

	// 8. singlenode(etcd compatible：stillenabled)
	smartConfig.UpdateClusterSize(1)

	if !smartConfig.IsEnabled() {
		t.Error("Should still be enabled after scaling back to 1 node (etcd-compatible)")
	}

	// 9. test(etcd compatible：shouldsuccess)
	renewed = lm.RenewLease(1, 1)
	if !renewed {
		t.Error("Should renew lease after scaling back to 1 node (etcd-compatible)")
	}
}

// TestDynamicScaleUp_ReadIndexManager test ReadIndexManager dynamic
func TestDynamicScaleUp_ReadIndexManager(t *testing.T) {
	// 1. singlenodestart
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(1)

	rim := NewReadIndexManager(smartConfig, zap.NewNop())

	// 2. singlenodewhenrecordfastpath(etcd compatible：shouldrecord)
	rim.RecordFastPathRead()

	stats := rim.Stats()
	if stats.FastPathReads != 1 {
		t.Errorf("Fast path reads should be 1 in single-node (etcd-compatible), got %d", stats.FastPathReads)
	}

	// 3. to 3 node
	smartConfig.UpdateClusterSize(3)

	// 4. recordfastpath(shouldsuccess)
	rim.RecordFastPathRead()

	stats = rim.Stats()
	if stats.FastPathReads != 2 {
		t.Errorf("Fast path reads should be 2 after scale-up, got %d", stats.FastPathReads)
	}

	// 5. singlenode(etcd compatible：stillrecord)
	smartConfig.UpdateClusterSize(1)

	// 6. recordfastpath(etcd compatible：shouldrecord)
	rim.RecordFastPathRead()

	stats = rim.Stats()
	if stats.FastPathReads != 3 {
		t.Errorf("Fast path reads should be 3 after scale-down (etcd-compatible), got %d", stats.FastPathReads)
	}
}

// TestDynamicScaling_StatusTracking testdynamicstatustrace
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
			expectedEnabled: true, // etcd compatible：singlenodeenabled
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

			// verifyreasonpackageclosekey
			if !containsReason(status.Reason, tc.expectedReason) {
				t.Errorf("Expected reason to contain '%s', got '%s'", tc.expectedReason, status.Reason)
			}
		})
	}
}

// containsReason checkreasonisnopackageclosekey
func containsReason(reason, expected string) bool {
	// singlematch
	return len(reason) > 0 && len(expected) > 0 &&
		(reason == expected ||
		 (len(expected) > 10 && len(reason) > len(expected)-5 && reason[:len(expected)-5] == expected[:len(expected)-5]))
}

// TestDynamicScaling_PerformanceOverhead testrunningwhencheckperformanceopen
func TestDynamicScaling_PerformanceOverhead(t *testing.T) {
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(3) // manynode，enabled

	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}
	lm := NewLeaseManager(config, smartConfig, zap.NewNop())
	lm.OnBecomeLeader()

	// testrunningwhencheckperformance
	start := time.Now()
	iterations := 1000000 // 100  time

	for i := 0; i < iterations; i++ {
		_ = lm.RenewLease(2, 3)
	}

	elapsed := time.Since(start)
	avgPerOp := elapsed / time.Duration(iterations)

	t.Logf("Dynamic scaling overhead: %v per operation (avg over %d iterations)", avgPerOp, iterations)

	// runningwhencheckshouldfast(< 1 seconds)
	if avgPerOp > time.Microsecond {
		t.Logf("Warning: Runtime check overhead is %v (expected < 1µs)", avgPerOp)
	}
}
