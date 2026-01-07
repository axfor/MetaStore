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
	"metaStore/internal/kvstore"
	"metaStore/pkg/log"
	"sync"

	"go.uber.org/zap"
)

// batch_apply.go implement批量 Apply optimize (Phase 2)
//
// 核心optimize：减少lock开销
// - Before: N 个operation → N 次加lock/unlock → lock开销 O(N)
// - After: N 个operation → 1 次加lock/unlock → lock开销 O(1)
//
// 性能提升原理：
// 1. 按shard分groupoperation
// 2. 对eachshard，一次加lock，批量execute
// 3. parallelismhandledifferentshard
//
// 预期提升: 5-10x (lock开销减少 100x，但受其他因素limit)

// applyBatch 批量应用 Raft operation
//
// 核心optimize (Phase 2):
// - 按shard分groupoperation
// - eachshard一次加lock，批量execute
// - differentshardparallelismhandle
//
// example：
//   100 个operation → distributionto 50 个shard
//   Before: 100 次加lock
//   After: 50 次加lock (eachshard 1 次)
//
// argument：
//   - ops: 批量operationlist
func (m *Memory) applyBatch(ops []RaftOperation) {
	if len(ops) == 0 {
		return
	}

	// 特殊handle：只有 1 个operation，直接应用（避免分group开销）
	if len(ops) == 1 {
		m.applyOperation(ops[0])
		return
	}

	// ✅ Phase 2 核心optimize：按orderhandle，批量应用连续同typeoperation
	//
	// 设计原则：
	// 1. 保持operationorder（保证 revision correct递增）
	// 2. 批量应用连续同typeoperation（减少lock开销）
	// 3. whenoperationtype改变时，refreshcurrent批次
	//
	// example：
	//   [PUT, PUT, DELETE, PUT, TXN]
	//   → Batch1: [PUT, PUT] → Batch2: [DELETE] → Batch3: [PUT] → Batch4: [TXN]
	//
	// 性能提升原理：
	//   Before: N 个operation → N 次加lock
	//   After: N 个operation → ~N/avg_batch_size 次加lock

	var currentBatch []RaftOperation
	var currentType string

	// flushBatch refreshcurrent批次
	flushBatch := func() {
		if len(currentBatch) == 0 {
			return
		}

		switch currentType {
		case "PUT":
			m.batchApplyPut(currentBatch)
		case "DELETE":
			m.batchApplyDelete(currentBatch)
		case "TXN":
			// transactionoperation逐个execute（usegloballock）
			for _, op := range currentBatch {
				txnResp, err := m.MemoryEtcd.applyTxnWithShardLocks(op.Compares, op.ThenOps, op.ElseOps)
				if err != nil {
					log.Error("Failed to apply TXN operation",
						zap.Error(err),
						zap.String("component", "storage-memory"))
				}
				// savetransactionresult
				if op.SeqNum != "" && txnResp != nil {
					m.pendingMu.Lock()
					m.pendingTxnResults[op.SeqNum] = txnResp
					m.pendingMu.Unlock()
				}
			}
		case "LEASE_GRANT", "LEASE_REVOKE":
			// Lease operation（use独立 leaseMu）
			for _, op := range currentBatch {
				m.MemoryEtcd.applyLeaseOperationDirect(op.Type, op.LeaseID, op.TTL)
			}
		}

		// clear批次
		currentBatch = nil
	}

	// 按orderhandleoperation，批量应用连续同typeoperation
	for _, op := range ops {
		// operationtype改变，refreshcurrent批次
		if currentType != op.Type && len(currentBatch) > 0 {
			flushBatch()
		}

		// updatecurrenttype
		currentType = op.Type

		// addtocurrent批次
		currentBatch = append(currentBatch, op)
	}

	// refreshlast一个批次
	flushBatch()

	// notificationallwaitclient
	m.pendingMu.Lock()
	for _, op := range ops {
		if op.SeqNum != "" {
			if ch, exists := m.pendingOps[op.SeqNum]; exists {
				close(ch)
				delete(m.pendingOps, op.SeqNum)
			}
		}
	}
	m.pendingMu.Unlock()
}

// batchApplyPut 批量应用 PUT operation
//
// 核心optimize：
// - 按shard分group
// - eachshard一次加lock，批量execute
//
// argument：
//   - ops: PUT operationlist
func (m *Memory) batchApplyPut(ops []RaftOperation) {
	// 按shard分group
	shardOps := make(map[uint32][]RaftOperation)
	for _, op := range ops {
		shardIdx := m.MemoryEtcd.kvData.getShard(op.Key)
		shardOps[shardIdx] = append(shardOps[shardIdx], op)
	}

	// parallelismhandleeachshard
	var wg sync.WaitGroup
	for shardIdx, ops := range shardOps {
		wg.Add(1)
		go func(shardIdx uint32, ops []RaftOperation) {
			defer wg.Done()

			// ✅ 关keyoptimize: lockshard一次
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// 批量execute PUT operation
			for _, op := range ops {
				m.batchApplyPutNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyPutNoLock in持有shardlock情况下execute PUT
//
// 注意：call者must持有 shard.mu.Lock()
//
// argument：
//   - shard: shard
//   - op: PUT operation
func (m *Memory) batchApplyPutNoLock(shard *shard, op RaftOperation) {
	// 1. 生成new revision
	newRevision := m.MemoryEtcd.revision.Add(1)

	// 2. get之前value
	key := op.Key
	prevKv, exists := shard.data[key]

	// 3. createnew KeyValue
	var createRevision int64
	var version int64
	if exists {
		createRevision = prevKv.CreateRevision
		version = prevKv.Version + 1
	} else {
		createRevision = newRevision
		version = 1
	}

	kv := &kvstore.KeyValue{
		Key:            []byte(key),
		Value:          []byte(op.Value),
		CreateRevision: createRevision,
		ModRevision:    newRevision,
		Version:        version,
		Lease:          op.LeaseID,
	}

	// 4. writeshard (已持有lock，直接operation data)
	shard.data[key] = kv

	// 5. 关联 lease
	if op.LeaseID != 0 {
		m.MemoryEtcd.leaseMu.Lock()
		if lease, ok := m.MemoryEtcd.leases[op.LeaseID]; ok {
			lease.Keys[key] = true
		}
		m.MemoryEtcd.leaseMu.Unlock()
	}

	// 6. notification watchers
	m.MemoryEtcd.notifyWatchers(key, kv, kvstore.EventTypePut)
}

// batchApplyDelete 批量应用 DELETE operation
//
// 核心optimize：按shard分group，eachshard一次加lock
//
// argument：
//   - ops: DELETE operationlist
func (m *Memory) batchApplyDelete(ops []RaftOperation) {
	// 分离单keydeleteandrangedelete
	var singleKeyOps []RaftOperation
	var rangeOps []RaftOperation

	for _, op := range ops {
		if op.RangeEnd == "" {
			singleKeyOps = append(singleKeyOps, op)
		} else {
			rangeOps = append(rangeOps, op)
		}
	}

	// 批量单keydelete（parallelism）
	if len(singleKeyOps) > 0 {
		m.batchApplyDeleteSingleKey(singleKeyOps)
	}

	// rangedelete（serial，lockallshard）
	for _, op := range rangeOps {
		_, _, _, err := m.MemoryEtcd.deleteDirect(op.Key, op.RangeEnd)
		if err != nil {
			log.Error("Failed to apply DELETE range operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("rangeEnd", op.RangeEnd),
				zap.String("component", "storage-memory"))
		}
	}
}

// batchApplyDeleteSingleKey 批量应用单keydelete
func (m *Memory) batchApplyDeleteSingleKey(ops []RaftOperation) {
	// 按shard分group
	shardOps := make(map[uint32][]RaftOperation)
	for _, op := range ops {
		shardIdx := m.MemoryEtcd.kvData.getShard(op.Key)
		shardOps[shardIdx] = append(shardOps[shardIdx], op)
	}

	// parallelismhandleeachshard
	var wg sync.WaitGroup
	for shardIdx, ops := range shardOps {
		wg.Add(1)
		go func(shardIdx uint32, ops []RaftOperation) {
			defer wg.Done()

			// ✅ 关keyoptimize: lockshard一次
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// 批量execute DELETE operation
			for _, op := range ops {
				m.batchApplyDeleteNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyDeleteNoLock in持有shardlock情况下execute DELETE
//
// 注意：call者must持有 shard.mu.Lock()
func (m *Memory) batchApplyDeleteNoLock(shard *shard, op RaftOperation) {
	key := op.Key

	// checkkeyisno存in
	kv, exists := shard.data[key]
	if !exists {
		return
	}

	// 生成new revision
	m.MemoryEtcd.revision.Add(1)

	// deletekey
	delete(shard.data, key)

	// 解除 lease 关联
	if kv.Lease != 0 {
		m.MemoryEtcd.leaseMu.Lock()
		if lease, ok := m.MemoryEtcd.leases[kv.Lease]; ok {
			delete(lease.Keys, key)
		}
		m.MemoryEtcd.leaseMu.Unlock()
	}

	// notification watchers
	m.MemoryEtcd.notifyWatchers(key, kv, kvstore.EventTypeDelete)
}
