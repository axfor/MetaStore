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

package etcd

import (
	"context"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	"sync"
	"sync/atomic"
)

// WatchManager manages all watch subscriptions
type WatchManager struct {
	mu            sync.RWMutex
	store         kvstore.Store
	watches       map[int64]*watchStream // watchID -> stream
	nextID        atomic.Int64           // next watch ID
	stopped       atomic.Bool            // whether stopped
	maxWatchCount int                    // maximum watch count limit (0 means unlimited)
}

// watchStream represents a watch stream
type watchStream struct {
	watchID       int64
	key           string
	rangeEnd      string
	startRevision int64
	eventCh       <-chan kvstore.WatchEvent // receives events from store
	cancel        func()                     // cancel function
}

// NewWatchManager creates a new Watch manager
// Optional parameter cfg is used to set watch count limit
func NewWatchManager(store kvstore.Store, cfg ...*config.LimitsConfig) *WatchManager {
	maxWatches := 0 // default unlimited
	if len(cfg) > 0 && cfg[0] != nil {
		maxWatches = cfg[0].MaxWatchCount
	}

	return &WatchManager{
		store:         store,
		watches:       make(map[int64]*watchStream),
		maxWatchCount: maxWatches,
	}
}

// Create creates a new watch
func (wm *WatchManager) Create(key, rangeEnd string, startRevision int64, opts *kvstore.WatchOptions) int64 {
	watchID := wm.nextID.Add(1)
	return wm.CreateWithID(watchID, key, rangeEnd, startRevision, opts)
}

// CreateWithID creates a watch with specified watchID
func (wm *WatchManager) CreateWithID(watchID int64, key, rangeEnd string, startRevision int64, opts *kvstore.WatchOptions) int64 {
	if wm.stopped.Load() {
		return -1
	}

	// Check watch count limit
	wm.mu.RLock()
	currentCount := len(wm.watches)
	wm.mu.RUnlock()

	if wm.maxWatchCount > 0 && currentCount >= wm.maxWatchCount {
		// Watch limit exceeded
		return -1
	}

	// Check if watchID already exists
	wm.mu.Lock()
	if _, exists := wm.watches[watchID]; exists {
		wm.mu.Unlock()
		return -1 // WatchID already in use
	}
	wm.mu.Unlock()

	// Create watch from store
	var eventCh <-chan kvstore.WatchEvent
	var err error

	// Try to call WatchWithOptions if available
	type watchWithOptions interface {
		WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error)
	}

	if wwo, ok := wm.store.(watchWithOptions); ok && opts != nil {
		eventCh, err = wwo.WatchWithOptions(key, rangeEnd, startRevision, watchID, opts)
	} else {
		eventCh, err = wm.store.Watch(context.Background(), key, rangeEnd, startRevision, watchID)
	}

	if err != nil {
		return -1
	}

	ws := &watchStream{
		watchID:       watchID,
		key:           key,
		rangeEnd:      rangeEnd,
		startRevision: startRevision,
		eventCh:       eventCh,
	}

	wm.mu.Lock()
	wm.watches[watchID] = ws
	wm.mu.Unlock()

	return watchID
}

// Cancel cancels a watch
func (wm *WatchManager) Cancel(watchID int64) error {
	wm.mu.Lock()
	_, ok := wm.watches[watchID]
	if !ok {
		wm.mu.Unlock()
		return ErrWatchCanceled
	}
	delete(wm.watches, watchID)
	wm.mu.Unlock()

	// Cancel watch in store
	return wm.store.CancelWatch(watchID)
}

// GetEventChan gets the event channel for a watch
func (wm *WatchManager) GetEventChan(watchID int64) (<-chan kvstore.WatchEvent, bool) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	ws, ok := wm.watches[watchID]
	if !ok {
		return nil, false
	}
	return ws.eventCh, true
}

// Stop stops all watches
func (wm *WatchManager) Stop() {
	if !wm.stopped.CompareAndSwap(false, true) {
		return
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Cancel all watches
	for watchID := range wm.watches {
		wm.store.CancelWatch(watchID)
	}
	wm.watches = make(map[int64]*watchStream)
}
