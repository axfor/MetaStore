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

package memory

import (
	"fmt"
	"metaStore/internal/kvstore"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPutDirectConcurrent test putDirect concurrencysafe
func TestPutDirectConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	operationsPerGoroutine := 100

	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// startmany  goroutine concurrencywrite
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// wait all goroutine ready
			<-startCh

			// each goroutine writedifferent key
			for j := 0; j < operationsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				value := fmt.Sprintf("value-%d-%d", id, j)

				_, _, err := m.putDirect(key, value, 0)
				if err != nil {
					t.Errorf("putDirect failed: %v", err)
				}
			}
		}(i)
	}

	// simultaneously start all goroutines
	close(startCh)
	wg.Wait()

	// verify all key all becorrectwrite
	expectedCount := concurrency * operationsPerGoroutine
	actualCount := m.kvData.Len()

	if actualCount != expectedCount {
		t.Errorf("Expected %d keys, got %d", expectedCount, actualCount)
	}

	// verify each key value
	for i := 0; i < concurrency; i++ {
		for j := 0; j < operationsPerGoroutine; j++ {
			key := fmt.Sprintf("key-%d-%d", i, j)
			expectedValue := fmt.Sprintf("value-%d-%d", i, j)

			kv, exists := m.kvData.Get(key)
			if !exists {
				t.Errorf("Key %s not found", key)
				continue
			}

			if string(kv.Value) != expectedValue {
				t.Errorf("Key %s: expected %s, got %s", key, expectedValue, string(kv.Value))
			}
		}
	}
}

// TestPutDirectSameKeyConcurrent testconcurrencywritesame key
func TestPutDirectSameKeyConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	key := "shared-key"

	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// startmany  goroutine concurrencywritesame key
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startCh

			value := fmt.Sprintf("value-%d", id)
			m.putDirect(key, value, 0)
		}(i)
	}

	close(startCh)
	wg.Wait()

	// verify key in
	kv, exists := m.kvData.Get(key)
	if !exists {
		t.Errorf("Key %s not found", key)
	}

	// verify revision correctincrease
	expectedRevision := int64(concurrency)
	actualRevision := m.getRevision()

	if actualRevision != expectedRevision {
		t.Errorf("Expected revision %d, got %d", expectedRevision, actualRevision)
	}

	// verify version increase(becauseconcurrencycompete，mayless than concurrency)
	// note：istestenvironmenthave，usein Raft apply isserial
	if kv.Version < 1 || kv.Version > int64(concurrency) {
		t.Errorf("Version out of range: got %d, expected 1-%d", kv.Version, concurrency)
	}

	t.Logf("Concurrent writes: revision=%d, version=%d (race window expected)", actualRevision, kv.Version)
}

// TestDeleteDirectConcurrent test deleteDirect concurrencysafe
func TestDeleteDirectConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	// writedata
	numKeys := 1000
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		m.putDirect(key, "value", 0)
	}

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// concurrencydelete
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startCh

			// each goroutine deletefirstpartially key
			for j := id * (numKeys / concurrency); j < (id+1)*(numKeys/concurrency); j++ {
				key := fmt.Sprintf("key-%d", j)
				m.deleteDirect(key, "")
			}
		}(i)
	}

	close(startCh)
	wg.Wait()

	// verify all key all bedelete
	remainingKeys := m.kvData.Len()
	if remainingKeys != 0 {
		t.Errorf("Expected 0 keys remaining, got %d", remainingKeys)
	}
}

// TestApplyTxnWithShardLocks testtransactionfine-grainedlock
func TestApplyTxnWithShardLocks(t *testing.T) {
	m := NewMemoryEtcd()

	// writeinitialdata
	m.putDirect("key1", "value1", 0)
	m.putDirect("key2", "value2", 0)

	// testtransaction: if key1 == "value1" then put key2 = "updated"
	compares := []kvstore.Compare{
		{
			Key:    []byte("key1"),
			Target: kvstore.CompareValue,
			Result: kvstore.CompareEqual,
			TargetUnion: kvstore.CompareUnion{
				Value: []byte("value1"),
			},
		},
	}

	thenOps := []kvstore.Op{
		{
			Type:  kvstore.OpPut,
			Key:   []byte("key2"),
			Value: []byte("updated"),
		},
	}

	elseOps := []kvstore.Op{}

	resp, err := m.applyTxnWithShardLocks(compares, thenOps, elseOps)
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}

	if !resp.Succeeded {
		t.Error("Transaction should have succeeded")
	}

	// verify key2 beupdate
	kv, exists := m.kvData.Get("key2")
	if !exists {
		t.Error("key2 not found")
	}

	if string(kv.Value) != "updated" {
		t.Errorf("Expected 'updated', got '%s'", string(kv.Value))
	}
}

// TestConcurrentTransactions testconcurrencytransaction
func TestConcurrentTransactions(t *testing.T) {
	m := NewMemoryEtcd()

	// initializecount
	m.putDirect("counter", "0", 0)

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	var successCount atomic.Int64

	// concurrencyexecutetransaction: increasecount
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-startCh

			// readcurrentvalue
			kv, exists := m.kvData.Get("counter")
			if !exists {
				return
			}

			currentValue := string(kv.Value)
			newValue := fmt.Sprintf("%s1", currentValue) // single "1"

			// transaction: if counter == currentValue then counter = newValue
			compares := []kvstore.Compare{
				{
					Key:    []byte("counter"),
					Target: kvstore.CompareValue,
					Result: kvstore.CompareEqual,
					TargetUnion: kvstore.CompareUnion{
						Value: []byte(currentValue),
					},
				},
			}

			thenOps := []kvstore.Op{
				{
					Type:  kvstore.OpPut,
					Key:   []byte("counter"),
					Value: []byte(newValue),
				},
			}

			resp, err := m.applyTxnWithShardLocks(compares, thenOps, []kvstore.Op{})
			if err == nil && resp.Succeeded {
				successCount.Add(1)
			}
		}()
	}

	close(startCh)
	wg.Wait()

	// verifytofewhavesometransactionsuccess
	// (asconcurrency，notisalltransactionallwillsuccess)
	if successCount.Load() == 0 {
		t.Error("Expected at least some transactions to succeed")
	}

	t.Logf("Successful transactions: %d / %d", successCount.Load(), concurrency)
}

// TestLeaseOperationsConcurrent testconcurrency lease operation
func TestLeaseOperationsConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// concurrencycreate lease
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startCh

			leaseID := int64(id)
			m.applyLeaseOperationDirect("LEASE_GRANT", leaseID, 60)
		}(i)
	}

	close(startCh)
	wg.Wait()

	// verify all lease all becreate
	m.leaseMu.RLock()
	leaseCount := len(m.leases)
	m.leaseMu.RUnlock()

	if leaseCount != concurrency {
		t.Errorf("Expected %d leases, got %d", concurrency, leaseCount)
	}

	// concurrencyrevoked lease
	startCh2 := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startCh2

			leaseID := int64(id)
			m.applyLeaseOperationDirect("LEASE_REVOKE", leaseID, 0)
		}(i)
	}

	close(startCh2)
	wg.Wait()

	// verify all lease all berevoked
	m.leaseMu.RLock()
	leaseCount = len(m.leases)
	m.leaseMu.RUnlock()

	if leaseCount != 0 {
		t.Errorf("Expected 0 leases, got %d", leaseCount)
	}
}

// BenchmarkPutDirectSequential prepare test: serialwrite
func BenchmarkPutDirectSequential(b *testing.B) {
	m := NewMemoryEtcd()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i)
		m.putDirect(key, "value", 0)
	}
}

// BenchmarkPutDirectParallel prepare test: parallelismwrite
func BenchmarkPutDirectParallel(b *testing.B) {
	m := NewMemoryEtcd()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("key-%d", i)
			m.putDirect(key, "value", 0)
			i++
		}
	})
}

// BenchmarkTxnWithShardLocks prepare test: transactionoperation
func BenchmarkTxnWithShardLocks(b *testing.B) {
	m := NewMemoryEtcd()

	// initializedata
	m.putDirect("key1", "value1", 0)

	compares := []kvstore.Compare{
		{
			Key:    []byte("key1"),
			Target: kvstore.CompareValue,
			Result: kvstore.CompareEqual,
			TargetUnion: kvstore.CompareUnion{
				Value: []byte("value1"),
			},
		},
	}

	thenOps := []kvstore.Op{
		{
			Type:  kvstore.OpPut,
			Key:   []byte("key2"),
			Value: []byte("value2"),
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.applyTxnWithShardLocks(compares, thenOps, []kvstore.Op{})
	}
}

// TestRaceConditions test: mergeoperation
func TestRaceConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	m := NewMemoryEtcd()

	concurrency := 50
	duration := 5 * time.Second
	stopCh := make(chan struct{})

	var totalOps atomic.Int64

	// startmany  goroutine executedifferenttypeoperation
	var wg sync.WaitGroup

	// Put operations
	for i := 0; i < concurrency/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for {
				select {
				case <-stopCh:
					return
				default:
					key := fmt.Sprintf("key-%d", id%1000)
					value := fmt.Sprintf("value-%d", time.Now().UnixNano())
					m.putDirect(key, value, 0)
					totalOps.Add(1)
				}
			}
		}(i)
	}

	// Delete operations
	for i := 0; i < concurrency/4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for {
				select {
				case <-stopCh:
					return
				default:
					key := fmt.Sprintf("key-%d", id%1000)
					m.deleteDirect(key, "")
					totalOps.Add(1)
				}
			}
		}(i)
	}

	// Get operations
	for i := 0; i < concurrency/4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for {
				select {
				case <-stopCh:
					return
				default:
					key := fmt.Sprintf("key-%d", id%1000)
					m.kvData.Get(key)
					totalOps.Add(1)
				}
			}
		}(i)
	}

	// runningschedule
	time.Sleep(duration)
	close(stopCh)
	wg.Wait()

	t.Logf("Completed %d operations in %v", totalOps.Load(), duration)
	t.Logf("Throughput: %.2f ops/sec", float64(totalOps.Load())/duration.Seconds())
}
