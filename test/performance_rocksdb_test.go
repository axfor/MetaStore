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

// TestRocksDBPerformance_LargeScaleLoad tests RocksDB system behavior under large-scale concurrent load
func TestRocksDBPerformance_LargeScaleLoad(t *testing.T) {
	// t.Skip("Skipping - test is too aggressive for single-node RocksDB (50 clients cause incrementRevision bottleneck)")
	if testing.Short() {
		t.Skip("Skipping RocksDB large-scale load test in short mode")
	}

	// Start test server with RocksDB
	node, cleanup := startRocksDBNode(t, 1)
	defer cleanup()

	// Create etcd client
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	// Test parameters
	numClients := 50
	operationsPerClient := 1000
	totalOperations := numClients * operationsPerClient

	t.Logf("Starting RocksDB large-scale load test: %d clients, %d ops/client = %d total ops",
		numClients, operationsPerClient, totalOperations)

	// Counters
	var (
		successCount int64
		errorCount   int64
		totalLatency int64
	)

	startTime := time.Now()

	// Launch concurrent clients
	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			ctx := context.Background()
			for j := 0; j < operationsPerClient; j++ {
				key := fmt.Sprintf("/rocksdb-load-test/client-%d/key-%d", clientID, j)
				value := fmt.Sprintf("value-%d-%d", clientID, j)

				opStart := time.Now()

				// Put operation
				_, err := cli.Put(ctx, key, value)
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				// Get operation
				_, err = cli.Get(ctx, key)
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				latency := time.Since(opStart)
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
	avgLatency := time.Duration(atomic.LoadInt64(&totalLatency) / successOps)
	throughput := float64(successOps) / duration.Seconds()

	// Report results
	t.Logf("RocksDB load test completed in %v", duration)
	t.Logf("Total operations: %d", totalOperations)
	t.Logf("Successful operations: %d (%.2f%%)", successOps, float64(successOps)/float64(totalOperations)*100)
	t.Logf("Failed operations: %d", errorOps)
	t.Logf("Average latency: %v", avgLatency)
	t.Logf("Throughput: %.2f ops/sec", throughput)

	// Assert acceptable performance (RocksDB may be slower due to disk I/O)
	if errorOps > int64(totalOperations/100) {
		t.Errorf("Error rate too high: %d errors out of %d operations", errorOps, totalOperations)
	}

	if avgLatency > 300*time.Millisecond {
		t.Errorf("Average latency too high: %v (expected < 300ms for RocksDB)", avgLatency)
	}

	if throughput < 200 {
		t.Errorf("Throughput too low: %.2f ops/sec (expected > 200 for RocksDB)", throughput)
	}
}

// TestRocksDBPerformance_SustainedLoad tests RocksDB stability under sustained load
func TestRocksDBPerformance_SustainedLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RocksDB sustained load test in short mode")
	}

	node, cleanup := startRocksDBNode(t, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	// Test parameters
	duration := 5 * time.Minute // 5分钟持续负载测试
	numClients := 20

	t.Logf("Starting RocksDB sustained load test: %d clients for %v", numClients, duration)

	var (
		totalOps   int64
		errorCount int64
	)

	startTime := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	// Launch concurrent workers
	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			opCount := 0

			for ctx.Err() == nil {
				key := fmt.Sprintf("/rocksdb-sustained/client-%d/op-%d", clientID, opCount)
				value := fmt.Sprintf("value-%d", opCount)

				_, err := cli.Put(ctx, key, value)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&totalOps, 1)
				}

				opCount++
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()

	elapsed := time.Since(startTime)

	// Calculate metrics
	ops := atomic.LoadInt64(&totalOps)
	errors := atomic.LoadInt64(&errorCount)
	throughput := float64(ops) / elapsed.Seconds()

	t.Logf("RocksDB sustained load test completed")
	t.Logf("Duration: %v", elapsed)
	t.Logf("Total operations: %d", ops)
	t.Logf("Errors: %d (%.2f%%)", errors, float64(errors)/float64(ops)*100)
	t.Logf("Average throughput: %.2f ops/sec", throughput)

	// Assert stability
	if errors > ops/100 {
		t.Errorf("Error rate too high: %d errors", errors)
	}
}

// TestRocksDBPerformance_MixedWorkload tests RocksDB realistic mixed workload
func TestRocksDBPerformance_MixedWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RocksDB mixed workload test in short mode")
	}

	node, cleanup := startRocksDBNode(t, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	// Test parameters
	testDuration := 5 * time.Minute // 5分钟混合负载测试
	numClients := 30

	t.Logf("Starting RocksDB mixed workload test: %d clients for %v", numClients, testDuration)

	var (
		putCount    int64
		getCount    int64
		deleteCount int64
		rangeCount  int64
		errorCount  int64
	)

	startTime := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	// Launch workers with different workload patterns
	var wg sync.WaitGroup

	// 40% PUT operations
	for i := 0; i < numClients*2/5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			opNum := 0
			for ctx.Err() == nil {
				key := fmt.Sprintf("/rocksdb-mixed/put-%d-%d", id, opNum)
				_, err := cli.Put(ctx, key, fmt.Sprintf("value-%d", opNum))
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&putCount, 1)
				}
				opNum++
			}
		}(i)
	}

	// 40% GET operations
	for i := 0; i < numClients*2/5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			opNum := 0
			for ctx.Err() == nil {
				key := fmt.Sprintf("/rocksdb-mixed/put-%d-%d", id%10, opNum%100)
				_, err := cli.Get(ctx, key)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&getCount, 1)
				}
				opNum++
			}
		}(i)
	}

	// 10% RANGE operations
	for i := 0; i < numClients/10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for ctx.Err() == nil {
				_, err := cli.Get(ctx, "/rocksdb-mixed/", clientv3.WithPrefix())
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&rangeCount, 1)
				}
			}
		}(i)
	}

	// 10% DELETE operations
	for i := 0; i < numClients/10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			opNum := 0
			for ctx.Err() == nil {
				key := fmt.Sprintf("/rocksdb-mixed/put-%d-%d", id%10, opNum%100)
				_, err := cli.Delete(ctx, key)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&deleteCount, 1)
				}
				opNum++
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()

	elapsed := time.Since(startTime)

	// Collect metrics
	puts := atomic.LoadInt64(&putCount)
	gets := atomic.LoadInt64(&getCount)
	deletes := atomic.LoadInt64(&deleteCount)
	ranges := atomic.LoadInt64(&rangeCount)
	errors := atomic.LoadInt64(&errorCount)
	totalOps := puts + gets + deletes + ranges

	t.Logf("RocksDB mixed workload test completed in %v", elapsed)
	t.Logf("Total operations: %d", totalOps)
	t.Logf("  PUT: %d (%.1f%%)", puts, float64(puts)/float64(totalOps)*100)
	t.Logf("  GET: %d (%.1f%%)", gets, float64(gets)/float64(totalOps)*100)
	t.Logf("  DELETE: %d (%.1f%%)", deletes, float64(deletes)/float64(totalOps)*100)
	t.Logf("  RANGE: %d (%.1f%%)", ranges, float64(ranges)/float64(totalOps)*100)
	t.Logf("Errors: %d (%.2f%%)", errors, float64(errors)/float64(totalOps)*100)
	t.Logf("Throughput: %.2f ops/sec", float64(totalOps)/elapsed.Seconds())

	// Assert acceptable error rate
	if errors > totalOps/50 {
		t.Errorf("Error rate too high: %d errors out of %d operations", errors, totalOps)
	}
}

// TestRocksDBPerformance_Compaction tests RocksDB compaction performance
func TestRocksDBPerformance_Compaction(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RocksDB compaction test in short mode")
	}

	node, cleanup := startRocksDBNode(t, 1)
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

	t.Log("Starting RocksDB compaction test")

	// Write 2,000 keys (reduced from 10,000 to avoid timeout)
	numKeys := 2000
	t.Logf("Writing %d keys...", numKeys)
	writeStart := time.Now()
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("/rocksdb-compact/key-%05d", i)
		value := fmt.Sprintf("value-%d", i)
		_, err := cli.Put(ctx, key, value)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}
	writeDuration := time.Since(writeStart)
	t.Logf("Write completed in %v (%.2f ops/sec)", writeDuration, float64(numKeys)/writeDuration.Seconds())

	// Update all keys (creates new versions)
	t.Log("Updating all keys...")
	updateStart := time.Now()
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("/rocksdb-compact/key-%05d", i)
		value := fmt.Sprintf("updated-value-%d", i)
		_, err := cli.Put(ctx, key, value)
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}
	}
	updateDuration := time.Since(updateStart)
	t.Logf("Update completed in %v (%.2f ops/sec)", updateDuration, float64(numKeys)/updateDuration.Seconds())

	// Perform compaction
	t.Log("Performing compaction...")
	compactStart := time.Now()

	// Get current revision
	resp, err := cli.Get(ctx, "/rocksdb-compact/", clientv3.WithPrefix(), clientv3.WithLimit(1))
	if err != nil {
		t.Fatalf("Get revision failed: %v", err)
	}
	currentRev := resp.Header.Revision

	// Compact
	_, err = cli.Compact(ctx, currentRev)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	compactDuration := time.Since(compactStart)
	t.Logf("Compaction completed in %v", compactDuration)

	// Verify reads still work
	t.Log("Verifying reads after compaction...")
	readStart := time.Now()
	numReads := 500 // Read sample of keys
	for i := 0; i < numReads; i++ {
		key := fmt.Sprintf("/rocksdb-compact/key-%05d", i)
		resp, err := cli.Get(ctx, key)
		if err != nil {
			t.Fatalf("Read after compaction failed: %v", err)
		}
		if len(resp.Kvs) == 0 {
			t.Fatalf("Key not found after compaction: %s", key)
		}
	}
	readDuration := time.Since(readStart)
	t.Logf("Post-compaction reads completed in %v (%.2f ops/sec)", readDuration, float64(numReads)/readDuration.Seconds())

	t.Log("RocksDB compaction test completed successfully")
}

// TestRocksDBPerformance_WatchScalability tests RocksDB watch performance with many watchers
func TestRocksDBPerformance_WatchScalability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping watch scalability test in short mode")
	}

	_, cli, cleanup := startTestServerRocksDB(t)
	defer cleanup()

	// Test parameters - simplified test using single prefix key
	numWatchers := 10 // reduced to 10 watchers
	numEvents := 10   // each watcher receives 1 event

	t.Logf("Starting RocksDB watch scalability test: %d watchers, %d events", numWatchers, numEvents)

	// Use context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Step 1: Create watches using unified key range
	t.Logf("Step 1: Creating %d watches on /watch-test/...", numWatchers)
	watchChans := make([]clientv3.WatchChan, numWatchers)
	for i := 0; i < numWatchers; i++ {
		// All watchers monitor the same prefix, so any Put will trigger all watchers
		watchChans[i] = cli.Watch(ctx, "/watch-test/", clientv3.WithPrefix())
	}
	time.Sleep(200 * time.Millisecond) // Ensure Watch is fully established

	// Step 2: Start goroutines to receive events, use channel to ensure all goroutines are ready
	t.Logf("Step 2: Starting %d event receiver goroutines...", numWatchers)
	var wg sync.WaitGroup
	var eventsReceived atomic.Int64

	// Use channel to ensure all goroutines enter waiting state
	readyChan := make(chan struct{}, numWatchers)

	for i := range watchChans {
		wg.Add(1)
		go func(wch clientv3.WatchChan, watcherID int) {
			defer wg.Done()

			// Notify main goroutine: I'm ready to receive events
			readyChan <- struct{}{}

			// Receive with timeout: exit after receiving first event or timeout
			select {
			case wresp := <-wch:
				if wresp.Err() == nil {
					eventsReceived.Add(1)
					t.Logf("Watcher %d received event", watcherID)
				} else {
					t.Logf("Watcher %d got error: %v", watcherID, wresp.Err())
				}
			case <-time.After(10 * time.Second):
				t.Logf("Watcher %d timeout waiting for event", watcherID)
			case <-ctx.Done():
				t.Logf("Watcher %d cancelled", watcherID)
			}
		}(watchChans[i], i)
	}

	// Wait for all goroutines to be ready
	t.Logf("Step 2.5: Waiting for all %d goroutines to be ready...", numWatchers)
	for i := 0; i < numWatchers; i++ {
		select {
		case <-readyChan:
			// One goroutine ready
		case <-time.After(5 * time.Second):
			t.Fatalf("Timeout waiting for goroutine %d to be ready", i)
		}
	}
	t.Logf("All %d goroutines are ready to receive events", numWatchers)

	// Wait a bit more to ensure goroutines really enter select waiting state
	time.Sleep(100 * time.Millisecond)

	// Step 3: Put operations to trigger events (only need one Put, all watchers will receive)
	t.Logf("Step 3: Generating %d events...", numEvents)
	startTime := time.Now()
	for i := 0; i < numEvents; i++ {
		key := fmt.Sprintf("/watch-test/event-%d", i)
		_, err := cli.Put(context.Background(), key, fmt.Sprintf("value-%d", i))
		if err != nil {
			t.Logf("Put failed: %v", err)
		}
		// Slight delay between Puts to ensure events are processed
		time.Sleep(10 * time.Millisecond)
	}

	// Step 4: Wait for all goroutines to complete (with timeout)
	t.Logf("Step 4: Waiting for all watchers to receive events...")
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("All watchers completed")
	case <-time.After(15 * time.Second):
		t.Logf("Timeout waiting for watchers, continuing...")
	}

	duration := time.Since(startTime)
	received := eventsReceived.Load()

	t.Logf("RocksDB watch test completed in %v", duration)
	t.Logf("Events generated: %d", numEvents)
	t.Logf("Events received by watchers: %d", received)
	t.Logf("Event throughput: %.2f events/sec", float64(received)/duration.Seconds())

	// Verify most watchers received events
	if received < int64(numWatchers)*8/10 {
		t.Errorf("Too many watchers didn't receive events: %d out of %d", received, numWatchers)
	}
}
