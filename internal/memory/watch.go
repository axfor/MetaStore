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

// Watch creates a new watch and returns event channel
func (m *MemoryEtcd) Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return m.WatchWithOptions(key, rangeEnd, startRevision, watchID, nil)
}

// WatchWithOptions creates a watch with options
func (m *MemoryEtcd) WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	// Check if watchID already exists
	if _, exists := m.watches[watchID]; exists {
		return nil, fmt.Errorf("watch ID %d already exists", watchID)
	}

	// Create event channel with buffer to prevent blocking
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

	// Create subscription
	sub := &watchSubscription{
		watchID:        watchID,
		key:            key,
		rangeEnd:       rangeEnd,
		startRev:       startRevision,
		eventCh:        eventCh,
		cancel:         make(chan struct{}),
		slowSendSem:    make(chan struct{}, 8), // cap concurrent slowSend goroutines per watcher
		prevKV:         prevKV,
		progressNotify: progressNotify,
		filters:        filters,
		fragment:       fragment,
	}

	m.watches[watchID] = sub

	// If startRevision > 0, send historical events
	// Note: Current implementation is simplified, uses current data as initial snapshot
	if startRevision > 0 && startRevision < m.getRevision() {
		// Asynchronously send all matching keys as PUT events
		go m.sendHistoricalEvents(sub, key, rangeEnd)
	}

	return eventCh, nil
}

// sendHistoricalEvents sends historical events from current data snapshot
func (m *MemoryEtcd) sendHistoricalEvents(sub *watchSubscription, key, rangeEnd string) {
	// Recover from panic if eventCh is closed before cancel is signaled
	defer func() {
		if r := recover(); r != nil {
			log.Warn("Watch channel closed during historical event send",
				zap.Int64("watchID", sub.watchID),
				zap.String("key", key),
				zap.String("component", "watch"))
		}
	}()

	// Use ShardedMap.GetAll() to get all data (internally locked)
	allData := m.kvData.GetAll()
	currentRev := m.getRevision() // Capture current revision

	foundAny := false
	// Get all matching keys
	for k, kv := range allData {
		if m.matchWatch(k, key, rangeEnd) {
			foundAny = true
			// Only send events for revisions >= startRev
			if kv.ModRevision >= sub.startRev {
				event := kvstore.WatchEvent{
					Type:     kvstore.EventTypePut,
					Kv:       kv,
					PrevKv:   nil, // Historical events don't return prevKv
					Revision: kv.ModRevision,
				}

				// Non-blocking send
				select {
				case sub.eventCh <- event:
					// Successfully sent
				case <-sub.cancel:
					// Watch already cancelled
					return
				default:
					// Channel full, skip event
					log.Warn("Watch channel full, skipping historical event",
						zap.Int64("watchID", sub.watchID),
						zap.String("key", k),
						zap.String("component", "watch"))
				}
			}
		}
	}

	// CRITICAL FIX: If watching a specific key (not a range) and it doesn't exist,
	// send a DELETE event to indicate the key was deleted
	// This solves the race condition where Watch starts after a key is deleted
	if !foundAny && rangeEnd == "" {
		// Single key watch, key doesn't exist - likely was deleted
		// Send a synthetic DELETE event so watchers don't wait forever
		event := kvstore.WatchEvent{
			Type: kvstore.EventTypeDelete,
			Kv: &kvstore.KeyValue{
				Key:            []byte(key),
				Value:          nil,
				CreateRevision: 0,
				ModRevision:    currentRev, // Use current revision
				Version:        0,
				Lease:          0,
			},
			PrevKv:   nil,
			Revision: currentRev, // Use current revision
		}

		select {
		case sub.eventCh <- event:
			// Successfully sent DELETE notification
		case <-sub.cancel:
			// Watch cancelled
			return
		default:
			// Channel full
			log.Warn("Watch channel full, skipping synthetic DELETE event",
				zap.Int64("watchID", sub.watchID),
				zap.String("key", key),
				zap.String("component", "watch"))
		}
	}
}

// CancelWatch cancels a watch
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
		// Close cancel first to unblock in-flight slowSendEvent goroutines
		close(sub.cancel)
		// Drain semaphore to wait for all slowSendEvent goroutines to finish
		if sub.slowSendSem != nil {
			for i := 0; i < cap(sub.slowSendSem); i++ {
				sub.slowSendSem <- struct{}{}
			}
		}
		// Now safe to close eventCh — no goroutines are sending to it
		close(sub.eventCh)
	})

	return nil
}

// notifyWatches notifies all matching watches (high-performance lock-free version)
func (m *MemoryEtcd) notifyWatches(event kvstore.WatchEvent) {
	// Recover from panic if eventCh is closed during send
	defer func() {
		if r := recover(); r != nil {
			log.Warn("Watch channel closed during event notification",
				zap.Any("recover", r),
				zap.String("component", "watch"))
		}
	}()
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
		// Re-check closed flag — CancelWatch may have fired between calls
		if sub.closed.Load() {
			continue
		}

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
			// Watch already cancelled
		default:
			// Channel full — use semaphore to bound goroutines
			select {
			case sub.slowSendSem <- struct{}{}:
				go func() {
					defer func() { <-sub.slowSendSem }()
					m.slowSendEvent(sub, eventToSend)
				}()
			default:
				// Semaphore full — watcher is severely behind, force cancel.
				// Mark closed immediately so subsequent notifyWatches calls
				// on the same goroutine skip this watcher (prevents send-on-closed race).
				if sub.closed.CompareAndSwap(false, true) {
					log.Warn("Watch severely behind, force cancelling",
						zap.Int64("watch_id", sub.watchID),
						zap.String("component", "memory-watch"))
					go func(id int64, s *watchSubscription) {
						// Remove from watches map first
						m.watchMu.Lock()
						delete(m.watches, id)
						m.watchMu.Unlock()

						s.closeOnce.Do(func() {
							// Close cancel first to unblock in-flight slowSendEvent goroutines
							close(s.cancel)
							// Drain semaphore to wait for all slowSendEvent goroutines to finish
							for i := 0; i < cap(s.slowSendSem); i++ {
								s.slowSendSem <- struct{}{}
							}
							// Now safe to close eventCh — no goroutines are sending to it
							close(s.eventCh)
						})
					}(sub.watchID, sub)
				}
			}
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
	// Check if already closed before attempting to send
	if sub.closed.Load() {
		return
	}

	// Recover from panic if eventCh is closed during send (safety net)
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed, watch was cancelled - this is normal during cleanup
		}
	}()

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

// matchWatch checks if key matches watch range
func (m *MemoryEtcd) matchWatch(key, watchKey, rangeEnd string) bool {
	if rangeEnd == "" {
		// Single key match
		return key == watchKey
	}
	// Range match
	return key >= watchKey && (rangeEnd == "\x00" || key < rangeEnd)
}

// LeaseGrant creates a new lease
func (m *MemoryEtcd) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()

	// Check if lease already exists
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

// LeaseRevoke revokes a lease and deletes all associated keys
func (m *MemoryEtcd) LeaseRevoke(ctx context.Context, id int64) error {
	m.leaseMu.Lock()

	lease, ok := m.leases[id]
	if !ok {
		m.leaseMu.Unlock()
		return fmt.Errorf("lease not found: %d", id)
	}

	// Collect events to send after releasing lock
	events := make([]kvstore.WatchEvent, 0, len(lease.Keys))

	// Delete all associated keys
	for key := range lease.Keys {
		if kv, exists := m.kvData.Get(key); exists {
			// Increase revision
			newRevision := m.nextRevision()

			// Delete key (ShardedMap has internal lock)
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

	// Delete lease
	delete(m.leases, id)

	// Release lock before notifying watches (data is already committed)
	m.leaseMu.Unlock()

	// Trigger watch events
	for _, event := range events {
		m.notifyWatches(event)
	}

	return nil
}

// LeaseRenew renews a lease
func (m *MemoryEtcd) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.leaseMu.Lock()
	defer m.leaseMu.Unlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// Renew the lease
	lease.Renew(lease.TTL)
	return lease, nil
}

// LeaseTimeToLive gets lease remaining time
func (m *MemoryEtcd) LeaseTimeToLive(ctx context.Context, id int64) (*kvstore.Lease, error) {
	m.leaseMu.RLock()
	defer m.leaseMu.RUnlock()

	lease, ok := m.leases[id]
	if !ok {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// Return lease copy
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

// Leases returns all leases
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
