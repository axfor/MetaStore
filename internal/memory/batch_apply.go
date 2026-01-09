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

// batch_apply.go implement Apply optimize (Phase 2)
//
// optimize：decreasefewlockopen
// - Before: N  operation → N  timelock/unlock → lockopen O(N)
// - After: N  operation → 1  timelock/unlock → lockopen O(1)
//
// performance：
// 1. shardseparategroupoperation
// 2. toeachshard，first timelock，execute
// 3. parallelismhandledifferentshard
//
// : 5-10x (lockopendecreasefew 100x，limit)

// applyBatch applied Raft operation
//
// optimize (Phase 2):
// - shardseparategroupoperation
// - eachshardfirst timelock，execute
// - differentshardparallelismhandle
//
// example：
//   100  operation → distributionto 50  shard
//   Before: 100  timelock
//   After: 50  timelock (eachshard 1  time)
//
// argument：
//   - ops: operationlist
func (m *Memory) applyBatch(ops []RaftOperation) {
	if len(ops) == 0 {
		return
	}

	// handle：have 1  operation，applied(separategroupopen)
	if len(ops) == 1 {
		m.applyOperation(ops[0])
		return
	}

	// ✅ Phase 2 optimize：orderhandle，appliedtypeoperation
	//
	// ：
	// 1. holdoperationorder(certify revision correctincrease)
	// 2. appliedtypeoperation(decreasefewlockopen)
	// 3. whenoperationtypechangewhen，refreshcurrent time
	//
	// example：
	//   [PUT, PUT, DELETE, PUT, TXN]
	//   → Batch1: [PUT, PUT] → Batch2: [DELETE] → Batch3: [PUT] → Batch4: [TXN]
	//
	// performance：
	//   Before: N  operation → N  timelock
	//   After: N  operation → ~N/avg_batch_size  timelock

	var currentBatch []RaftOperation
	var currentType string

	// flushBatch refreshcurrent time
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
			// transactionoperation execute(usegloballock)
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
			// Lease operation(use leaseMu)
			for _, op := range currentBatch {
				m.MemoryEtcd.applyLeaseOperationDirect(op.Type, op.LeaseID, op.TTL)
			}
		}

		// clear time
		currentBatch = nil
	}

	// orderhandleoperation，appliedtypeoperation
	for _, op := range ops {
		// operationtypechange，refreshcurrent time
		if currentType != op.Type && len(currentBatch) > 0 {
			flushBatch()
		}

		// updatecurrenttype
		currentType = op.Type

		// addtocurrent time
		currentBatch = append(currentBatch, op)
	}

	// refreshlastfirst  time
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

// batchApplyPut applied PUT operation
//
// optimize：
// - shardseparategroup
// - eachshardfirst timelock，execute
//
// argument：
//   - ops: PUT operationlist
func (m *Memory) batchApplyPut(ops []RaftOperation) {
	// shardseparategroup
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

			// ✅ closekeyoptimize: lockshardfirst time
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// execute PUT operation
			for _, op := range ops {
				m.batchApplyPutNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyPutNoLock inholdingshardlocknextexecute PUT
//
// note：callermustholding shard.mu.Lock()
//
// argument：
//   - shard: shard
//   - op: PUT operation
func (m *Memory) batchApplyPutNoLock(shard *shard, op RaftOperation) {
	// 1. becomenew revision
	newRevision := m.MemoryEtcd.nextRevision()

	// 2. getbeforevalue
	key := op.Key
	prevKv, exists := shard.data[key]

	// 3. create new KeyValue
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

	// 4. writeshard (already holding lock，operation data)
	shard.data[key] = kv

	// 5. close lease
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

// batchApplyDelete applied DELETE operation
//
// optimize：shardseparategroup，eachshardfirst timelock
//
// argument：
//   - ops: DELETE operationlist
func (m *Memory) batchApplyDelete(ops []RaftOperation) {
	// separatesinglekeydeleteandrangedelete
	var singleKeyOps []RaftOperation
	var rangeOps []RaftOperation

	for _, op := range ops {
		if op.RangeEnd == "" {
			singleKeyOps = append(singleKeyOps, op)
		} else {
			rangeOps = append(rangeOps, op)
		}
	}

	// singlekeydelete(parallelism)
	if len(singleKeyOps) > 0 {
		m.batchApplyDeleteSingleKey(singleKeyOps)
	}

	// rangedelete(serial，lockallshard)
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

// batchApplyDeleteSingleKey appliedsinglekeydelete
func (m *Memory) batchApplyDeleteSingleKey(ops []RaftOperation) {
	// shardseparategroup
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

			// ✅ closekeyoptimize: lockshardfirst time
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// execute DELETE operation
			for _, op := range ops {
				m.batchApplyDeleteNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyDeleteNoLock inholdingshardlocknextexecute DELETE
//
// note：callermustholding shard.mu.Lock()
func (m *Memory) batchApplyDeleteNoLock(shard *shard, op RaftOperation) {
	key := op.Key

	// checkkeyisnoin
	kv, exists := shard.data[key]
	if !exists {
		return
	}

	// becomenew revision
	m.MemoryEtcd.nextRevision()

	// deletekey
	delete(shard.data, key)

	//  lease close
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
