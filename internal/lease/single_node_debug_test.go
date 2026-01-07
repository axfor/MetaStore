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

// TestSingleNodeLeaseRenewal_Debug debugsinglenode
// singlenodescenarioscene，observewatchrowas
func TestSingleNodeLeaseRenewal_Debug(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}

	// notuse SmartConfig(nil = alwaysenabled)
	lm := NewLeaseManager(config, nil, zap.NewNop())
	lm.OnBecomeLeader()

	t.Log("=== testscenarioscene 1: totalNodes=0, receivedAcks=0 ===")
	renewed := lm.RenewLease(0, 0)
	t.Logf("Result: %v (expected: false, majority=1, 0 < 1)", renewed)

	t.Log("\n=== testscenarioscene 2: totalNodes=1, receivedAcks=0 ===")
	renewed = lm.RenewLease(0, 1)
	t.Logf("Result: %v (expected: false, majority=1, 0 < 1)", renewed)

	t.Log("\n=== testscenarioscene 3: totalNodes=1, receivedAcks=1 ===")
	renewed = lm.RenewLease(1, 1)
	t.Logf("Result: %v (expected: true, majority=1, 1 >= 1)", renewed)
	t.Logf("HasValidLease: %v", lm.HasValidLease())
	stats := lm.Stats()
	t.Logf("Stats: RenewCount=%d, ExpireCount=%d, HasValidLease=%v",
		stats.LeaseRenewCount, stats.LeaseExpireCount, stats.HasValidLease)

	if !renewed {
		t.Error("Should renew lease when totalNodes=1, receivedAcks=1")
	}
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after successful renewal")
	}
}

// TestSingleNodeWithSmartConfig testsinglenode + SmartConfig rowas
func TestSingleNodeWithSmartConfig(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}

	t.Log("=== newimplement：SmartConfig enabledsinglenode(etcd compatible)===")
	smartConfig := NewSmartLeaseConfig(true, zap.NewNop())
	smartConfig.UpdateClusterSize(1)

	t.Logf("SmartConfig.IsEnabled(): %v (expected: true, following etcd)", smartConfig.IsEnabled())

	lm := NewLeaseManager(config, smartConfig, zap.NewNop())
	lm.OnBecomeLeader()

	t.Log("\ntest：totalNodes=1, receivedAcks=1")
	renewed := lm.RenewLease(1, 1)
	t.Logf("Result: %v (expected: true, etcd-compatible single-node support)", renewed)

	if !renewed {
		t.Error("Should renew lease in single-node with SmartConfig (etcd-compatible)")
	}
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after successful renewal")
	}
}

// TestSingleNodeWithoutSmartConfig testsinglenodenotuse SmartConfig
func TestSingleNodeWithoutSmartConfig(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}

	t.Log("=== notuse SmartConfig (nil) ===")
	lm := NewLeaseManager(config, nil, zap.NewNop())
	lm.OnBecomeLeader()

	t.Log("\ntest：totalNodes=1, receivedAcks=1")
	renewed := lm.RenewLease(1, 1)
	t.Logf("Result: %v (expected: true)", renewed)
	t.Logf("HasValidLease: %v", lm.HasValidLease())

	if !renewed {
		t.Error("Should renew lease when SmartConfig is nil (always enabled)")
	}
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after successful renewal")
	}

	// verifycanmultiple
	time.Sleep(50 * time.Millisecond)
	renewed = lm.RenewLease(1, 1)
	t.Logf("\nthe second time Result: %v", renewed)
	if !renewed {
		t.Error("Should be able to renew multiple times")
	}

	stats := lm.Stats()
	t.Logf("\nFinal Stats: RenewCount=%d, HasValidLease=%v, Remaining=%v",
		stats.LeaseRenewCount, stats.HasValidLease, stats.LeaseRemaining)

	if stats.LeaseRenewCount < 2 {
		t.Errorf("Expected at least 2 renewals, got %d", stats.LeaseRenewCount)
	}
}

// TestMajorityCalculation testmanycalculate
func TestMajorityCalculation(t *testing.T) {
	testCases := []struct {
		totalNodes int
		majority   int
	}{
		{0, 1},  // 0/2 + 1 = 1
		{1, 1},  // 1/2 + 1 = 1 (0 + 1)
		{2, 2},  // 2/2 + 1 = 2 (1 + 1)
		{3, 2},  // 3/2 + 1 = 2 (1 + 1)
		{4, 3},  // 4/2 + 1 = 3 (2 + 1)
		{5, 3},  // 5/2 + 1 = 3 (2 + 1)
	}

	for _, tc := range testCases {
		majority := tc.totalNodes/2 + 1
		if majority != tc.majority {
			t.Errorf("totalNodes=%d: expected majority=%d, got %d",
				tc.totalNodes, tc.majority, majority)
		}
		t.Logf("totalNodes=%d → majority=%d ✓", tc.totalNodes, majority)
	}
}
