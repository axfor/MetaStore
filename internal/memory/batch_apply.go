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

// batch_apply.go 实现批量 Apply 优化 (Phase 2)
//
// 核心优化：减少锁开销
// - Before: N 个操作 → N 次加锁/解锁 → 锁开销 O(N)
// - After: N 个操作 → 1 次加锁/解锁 → 锁开销 O(1)
//
// 性能提升原理：
// 1. 按分片分组操作
// 2. 对每个分片，一次加锁，批量执行
// 3. 并行处理不同分片
//
// 预期提升: 5-10x (锁开销减少 100x，但受其他因素限制)

// applyBatch 批量应用 Raft 操作
//
// 核心优化 (Phase 2):
// - 按分片分组操作
// - 每个分片一次加锁，批量执行
// - 不同分片并行处理
//
// 示例：
//   100 个操作 → 分布到 50 个分片
//   Before: 100 次加锁
//   After: 50 次加锁 (每个分片 1 次)
//
// 参数：
//   - ops: 批量操作列表
func (m *Memory) applyBatch(ops []RaftOperation) {
	if len(ops) == 0 {
		return
	}

	// 特殊处理：只有 1 个操作，直接应用（避免分组开销）
	if len(ops) == 1 {
		m.applyOperation(ops[0])
		return
	}

	// ✅ Phase 2 核心优化：按顺序处理，批量应用连续的同类型操作
	//
	// 设计原则：
	// 1. 保持操作顺序（保证 revision 正确递增）
	// 2. 批量应用连续的同类型操作（减少锁开销）
	// 3. 当操作类型改变时，刷新当前批次
	//
	// 示例：
	//   [PUT, PUT, DELETE, PUT, TXN]
	//   → Batch1: [PUT, PUT] → Batch2: [DELETE] → Batch3: [PUT] → Batch4: [TXN]
	//
	// 性能提升原理：
	//   Before: N 个操作 → N 次加锁
	//   After: N 个操作 → ~N/avg_batch_size 次加锁

	var currentBatch []RaftOperation
	var currentType string

	// flushBatch 刷新当前批次
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
			// 事务操作逐个执行（使用全局锁）
			for _, op := range currentBatch {
				txnResp, err := m.MemoryEtcd.applyTxnWithShardLocks(op.Compares, op.ThenOps, op.ElseOps)
				if err != nil {
					log.Error("Failed to apply TXN operation",
						zap.Error(err),
						zap.String("component", "storage-memory"))
				}
				// 保存事务结果
				if op.SeqNum != "" && txnResp != nil {
					m.pendingMu.Lock()
					m.pendingTxnResults[op.SeqNum] = txnResp
					m.pendingMu.Unlock()
				}
			}
		case "LEASE_GRANT", "LEASE_REVOKE":
			// Lease 操作（使用独立的 leaseMu）
			for _, op := range currentBatch {
				m.MemoryEtcd.applyLeaseOperationDirect(op.Type, op.LeaseID, op.TTL)
			}
		}

		// 清空批次
		currentBatch = nil
	}

	// 按顺序处理操作，批量应用连续的同类型操作
	for _, op := range ops {
		// 操作类型改变，刷新当前批次
		if currentType != op.Type && len(currentBatch) > 0 {
			flushBatch()
		}

		// 更新当前类型
		currentType = op.Type

		// 添加到当前批次
		currentBatch = append(currentBatch, op)
	}

	// 刷新最后一个批次
	flushBatch()

	// 通知所有等待的客户端
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

// batchApplyPut 批量应用 PUT 操作
//
// 核心优化：
// - 按分片分组
// - 每个分片一次加锁，批量执行
//
// 参数：
//   - ops: PUT 操作列表
func (m *Memory) batchApplyPut(ops []RaftOperation) {
	// 按分片分组
	shardOps := make(map[uint32][]RaftOperation)
	for _, op := range ops {
		shardIdx := m.MemoryEtcd.kvData.getShard(op.Key)
		shardOps[shardIdx] = append(shardOps[shardIdx], op)
	}

	// 并行处理每个分片
	var wg sync.WaitGroup
	for shardIdx, ops := range shardOps {
		wg.Add(1)
		go func(shardIdx uint32, ops []RaftOperation) {
			defer wg.Done()

			// ✅ 关键优化: 锁定分片一次
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// 批量执行 PUT 操作
			for _, op := range ops {
				m.batchApplyPutNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyPutNoLock 在持有分片锁的情况下执行 PUT
//
// 注意：调用者必须持有 shard.mu.Lock()
//
// 参数：
//   - shard: 分片
//   - op: PUT 操作
func (m *Memory) batchApplyPutNoLock(shard *shard, op RaftOperation) {
	// 1. 生成新 revision
	newRevision := m.MemoryEtcd.revision.Add(1)

	// 2. 获取之前的值
	key := op.Key
	prevKv, exists := shard.data[key]

	// 3. 创建新 KeyValue
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

	// 4. 写入分片 (已持有锁，直接操作 data)
	shard.data[key] = kv

	// 5. 关联 lease
	if op.LeaseID != 0 {
		m.MemoryEtcd.leaseMu.Lock()
		if lease, ok := m.MemoryEtcd.leases[op.LeaseID]; ok {
			lease.Keys[key] = true
		}
		m.MemoryEtcd.leaseMu.Unlock()
	}

	// 6. 通知 watchers
	m.MemoryEtcd.notifyWatchers(key, kv, kvstore.EventTypePut)
}

// batchApplyDelete 批量应用 DELETE 操作
//
// 核心优化：按分片分组，每个分片一次加锁
//
// 参数：
//   - ops: DELETE 操作列表
func (m *Memory) batchApplyDelete(ops []RaftOperation) {
	// 分离单键删除和范围删除
	var singleKeyOps []RaftOperation
	var rangeOps []RaftOperation

	for _, op := range ops {
		if op.RangeEnd == "" {
			singleKeyOps = append(singleKeyOps, op)
		} else {
			rangeOps = append(rangeOps, op)
		}
	}

	// 批量单键删除（并行）
	if len(singleKeyOps) > 0 {
		m.batchApplyDeleteSingleKey(singleKeyOps)
	}

	// 范围删除（串行，锁定所有分片）
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

// batchApplyDeleteSingleKey 批量应用单键删除
func (m *Memory) batchApplyDeleteSingleKey(ops []RaftOperation) {
	// 按分片分组
	shardOps := make(map[uint32][]RaftOperation)
	for _, op := range ops {
		shardIdx := m.MemoryEtcd.kvData.getShard(op.Key)
		shardOps[shardIdx] = append(shardOps[shardIdx], op)
	}

	// 并行处理每个分片
	var wg sync.WaitGroup
	for shardIdx, ops := range shardOps {
		wg.Add(1)
		go func(shardIdx uint32, ops []RaftOperation) {
			defer wg.Done()

			// ✅ 关键优化: 锁定分片一次
			shard := &m.MemoryEtcd.kvData.shards[shardIdx]
			shard.mu.Lock()
			defer shard.mu.Unlock()

			// 批量执行 DELETE 操作
			for _, op := range ops {
				m.batchApplyDeleteNoLock(shard, op)
			}
		}(shardIdx, ops)
	}

	wg.Wait()
}

// batchApplyDeleteNoLock 在持有分片锁的情况下执行 DELETE
//
// 注意：调用者必须持有 shard.mu.Lock()
func (m *Memory) batchApplyDeleteNoLock(shard *shard, op RaftOperation) {
	key := op.Key

	// 检查键是否存在
	kv, exists := shard.data[key]
	if !exists {
		return
	}

	// 生成新 revision
	m.MemoryEtcd.revision.Add(1)

	// 删除键
	delete(shard.data, key)

	// 解除 lease 关联
	if kv.Lease != 0 {
		m.MemoryEtcd.leaseMu.Lock()
		if lease, ok := m.MemoryEtcd.leases[kv.Lease]; ok {
			delete(lease.Keys, key)
		}
		m.MemoryEtcd.leaseMu.Unlock()
	}

	// 通知 watchers
	m.MemoryEtcd.notifyWatchers(key, kv, kvstore.EventTypeDelete)
}
