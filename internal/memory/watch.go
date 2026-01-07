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
	"metaStore/internal/kvstore"
	"metaStore/pkg/log"
	"time"

	"go.uber.org/zap"
)

// Watch createfirst  watch，returneventchannel
func (m *MemoryEtcd) Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return m.WatchWithOptions(key, rangeEnd, startRevision, watchID, nil)
}

// WatchWithOptions createoption watch
func (m *MemoryEtcd) WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	// Check if watchID already exists
	if _, exists := m.watches[watchID]; exists {
		return nil, fmt.Errorf("watch ID %d already exists", watchID)
	}

	// createeventchannel(bufferblocking)
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

	// createsubscribe
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

	// if startRevision > 0，sendevent
	// note：currentimplementnotcomplete，canfromcurrentdatabecomeinitialsnapshot
	if startRevision > 0 && startRevision < m.revision.Load() {
		// asynchronoussendcurrentallmatchkeyas PUT event
		go m.sendHistoricalEvents(sub, key, rangeEnd)
	}

	return eventCh, nil
}

// sendHistoricalEvents sendevent(fromcurrentdatasnapshot)
func (m *MemoryEtcd) sendHistoricalEvents(sub *watchSubscription, key, rangeEnd string) {
	// use ShardedMap.GetAll() getalldata(internallock)
	allData := m.kvData.GetAll()

	// getallmatchkey
	for k, kv := range allData {
		if m.matchWatch(k, key, rangeEnd) {
			event := kvstore.WatchEvent{
				Type:     kvstore.EventTypePut,
				Kv:       kv,
				PrevKv:   nil, // eventnotreturn prevKv
				Revision: kv.ModRevision,
			}

			// non-blockingsend
			select {
			case sub.eventCh <- event:
				// successsend
			case <-sub.cancel:
				// Watch already cancel
				return
			default:
				// Channel full，skipevent
				log.Warn("Watch channel full, skipping historical event",
				zap.Int64("watchID", sub.watchID),
				zap.String("key", k),
				zap.String("component", "watch"))
			}
		}
	}
}

// CancelWatch cancelfirst  watch
func (m *MemoryEtcd) CancelWatch(watchID int64) error {
	m.watchMu.Lock()
	sub, ok := m.watches[watchID]
	if !ok {
		m.watchMu.Unlock()
		return fmt.Errorf("watch not found: %d", watchID)
	}

	// Check if already closed
	if !sub.closed.CompareAndSwap(false, true) {
		m.watchMu.Unlock()
		return nil // Already cancelled
	}

	// Remove from map
	delete(m.watches, watchID)
	m.watchMu.Unlock()

	// Close channels only once using sync.Once
	sub.closeOnce.Do(func() {
		close(sub.cancel)
		close(sub.eventCh)
	})

	return nil
}

// notifyWatches notificationallmatch watch (high-performance lock-free version)
func (m *MemoryEtcd) notifyWatches(event kvstore.WatchEvent) {
	key := ""
	if event.Kv != nil {
		key = string(event.Kv.Key)
	} else if event.PrevKv != nil {
		key = string(event.PrevKv.Key)
	}

	// Fast path: copy matching subscriptions (minimal lock time)
	m.watchMu.RLock()
	matchingSubs := make([]*watchSubscription, 0, len(m.watches))
	for _, sub := range m.watches {
		if sub.closed.Load() {
			continue // Skip closed watches
		}
		if m.matchWatch(key, sub.key, sub.rangeEnd) {
			matchingSubs = append(matchingSubs, sub)
		}
	}
	m.watchMu.RUnlock()

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
			// Watchalready cancel
		default:
			// Channelfull，asynchronoussend(slowclient)
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
		log.Warn("Watch is too slow, force cancelling", zap.Int64("watch_id", sub.watchID), zap.String("component", "memory-watch"))
		m.CancelWatch(sub.watchID)
	}
}

// matchWatch check key isnomatch watch range
func (m *MemoryEtcd) matchWatch(key, watchKey, rangeEnd string) bool {
	if rangeEnd == "" {
		// singlekeymatch
		return key == watchKey
	}
	// rangematch
	return key >= watchKey && (rangeEnd == "\x00" || key < rangeEnd)
}

// LeaseGrant createfirst new lease
func (m *MemoryEtcd) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()

	// check lease isnoexists
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

// LeaseRevoke revokedfirst  lease(deleteallclosekey)
func (m *MemoryEtcd) LeaseRevoke(ctx context.Context, id int64) error {
	m.leaseMu.Lock()

	lease, ok := m.leases[id]
	if !ok {
		m.leaseMu.Unlock()
		return fmt.Errorf("lease not found: %d", id)
	}

	// Collect events to send after releasing lock
	events := make([]kvstore.WatchEvent, 0, len(lease.Keys))

	// deleteallclosekey
	for key := range lease.Keys {
		if kv, exists := m.kvData.Get(key); exists {
			// increase revision
			newRevision := m.revision.Add(1)

			// deletekey(ShardedMap internallock)
			m.kvData.Delete(key)

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

	// delete lease
	delete(m.leases, id)

	// Release lock before notifying watches (data is already committed)
	m.leaseMu.Unlock()

	// trigger watch event
	for _, event := range events {
		m.notifyWatches(event)
	}

	return nil
}

// LeaseRenew renewalfirst  lease
func (m *MemoryEtcd) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// renewal
	lease.Renew(lease.TTL)
	return lease, nil
}

// LeaseTimeToLive get lease time
func (m *MemoryEtcd) LeaseTimeToLive(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.leaseMu.RLock()
	defer m.leaseMu.RUnlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// return lease replica
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

// Leases returnall lease
func (m *MemoryEtcd) Leases(ctx context.Context) ([]*kvstore.Lease, error) {
	m.leaseMu.RLock()
	defer m.leaseMu.RUnlock()

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
