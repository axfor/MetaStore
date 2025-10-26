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
	"bytes"
	"fmt"
	"metaStore/internal/kvstore"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MemoryEtcd 支持 etcd 语义的内存存储
type MemoryEtcd struct {
	mu           sync.RWMutex
	kvData       map[string]*kvstore.KeyValue // key -> KeyValue
	revision     atomic.Int64                 // 全局 revision 计数器
	leases       map[int64]*kvstore.Lease     // leaseID -> Lease
	watches      map[int64]*watchSubscription // watchID -> subscription
	nextWatchID  atomic.Int64
}

// watchSubscription 表示一个 watch 订阅
type watchSubscription struct {
	watchID      int64
	key          string
	rangeEnd     string
	startRev     int64
	eventCh      chan kvstore.WatchEvent
	cancel       chan struct{}
	closed       atomic.Bool  // 防止重复关闭
	closeOnce    sync.Once    // 确保只关闭一次

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}

// NewMemoryEtcd 创建支持 etcd 语义的内存存储
func NewMemoryEtcd() *MemoryEtcd {
	m := &MemoryEtcd{
		kvData:  make(map[string]*kvstore.KeyValue),
		leases:  make(map[int64]*kvstore.Lease),
		watches: make(map[int64]*watchSubscription),
	}
	m.revision.Store(0)
	return m
}

// CurrentRevision 返回当前 revision
func (m *MemoryEtcd) CurrentRevision() int64 {
	return m.revision.Load()
}

// Range 执行范围查询
func (m *MemoryEtcd) Range(key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var kvs []*kvstore.KeyValue

	// 如果 rangeEnd 为空，查询单个键
	if rangeEnd == "" {
		if kv, ok := m.kvData[key]; ok {
			kvs = append(kvs, kv)
		}
	} else {
		// 范围查询
		for k, v := range m.kvData {
			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				kvs = append(kvs, v)
			}
		}
		// 排序
		sort.Slice(kvs, func(i, j int) bool {
			return string(kvs[i].Key) < string(kvs[j].Key)
		})
	}

	// 应用 limit
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

// PutWithLease 存储键值对，可选关联 lease
func (m *MemoryEtcd) PutWithLease(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	m.mu.Lock()

	// 验证 lease（如果指定）
	if leaseID != 0 {
		lease, ok := m.leases[leaseID]
		if !ok {
			m.mu.Unlock()
			return 0, nil, fmt.Errorf("lease not found: %d", leaseID)
		}
		// 过期检查
		if lease.IsExpired() {
			m.mu.Unlock()
			return 0, nil, fmt.Errorf("lease expired: %d", leaseID)
		}
	}

	// 获取旧值
	prevKv := m.kvData[key]

	// 递增 revision
	newRevision := m.revision.Add(1)

	// 创建或更新 KeyValue
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

	m.kvData[key] = kv

	// 如果有 lease，关联 key
	if leaseID != 0 {
		if lease, ok := m.leases[leaseID]; ok {
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true
		}
	}

	// Release lock before notifying watches (data is already committed)
	m.mu.Unlock()

	// 触发 watch 事件
	m.notifyWatches(kvstore.WatchEvent{
		Type:     kvstore.EventTypePut,
		Kv:       kv,
		PrevKv:   prevKv,
		Revision: newRevision,
	})

	return newRevision, prevKv, nil
}

// DeleteRange 删除范围内的键
func (m *MemoryEtcd) DeleteRange(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	m.mu.Lock()

	var deleted int64
	var prevKvs []*kvstore.KeyValue

	// 收集要删除的键
	keysToDelete := make([]string, 0)

	if rangeEnd == "" {
		// 删除单个键
		if kv, ok := m.kvData[key]; ok {
			keysToDelete = append(keysToDelete, key)
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// 范围删除
		for k, v := range m.kvData {
			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				keysToDelete = append(keysToDelete, k)
				prevKvs = append(prevKvs, v)
			}
		}
	}

	if len(keysToDelete) == 0 {
		currentRev := m.revision.Load()
		m.mu.Unlock()
		return 0, nil, currentRev, nil
	}

	// 递增 revision
	newRevision := m.revision.Add(1)

	// Collect events to send after releasing lock
	events := make([]kvstore.WatchEvent, 0, len(keysToDelete))

	// 执行删除
	for _, k := range keysToDelete {
		prevKv := m.kvData[k]
		delete(m.kvData, k)
		deleted++

		// 从 lease 中移除 key
		if prevKv.Lease != 0 {
			if lease, ok := m.leases[prevKv.Lease]; ok {
				delete(lease.Keys, k)
			}
		}

		// Prepare watch event
		// For DELETE events, Kv contains the deleted key with ModRevision set to deletion revision
		deletedKv := &kvstore.KeyValue{
			Key:            prevKv.Key,
			Value:          nil, // Value is nil for deleted key
			CreateRevision: prevKv.CreateRevision,
			ModRevision:    newRevision, // Set to deletion revision
			Version:        0,           // Version is 0 for deleted key
			Lease:          0,
		}
		events = append(events, kvstore.WatchEvent{
			Type:     kvstore.EventTypeDelete,
			Kv:       deletedKv,
			PrevKv:   prevKv,
			Revision: newRevision,
		})
	}

	// Release lock before notifying watches (data is already committed)
	m.mu.Unlock()

	// 触发 watch 事件
	for _, event := range events {
		m.notifyWatches(event)
	}

	return deleted, prevKvs, newRevision, nil
}

// Txn 执行事务
func (m *MemoryEtcd) Txn(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 评估所有 compare 条件
	succeeded := true
	for _, cmp := range cmps {
		if !m.evaluateCompare(cmp) {
			succeeded = false
			break
		}
	}

	// 选择要执行的操作
	var ops []kvstore.Op
	if succeeded {
		ops = thenOps
	} else {
		ops = elseOps
	}

	// 执行操作
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

// evaluateCompare 评估比较条件（需要持有锁）
func (m *MemoryEtcd) evaluateCompare(cmp kvstore.Compare) bool {
	kv, exists := m.kvData[string(cmp.Key)]

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

// compareInt 比较整数
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

// compareBytes 比较字节数组
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

// 未加锁的内部方法
func (m *MemoryEtcd) rangeUnlocked(key, rangeEnd string, limit int64) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	if rangeEnd == "" {
		if kv, ok := m.kvData[key]; ok {
			kvs = append(kvs, kv)
		}
	} else {
		for k, v := range m.kvData {
			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				kvs = append(kvs, v)
			}
		}
		sort.Slice(kvs, func(i, j int) bool {
			return string(kvs[i].Key) < string(kvs[j].Key)
		})
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
		lease, ok := m.leases[leaseID]
		if !ok || lease.IsExpired() {
			return 0, nil, fmt.Errorf("invalid lease")
		}
	}

	prevKv := m.kvData[key]
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

	m.kvData[key] = kv

	if leaseID != 0 {
		if lease, ok := m.leases[leaseID]; ok {
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true
		}
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
		if kv, ok := m.kvData[key]; ok {
			keysToDelete = append(keysToDelete, key)
			prevKvs = append(prevKvs, kv)
		}
	} else {
		for k, v := range m.kvData {
			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				keysToDelete = append(keysToDelete, k)
				prevKvs = append(prevKvs, v)
			}
		}
	}

	if len(keysToDelete) == 0 {
		return 0, nil, m.revision.Load(), nil
	}

	newRevision := m.revision.Add(1)

	for _, k := range keysToDelete {
		prevKv := m.kvData[k]
		delete(m.kvData, k)
		deleted++

		if prevKv.Lease != 0 {
			if lease, ok := m.leases[prevKv.Lease]; ok {
				delete(lease.Keys, k)
			}
		}
	}

	// NOTE: deleteUnlocked does NOT notify watches - caller must do it after releasing lock
	// This avoids deadlock when called from Txn which holds the lock

	return deleted, prevKvs, newRevision, nil
}

// 保持向后兼容的原有方法
func (m *MemoryEtcd) Lookup(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if kv, ok := m.kvData[key]; ok {
		return string(kv.Value), true
	}
	return "", false
}

func (m *MemoryEtcd) Propose(k string, v string) {
	// 简化实现，直接调用 PutWithLease
	m.PutWithLease(k, v, 0)
}

func (m *MemoryEtcd) GetSnapshot() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// TODO: 实现完整的快照序列化
	var buf strings.Builder
	for key, kv := range m.kvData {
		buf.WriteString(fmt.Sprintf("%s=%s\n", key, string(kv.Value)))
	}
	return []byte(buf.String()), nil
}

// Compact 压缩（暂时简化实现）
func (m *MemoryEtcd) Compact(revision int64) error {
	// TODO: 实现完整的压缩逻辑
	return nil
}
