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

// MemoryEtcd 支持 etcd 语义的内存存储
type MemoryEtcd struct {
	kvData       *ShardedMap                  // 分片 map，支持高并发访问
	revision     atomic.Int64                 // 全局 revision 计数器（无锁 atomic 操作）
	leases       map[int64]*kvstore.Lease     // leaseID -> Lease
	leaseMu      sync.RWMutex                 // 保护 leases map
	watches      map[int64]*watchSubscription // watchID -> subscription
	watchMu      sync.RWMutex                 // 保护 watches map
	txnMu        sync.Mutex                   // 保护事务操作的原子性
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
		kvData:  NewShardedMap(),
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
func (m *MemoryEtcd) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	// 如果 rangeEnd 为空，查询单个键
	if rangeEnd == "" {
		if kv, ok := m.kvData.Get(key); ok {
			kvs = append(kvs, kv)
		}
	} else {
		// 范围查询 - ShardedMap 内部会处理锁和排序
		kvs = m.kvData.Range(key, rangeEnd, limit)
	}

	// 应用 limit（Range 已经处理了，这里是为了计算 more 和 count）
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
		Revision: m.revision.Load(), // ✅ atomic 操作，无需加锁
	}, nil
}

// PutWithLease 存储键值对，可选关联 lease
func (m *MemoryEtcd) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// 验证 lease（如果指定）
	if leaseID != 0 {
		m.leaseMu.RLock()
		lease, ok := m.leases[leaseID]
		if !ok {
			m.leaseMu.RUnlock()
			return 0, nil, fmt.Errorf("lease not found: %d", leaseID)
		}
		// 过期检查
		if lease.IsExpired() {
			m.leaseMu.RUnlock()
			return 0, nil, fmt.Errorf("lease expired: %d", leaseID)
		}
		m.leaseMu.RUnlock()
	}

	// 获取旧值（ShardedMap 内部加锁）
	prevKv, _ := m.kvData.Get(key)

	// 递增 revision（atomic 操作，无需加锁）
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

	// 存储到 ShardedMap（内部加锁）
	m.kvData.Set(key, kv)

	// 如果有 lease，关联 key
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

	// 触发 watch 事件（无需持有锁）
	m.notifyWatches(kvstore.WatchEvent{
		Type:     kvstore.EventTypePut,
		Kv:       kv,
		PrevKv:   prevKv,
		Revision: newRevision,
	})

	return newRevision, prevKv, nil
}

// DeleteRange 删除范围内的键
func (m *MemoryEtcd) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	// 收集要删除的键
	keysToDelete := make([]string, 0)

	if rangeEnd == "" {
		// 删除单个键（ShardedMap 内部加锁）
		if kv, ok := m.kvData.Get(key); ok {
			keysToDelete = append(keysToDelete, key)
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// 范围删除 - 使用 ShardedMap.Range() 收集要删除的键
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

	// 递增 revision（atomic 操作，无需加锁）
	newRevision := m.revision.Add(1)

	// Collect events to send after deletion
	events := make([]kvstore.WatchEvent, 0, len(keysToDelete))

	// 执行删除
	for _, k := range keysToDelete {
		prevKv, _ := m.kvData.Get(k)

		// 从 ShardedMap 删除（内部加锁）
		m.kvData.Delete(k)
		deleted++

		// 从 lease 中移除 key
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

	// 触发 watch 事件（无需持有锁）
	for _, event := range events {
		m.notifyWatches(event)
	}

	return deleted, prevKvs, newRevision, nil
}

// Txn 执行事务
func (m *MemoryEtcd) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// 使用 txnMu 保护事务的原子性
	m.txnMu.Lock()
	defer m.txnMu.Unlock()

	return m.txnUnlocked(cmps, thenOps, elseOps)
}

// txnUnlocked 执行事务（需要持有锁）
func (m *MemoryEtcd) txnUnlocked(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
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

// evaluateCompare 评估比较条件（需要持有 txnMu）
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

// 未加锁的内部方法（需要持有 txnMu）
func (m *MemoryEtcd) rangeUnlocked(key, rangeEnd string, limit int64) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	if rangeEnd == "" {
		if kv, ok := m.kvData.Get(key); ok {
			kvs = append(kvs, kv)
		}
	} else {
		// 使用 ShardedMap.Range() 获取范围内的键值对（内部已排序）
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
		// 使用 ShardedMap.Range() 获取范围内的键值对
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

// 保持向后兼容的原有方法
func (m *MemoryEtcd) Lookup(key string) (string, bool) {
	if kv, ok := m.kvData.Get(key); ok {
		return string(kv.Value), true
	}
	return "", false
}

func (m *MemoryEtcd) Propose(k string, v string) {
	// 简化实现，直接调用 PutWithLease
	m.PutWithLease(context.Background(), k, v, 0)
}

func (m *MemoryEtcd) GetSnapshot() ([]byte, error) {
	// 使用 ShardedMap.GetAll() 获取所有数据（内部加锁）
	allData := m.kvData.GetAll()

	// TODO: 实现完整的快照序列化
	var buf strings.Builder
	for key, kv := range allData {
		buf.WriteString(fmt.Sprintf("%s=%s\n", key, string(kv.Value)))
	}
	return []byte(buf.String()), nil
}

// Compact 压缩指定 revision 之前的历史数据
func (m *MemoryEtcd) Compact(ctx context.Context, revision int64) error {
	// etcd 的 Compact 用于压缩历史版本，清理指定 revision 之前的数据
	//
	// 对于内存存储：
	// 1. 当前不保留 MVCC 历史版本，每次更新直接覆盖
	// 2. 过期 Lease 的清理由 LeaseManager 定期处理
	// 3. 这里只需保持 API 兼容性
	//
	// 未来可扩展：实现 MVCC 历史版本管理和压缩
	// 当前实现：no-op

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
