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
	"testing"
	"time"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
)

// TestBatchApplyPut test批量 PUT correct性
func TestBatchApplyPut(t *testing.T) {
	// createtest存储
	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())

	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// create批量operation
	ops := make([]RaftOperation, 100)
	for i := 0; i < 100; i++ {
		ops[i] = RaftOperation{
			Type:   "PUT",
			Key:    fmt.Sprintf("key-%d", i),
			Value:  fmt.Sprintf("value-%d", i),
			SeqNum: fmt.Sprintf("seq-%d", i),
		}
	}

	// 批量应用
	m.applyBatch(ops)

	// verifyallkey都被write
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key-%d", i)
		kv, exists := m.MemoryEtcd.kvData.Get(key)
		if !exists {
			t.Errorf("Key %s not found", key)
			continue
		}

		expectedValue := fmt.Sprintf("value-%d", i)
		if string(kv.Value) != expectedValue {
			t.Errorf("Key %s: expected %s, got %s", key, expectedValue, string(kv.Value))
		}
	}

	// verify revision correct
	expectedRevision := int64(100)
	actualRevision := m.MemoryEtcd.revision.Load()
	if actualRevision != expectedRevision {
		t.Errorf("Expected revision %d, got %d", expectedRevision, actualRevision)
	}
}

// TestBatchApplyDelete test批量 DELETE correct性
func TestBatchApplyDelete(t *testing.T) {
	// createtest存储
	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())

	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// 先writedata
	putOps := make([]RaftOperation, 100)
	for i := 0; i < 100; i++ {
		putOps[i] = RaftOperation{
			Type:   "PUT",
			Key:    fmt.Sprintf("key-%d", i),
			Value:  "value",
			SeqNum: fmt.Sprintf("put-seq-%d", i),
		}
	}
	m.applyBatch(putOps)

	// 批量delete
	deleteOps := make([]RaftOperation, 100)
	for i := 0; i < 100; i++ {
		deleteOps[i] = RaftOperation{
			Type:   "DELETE",
			Key:    fmt.Sprintf("key-%d", i),
			SeqNum: fmt.Sprintf("del-seq-%d", i),
		}
	}
	m.applyBatch(deleteOps)

	// verifyallkey都被delete
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, exists := m.MemoryEtcd.kvData.Get(key)
		if exists {
			t.Errorf("Key %s should be deleted", key)
		}
	}
}

// TestBatchApplyMixed test批量混合operation
func TestBatchApplyMixed(t *testing.T) {
	// createtest存储
	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())

	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// create混合operation: 50 PUT + 30 DELETE + 20 LEASE_GRANT
	ops := make([]RaftOperation, 100)

	// 50 PUT
	for i := 0; i < 50; i++ {
		ops[i] = RaftOperation{
			Type:   "PUT",
			Key:    fmt.Sprintf("key-%d", i),
			Value:  fmt.Sprintf("value-%d", i),
			SeqNum: fmt.Sprintf("put-seq-%d", i),
		}
	}

	// 先writesomedata供delete
	for i := 50; i < 80; i++ {
		m.MemoryEtcd.putDirect(fmt.Sprintf("key-%d", i), "old-value", 0)
	}

	// 30 DELETE
	for i := 50; i < 80; i++ {
		ops[i] = RaftOperation{
			Type:   "DELETE",
			Key:    fmt.Sprintf("key-%d", i),
			SeqNum: fmt.Sprintf("del-seq-%d", i),
		}
	}

	// 20 LEASE_GRANT
	for i := 80; i < 100; i++ {
		ops[i] = RaftOperation{
			Type:    "LEASE_GRANT",
			LeaseID: int64(i),
			TTL:     60,
			SeqNum:  fmt.Sprintf("lease-seq-%d", i),
		}
	}

	// 批量应用
	m.applyBatch(ops)

	// verify PUT operation
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%d", i)
		kv, exists := m.MemoryEtcd.kvData.Get(key)
		if !exists {
			t.Errorf("PUT: Key %s not found", key)
		} else if string(kv.Value) != fmt.Sprintf("value-%d", i) {
			t.Errorf("PUT: Key %s value mismatch", key)
		}
	}

	// verify DELETE operation
	for i := 50; i < 80; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, exists := m.MemoryEtcd.kvData.Get(key)
		if exists {
			t.Errorf("DELETE: Key %s should be deleted", key)
		}
	}

	// verify LEASE_GRANT operation
	for i := 80; i < 100; i++ {
		leaseID := int64(i)
		m.MemoryEtcd.leaseMu.RLock()
		_, exists := m.MemoryEtcd.leases[leaseID]
		m.MemoryEtcd.leaseMu.RUnlock()

		if !exists {
			t.Errorf("LEASE_GRANT: Lease %d not found", leaseID)
		}
	}
}

// TestBatchApplyCorrectnessVsSingle test批量and单个应用等价性
func TestBatchApplyCorrectnessVsSingle(t *testing.T) {
	// testdata
	testOps := []RaftOperation{
		{Type: "PUT", Key: "key1", Value: "value1", SeqNum: "seq1"},
		{Type: "PUT", Key: "key2", Value: "value2", SeqNum: "seq2"},
		{Type: "PUT", Key: "key3", Value: "value3", SeqNum: "seq3"},
		{Type: "DELETE", Key: "key1", SeqNum: "seq4"},
		{Type: "PUT", Key: "key1", Value: "new-value1", SeqNum: "seq5"},
	}

	// 方式 1: 单个应用
	proposeC1 := make(chan string)
	commitC1 := make(chan *kvstore.Commit)
	errorC1 := make(chan error)
	snapshotter1 := snap.New(nil, t.TempDir())
	m1 := NewMemory(snapshotter1, proposeC1, commitC1, errorC1)

	for _, op := range testOps {
		m1.applyOperation(op)
	}

	// 方式 2: 批量应用
	proposeC2 := make(chan string)
	commitC2 := make(chan *kvstore.Commit)
	errorC2 := make(chan error)
	snapshotter2 := snap.New(nil, t.TempDir())
	m2 := NewMemory(snapshotter2, proposeC2, commitC2, errorC2)

	m2.applyBatch(testOps)

	// verify两种方式result一致
	keys := []string{"key1", "key2", "key3"}
	for _, key := range keys {
		kv1, exists1 := m1.MemoryEtcd.kvData.Get(key)
		kv2, exists2 := m2.MemoryEtcd.kvData.Get(key)

		if exists1 != exists2 {
			t.Errorf("Key %s existence mismatch: single=%v, batch=%v", key, exists1, exists2)
		}

		if exists1 && exists2 {
			if string(kv1.Value) != string(kv2.Value) {
				t.Errorf("Key %s value mismatch: single=%s, batch=%s", key, kv1.Value, kv2.Value)
			}
		}
	}

	// verify revision 一致
	if m1.MemoryEtcd.revision.Load() != m2.MemoryEtcd.revision.Load() {
		t.Errorf("Revision mismatch: single=%d, batch=%d",
			m1.MemoryEtcd.revision.Load(), m2.MemoryEtcd.revision.Load())
	}
}

// BenchmarkBatchApplyVsSingle 对比批量and单个应用性能
func BenchmarkBatchApplyVsSingle(b *testing.B) {
	b.Run("Single", func(b *testing.B) {
		proposeC := make(chan string)
		commitC := make(chan *kvstore.Commit)
		errorC := make(chan error)
		snapshotter := snap.New(nil, b.TempDir())
		m := NewMemory(snapshotter, proposeC, commitC, errorC)

		// 准备operation
		ops := make([]RaftOperation, 100)
		for i := 0; i < 100; i++ {
			ops[i] = RaftOperation{
				Type:   "PUT",
				Key:    fmt.Sprintf("key-%d", i),
				Value:  "value",
				SeqNum: fmt.Sprintf("seq-%d", i),
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, op := range ops {
				m.applyOperation(op)
			}
		}
	})

	b.Run("Batch", func(b *testing.B) {
		proposeC := make(chan string)
		commitC := make(chan *kvstore.Commit)
		errorC := make(chan error)
		snapshotter := snap.New(nil, b.TempDir())
		m := NewMemory(snapshotter, proposeC, commitC, errorC)

		// 准备operation
		ops := make([]RaftOperation, 100)
		for i := 0; i < 100; i++ {
			ops[i] = RaftOperation{
				Type:   "PUT",
				Key:    fmt.Sprintf("key-%d", i),
				Value:  "value",
				SeqNum: fmt.Sprintf("seq-%d", i),
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.applyBatch(ops)
		}
	})
}

// TestBatchApplyEmptyOps testemptyoperationlist
func TestBatchApplyEmptyOps(t *testing.T) {
	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())
	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// emptyoperationlist
	m.applyBatch([]RaftOperation{})

	// verify无副作用
	if m.MemoryEtcd.revision.Load() != 0 {
		t.Errorf("Expected revision 0, got %d", m.MemoryEtcd.revision.Load())
	}
}

// TestBatchApplySingleOp test单个operationoptimizepath
func TestBatchApplySingleOp(t *testing.T) {
	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())
	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// 单个operation
	ops := []RaftOperation{
		{Type: "PUT", Key: "key1", Value: "value1", SeqNum: "seq1"},
	}

	m.applyBatch(ops)

	// verifyoperationsuccess
	kv, exists := m.MemoryEtcd.kvData.Get("key1")
	if !exists {
		t.Error("Key key1 not found")
	}
	if string(kv.Value) != "value1" {
		t.Errorf("Expected value1, got %s", string(kv.Value))
	}
}

// TestBatchApplyStressTest 压力test
func TestBatchApplyStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	proposeC := make(chan string)
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)
	snapshotter := snap.New(nil, t.TempDir())
	m := NewMemory(snapshotter, proposeC, commitC, errorC)

	// 大批量operation
	numOps := 10000
	ops := make([]RaftOperation, numOps)
	for i := 0; i < numOps; i++ {
		ops[i] = RaftOperation{
			Type:   "PUT",
			Key:    fmt.Sprintf("key-%d", i%1000), // duplicatewritesame key
			Value:  fmt.Sprintf("value-%d", i),
			SeqNum: fmt.Sprintf("seq-%d", i),
		}
	}

	start := time.Now()
	m.applyBatch(ops)
	duration := time.Since(start)

	t.Logf("Applied %d operations in %v", numOps, duration)
	t.Logf("Throughput: %.2f ops/sec", float64(numOps)/duration.Seconds())

	// verifyfinalstatus
	finalRevision := m.MemoryEtcd.revision.Load()
	if finalRevision != int64(numOps) {
		t.Errorf("Expected revision %d, got %d", numOps, finalRevision)
	}
}
