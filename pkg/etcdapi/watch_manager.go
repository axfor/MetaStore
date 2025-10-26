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

package etcdapi

import (
	"metaStore/internal/kvstore"
	"sync"
	"sync/atomic"
)

// WatchManager 管理所有的 watch 订阅
type WatchManager struct {
	mu       sync.RWMutex
	store    kvstore.Store
	watches  map[int64]*watchStream // watchID -> stream
	nextID   atomic.Int64           // 下一个 watch ID
	stopped  atomic.Bool            // 是否已停止
}

// watchStream 表示一个 watch 流
type watchStream struct {
	watchID       int64
	key           string
	rangeEnd      string
	startRevision int64
	eventCh       <-chan kvstore.WatchEvent // 从 store 接收事件
	cancel        func()                     // 取消函数
}

// NewWatchManager 创建新的 Watch 管理器
func NewWatchManager(store kvstore.Store) *WatchManager {
	return &WatchManager{
		store:   store,
		watches: make(map[int64]*watchStream),
	}
}

// Create 创建一个新的 watch
func (wm *WatchManager) Create(key, rangeEnd string, startRevision int64, opts *kvstore.WatchOptions) int64 {
	watchID := wm.nextID.Add(1)
	return wm.CreateWithID(watchID, key, rangeEnd, startRevision, opts)
}

// CreateWithID 使用指定的 watchID 创建 watch
func (wm *WatchManager) CreateWithID(watchID int64, key, rangeEnd string, startRevision int64, opts *kvstore.WatchOptions) int64 {
	if wm.stopped.Load() {
		return -1
	}

	// Check if watchID already exists
	wm.mu.Lock()
	if _, exists := wm.watches[watchID]; exists {
		wm.mu.Unlock()
		return -1 // WatchID already in use
	}
	wm.mu.Unlock()

	// 从 store 创建 watch
	var eventCh <-chan kvstore.WatchEvent
	var err error

	// Try to call WatchWithOptions if available
	type watchWithOptions interface {
		WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error)
	}

	if wwo, ok := wm.store.(watchWithOptions); ok && opts != nil {
		eventCh, err = wwo.WatchWithOptions(key, rangeEnd, startRevision, watchID, opts)
	} else {
		eventCh, err = wm.store.Watch(key, rangeEnd, startRevision, watchID)
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

// Cancel 取消一个 watch
func (wm *WatchManager) Cancel(watchID int64) error {
	wm.mu.Lock()
	_, ok := wm.watches[watchID]
	if !ok {
		wm.mu.Unlock()
		return ErrWatchCanceled
	}
	delete(wm.watches, watchID)
	wm.mu.Unlock()

	// 取消 store 中的 watch
	return wm.store.CancelWatch(watchID)
}

// GetEventChan 获取 watch 的事件通道
func (wm *WatchManager) GetEventChan(watchID int64) (<-chan kvstore.WatchEvent, bool) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	ws, ok := wm.watches[watchID]
	if !ok {
		return nil, false
	}
	return ws.eventCh, true
}

// Stop 停止所有 watch
func (wm *WatchManager) Stop() {
	if !wm.stopped.CompareAndSwap(false, true) {
		return
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()

	// 取消所有 watch
	for watchID := range wm.watches {
		wm.store.CancelWatch(watchID)
	}
	wm.watches = make(map[int64]*watchStream)
}
