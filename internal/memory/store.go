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
	"context"
	"bytes"
	"fmt"
	"metaStore/internal/kvstore"
	"strings"
	"sync"
	"sync/atomic"
)

// MemoryEtcd supported etcd memorystorage
type MemoryEtcd struct {
	kvData       *ShardedMap                  // shard map，supportedhighconcurrency
	revision     atomic.Int64                 // global revision count(nolock atomic operation)
	leases       map[int64]*kvstore.Lease     // leaseID -> Lease
	leaseMu      sync.RWMutex                 // protected leases map
	watches      map[int64]*watchSubscription // watchID -> subscription
	watchMu      sync.RWMutex                 // protected watches map
	txnMu        sync.Mutex                   // protectedtransactionoperationatomic
	nextWatchID  atomic.Int64
}

// watchSubscription indicatesfirst  watch subscribe
type watchSubscription struct {
	watchID      int64
	key          string
	rangeEnd     string
	startRev     int64
	eventCh      chan kvstore.WatchEvent
	cancel       chan struct{}
	closed       atomic.Bool  // duplicateclose
	closeOnce    sync.Once    // close first time

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}

// NewMemoryEtcd createsupported etcd memorystorage
func NewMemoryEtcd() *MemoryEtcd {
	m := &MemoryEtcd{
		kvData:  NewShardedMap(),
		leases:  make(map[int64]*kvstore.Lease),
		watches: make(map[int64]*watchSubscription),
	}
	m.revision.Store(0)
	return m
}

// CurrentRevision returncurrent revision
func (m *MemoryEtcd) CurrentRevision() int64 {
	return m.revision.Load()
}

// Range executerangequery
func (m *MemoryEtcd) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	// convertas RangeOptions call
	return m.RangeWithOptions(ctx, key, rangeEnd, kvstore.RangeOptions{
		Limit:    limit,
		Revision: revision,
	})
}

// RangeWithOptions executerangequery(supportedcompleteoption)
func (m *MemoryEtcd) RangeWithOptions(ctx context.Context, key, rangeEnd string, opts kvstore.RangeOptions) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	// if rangeEnd asempty，querysingle key
	if rangeEnd == "" {
		if kv, ok := m.kvData.Get(key); ok {
			kvs = append(kvs, kv)
		}
	} else {
		// rangequery - ShardedMap internalwillhandlelockandsort
		// getall，afterappliedfilterandsort
		kvs = m.kvData.Range(key, rangeEnd, 0)
	}

	// Apply CreateRevision filter
	// Note: MaxCreateRevision filtering should be applied when explicitly set
	// When the etcd client uses WithMaxCreateRev(myRev-1) and myRev=1, MaxCreateRevision=0
	// In this case, all keys should be filtered out (all keys have CreateRevision >= 1)
	if opts.MaxCreateRevision > 0 || opts.MinCreateRevision > 0 {
		filtered := make([]*kvstore.KeyValue, 0, len(kvs))
		for _, kv := range kvs {
			if opts.MaxCreateRevision > 0 && kv.CreateRevision > opts.MaxCreateRevision {
				continue
			}
			if opts.MinCreateRevision > 0 && kv.CreateRevision < opts.MinCreateRevision {
				continue
			}
			filtered = append(filtered, kv)
		}
		kvs = filtered
	}

	// applied ModRevision filter
	if opts.MaxModRevision > 0 || opts.MinModRevision > 0 {
		filtered := make([]*kvstore.KeyValue, 0, len(kvs))
		for _, kv := range kvs {
			if opts.MaxModRevision > 0 && kv.ModRevision > opts.MaxModRevision {
				continue
			}
			if opts.MinModRevision > 0 && kv.ModRevision < opts.MinModRevision {
				continue
			}
			filtered = append(filtered, kv)
		}
		kvs = filtered
	}

	// appliedsort
	if opts.SortOrder != kvstore.SortNone && len(kvs) > 1 {
		m.sortKvs(kvs, opts.SortTarget, opts.SortOrder)
	}

	// calculate count(inapplied limit before)
	count := int64(len(kvs))

	// ifneedcount
	if opts.CountOnly {
		return &kvstore.RangeResponse{
			Kvs:      nil,
			More:     false,
			Count:    count,
			Revision: m.revision.Load(),
		}, nil
	}

	// applied limit
	more := false
	if opts.Limit > 0 && int64(len(kvs)) > opts.Limit {
		kvs = kvs[:opts.Limit]
		more = true
	}

	// ifneed keys
	if opts.KeysOnly {
		for _, kv := range kvs {
			kv.Value = nil
		}
	}

	return &kvstore.RangeResponse{
		Kvs:      kvs,
		More:     more,
		Count:    count,
		Revision: m.revision.Load(),
	}, nil
}

// sortKvs to kvs rowsort
func (m *MemoryEtcd) sortKvs(kvs []*kvstore.KeyValue, target kvstore.SortTarget, order kvstore.SortOrder) {
	// usestandardsort
	less := func(i, j int) bool {
		var cmp int
		switch target {
		case kvstore.SortByKey:
			cmp = bytes.Compare(kvs[i].Key, kvs[j].Key)
		case kvstore.SortByCreate:
			cmp = int(kvs[i].CreateRevision - kvs[j].CreateRevision)
		case kvstore.SortByMod:
			cmp = int(kvs[i].ModRevision - kvs[j].ModRevision)
		case kvstore.SortByVersion:
			cmp = int(kvs[i].Version - kvs[j].Version)
		case kvstore.SortByValue:
			cmp = bytes.Compare(kvs[i].Value, kvs[j].Value)
		default:
			cmp = bytes.Compare(kvs[i].Key, kvs[j].Key)
		}
		if order == kvstore.SortDescend {
			return cmp > 0
		}
		return cmp < 0
	}

	// singlesort(fordistributionedlockhavesmall number of key)
	n := len(kvs)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if !less(j, j+1) {
				kvs[j], kvs[j+1] = kvs[j+1], kvs[j]
			}
		}
	}
}

// PutWithLease storagekey-value pair，optionalclose lease
func (m *MemoryEtcd) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// verify lease(ifspecified)
	if leaseID != 0 {
		m.leaseMu.RLock()
		lease, ok := m.leases[leaseID]
		if !ok {
			m.leaseMu.RUnlock()
			return 0, nil, fmt.Errorf("lease not found: %d", leaseID)
		}
		// expirationcheck
		if lease.IsExpired() {
			m.leaseMu.RUnlock()
			return 0, nil, fmt.Errorf("lease expired: %d", leaseID)
		}
		m.leaseMu.RUnlock()
	}

	// getoldvalue(ShardedMap internallock)
	prevKv, _ := m.kvData.Get(key)

	// increase revision(atomic operation，nolock)
	newRevision := m.revision.Add(1)

	// createorupdate KeyValue
	var version int64 = 1
	var createRevision int64 = newRevision
	if prevKv != nil {
		version = prevKv.Version + 1
		createRevision = prevKv.CreateRevision
	}

	kv := &kvstore.KeyValue{
		Key:            []byte(key),
		Value:          []byte(value),
		CreateRevision: createRevision,
		ModRevision:    newRevision,
		Version:        version,
		Lease:          leaseID,
	}

	// storageto ShardedMap(internallock)
	m.kvData.Set(key, kv)

	// ifhave lease，close key
	if leaseID != 0 {
		m.leaseMu.Lock()
		if lease, ok := m.leases[leaseID]; ok {
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true
		}
		m.leaseMu.Unlock()
	}

	// trigger watch event(noholding lock)
	m.notifyWatches(kvstore.WatchEvent{
		Type:     kvstore.EventTypePut,
		Kv:       kv,
		PrevKv:   prevKv,
		Revision: newRevision,
	})

	return newRevision, prevKv, nil
}

// DeleteRange deleterangeinternalkey
func (m *MemoryEtcd) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	// collectcollectneeddeletekey
	keysToDelete := make([]string, 0)

	if rangeEnd == "" {
		// deletesingle key(ShardedMap internallock)
		if kv, ok := m.kvData.Get(key); ok {
			keysToDelete = append(keysToDelete, key)
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// rangedelete - use ShardedMap.Range() collectcollectneeddeletekey
		allKvs := m.kvData.Range(key, rangeEnd, 0)
		for _, kv := range allKvs {
			k := string(kv.Key)
			keysToDelete = append(keysToDelete, k)
			prevKvs = append(prevKvs, kv)
		}
	}

	if len(keysToDelete) == 0 {
		currentRev := m.revision.Load()
		return 0, nil, currentRev, nil
	}

	// increase revision(atomic operation，nolock)
	newRevision := m.revision.Add(1)

	// Collect events to send after deletion
	events := make([]kvstore.WatchEvent, 0, len(keysToDelete))

	// executedelete
	for _, k := range keysToDelete {
		prevKv, _ := m.kvData.Get(k)

		// from ShardedMap delete(internallock)
		m.kvData.Delete(k)
		deleted++

		// from lease in key
		if prevKv != nil && prevKv.Lease != 0 {
			m.leaseMu.Lock()
			if lease, ok := m.leases[prevKv.Lease]; ok {
				delete(lease.Keys, k)
			}
			m.leaseMu.Unlock()
		}

		// Prepare watch event
		if prevKv != nil {
			deletedKv := &kvstore.KeyValue{
				Key:            prevKv.Key,
				Value:          nil,
				CreateRevision: prevKv.CreateRevision,
				ModRevision:    newRevision,
				Version:        0,
				Lease:          0,
			}
			events = append(events, kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   prevKv,
				Revision: newRevision,
			})
		}
	}

	// trigger watch event(noholding lock)
	for _, event := range events {
		m.notifyWatches(event)
	}

	return deleted, prevKvs, newRevision, nil
}

// Txn executetransaction
func (m *MemoryEtcd) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// use txnMu protectedtransactionatomic
	m.txnMu.Lock()
	defer m.txnMu.Unlock()

	return m.txnUnlocked(cmps, thenOps, elseOps)
}

// txnUnlocked executetransaction(needholding lock)
func (m *MemoryEtcd) txnUnlocked(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// all compare condition
	succeeded := true
	for _, cmp := range cmps {
		if !m.evaluateCompare(cmp) {
			succeeded = false
			break
		}
	}

	// electneedexecuteoperation
	var ops []kvstore.Op
	if succeeded {
		ops = thenOps
	} else {
		ops = elseOps
	}

	// executeoperation
	responses := make([]kvstore.OpResponse, len(ops))
	for i, op := range ops {
		switch op.Type {
		case kvstore.OpRange:
			resp, err := m.rangeUnlocked(string(op.Key), string(op.RangeEnd), op.Limit)
			if err != nil {
				return nil, err
			}
			responses[i] = kvstore.OpResponse{
				Type:      kvstore.OpRange,
				RangeResp: resp,
			}
		case kvstore.OpPut:
			revision, prevKv, err := m.putUnlocked(string(op.Key), string(op.Value), op.LeaseID)
			if err != nil {
				return nil, err
			}
			responses[i] = kvstore.OpResponse{
				Type: kvstore.OpPut,
				PutResp: &kvstore.PutResponse{
					PrevKv:   prevKv,
					Revision: revision,
				},
			}
		case kvstore.OpDelete:
			deleted, prevKvs, revision, err := m.deleteUnlocked(string(op.Key), string(op.RangeEnd))
			if err != nil {
				return nil, err
			}
			responses[i] = kvstore.OpResponse{
				Type: kvstore.OpDelete,
				DeleteResp: &kvstore.DeleteResponse{
					Deleted:  deleted,
					PrevKvs:  prevKvs,
					Revision: revision,
				},
			}
		}
	}

	return &kvstore.TxnResponse{
		Succeeded: succeeded,
		Responses: responses,
		Revision:  m.revision.Load(),
	}, nil
}

// evaluateCompare comparecondition(needholding txnMu)
func (m *MemoryEtcd) evaluateCompare(cmp kvstore.Compare) bool {
	kv, exists := m.kvData.Get(string(cmp.Key))

	switch cmp.Target {
	case kvstore.CompareVersion:
		v := int64(0)
		if exists {
			v = kv.Version
		}
		return m.compareInt(v, cmp.TargetUnion.Version, cmp.Result)
	case kvstore.CompareCreate:
		v := int64(0)
		if exists {
			v = kv.CreateRevision
		}
		return m.compareInt(v, cmp.TargetUnion.CreateRevision, cmp.Result)
	case kvstore.CompareMod:
		v := int64(0)
		if exists {
			v = kv.ModRevision
		}
		return m.compareInt(v, cmp.TargetUnion.ModRevision, cmp.Result)
	case kvstore.CompareValue:
		v := []byte{}
		if exists {
			v = kv.Value
		}
		return m.compareBytes(v, cmp.TargetUnion.Value, cmp.Result)
	case kvstore.CompareLease:
		v := int64(0)
		if exists {
			v = kv.Lease
		}
		return m.compareInt(v, cmp.TargetUnion.Lease, cmp.Result)
	}
	return false
}

// compareInt comparecomplete
func (m *MemoryEtcd) compareInt(a, b int64, result kvstore.CompareResult) bool {
	switch result {
	case kvstore.CompareEqual:
		return a == b
	case kvstore.CompareGreater:
		return a > b
	case kvstore.CompareLess:
		return a < b
	case kvstore.CompareNotEqual:
		return a != b
	}
	return false
}

// compareBytes comparearray
func (m *MemoryEtcd) compareBytes(a, b []byte, result kvstore.CompareResult) bool {
	cmp := bytes.Compare(a, b)
	switch result {
	case kvstore.CompareEqual:
		return cmp == 0
	case kvstore.CompareGreater:
		return cmp > 0
	case kvstore.CompareLess:
		return cmp < 0
	case kvstore.CompareNotEqual:
		return cmp != 0
	}
	return false
}

// not lockinternalmethod(needholding txnMu)
func (m *MemoryEtcd) rangeUnlocked(key, rangeEnd string, limit int64) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	if rangeEnd == "" {
		if kv, ok := m.kvData.Get(key); ok {
			kvs = append(kvs, kv)
		}
	} else {
		// use ShardedMap.Range() getrangeinternalkey-value pair(internalalready sort)
		kvs = m.kvData.Range(key, rangeEnd, limit)
	}

	more := false
	count := int64(len(kvs))
	if limit > 0 && int64(len(kvs)) > limit {
		kvs = kvs[:limit]
		more = true
	}

	return &kvstore.RangeResponse{
		Kvs:      kvs,
		More:     more,
		Count:    count,
		Revision: m.revision.Load(),
	}, nil
}

func (m *MemoryEtcd) putUnlocked(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	if leaseID != 0 {
		m.leaseMu.RLock()
		lease, ok := m.leases[leaseID]
		if !ok || lease.IsExpired() {
			m.leaseMu.RUnlock()
			return 0, nil, fmt.Errorf("invalid lease")
		}
		m.leaseMu.RUnlock()
	}

	prevKv, _ := m.kvData.Get(key)
	newRevision := m.revision.Add(1)

	var version int64 = 1
	var createRevision int64 = newRevision
	if prevKv != nil {
		version = prevKv.Version + 1
		createRevision = prevKv.CreateRevision
	}

	kv := &kvstore.KeyValue{
		Key:            []byte(key),
		Value:          []byte(value),
		CreateRevision: createRevision,
		ModRevision:    newRevision,
		Version:        version,
		Lease:          leaseID,
	}

	m.kvData.Set(key, kv)

	if leaseID != 0 {
		m.leaseMu.Lock()
		if lease, ok := m.leases[leaseID]; ok {
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true
		}
		m.leaseMu.Unlock()
	}

	// NOTE: putUnlocked does NOT notify watches - caller must do it after releasing lock
	// This avoids deadlock when called from Txn which holds the lock

	return newRevision, prevKv, nil
}

func (m *MemoryEtcd) deleteUnlocked(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue
	keysToDelete := make([]string, 0)

	if rangeEnd == "" {
		if kv, ok := m.kvData.Get(key); ok {
			keysToDelete = append(keysToDelete, key)
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// use ShardedMap.Range() getrangeinternalkey-value pair
		allKvs := m.kvData.Range(key, rangeEnd, 0)
		for _, kv := range allKvs {
			k := string(kv.Key)
			keysToDelete = append(keysToDelete, k)
			prevKvs = append(prevKvs, kv)
		}
	}

	if len(keysToDelete) == 0 {
		return 0, nil, m.revision.Load(), nil
	}

	newRevision := m.revision.Add(1)

	for _, k := range keysToDelete {
		prevKv, _ := m.kvData.Get(k)
		m.kvData.Delete(k)
		deleted++

		if prevKv != nil && prevKv.Lease != 0 {
			m.leaseMu.Lock()
			if lease, ok := m.leases[prevKv.Lease]; ok {
				delete(lease.Keys, k)
			}
			m.leaseMu.Unlock()
		}
	}

	// NOTE: deleteUnlocked does NOT notify watches - caller must do it after releasing lock
	// This avoids deadlock when called from Txn which holds the lock

	return deleted, prevKvs, newRevision, nil
}

// holdaftercompatiblehavemethod
func (m *MemoryEtcd) Lookup(key string) (string, bool) {
	if kv, ok := m.kvData.Get(key); ok {
		return string(kv.Value), true
	}
	return "", false
}

func (m *MemoryEtcd) Propose(k string, v string) {
	// transformimplement，call PutWithLease
	m.PutWithLease(context.Background(), k, v, 0)
}

func (m *MemoryEtcd) GetSnapshot() ([]byte, error) {
	// use ShardedMap.GetAll() getalldata(internallock)
	allData := m.kvData.GetAll()

	// TODO: implementcompletesnapshotserialize
	var buf strings.Builder
	for key, kv := range allData {
		buf.WriteString(fmt.Sprintf("%s=%s\n", key, string(kv.Value)))
	}
	return []byte(buf.String()), nil
}

// Compact compressspecified revision beforedata
func (m *MemoryEtcd) Compact(ctx context.Context, revision int64) error {
	// etcd  Compact for compressversion，clean upspecified revision beforedata
	//
	// formemorystorage：
	// 1. currentnot MVCC version， timeupdateoverride
	// 2. expiration Lease clean up LeaseManager handle
	// 3. hold API compatible
	//
	// not comecanextend：implement MVCC versionmanagementandcompress
	// currentimplement：no-op

	return nil
}

// GetRaftStatus returns Raft status information
// For standalone MemoryEtcd (no Raft), returns a simple status
func (m *MemoryEtcd) GetRaftStatus() kvstore.RaftStatus {
	return kvstore.RaftStatus{
		NodeID:   1,
		Term:     1,
		LeaderID: 1,
		State:    "leader", // Standalone mode, always leader
		Applied:  uint64(m.revision.Load()),
		Commit:   uint64(m.revision.Load()),
	}
}

// TransferLeadership is not supported in standalone mode
func (m *MemoryEtcd) TransferLeadership(targetID uint64) error {
	return fmt.Errorf("leadership transfer not supported in standalone mode")
}
