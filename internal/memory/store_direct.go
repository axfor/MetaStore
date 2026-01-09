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

// store_direct.go nogloballockoperationmethod
//
// optimize：singlekeyoperationnotuseglobal txnMu lock，use ShardedMap shardlock
// candifferentshardoperationparallelismexecute，separate 512  shardconcurrencycan
//
// performance：
// - Before: alloperationcompete txnMu globallock → concurrency = 1
// - After: operationseparateto 512  shardlock → concurrency = 512
// - : 10-50x throughput (getatconcurrency)

// putDirect write key-value，notusegloballock
//
// concurrencysafe：
// - ShardedMap.Set() internaluseshardlevellock
// - revision use atomic.Int64 certifyatomic
// - lease closeuse leaseMu
//
// argument：
//   - key: key
//   - value: value
//   - leaseID: lease ID (0 indicatesnolease)
//
// return：
//   - revision: current revision
//   - prevKv: beforevalue (ifin)
//   - error: incorrectinfo
func (m *MemoryEtcd) putDirect(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// 1. becomenew revision (Raft guarantees serialization)
	newRevision := m.nextRevision()

	// 2. getbeforevalue (ShardedMap internallock)
	prevKv, exists := m.kvData.Get(key)

	// 3. create new KeyValue
	var createRevision int64
	var version int64
	if exists {
		// keyexists， CreateRevision，increase Version
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

	// 4. write ShardedMap (internallock)
	m.kvData.Set(key, kv)

	// 5. closelease (need leaseMu，as leases notis ShardedMap)
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

// deleteDirect delete key，notusegloballock
//
// concurrencysafe： putDirect
//
// argument：
//   - key: startkey
//   - rangeEnd: endkey (emptyindicatessinglekeydelete)
//
// return：
//   - deleted: deletekeyquantity
//   - prevKvs: deletebeforevaluelist
//   - revision: current revision
//   - error: incorrectinfo
func (m *MemoryEtcd) deleteDirect(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		// singlekeydelete
		if kv, exists := m.kvData.Get(key); exists {
			// becomenew revision (Raft guarantees serialization)
			newRevision := m.nextRevision()

			// deletekey (ShardedMap internallock)
			m.kvData.Delete(key)

			//  lease close
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
		return 0, nil, m.getRevision(), nil
	}

	// rangedelete
	// note：needgetrangeinternalallkey，after delete
	// ShardedMap.Range() willlockallshard，iscurrentlimit
	// not comecanoptimizeasincrease( SIMPLE_OPTIMIZATION_PLAN.md)
	keysToDelete := m.kvData.Range(key, rangeEnd, 0)

	if len(keysToDelete) == 0 {
		return 0, nil, m.getRevision(), nil
	}

	//  deletekey
	for _, kv := range keysToDelete {
		// becomenew revision ( timedeleteallupdate revision)
		// Note: Raft guarantees serialization
		m.nextRevision()

		keyStr := string(kv.Key)

		// deletekey
		m.kvData.Delete(keyStr)

		//  lease close
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

	return deleted, prevKvs, m.getRevision(), nil
}

// applyTxnWithShardLocks usegloballockexecutetransaction
//
// note：transactionoperationandmany keyatomic，useglobal txnMu lockismerge
//
// astransactionusegloballock？
// 1. transactionneedmanykeyatomic(Compare + Then/Else)
// 2. fine-grainedlockwillguidelock
// 3. transactionoperationtofew(largepartiallyissinglekey PUT/DELETE)
// 4. toperformancehave(transaction < 10% operation)
//
// not comeoptimize：
// - iftransactionoperationhigh，canimplement MVCC + observelock
// - reference CockroachDB  Intent Resolution 
//
// argument：
//   - compares: comparecondition
//   - thenOps: successwhenexecuteoperation
//   - elseOps: failurewhenexecuteoperation
//
// return：
//   - *kvstore.TxnResponse: transactionresponse
//   - error: incorrectinfo
func (m *MemoryEtcd) applyTxnWithShardLocks(compares []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// Raft guarantees serialization, no storage-level lock needed
	return m.txnInternal(compares, thenOps, elseOps)
}

// applyLeaseOperationDirect execute lease operation，notusegloballock
//
// concurrencysafe：
// - leases map use leaseMu
// - and KV operationconcurrencysafe
//
// argument：
//   - opType: operationtype ("LEASE_GRANT" or "LEASE_REVOKE")
//   - leaseID: lease ID
//   - ttl: TTL ( GRANT when using)
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

		// collectcollectneeddeletekey
		keysToDelete := make([]string, 0, len(lease.Keys))
		for key := range lease.Keys {
			keysToDelete = append(keysToDelete, key)
		}

		// deletelease
		delete(m.leases, leaseID)
		m.leaseMu.Unlock()

		// deleteclosekey (notholding leaseMu，lock)
		for _, key := range keysToDelete {
			m.deleteDirect(key, "")
		}
	}
}

// notifyWatchers notificationallmatch watchers
//
// concurrencysafe：use watchMu protected watches map
//
// argument：
//   - key: key
//   - kv: KeyValue
//   - eventType: eventtype
func (m *MemoryEtcd) notifyWatchers(key string, kv *kvstore.KeyValue, eventType kvstore.EventType) {
	m.watchMu.RLock()
	defer m.watchMu.RUnlock()

	for _, sub := range m.watches {
		// checkisnomatch
		if m.watchMatches(sub, key) {
			// sendevent (non-blocking)
			select {
			case sub.eventCh <- kvstore.WatchEvent{
				Type: eventType,
				Kv:   kv,
			}:
			default:
				// if channel full，skip (blocking)
			}
		}
	}
}

// watchMatches check key isnomatch watch subscribe
func (m *MemoryEtcd) watchMatches(sub *watchSubscription, key string) bool {
	if sub.rangeEnd == "" {
		// singlekeymatch
		return key == sub.key
	}

	// rangematch
	return key >= sub.key && (sub.rangeEnd == "\x00" || key < sub.rangeEnd)
}
