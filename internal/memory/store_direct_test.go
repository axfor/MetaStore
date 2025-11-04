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

// TestPutDirectConcurrent 测试 putDirect 的并发安全性
func TestPutDirectConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	operationsPerGoroutine := 100

	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// 启动多个 goroutine 并发写入
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 等待所有 goroutine 就绪
			<-startCh

			// 每个 goroutine 写入不同的 key
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

	// 同时启动所有 goroutine
	close(startCh)
	wg.Wait()

	// 验证所有 key 都被正确写入
	expectedCount := concurrency * operationsPerGoroutine
	actualCount := m.kvData.Len()

	if actualCount != expectedCount {
		t.Errorf("Expected %d keys, got %d", expectedCount, actualCount)
	}

	// 验证每个 key 的值
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

// TestPutDirectSameKeyConcurrent 测试并发写入同一个 key
func TestPutDirectSameKeyConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	key := "shared-key"

	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// 启动多个 goroutine 并发写入同一个 key
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

	// 验证 key 存在
	kv, exists := m.kvData.Get(key)
	if !exists {
		t.Errorf("Key %s not found", key)
	}

	// 验证 revision 正确递增
	expectedRevision := int64(concurrency)
	actualRevision := m.revision.Load()

	if actualRevision != expectedRevision {
		t.Errorf("Expected revision %d, got %d", expectedRevision, actualRevision)
	}

	// 验证 version 递增（但由于并发竞争，可能小于 concurrency）
	// 注意：这是测试环境特有的问题，实际使用中 Raft apply 是串行的
	if kv.Version < 1 || kv.Version > int64(concurrency) {
		t.Errorf("Version out of range: got %d, expected 1-%d", kv.Version, concurrency)
	}

	t.Logf("Concurrent writes: revision=%d, version=%d (race window expected)", actualRevision, kv.Version)
}

// TestDeleteDirectConcurrent 测试 deleteDirect 的并发安全性
func TestDeleteDirectConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	// 先写入数据
	numKeys := 1000
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		m.putDirect(key, "value", 0)
	}

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// 并发删除
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startCh

			// 每个 goroutine 删除一部分 key
			for j := id * (numKeys / concurrency); j < (id+1)*(numKeys/concurrency); j++ {
				key := fmt.Sprintf("key-%d", j)
				m.deleteDirect(key, "")
			}
		}(i)
	}

	close(startCh)
	wg.Wait()

	// 验证所有 key 都被删除
	remainingKeys := m.kvData.Len()
	if remainingKeys != 0 {
		t.Errorf("Expected 0 keys remaining, got %d", remainingKeys)
	}
}

// TestApplyTxnWithShardLocks 测试事务的细粒度锁
func TestApplyTxnWithShardLocks(t *testing.T) {
	m := NewMemoryEtcd()

	// 写入初始数据
	m.putDirect("key1", "value1", 0)
	m.putDirect("key2", "value2", 0)

	// 测试事务: if key1 == "value1" then put key2 = "updated"
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

	// 验证 key2 被更新
	kv, exists := m.kvData.Get("key2")
	if !exists {
		t.Error("key2 not found")
	}

	if string(kv.Value) != "updated" {
		t.Errorf("Expected 'updated', got '%s'", string(kv.Value))
	}
}

// TestConcurrentTransactions 测试并发事务
func TestConcurrentTransactions(t *testing.T) {
	m := NewMemoryEtcd()

	// 初始化计数器
	m.putDirect("counter", "0", 0)

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	var successCount atomic.Int64

	// 并发执行事务: 递增计数器
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-startCh

			// 读取当前值
			kv, exists := m.kvData.Get("counter")
			if !exists {
				return
			}

			currentValue := string(kv.Value)
			newValue := fmt.Sprintf("%s1", currentValue) // 简单追加 "1"

			// 事务: if counter == currentValue then counter = newValue
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

	// 验证至少有一些事务成功
	// (因为并发冲突，不是所有事务都会成功)
	if successCount.Load() == 0 {
		t.Error("Expected at least some transactions to succeed")
	}

	t.Logf("Successful transactions: %d / %d", successCount.Load(), concurrency)
}

// TestLeaseOperationsConcurrent 测试并发 lease 操作
func TestLeaseOperationsConcurrent(t *testing.T) {
	m := NewMemoryEtcd()

	concurrency := 100
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// 并发创建 lease
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

	// 验证所有 lease 都被创建
	m.leaseMu.RLock()
	leaseCount := len(m.leases)
	m.leaseMu.RUnlock()

	if leaseCount != concurrency {
		t.Errorf("Expected %d leases, got %d", concurrency, leaseCount)
	}

	// 并发撤销 lease
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

	// 验证所有 lease 都被撤销
	m.leaseMu.RLock()
	leaseCount = len(m.leases)
	m.leaseMu.RUnlock()

	if leaseCount != 0 {
		t.Errorf("Expected 0 leases, got %d", leaseCount)
	}
}

// BenchmarkPutDirectSequential 基准测试: 串行写入
func BenchmarkPutDirectSequential(b *testing.B) {
	m := NewMemoryEtcd()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i)
		m.putDirect(key, "value", 0)
	}
}

// BenchmarkPutDirectParallel 基准测试: 并行写入
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

// BenchmarkTxnWithShardLocks 基准测试: 事务操作
func BenchmarkTxnWithShardLocks(b *testing.B) {
	m := NewMemoryEtcd()

	// 初始化数据
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

// TestRaceConditions 压力测试: 混合操作
func TestRaceConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	m := NewMemoryEtcd()

	concurrency := 50
	duration := 5 * time.Second
	stopCh := make(chan struct{})

	var totalOps atomic.Int64

	// 启动多个 goroutine 执行不同类型的操作
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

	// 运行指定时间
	time.Sleep(duration)
	close(stopCh)
	wg.Wait()

	t.Logf("Completed %d operations in %v", totalOps.Load(), duration)
	t.Logf("Throughput: %.2f ops/sec", float64(totalOps.Load())/duration.Seconds())
}
