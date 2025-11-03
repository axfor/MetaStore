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

// TestLeaseManager_Creation tests creating a new lease manager
func TestLeaseManager_Creation(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      500 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用

	if lm == nil {
		t.Fatal("NewLeaseManager returned nil")
	}

	if lm.electionTimeout != config.ElectionTimeout {
		t.Errorf("electionTimeout mismatch: got %v, want %v", lm.electionTimeout, config.ElectionTimeout)
	}

	if lm.heartbeatTick != config.HeartbeatTick {
		t.Errorf("heartbeatTick mismatch: got %v, want %v", lm.heartbeatTick, config.HeartbeatTick)
	}

	if lm.clockDrift != config.ClockDrift {
		t.Errorf("clockDrift mismatch: got %v, want %v", lm.clockDrift, config.ClockDrift)
	}

	// Should not be leader initially
	if lm.IsLeader() {
		t.Error("Newly created lease manager should not be leader")
	}

	// Should not have valid lease initially
	if lm.HasValidLease() {
		t.Error("Newly created lease manager should not have valid lease")
	}
}

// TestLeaseManager_DefaultClockDrift tests default clock drift value
func TestLeaseManager_DefaultClockDrift(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		// ClockDrift not set, should default to 500ms
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用

	expectedDrift := 500 * time.Millisecond
	if lm.clockDrift != expectedDrift {
		t.Errorf("clockDrift should default to %v, got %v", expectedDrift, lm.clockDrift)
	}
}

// TestLeaseManager_BecomeLeader tests becoming leader
func TestLeaseManager_BecomeLeader(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用

	// Initially not leader
	if lm.IsLeader() {
		t.Error("Should not be leader initially")
	}

	// Become leader
	lm.OnBecomeLeader()

	// Should be leader now
	if !lm.IsLeader() {
		t.Error("Should be leader after OnBecomeLeader()")
	}

	// Should not have valid lease yet (needs renewal)
	if lm.HasValidLease() {
		t.Error("Should not have valid lease without renewal")
	}
}

// TestLeaseManager_BecomeFollower tests becoming follower
func TestLeaseManager_BecomeFollower(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond, // Smaller drift for testing
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用

	// Become leader first
	lm.OnBecomeLeader()
	if !lm.IsLeader() {
		t.Fatal("Should be leader")
	}

	// Renew lease
	lm.RenewLease(3, 3) // Majority of 3 nodes

	// Should have valid lease
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after renewal")
	}

	// Become follower
	lm.OnBecomeFollower()

	// Should not be leader
	if lm.IsLeader() {
		t.Error("Should not be leader after OnBecomeFollower()")
	}

	// Lease should be invalidated
	if lm.HasValidLease() {
		t.Error("Lease should be invalidated after stepping down")
	}
}

// TestLeaseManager_RenewLease_Success tests successful lease renewal
func TestLeaseManager_RenewLease_Success(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond, // Smaller drift for testing
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	lm.OnBecomeLeader()

	// Renew lease with majority acks
	totalNodes := 3
	receivedAcks := 2 // Majority

	renewed := lm.RenewLease(receivedAcks, totalNodes)
	if !renewed {
		t.Fatal("Lease renewal should succeed with majority acks")
	}

	// Should have valid lease now
	if !lm.HasValidLease() {
		t.Error("Should have valid lease after renewal")
	}

	// Lease remaining should be positive
	remaining := lm.GetLeaseRemaining()
	if remaining <= 0 {
		t.Errorf("Lease remaining should be positive, got %v", remaining)
	}

	// Check stats
	stats := lm.Stats()
	if stats.LeaseRenewCount != 1 {
		t.Errorf("LeaseRenewCount should be 1, got %d", stats.LeaseRenewCount)
	}
}

// TestLeaseManager_RenewLease_InsufficientAcks tests renewal failure
func TestLeaseManager_RenewLease_InsufficientAcks(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	lm.OnBecomeLeader()

	// Try to renew with insufficient acks
	totalNodes := 3
	receivedAcks := 1 // Not majority

	renewed := lm.RenewLease(receivedAcks, totalNodes)
	if renewed {
		t.Error("Lease renewal should fail with insufficient acks")
	}

	// Should not have valid lease
	if lm.HasValidLease() {
		t.Error("Should not have valid lease without majority acks")
	}
}

// TestLeaseManager_RenewLease_NotLeader tests renewal when not leader
func TestLeaseManager_RenewLease_NotLeader(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	// Not leader

	// Try to renew lease
	renewed := lm.RenewLease(3, 3)
	if renewed {
		t.Error("Lease renewal should fail when not leader")
	}
}

// TestLeaseManager_LeaseExpiration tests lease expiration
func TestLeaseManager_LeaseExpiration(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 200 * time.Millisecond, // Short timeout for testing
		HeartbeatTick:   50 * time.Millisecond,
		ClockDrift:      20 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	lm.OnBecomeLeader()

	// Renew lease
	lm.RenewLease(3, 3)

	// Should have valid lease
	if !lm.HasValidLease() {
		t.Fatal("Should have valid lease after renewal")
	}

	// Wait for lease to expire
	// Lease duration = min(200ms/2, 50ms*3) - 20ms = min(100ms, 150ms) - 20ms = 80ms
	time.Sleep(150 * time.Millisecond) // Wait longer than lease duration

	// Lease should be expired now
	if lm.HasValidLease() {
		t.Error("Lease should have expired")
	}

	// Check stats
	stats := lm.Stats()
	if stats.LeaseExpireCount == 0 {
		t.Error("LeaseExpireCount should be > 0 after expiration")
	}
}

// TestLeaseManager_MultipleRenewals tests multiple lease renewals
func TestLeaseManager_MultipleRenewals(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	lm.OnBecomeLeader()

	// Renew lease multiple times
	for i := 0; i < 5; i++ {
		renewed := lm.RenewLease(3, 3)
		if !renewed {
			t.Errorf("Renewal %d failed", i+1)
		}

		// Should have valid lease
		if !lm.HasValidLease() {
			t.Errorf("Should have valid lease after renewal %d", i+1)
		}

		time.Sleep(50 * time.Millisecond) // Wait a bit between renewals
	}

	// Check stats
	stats := lm.Stats()
	if stats.LeaseRenewCount != 5 {
		t.Errorf("LeaseRenewCount should be 5, got %d", stats.LeaseRenewCount)
	}
}

// TestLeaseManager_LeaseRemaining tests lease remaining calculation
func TestLeaseManager_LeaseRemaining(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 1 * time.Second,
		HeartbeatTick:   100 * time.Millisecond,
		ClockDrift:      100 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用

	// Not leader: should return 0
	remaining := lm.GetLeaseRemaining()
	if remaining != 0 {
		t.Errorf("Non-leader should have 0 lease remaining, got %v", remaining)
	}

	// Become leader
	lm.OnBecomeLeader()

	// No renewal yet: should return 0
	remaining = lm.GetLeaseRemaining()
	if remaining != 0 {
		t.Errorf("Leader without renewal should have 0 lease remaining, got %v", remaining)
	}

	// Renew lease
	lm.RenewLease(3, 3)

	// Should have positive remaining time
	remaining = lm.GetLeaseRemaining()
	if remaining <= 0 {
		t.Errorf("Leader with valid lease should have positive remaining, got %v", remaining)
	}

	// Lease duration = min(1000ms/2, 100ms*3) - 100ms = min(500ms, 300ms) - 100ms = 200ms
	expectedMin := 100 * time.Millisecond // Some margin for execution time
	expectedMax := 300 * time.Millisecond

	if remaining < expectedMin || remaining > expectedMax {
		t.Errorf("Lease remaining %v out of expected range [%v, %v]", remaining, expectedMin, expectedMax)
	}
}

// TestLeaseManager_Stats tests statistics collection
func TestLeaseManager_Stats(t *testing.T) {
	config := LeaseConfig{
		ElectionTimeout: 200 * time.Millisecond,
		HeartbeatTick:   50 * time.Millisecond,
		ClockDrift:      20 * time.Millisecond,
	}

	lm := NewLeaseManager(config, nil, zap.NewNop()) // nil = 总是启用
	lm.OnBecomeLeader()

	// Initial stats
	stats := lm.Stats()
	if !stats.IsLeader {
		t.Error("Stats should show IsLeader=true")
	}
	if stats.HasValidLease {
		t.Error("Stats should show HasValidLease=false before renewal")
	}

	// Renew lease
	lm.RenewLease(3, 3)

	// Stats after renewal
	stats = lm.Stats()
	if !stats.HasValidLease {
		t.Error("Stats should show HasValidLease=true after renewal")
	}
	if stats.LeaseRemaining <= 0 {
		t.Errorf("Stats should show positive LeaseRemaining, got %v", stats.LeaseRemaining)
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Stats after expiration
	stats = lm.Stats()
	if stats.HasValidLease {
		t.Error("Stats should show HasValidLease=false after expiration")
	}
	if stats.LeaseExpireCount == 0 {
		t.Error("Stats should show LeaseExpireCount > 0")
	}
}

// TestMinDuration tests the minDuration helper
func TestMinDuration(t *testing.T) {
	tests := []struct {
		name     string
		a        time.Duration
		b        time.Duration
		expected time.Duration
	}{
		{"a < b", 100 * time.Millisecond, 200 * time.Millisecond, 100 * time.Millisecond},
		{"a > b", 200 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
		{"a == b", 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := minDuration(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("minDuration(%v, %v) = %v, want %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}
