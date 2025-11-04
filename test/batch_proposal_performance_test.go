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

	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestBatchProposal_LowLoad tests batch proposal performance under low load
// Expected: batchSize stays low (1-8), minimal latency increase
func TestBatchProposal_LowLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping batch proposal low load test in short mode")
	}

	t.Log("=== Testing Batch Proposal: Low Load Scenario ===")

	// Test with batch enabled
	t.Log("\n--- Test 1: Batch Enabled ---")
	batchResult := runLoadTest(t, "low-load-batch", 5, 200, true)

	// Test without batch (baseline)
	t.Log("\n--- Test 2: Batch Disabled (Baseline) ---")
	noBatchResult := runLoadTest(t, "low-load-nobatch", 5, 200, false)

	// Report comparison
	t.Log("\n=== Low Load Performance Comparison ===")
	t.Logf("Batch Enabled:")
	t.Logf("  Throughput: %.2f ops/sec", batchResult.throughput)
	t.Logf("  Avg Latency: %v", batchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", batchResult.successRate)

	t.Logf("\nBatch Disabled (Baseline):")
	t.Logf("  Throughput: %.2f ops/sec", noBatchResult.throughput)
	t.Logf("  Avg Latency: %v", noBatchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", noBatchResult.successRate)

	t.Logf("\nComparison:")
	t.Logf("  Throughput Improvement: %.2fx", batchResult.throughput/noBatchResult.throughput)
	t.Logf("  Latency Overhead: +%v", batchResult.avgLatency-noBatchResult.avgLatency)

	// Low load: expect minimal latency increase
	latencyOverhead := batchResult.avgLatency - noBatchResult.avgLatency
	if latencyOverhead > 50*time.Millisecond {
		t.Errorf("Low load latency overhead too high: %v (expected < 50ms)", latencyOverhead)
	}

	// Throughput should be similar or better
	if batchResult.throughput < noBatchResult.throughput*0.8 {
		t.Errorf("Batch throughput significantly worse: %.2f vs %.2f ops/sec",
			batchResult.throughput, noBatchResult.throughput)
	}
}

// TestBatchProposal_MediumLoad tests batch proposal performance under medium load
// Expected: batchSize 8-64, 5-10x throughput improvement
func TestBatchProposal_MediumLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping batch proposal medium load test in short mode")
	}

	t.Log("=== Testing Batch Proposal: Medium Load Scenario ===")

	// Test with batch enabled
	t.Log("\n--- Test 1: Batch Enabled ---")
	batchResult := runLoadTest(t, "medium-load-batch", 20, 500, true)

	// Test without batch (baseline)
	t.Log("\n--- Test 2: Batch Disabled (Baseline) ---")
	noBatchResult := runLoadTest(t, "medium-load-nobatch", 20, 500, false)

	// Report comparison
	t.Log("\n=== Medium Load Performance Comparison ===")
	t.Logf("Batch Enabled:")
	t.Logf("  Throughput: %.2f ops/sec", batchResult.throughput)
	t.Logf("  Avg Latency: %v", batchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", batchResult.successRate)

	t.Logf("\nBatch Disabled (Baseline):")
	t.Logf("  Throughput: %.2f ops/sec", noBatchResult.throughput)
	t.Logf("  Avg Latency: %v", noBatchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", noBatchResult.successRate)

	t.Logf("\nComparison:")
	improvement := batchResult.throughput / noBatchResult.throughput
	t.Logf("  Throughput Improvement: %.2fx", improvement)
	t.Logf("  Latency Overhead: +%v", batchResult.avgLatency-noBatchResult.avgLatency)

	// Medium load: expect 3-10x throughput improvement
	if improvement < 3.0 {
		t.Logf("Warning: Medium load throughput improvement lower than expected: %.2fx (expected > 3x)", improvement)
	}

	// Latency increase should be acceptable
	latencyOverhead := batchResult.avgLatency - noBatchResult.avgLatency
	if latencyOverhead > 100*time.Millisecond {
		t.Logf("Warning: Medium load latency overhead high: %v (expected < 100ms)", latencyOverhead)
	}
}

// TestBatchProposal_HighLoad tests batch proposal performance under high load
// Expected: batchSize 64-256, 10-50x throughput improvement
func TestBatchProposal_HighLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping batch proposal high load test in short mode")
	}

	t.Log("=== Testing Batch Proposal: High Load Scenario ===")

	// Test with batch enabled
	t.Log("\n--- Test 1: Batch Enabled ---")
	batchResult := runLoadTest(t, "high-load-batch", 50, 1000, true)

	// Test without batch (baseline)
	t.Log("\n--- Test 2: Batch Disabled (Baseline) ---")
	noBatchResult := runLoadTest(t, "high-load-nobatch", 50, 1000, false)

	// Report comparison
	t.Log("\n=== High Load Performance Comparison ===")
	t.Logf("Batch Enabled:")
	t.Logf("  Throughput: %.2f ops/sec", batchResult.throughput)
	t.Logf("  Avg Latency: %v", batchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", batchResult.successRate)

	t.Logf("\nBatch Disabled (Baseline):")
	t.Logf("  Throughput: %.2f ops/sec", noBatchResult.throughput)
	t.Logf("  Avg Latency: %v", noBatchResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", noBatchResult.successRate)

	t.Logf("\nComparison:")
	improvement := batchResult.throughput / noBatchResult.throughput
	t.Logf("  Throughput Improvement: %.2fx", improvement)
	t.Logf("  Latency Overhead: +%v", batchResult.avgLatency-noBatchResult.avgLatency)

	// High load: expect 5-50x throughput improvement
	if improvement < 5.0 {
		t.Logf("Warning: High load throughput improvement lower than expected: %.2fx (expected > 5x)", improvement)
	}

	// Success rate should remain high
	if batchResult.successRate < 95.0 {
		t.Errorf("High load success rate too low: %.2f%% (expected > 95%%)", batchResult.successRate)
	}
}

// TestBatchProposal_TrafficSurge tests dynamic adjustment during traffic surge
// Expected: quickly adjust from low batch to high batch (1-2 cycles)
func TestBatchProposal_TrafficSurge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping batch proposal traffic surge test in short mode")
	}

	t.Log("=== Testing Batch Proposal: Traffic Surge Scenario ===")

	// Start node with batch enabled
	node, cleanup := startMemoryNode(t, 1, WithBatchProposal(1, 256, 5*time.Millisecond, 20*time.Millisecond, 0.7))
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Phase 1: Low traffic (5 ops/sec) for 2 seconds
	t.Log("\n--- Phase 1: Low Traffic (5 ops/sec) ---")
	lowTrafficStart := time.Now()
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("/surge-test/low-%d", i)
		_, err := cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		if err != nil {
			t.Logf("Put failed: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	lowTrafficDuration := time.Since(lowTrafficStart)
	t.Logf("Low traffic phase completed in %v", lowTrafficDuration)

	// Phase 2: Traffic surge (500 ops as fast as possible)
	t.Log("\n--- Phase 2: Traffic Surge (500 ops burst) ---")
	surgeStart := time.Now()

	var wg sync.WaitGroup
	numClients := 50
	opsPerClient := 10

	var successCount, errorCount atomic.Int64

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			for j := 0; j < opsPerClient; j++ {
				key := fmt.Sprintf("/surge-test/high-%d-%d", clientID, j)
				_, err := cli.Put(ctx, key, fmt.Sprintf("value-%d-%d", clientID, j))
				if err != nil {
					errorCount.Add(1)
				} else {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	surgeDuration := time.Since(surgeStart)

	success := successCount.Load()
	errors := errorCount.Load()
	surgeThroughput := float64(success) / surgeDuration.Seconds()

	t.Logf("Traffic surge completed in %v", surgeDuration)
	t.Logf("Successful operations: %d", success)
	t.Logf("Failed operations: %d", errors)
	t.Logf("Surge throughput: %.2f ops/sec", surgeThroughput)

	// Phase 3: Return to low traffic
	t.Log("\n--- Phase 3: Return to Low Traffic (5 ops/sec) ---")
	recoveryStart := time.Now()
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("/surge-test/recovery-%d", i)
		_, err := cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		if err != nil {
			t.Logf("Put failed: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	recoveryDuration := time.Since(recoveryStart)
	t.Logf("Recovery phase completed in %v", recoveryDuration)

	// Success criteria: system should handle surge without too many errors
	if errors > int64(numClients*opsPerClient)/10 {
		t.Errorf("Too many errors during traffic surge: %d (> 10%%)", errors)
	}

	t.Log("\n=== Traffic Surge Test Summary ===")
	t.Logf("✓ System handled traffic surge from ~5 ops/sec to %.2f ops/sec", surgeThroughput)
	t.Logf("✓ Success rate during surge: %.2f%%", float64(success)/float64(success+errors)*100)
}

// TestBatchProposal_MemoryVsRocksDB compares batch performance between Memory and RocksDB
func TestBatchProposal_MemoryVsRocksDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory vs rocksdb comparison test in short mode")
	}

	t.Log("=== Testing Batch Proposal: Memory vs RocksDB Comparison ===")

	// Test Memory backend with batch
	t.Log("\n--- Test 1: Memory Backend (Batch Enabled) ---")
	memoryResult := runLoadTestMemory(t, "memory-batch", 20, 500, true)

	// Test RocksDB backend with batch
	t.Log("\n--- Test 2: RocksDB Backend (Batch Enabled) ---")
	rocksdbResult := runLoadTestRocksDB(t, "rocksdb-batch", 20, 500, true)

	// Report comparison
	t.Log("\n=== Memory vs RocksDB Performance Comparison ===")
	t.Logf("Memory Backend:")
	t.Logf("  Throughput: %.2f ops/sec", memoryResult.throughput)
	t.Logf("  Avg Latency: %v", memoryResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", memoryResult.successRate)

	t.Logf("\nRocksDB Backend:")
	t.Logf("  Throughput: %.2f ops/sec", rocksdbResult.throughput)
	t.Logf("  Avg Latency: %v", rocksdbResult.avgLatency)
	t.Logf("  Success Rate: %.2f%%", rocksdbResult.successRate)

	t.Logf("\nComparison:")
	t.Logf("  Memory vs RocksDB Throughput Ratio: %.2fx", memoryResult.throughput/rocksdbResult.throughput)
	t.Logf("  Memory vs RocksDB Latency Ratio: %.2fx",
		float64(rocksdbResult.avgLatency)/float64(memoryResult.avgLatency))

	// Both backends should benefit from batching
	if memoryResult.successRate < 95.0 {
		t.Errorf("Memory backend success rate too low: %.2f%%", memoryResult.successRate)
	}
	if rocksdbResult.successRate < 95.0 {
		t.Errorf("RocksDB backend success rate too low: %.2f%%", rocksdbResult.successRate)
	}
}

// loadTestResult holds the results of a load test
type loadTestResult struct {
	throughput  float64
	avgLatency  time.Duration
	successRate float64
	totalOps    int64
	successOps  int64
	errorOps    int64
}

// runLoadTest runs a load test with specified parameters
func runLoadTest(t *testing.T, testName string, numClients, opsPerClient int, batchEnabled bool) loadTestResult {
	// Choose appropriate test helper based on batchEnabled
	var node *testNode
	var cleanup func()

	if batchEnabled {
		node, cleanup = startMemoryNode(t, 1)
	} else {
		node, cleanup = startMemoryNode(t, 1, WithoutBatchProposal())
	}
	defer cleanup()

	return executeLoadTest(t, node.clientAddr, testName, numClients, opsPerClient)
}

// runLoadTestMemory runs a load test on Memory backend
func runLoadTestMemory(t *testing.T, testName string, numClients, opsPerClient int, batchEnabled bool) loadTestResult {
	var node *testNode
	var cleanup func()

	if batchEnabled {
		node, cleanup = startMemoryNode(t, 1)
	} else {
		node, cleanup = startMemoryNode(t, 1, WithoutBatchProposal())
	}
	defer cleanup()

	return executeLoadTest(t, node.clientAddr, testName, numClients, opsPerClient)
}

// runLoadTestRocksDB runs a load test on RocksDB backend
func runLoadTestRocksDB(t *testing.T, testName string, numClients, opsPerClient int, batchEnabled bool) loadTestResult {
	var cleanup func()
	var endpoint string

	if batchEnabled {
		rocksNode, rocksCleanup := startRocksDBNode(t, 1)
		endpoint = rocksNode.clientAddr
		cleanup = rocksCleanup
	} else {
		rocksNode, rocksCleanup := startRocksDBNode(t, 1, WithoutBatchProposal())
		endpoint = rocksNode.clientAddr
		cleanup = rocksCleanup
	}
	defer cleanup()

	return executeLoadTest(t, endpoint, testName, numClients, opsPerClient)
}

// executeLoadTest executes the actual load test
func executeLoadTest(t *testing.T, endpoint, testName string, numClients, opsPerClient int) loadTestResult {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	totalOperations := numClients * opsPerClient
	t.Logf("Starting test '%s': %d clients × %d ops = %d total ops",
		testName, numClients, opsPerClient, totalOperations)

	var (
		successCount int64
		errorCount   int64
		totalLatency int64
	)

	startTime := time.Now()
	var wg sync.WaitGroup

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			ctx := context.Background()
			for j := 0; j < opsPerClient; j++ {
				key := fmt.Sprintf("/%s/client-%d/key-%d", testName, clientID, j)
				value := fmt.Sprintf("value-%d-%d", clientID, j)

				opStart := time.Now()
				_, err := cli.Put(ctx, key, value)
				latency := time.Since(opStart)

				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				atomic.AddInt64(&totalLatency, int64(latency))
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Calculate metrics
	successOps := atomic.LoadInt64(&successCount)
	errorOps := atomic.LoadInt64(&errorCount)

	var avgLatency time.Duration
	if successOps > 0 {
		avgLatency = time.Duration(atomic.LoadInt64(&totalLatency) / successOps)
	}

	throughput := float64(successOps) / duration.Seconds()
	successRate := float64(successOps) / float64(totalOperations) * 100

	t.Logf("Test '%s' completed in %v", testName, duration)
	t.Logf("  Total operations: %d", totalOperations)
	t.Logf("  Successful: %d (%.2f%%)", successOps, successRate)
	t.Logf("  Failed: %d", errorOps)
	t.Logf("  Throughput: %.2f ops/sec", throughput)
	t.Logf("  Avg Latency: %v", avgLatency)

	return loadTestResult{
		throughput:  throughput,
		avgLatency:  avgLatency,
		successRate: successRate,
		totalOps:    int64(totalOperations),
		successOps:  successOps,
		errorOps:    errorOps,
	}
}
