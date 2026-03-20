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
	"sync"
	"sync/atomic"
	"testing"

	"metaStore/internal/memory"
	"metaStore/pkg/config"
)

func TestCreateWithID_ConcurrentCapacityLimit(t *testing.T) {
	store := memory.NewMemoryEtcd()
	limitsCfg := &config.LimitsConfig{MaxWatchCount: 10}
	wm := NewWatchManager(store, limitsCfg)
	defer wm.Stop()

	var wg sync.WaitGroup
	var successCount atomic.Int64

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			watchID := int64(id + 1)
			result := wm.CreateWithID(watchID, "/test", "", 0, nil)
			if result != -1 {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if successCount.Load() > 10 {
		t.Errorf("capacity exceeded: got %d watches, max is 10", successCount.Load())
	}
}

func TestCreateWithID_DuplicateIDRejected(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	result1 := wm.CreateWithID(42, "/test", "", 0, nil)
	if result1 == -1 {
		t.Fatal("first CreateWithID should succeed")
	}

	result2 := wm.CreateWithID(42, "/test", "", 0, nil)
	if result2 != -1 {
		t.Error("duplicate watchID should be rejected")
	}
}

func TestCreateWithID_ConcurrentDuplicateID(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	var wg sync.WaitGroup
	var successCount atomic.Int64

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := wm.CreateWithID(99, "/test", "", 0, nil)
			if result != -1 {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if successCount.Load() != 1 {
		t.Errorf("exactly 1 should succeed, got %d", successCount.Load())
	}
}

func TestGetEventChan_NilPlaceholder(t *testing.T) {
	store := memory.NewMemoryEtcd()
	wm := NewWatchManager(store)
	defer wm.Stop()

	wm.mu.Lock()
	wm.watches[999] = nil
	wm.mu.Unlock()

	ch, ok := wm.GetEventChan(999)
	if ok {
		t.Error("GetEventChan should return false for nil placeholder")
	}
	if ch != nil {
		t.Error("channel should be nil for placeholder entry")
	}
}
