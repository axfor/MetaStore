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
)

// store_direct.go 提供无全局锁的直接操作方法
//
// 核心优化：单键操作不使用全局 txnMu 锁，直接使用 ShardedMap 的分片锁
// 这样可以让不同分片的操作并行执行，充分利用 512 个分片的并发能力
//
// 性能提升原理：
// - Before: 所有操作竞争 txnMu 全局锁 → 并发度 = 1
// - After: 操作分散到 512 个分片锁 → 并发度 = 512
// - 预期提升: 10-50x 吞吐量 (取决于并发数)

// putDirect 直接写入 key-value，不使用全局锁
//
// 并发安全性：
// - ShardedMap.Set() 内部使用分片级别的锁
// - revision 使用 atomic.Int64 保证原子性
// - lease 关联使用独立的 leaseMu
//
// 参数：
//   - key: 键
//   - value: 值
//   - leaseID: 租约 ID (0 表示无租约)
//
// 返回：
//   - revision: 当前 revision
//   - prevKv: 之前的值 (如果存在)
//   - error: 错误信息
func (m *MemoryEtcd) putDirect(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// 1. 生成新的 revision (atomic 操作，无需加锁)
	newRevision := m.revision.Add(1)

	// 2. 获取之前的值 (ShardedMap 内部加锁)
	prevKv, exists := m.kvData.Get(key)

	// 3. 创建新的 KeyValue
	var createRevision int64
	var version int64
	if exists {
		// 键已存在，保留 CreateRevision，递增 Version
		createRevision = prevKv.CreateRevision
		version = prevKv.Version + 1
	} else {
		// 新键，CreateRevision = ModRevision
		createRevision = newRevision
		version = 1
	}

	kv := &kvstore.KeyValue{
		Key:            []byte(key),
		Value:          []byte(value),
		CreateRevision: createRevision,
		ModRevision:    newRevision,
		Version:        version,
		Lease:          leaseID,
	}

	// 4. 写入 ShardedMap (内部加锁)
	m.kvData.Set(key, kv)

	// 5. 关联租约 (需要 leaseMu，因为 leases 不是 ShardedMap)
	if leaseID != 0 {
		m.leaseMu.Lock()
		if lease, ok := m.leases[leaseID]; ok {
			lease.Keys[key] = true
		}
		m.leaseMu.Unlock()
	}

	// 6. 通知 watchers (watchMu 保护)
	m.notifyWatchers(key, kv, kvstore.EventTypePut)

	return newRevision, prevKv, nil
}

// deleteDirect 直接删除 key，不使用全局锁
//
// 并发安全性：同 putDirect
//
// 参数：
//   - key: 起始键
//   - rangeEnd: 结束键 (空字符串表示单键删除)
//
// 返回：
//   - deleted: 删除的键数量
//   - prevKvs: 删除前的值列表
//   - revision: 当前 revision
//   - error: 错误信息
func (m *MemoryEtcd) deleteDirect(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		// 单键删除
		if kv, exists := m.kvData.Get(key); exists {
			// 生成新 revision
			newRevision := m.revision.Add(1)

			// 删除键 (ShardedMap 内部加锁)
			m.kvData.Delete(key)

			// 解除 lease 关联
			if kv.Lease != 0 {
				m.leaseMu.Lock()
				if lease, ok := m.leases[kv.Lease]; ok {
					delete(lease.Keys, key)
				}
				m.leaseMu.Unlock()
			}

			// 通知 watchers
			m.notifyWatchers(key, kv, kvstore.EventTypeDelete)

			deleted = 1
			prevKvs = append(prevKvs, kv)

			return deleted, prevKvs, newRevision, nil
		}

		// 键不存在
		return 0, nil, m.revision.Load(), nil
	}

	// 范围删除
	// 注意：这里需要获取范围内的所有键，然后逐个删除
	// ShardedMap.Range() 会锁定所有分片，这是当前的设计限制
	// 未来可以优化为增量扫描（见 SIMPLE_OPTIMIZATION_PLAN.md）
	keysToDelete := m.kvData.Range(key, rangeEnd, 0)

	if len(keysToDelete) == 0 {
		return 0, nil, m.revision.Load(), nil
	}

	// 逐个删除键
	for _, kv := range keysToDelete {
		// 生成新 revision (每次删除都更新 revision)
		m.revision.Add(1)

		keyStr := string(kv.Key)

		// 删除键
		m.kvData.Delete(keyStr)

		// 解除 lease 关联
		if kv.Lease != 0 {
			m.leaseMu.Lock()
			if lease, ok := m.leases[kv.Lease]; ok {
				delete(lease.Keys, keyStr)
			}
			m.leaseMu.Unlock()
		}

		// 通知 watchers
		m.notifyWatchers(keyStr, kv, kvstore.EventTypeDelete)

		deleted++
		prevKvs = append(prevKvs, kv)
	}

	return deleted, prevKvs, m.revision.Load(), nil
}

// applyTxnWithShardLocks 使用全局锁执行事务
//
// 注意：事务操作涉及多个键的原子性，使用全局 txnMu 锁是合理的设计
//
// 为什么事务仍使用全局锁？
// 1. 事务需要多键原子性（Compare + Then/Else）
// 2. 细粒度锁会导致复杂的死锁问题
// 3. 事务操作相对较少（大部分是单键 PUT/DELETE）
// 4. 对性能影响有限（事务 < 10% 的操作）
//
// 未来优化方向：
// - 如果事务操作占比很高，可以实现 MVCC + 乐观锁
// - 参考 CockroachDB 的 Intent Resolution 机制
//
// 参数：
//   - compares: 比较条件
//   - thenOps: 成功时执行的操作
//   - elseOps: 失败时执行的操作
//
// 返回：
//   - *kvstore.TxnResponse: 事务响应
//   - error: 错误信息
func (m *MemoryEtcd) applyTxnWithShardLocks(compares []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// 使用全局 txnMu 锁保证事务原子性
	m.txnMu.Lock()
	defer m.txnMu.Unlock()

	// 执行事务逻辑
	return m.txnUnlocked(compares, thenOps, elseOps)
}

// applyLeaseOperationDirect 直接执行 lease 操作，不使用全局锁
//
// 并发安全性：
// - leases map 使用独立的 leaseMu
// - 与 KV 操作并发安全
//
// 参数：
//   - opType: 操作类型 ("LEASE_GRANT" 或 "LEASE_REVOKE")
//   - leaseID: 租约 ID
//   - ttl: TTL (仅 GRANT 时使用)
func (m *MemoryEtcd) applyLeaseOperationDirect(opType string, leaseID int64, ttl int64) {
	switch opType {
	case "LEASE_GRANT":
		m.leaseMu.Lock()
		if m.leases == nil {
			m.leases = make(map[int64]*kvstore.Lease)
		}
		m.leases[leaseID] = &kvstore.Lease{
			ID:        leaseID,
			TTL:       ttl,
			GrantTime: timeNow(),
			Keys:      make(map[string]bool),
		}
		m.leaseMu.Unlock()

	case "LEASE_REVOKE":
		m.leaseMu.Lock()
		lease, ok := m.leases[leaseID]
		if !ok {
			m.leaseMu.Unlock()
			return
		}

		// 收集需要删除的键
		keysToDelete := make([]string, 0, len(lease.Keys))
		for key := range lease.Keys {
			keysToDelete = append(keysToDelete, key)
		}

		// 删除租约
		delete(m.leases, leaseID)
		m.leaseMu.Unlock()

		// 删除关联的键 (不持有 leaseMu，避免死锁)
		for _, key := range keysToDelete {
			m.deleteDirect(key, "")
		}
	}
}

// notifyWatchers 通知所有匹配的 watchers
//
// 并发安全性：使用 watchMu 保护 watches map
//
// 参数：
//   - key: 键
//   - kv: KeyValue
//   - eventType: 事件类型
func (m *MemoryEtcd) notifyWatchers(key string, kv *kvstore.KeyValue, eventType kvstore.EventType) {
	m.watchMu.RLock()
	defer m.watchMu.RUnlock()

	for _, sub := range m.watches {
		// 检查是否匹配
		if m.watchMatches(sub, key) {
			// 发送事件 (non-blocking)
			select {
			case sub.eventCh <- kvstore.WatchEvent{
				Type: eventType,
				Kv:   kv,
			}:
			default:
				// 如果 channel 满了，跳过 (避免阻塞)
			}
		}
	}
}

// watchMatches 检查 key 是否匹配 watch 订阅
func (m *MemoryEtcd) watchMatches(sub *watchSubscription, key string) bool {
	if sub.rangeEnd == "" {
		// 单键匹配
		return key == sub.key
	}

	// 范围匹配
	return key >= sub.key && (sub.rangeEnd == "\x00" || key < sub.rangeEnd)
}
