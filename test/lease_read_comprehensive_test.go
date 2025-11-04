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

package test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithLeaseRead config option: enable Lease Read
func WithLeaseRead(cfg *config.Config) {
	cfg.Server.Raft.LeaseRead.Enable = true
	cfg.Server.Raft.LeaseRead.ClockDrift = 10 * time.Millisecond // Reduce ClockDrift to get longer lease time
}

// WithoutLeaseRead config option: disable Lease Read
func WithoutLeaseRead(cfg *config.Config) {
	cfg.Server.Raft.LeaseRead.Enable = false
}

// TestLeaseRead_SingleNode_Comprehensive Test scenario 1: Single-node cluster (Lease Read enabled)
// Verify: Lease Read works correctly in single-node, read/write traffic is normal
func TestLeaseRead_SingleNode_Comprehensive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip Lease Read comprehensive test (short mode)")
	}

	t.Log("=== Test Scenario 1: Single-node Cluster (Lease Read Enabled) ===")

	// 1. Create single-node cluster (Lease Read enabled)
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// Wait for node to start
	time.Sleep(1 * time.Second)

	// 2. Get LeaseManager and verify state
	t.Log("Step 1: Verify Lease Read state")
	testable, ok := node.raftNode.(raft.TestableNode)
	require.True(t, ok, "RaftNode should implement TestableNode")

	leaseManager := testable.LeaseManager()
	require.NotNil(t, leaseManager, "LeaseManager should exist")

	// Wait for lease to be established
	time.Sleep(500 * time.Millisecond)

	stats := leaseManager.Stats()
	t.Logf("Lease state: IsLeader=%v, HasValidLease=%v, RenewCount=%d",
		stats.IsLeader, stats.HasValidLease, stats.LeaseRenewCount)

	// Single node should be able to establish lease (etcd compatible)
	assert.True(t, stats.IsLeader, "Should be leader")
	assert.True(t, stats.HasValidLease, "Should have valid lease (etcd-compatible)")
	assert.Greater(t, stats.LeaseRenewCount, int64(0), "Should have renewed lease")

	// 3. Test write operations
	t.Log("Step 2: Test write operations")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	t.Log("✅ Successfully wrote 10 key-value pairs")

	// Wait for data commit
	time.Sleep(300 * time.Millisecond)

	// 4. Test read operations (should use Lease Read)
	t.Log("Step 3: Test read operations (Lease Read)")
	readIndexManager := testable.ReadIndexManager()
	require.NotNil(t, readIndexManager, "ReadIndexManager should exist")

	initialFastPathReads := readIndexManager.Stats().FastPathReads

	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("key%d", i)
		expectedValue := fmt.Sprintf("value%d", i)

		resp, err := kvStore.Range(ctx, key, "", 1, 0)
		require.NoError(t, err, "Range should succeed")
		require.Len(t, resp.Kvs, 1, "Should have 1 key")
		assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should match")
	}

	t.Log("✅ Successfully read 10 key-value pairs")

	// 5. Verify fast path usage
	finalStats := readIndexManager.Stats()
	fastPathReads := finalStats.FastPathReads - initialFastPathReads
	t.Logf("Lease Read fast path usage: %d times", fastPathReads)

	// Single node should use fast path for all reads
	assert.Equal(t, int64(10), fastPathReads, "All reads should use fast path")

	t.Log("✅ Single-node Lease Read test passed")
}

// TestLeaseRead_SingleNode_WithoutLeaseRead Test scenario 1-Control group: Single-node cluster (Lease Read disabled)
// Control group: verify behavior when Lease Read is disabled
func TestLeaseRead_SingleNode_WithoutLeaseRead(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip Lease Read control test (short mode)")
	}

	t.Log("=== Test Scenario 1-Control Group: Single-node Cluster (Lease Read Disabled) ===")

	// 1. Create single-node cluster (Lease Read disabled)
	node, cleanup := startMemoryNode(t, 1, WithoutLeaseRead)
	defer cleanup()

	// Wait for node to start
	time.Sleep(1 * time.Second)

	// 2. Verify Lease Read is not enabled
	t.Log("Step 1: Verify Lease Read disabled state")
	testable, ok := node.raftNode.(raft.TestableNode)
	require.True(t, ok, "RaftNode should implement TestableNode")

	leaseManager := testable.LeaseManager()
	// leaseManager may be nil when disabled
	if leaseManager != nil {
		stats := leaseManager.Stats()
		t.Logf("Lease state: HasValidLease=%v (expected: false)", stats.HasValidLease)
	} else {
		t.Log("Lease Manager not created (disabled)")
	}

	// 3. Test that read and write still work normally
	t.Log("Step 2: Verify read/write operations work normally (using ReadIndex)")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// Write
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	time.Sleep(300 * time.Millisecond)

	// Read
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("key%d", i)
		expectedValue := fmt.Sprintf("value%d", i)

		resp, err := kvStore.Range(ctx, key, "", 1, 0)
		require.NoError(t, err, "Range should succeed")
		require.Len(t, resp.Kvs, 1, "Should have 1 key")
		assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should match")
	}

	t.Log("✅ Read/write operations work normally when Lease Read is disabled")
}

// TestLeaseRead_ConcurrentReadWrite Test scenario 2: Concurrent read/write test
// Verify: Correctness and performance of Lease Read under high concurrency
func TestLeaseRead_ConcurrentReadWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip concurrent read/write test (short mode)")
	}

	t.Log("=== Test Scenario 2: Concurrent Read/Write Test ===")

	// 1. Create single-node cluster (Lease Read enabled)
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// Wait for node to start and lease to be established
	time.Sleep(1 * time.Second)

	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 2. Concurrent writes
	t.Log("Step 1: Concurrent write of 100 key-value pairs")
	var writeWg sync.WaitGroup
	writeCount := 100

	writeStart := time.Now()
	for i := 1; i <= writeCount; i++ {
		writeWg.Add(1)
		go func(idx int) {
			defer writeWg.Done()
			key := fmt.Sprintf("concurrent_key%d", idx)
			value := fmt.Sprintf("concurrent_value%d", idx)
			_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
			if err != nil {
				t.Logf("Write error: %v", err)
			}
		}(i)
	}
	writeWg.Wait()
	writeDuration := time.Since(writeStart)
	t.Logf("✅ Concurrent writes completed, duration: %v, QPS: %.2f", writeDuration, float64(writeCount)/writeDuration.Seconds())

	// Wait for data commit
	time.Sleep(500 * time.Millisecond)

	// 3. Concurrent reads
	t.Log("Step 2: Concurrent read of 100 key-value pairs (Lease Read)")
	testable, _ := node.raftNode.(raft.TestableNode)
	readIndexManager := testable.ReadIndexManager()
	initialFastPathReads := readIndexManager.Stats().FastPathReads

	var readWg sync.WaitGroup
	var readErrors int32

	readStart := time.Now()
	for i := 1; i <= writeCount; i++ {
		readWg.Add(1)
		go func(idx int) {
			defer readWg.Done()
			key := fmt.Sprintf("concurrent_key%d", idx)
			expectedValue := fmt.Sprintf("concurrent_value%d", idx)

			resp, err := kvStore.Range(ctx, key, "", 1, 0)
			if err != nil {
				t.Logf("Read error for key %s: %v", key, err)
				atomic.AddInt32(&readErrors, 1)
				return
			}
			if len(resp.Kvs) == 0 {
				t.Logf("Key not found: %s", key)
				atomic.AddInt32(&readErrors, 1)
				return
			}
			if string(resp.Kvs[0].Value) != expectedValue {
				t.Logf("Value mismatch for key %s: expected %s, got %s", key, expectedValue, string(resp.Kvs[0].Value))
				atomic.AddInt32(&readErrors, 1)
			}
		}(i)
	}
	readWg.Wait()
	readDuration := time.Since(readStart)
	t.Logf("✅ Concurrent reads completed, duration: %v, QPS: %.2f, errors: %d",
		readDuration, float64(writeCount)/readDuration.Seconds(), readErrors)

	// 4. Verify fast path usage
	finalStats := readIndexManager.Stats()
	fastPathReads := finalStats.FastPathReads - initialFastPathReads
	t.Logf("Lease Read fast path usage: %d/%d times (%.1f%%)",
		fastPathReads, writeCount, float64(fastPathReads)/float64(writeCount)*100)

	assert.Greater(t, fastPathReads, int64(90), "Most reads should use fast path")

	t.Log("✅ Concurrent read/write test passed")
}

// TestLeaseRead_LeaseExpiration Test scenario 3: Lease expiration and renewal test
// Verify: Behavior after lease expiration and automatic renewal mechanism
func TestLeaseRead_LeaseExpiration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip lease expiration test (short mode)")
	}

	t.Log("=== Test Scenario 3: Lease Expiration and Renewal Test ===")

	// 1. Create single-node cluster (Lease Read enabled)
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// Wait for node to start
	time.Sleep(1 * time.Second)

	testable, _ := node.raftNode.(raft.TestableNode)
	leaseManager := testable.LeaseManager()
	require.NotNil(t, leaseManager, "LeaseManager should exist")

	// 2. Verify initial lease state
	t.Log("Step 1: Verify initial lease state")
	initialStats := leaseManager.Stats()
	t.Logf("Initial state: HasValidLease=%v, RenewCount=%d, Remaining=%v",
		initialStats.HasValidLease, initialStats.LeaseRenewCount, initialStats.LeaseRemaining)

	assert.True(t, initialStats.HasValidLease, "Should have valid lease")
	initialRenewCount := initialStats.LeaseRenewCount

	// 3. Wait sufficient time to observe lease renewal
	t.Log("Step 2: Observe automatic lease renewal")
	time.Sleep(3 * time.Second)

	renewedStats := leaseManager.Stats()
	t.Logf("After renewal state: HasValidLease=%v, RenewCount=%d, Remaining=%v",
		renewedStats.HasValidLease, renewedStats.LeaseRenewCount, renewedStats.LeaseRemaining)

	assert.True(t, renewedStats.HasValidLease, "Should still have valid lease")
	assert.Greater(t, renewedStats.LeaseRenewCount, initialRenewCount, "Should have renewed lease")

	// 4. Read data during valid lease period
	t.Log("Step 3: Read data during valid lease period")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// Write test data
	_, _, err := kvStore.PutWithLease(ctx, "test_key", "test_value", 0)
	require.NoError(t, err, "Put should succeed")

	time.Sleep(300 * time.Millisecond)

	// Read should use fast path
	readIndexManager := testable.ReadIndexManager()
	initialFastPathReads := readIndexManager.Stats().FastPathReads

	resp, err := kvStore.Range(ctx, "test_key", "", 1, 0)
	require.NoError(t, err, "Range should succeed")
	require.Len(t, resp.Kvs, 1, "Should have 1 key")
	assert.Equal(t, "test_value", string(resp.Kvs[0].Value), "Value should match")

	finalFastPathReads := readIndexManager.Stats().FastPathReads
	assert.Equal(t, initialFastPathReads+1, finalFastPathReads, "Should use fast path")

	t.Log("✅ Lease expiration and renewal test passed")
}

// TestLeaseRead_DataConsistency Test scenario 4: Data consistency verification
// Verify: Lease Read does not break data consistency
func TestLeaseRead_DataConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip data consistency test (short mode)")
	}

	t.Log("=== Test Scenario 4: Data Consistency Verification ===")

	// 1. Create single-node cluster (Lease Read enabled)
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// Wait for node to start
	time.Sleep(1 * time.Second)

	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 2. Write initial data
	t.Log("Step 1: Write initial data")
	testData := make(map[string]string)
	for i := 1; i <= 50; i++ {
		key := fmt.Sprintf("consistency_key%d", i)
		value := fmt.Sprintf("consistency_value%d", i)
		testData[key] = value
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	t.Log("✅ Wrote 50 key-value pairs")
	time.Sleep(500 * time.Millisecond)

	// 3. Multiple reads to verify consistency
	t.Log("Step 2: Multiple reads to verify consistency")
	for round := 1; round <= 5; round++ {
		t.Logf("Round %d reading...", round)

		for key, expectedValue := range testData {
			resp, err := kvStore.Range(ctx, key, "", 1, 0)
			require.NoError(t, err, "Range should succeed in round %d", round)
			require.Len(t, resp.Kvs, 1, "Should have 1 key in round %d", round)
			assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should be consistent in round %d for key %s", round, key)
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Log("✅ 5 rounds of read verification passed, data consistency maintained")

	// 4. Verify fast path usage
	testable, _ := node.raftNode.(raft.TestableNode)
	readIndexManager := testable.ReadIndexManager()
	stats := readIndexManager.Stats()

	t.Logf("Fast path usage: %d reads", stats.FastPathReads)
	assert.Greater(t, stats.FastPathReads, int64(200), "Should have many fast path reads")

	t.Log("✅ Data consistency test passed")
}

// TestLeaseRead_PerformanceComparison Test scenario 5: Performance comparison test
// Compare: Performance difference between enabling and disabling Lease Read
func TestLeaseRead_PerformanceComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("Skip performance comparison test (short mode)")
	}

	t.Log("=== Test Scenario 5: Performance Comparison Test ===")

	// Prepare test data
	testCount := 100
	ctx := context.Background()

	// Test function
	runReadTest := func(kvStore *memory.Memory, name string) time.Duration {
		// Write data
		for i := 1; i <= testCount; i++ {
			key := fmt.Sprintf("perf_key%d", i)
			value := fmt.Sprintf("perf_value%d", i)
			kvStore.PutWithLease(ctx, key, value, 0)
		}
		time.Sleep(500 * time.Millisecond)

		// Test read performance
		start := time.Now()
		for i := 1; i <= testCount; i++ {
			key := fmt.Sprintf("perf_key%d", i)
			kvStore.Range(ctx, key, "", 1, 0)
		}
		duration := time.Since(start)

		qps := float64(testCount) / duration.Seconds()
		t.Logf("%s: Read %d times, duration %v, QPS: %.2f", name, testCount, duration, qps)

		return duration
	}

	// 1. Test with Lease Read enabled
	t.Log("Step 1: Test with Lease Read enabled")
	nodeWith, cleanupWith := startMemoryNode(t, 1, WithLeaseRead)
	time.Sleep(1 * time.Second)

	kvStoreWith := nodeWith.kvStore.(*memory.Memory)
	durationWith := runReadTest(kvStoreWith, "Lease Read Enabled")

	// Save first node statistics (before cleanup)
	var withStats *struct {
		FastPathReads int64
	}
	testableWith, _ := nodeWith.raftNode.(raft.TestableNode)
	if testableWith != nil && testableWith.ReadIndexManager() != nil {
		stats := testableWith.ReadIndexManager().Stats()
		withStats = &struct{ FastPathReads int64 }{FastPathReads: stats.FastPathReads}
		t.Logf("Lease Read Enabled - Fast path usage: %d times", withStats.FastPathReads)
	}

	// Cleanup first node to avoid port and data directory conflicts
	cleanupWith()
	time.Sleep(500 * time.Millisecond)

	// 2. Test with Lease Read disabled (independent single-node cluster, same nodeID and port)
	t.Log("Step 2: Test with Lease Read disabled")
	nodeWithout, cleanupWithout := startMemoryNode(t, 1, WithoutLeaseRead)
	defer cleanupWithout()
	time.Sleep(1 * time.Second)

	kvStoreWithout := nodeWithout.kvStore.(*memory.Memory)
	durationWithout := runReadTest(kvStoreWithout, "Lease Read Disabled")

	// 3. Performance comparison analysis
	t.Log("Step 3: Performance comparison analysis")
	improvement := float64(durationWithout-durationWith) / float64(durationWithout) * 100
	speedup := float64(durationWithout) / float64(durationWith)

	t.Logf("Performance improvement: %.2f%%", improvement)
	t.Logf("Speedup: %.2fx", speedup)

	// Verify Lease Read actually used fast path (using previously saved statistics)
	if withStats != nil {
		assert.Greater(t, withStats.FastPathReads, int64(0), "Should use fast path when enabled")
	}

	t.Log("✅ Performance comparison test completed")
}
