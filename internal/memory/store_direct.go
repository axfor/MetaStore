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

// store_direct.go 提供无globallock直接operationmethod
//
// 核心optimize：单keyoperationnotuseglobal txnMu lock，直接use ShardedMap shardlock
// 这样can让differentshardoperationparallelismexecute，充分利用 512 个shardconcurrency能力
//
// 性能提升原理：
// - Before: alloperation竞争 txnMu globallock → concurrency度 = 1
// - After: operation分散to 512 个shardlock → concurrency度 = 512
// - 预期提升: 10-50x throughput (取决atconcurrency数)

// putDirect 直接write key-value，notusegloballock
//
// concurrencysafe性：
// - ShardedMap.Set() internaluseshardlevellock
// - revision use atomic.Int64 保证atomic性
// - lease 关联use独立 leaseMu
//
// argument：
//   - key: key
//   - value: value
//   - leaseID: lease ID (0 indicates无lease)
//
// return：
//   - revision: current revision
//   - prevKv: 之前value (if存in)
//   - error: incorrectinfo
func (m *MemoryEtcd) putDirect(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// 1. 生成new revision (atomic operation，无需加lock)
	newRevision := m.revision.Add(1)

	// 2. get之前value (ShardedMap internal加lock)
	prevKv, exists := m.kvData.Get(key)

	// 3. createnew KeyValue
	var createRevision int64
	var version int64
	if exists {
		// keyexists，保留 CreateRevision，递增 Version
		createRevision = prevKv.CreateRevision
		version = prevKv.Version + 1
	} else {
		// newkey，CreateRevision = ModRevision
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

	// 4. write ShardedMap (internal加lock)
	m.kvData.Set(key, kv)

	// 5. 关联lease (need leaseMu，因as leases notis ShardedMap)
	if leaseID != 0 {
		m.leaseMu.Lock()
		if lease, ok := m.leases[leaseID]; ok {
			lease.Keys[key] = true
		}
		m.leaseMu.Unlock()
	}

	// 6. notification watchers (watchMu protected)
	m.notifyWatchers(key, kv, kvstore.EventTypePut)

	return newRevision, prevKv, nil
}

// deleteDirect 直接delete key，notusegloballock
//
// concurrencysafe性：同 putDirect
//
// argument：
//   - key: 起始key
//   - rangeEnd: endkey (empty字符串indicates单keydelete)
//
// return：
//   - deleted: deletekeyquantity
//   - prevKvs: delete前valuelist
//   - revision: current revision
//   - error: incorrectinfo
func (m *MemoryEtcd) deleteDirect(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		// 单keydelete
		if kv, exists := m.kvData.Get(key); exists {
			// 生成new revision
			newRevision := m.revision.Add(1)

			// deletekey (ShardedMap internal加lock)
			m.kvData.Delete(key)

			// 解除 lease 关联
			if kv.Lease != 0 {
				m.leaseMu.Lock()
				if lease, ok := m.leases[kv.Lease]; ok {
					delete(lease.Keys, key)
				}
				m.leaseMu.Unlock()
			}

			// notification watchers
			m.notifyWatchers(key, kv, kvstore.EventTypeDelete)

			deleted = 1
			prevKvs = append(prevKvs, kv)

			return deleted, prevKvs, newRevision, nil
		}

		// keydoes not exist
		return 0, nil, m.revision.Load(), nil
	}

	// rangedelete
	// 注意：这里needgetrange内allkey，然后逐个delete
	// ShardedMap.Range() willlockallshard，这iscurrent设计limit
	// 未来canoptimizeas增量扫描（见 SIMPLE_OPTIMIZATION_PLAN.md）
	keysToDelete := m.kvData.Range(key, rangeEnd, 0)

	if len(keysToDelete) == 0 {
		return 0, nil, m.revision.Load(), nil
	}

	// 逐个deletekey
	for _, kv := range keysToDelete {
		// 生成new revision (每次delete都update revision)
		m.revision.Add(1)

		keyStr := string(kv.Key)

		// deletekey
		m.kvData.Delete(keyStr)

		// 解除 lease 关联
		if kv.Lease != 0 {
			m.leaseMu.Lock()
			if lease, ok := m.leases[kv.Lease]; ok {
				delete(lease.Keys, keyStr)
			}
			m.leaseMu.Unlock()
		}

		// notification watchers
		m.notifyWatchers(keyStr, kv, kvstore.EventTypeDelete)

		deleted++
		prevKvs = append(prevKvs, kv)
	}

	return deleted, prevKvs, m.revision.Load(), nil
}

// applyTxnWithShardLocks usegloballockexecutetransaction
//
// 注意：transactionoperation涉and多个keyatomic性，useglobal txnMu lockis合理设计
//
// as什么transaction仍usegloballock？
// 1. transactionneed多keyatomic性（Compare + Then/Else）
// 2. fine-grainedlockwill导致复杂死lock问题
// 3. transactionoperation相对较少（大partiallyis单key PUT/DELETE）
// 4. 对性能影响有限（transaction < 10% operation）
//
// 未来optimize方向：
// - iftransactionoperation占比很high，canimplement MVCC + 乐观lock
// - reference CockroachDB  Intent Resolution 机制
//
// argument：
//   - compares: comparecondition
//   - thenOps: success时executeoperation
//   - elseOps: failure时executeoperation
//
// return：
//   - *kvstore.TxnResponse: transactionresponse
//   - error: incorrectinfo
func (m *MemoryEtcd) applyTxnWithShardLocks(compares []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// useglobal txnMu lock保证transactionatomic性
	m.txnMu.Lock()
	defer m.txnMu.Unlock()

	// executetransaction逻辑
	return m.txnUnlocked(compares, thenOps, elseOps)
}

// applyLeaseOperationDirect 直接execute lease operation，notusegloballock
//
// concurrencysafe性：
// - leases map use独立 leaseMu
// - and KV operationconcurrencysafe
//
// argument：
//   - opType: operationtype ("LEASE_GRANT" or "LEASE_REVOKE")
//   - leaseID: lease ID
//   - ttl: TTL (仅 GRANT 时use)
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

		// 收集needdeletekey
		keysToDelete := make([]string, 0, len(lease.Keys))
		for key := range lease.Keys {
			keysToDelete = append(keysToDelete, key)
		}

		// deletelease
		delete(m.leases, leaseID)
		m.leaseMu.Unlock()

		// delete关联key (not持有 leaseMu，避免死lock)
		for _, key := range keysToDelete {
			m.deleteDirect(key, "")
		}
	}
}

// notifyWatchers notificationall匹配 watchers
//
// concurrencysafe性：use watchMu protected watches map
//
// argument：
//   - key: key
//   - kv: KeyValue
//   - eventType: eventtype
func (m *MemoryEtcd) notifyWatchers(key string, kv *kvstore.KeyValue, eventType kvstore.EventType) {
	m.watchMu.RLock()
	defer m.watchMu.RUnlock()

	for _, sub := range m.watches {
		// checkisno匹配
		if m.watchMatches(sub, key) {
			// sendevent (non-blocking)
			select {
			case sub.eventCh <- kvstore.WatchEvent{
				Type: eventType,
				Kv:   kv,
			}:
			default:
				// if channel full，skip (避免blocking)
			}
		}
	}
}

// watchMatches check key isno匹配 watch subscribe
func (m *MemoryEtcd) watchMatches(sub *watchSubscription, key string) bool {
	if sub.rangeEnd == "" {
		// 单key匹配
		return key == sub.key
	}

	// range匹配
	return key >= sub.key && (sub.rangeEnd == "\x00" || key < sub.rangeEnd)
}
