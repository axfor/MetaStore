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
	"fmt"
	"log"
	"metaStore/internal/kvstore"
	"time"
)

// Watch 创建一个 watch，返回事件通道
func (m *MemoryEtcd) Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return m.WatchWithOptions(key, rangeEnd, startRevision, watchID, nil)
}

// WatchWithOptions 创建带选项的 watch
func (m *MemoryEtcd) WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if watchID already exists
	if _, exists := m.watches[watchID]; exists {
		return nil, fmt.Errorf("watch ID %d already exists", watchID)
	}

	// 创建事件通道（带缓冲以避免阻塞）
	eventCh := make(chan kvstore.WatchEvent, 100)

	// Parse options
	var prevKV, progressNotify, fragment bool
	var filters []kvstore.WatchFilterType
	if opts != nil {
		prevKV = opts.PrevKV
		progressNotify = opts.ProgressNotify
		filters = opts.Filters
		fragment = opts.Fragment
	}

	// 创建订阅
	sub := &watchSubscription{
		watchID:        watchID,
		key:            key,
		rangeEnd:       rangeEnd,
		startRev:       startRevision,
		eventCh:        eventCh,
		cancel:         make(chan struct{}),
		prevKV:         prevKV,
		progressNotify: progressNotify,
		filters:        filters,
		fragment:       fragment,
	}

	m.watches[watchID] = sub

	// 如果 startRevision > 0，发送历史事件
	// 注意：当前实现不保留完整历史，只能从当前数据生成初始快照
	if startRevision > 0 && startRevision < m.revision.Load() {
		// 异步发送当前所有匹配的键作为 PUT 事件
		go m.sendHistoricalEvents(sub, key, rangeEnd)
	}

	return eventCh, nil
}

// sendHistoricalEvents 发送历史事件（从当前数据快照）
func (m *MemoryEtcd) sendHistoricalEvents(sub *watchSubscription, key, rangeEnd string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 获取所有匹配的键
	for k, kv := range m.kvData {
		if m.matchWatch(k, key, rangeEnd) {
			event := kvstore.WatchEvent{
				Type:     kvstore.EventTypePut,
				Kv:       kv,
				PrevKv:   nil, // 历史事件不返回 prevKv
				Revision: kv.ModRevision,
			}

			// 非阻塞发送
			select {
			case sub.eventCh <- event:
				// 成功发送
			case <-sub.cancel:
				// Watch 已取消
				return
			default:
				// Channel 满了，跳过此事件
				log.Printf("Watch %d channel full, skipping historical event for key %s", sub.watchID, k)
			}
		}
	}
}

// CancelWatch 取消一个 watch
func (m *MemoryEtcd) CancelWatch(watchID int64) error {
	m.mu.Lock()
	sub, ok := m.watches[watchID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("watch not found: %d", watchID)
	}

	// Check if already closed
	if !sub.closed.CompareAndSwap(false, true) {
		m.mu.Unlock()
		return nil // Already cancelled
	}

	// Remove from map
	delete(m.watches, watchID)
	m.mu.Unlock()

	// Close channels only once using sync.Once
	sub.closeOnce.Do(func() {
		close(sub.cancel)
		close(sub.eventCh)
	})

	return nil
}

// notifyWatches 通知所有匹配的 watch (high-performance lock-free version)
func (m *MemoryEtcd) notifyWatches(event kvstore.WatchEvent) {
	key := ""
	if event.Kv != nil {
		key = string(event.Kv.Key)
	} else if event.PrevKv != nil {
		key = string(event.PrevKv.Key)
	}

	// Fast path: copy matching subscriptions (minimal lock time)
	m.mu.RLock()
	matchingSubs := make([]*watchSubscription, 0, len(m.watches))
	for _, sub := range m.watches {
		if sub.closed.Load() {
			continue // Skip closed watches
		}
		if m.matchWatch(key, sub.key, sub.rangeEnd) {
			matchingSubs = append(matchingSubs, sub)
		}
	}
	m.mu.RUnlock()

	// Send events outside of lock
	for _, sub := range matchingSubs {
		// Apply filters
		if m.shouldFilter(event.Type, sub.filters) {
			continue
		}

		// Prepare event based on prevKV option
		eventToSend := event
		if !sub.prevKV {
			eventToSend.PrevKv = nil
		}

		// Non-blocking send with slow client handling
		select {
		case sub.eventCh <- eventToSend:
			// Success
		case <-sub.cancel:
			// Watch已取消
		default:
			// Channel满了，异步发送（慢客户端）
			go m.slowSendEvent(sub, eventToSend)
		}
	}
}

// shouldFilter checks if event should be filtered out
func (m *MemoryEtcd) shouldFilter(eventType kvstore.EventType, filters []kvstore.WatchFilterType) bool {
	for _, f := range filters {
		switch f {
		case kvstore.FilterNoPut:
			if eventType == kvstore.EventTypePut {
				return true
			}
		case kvstore.FilterNoDelete:
			if eventType == kvstore.EventTypeDelete {
				return true
			}
		}
	}
	return false
}

// slowSendEvent handles slow clients with timeout
func (m *MemoryEtcd) slowSendEvent(sub *watchSubscription, event kvstore.WatchEvent) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case sub.eventCh <- event:
		// Successfully sent after retry
	case <-sub.cancel:
		// Watch cancelled
	case <-timer.C:
		// Timeout - force cancel this slow watch
		log.Printf("Watch %d is too slow, force cancelling", sub.watchID)
		m.CancelWatch(sub.watchID)
	}
}

// matchWatch 检查 key 是否匹配 watch 范围
func (m *MemoryEtcd) matchWatch(key, watchKey, rangeEnd string) bool {
	if rangeEnd == "" {
		// 单键匹配
		return key == watchKey
	}
	// 范围匹配
	return key >= watchKey && (rangeEnd == "\x00" || key < rangeEnd)
}

// LeaseGrant 创建一个新的 lease
func (m *MemoryEtcd) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查 lease 是否已存在
	if _, ok := m.leases[id]; ok {
		return nil, fmt.Errorf("lease already exists: %d", id)
	}

	lease := &kvstore.Lease{
		ID:        id,
		TTL:       ttl,
		GrantTime: time.Now(),
		Keys:      make(map[string]bool),
	}

	m.leases[id] = lease
	return lease, nil
}

// LeaseRevoke 撤销一个 lease（删除所有关联的键）
func (m *MemoryEtcd) LeaseRevoke(ctx context.Context, id int64) error {
	m.mu.Lock()

	lease, ok := m.leases[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("lease not found: %d", id)
	}

	// Collect events to send after releasing lock
	events := make([]kvstore.WatchEvent, 0, len(lease.Keys))

	// 删除所有关联的键
	for key := range lease.Keys {
		if kv, exists := m.kvData[key]; exists {
			// 递增 revision
			newRevision := m.revision.Add(1)

			// 删除键
			delete(m.kvData, key)

			// Prepare watch event
			// For DELETE events, Kv contains the deleted key with ModRevision set to deletion revision
			deletedKv := &kvstore.KeyValue{
				Key:            kv.Key,
				Value:          nil, // Value is nil for deleted key
				CreateRevision: kv.CreateRevision,
				ModRevision:    newRevision, // Set to deletion revision
				Version:        0,           // Version is 0 for deleted key
				Lease:          0,
			}
			events = append(events, kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   kv,
				Revision: newRevision,
			})
		}
	}

	// 删除 lease
	delete(m.leases, id)

	// Release lock before notifying watches (data is already committed)
	m.mu.Unlock()

	// 触发 watch 事件
	for _, event := range events {
		m.notifyWatches(event)
	}

	return nil
}

// LeaseRenew 续约一个 lease
func (m *MemoryEtcd) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// 续约
	lease.Renew(lease.TTL)
	return lease, nil
}

// LeaseTimeToLive 获取 lease 的剩余时间
func (m *MemoryEtcd) LeaseTimeToLive(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// 返回 lease 的副本
	leaseCopy := &kvstore.Lease{
		ID:        lease.ID,
		TTL:       lease.TTL,
		GrantTime: lease.GrantTime,
		Keys:      make(map[string]bool),
	}
	for k := range lease.Keys {
		leaseCopy.Keys[k] = true
	}

	return leaseCopy, nil
}

// Leases 返回所有 lease
func (m *MemoryEtcd) Leases(ctx context.Context) ([]*kvstore.Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	leases := make([]*kvstore.Lease, 0, len(m.leases))
	for _, lease := range m.leases {
		leaseCopy := &kvstore.Lease{
			ID:        lease.ID,
			TTL:       lease.TTL,
			GrantTime: lease.GrantTime,
			Keys:      make(map[string]bool),
		}
		for k := range lease.Keys {
			leaseCopy.Keys[k] = true
		}
		leases = append(leases, leaseCopy)
	}

	return leases, nil
}
