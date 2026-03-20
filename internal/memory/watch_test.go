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
	"runtime"
	"testing"
	"time"

	"metaStore/internal/kvstore"
)

func TestSlowWatcherBoundedGoroutines(t *testing.T) {
	m := NewMemoryEtcd()

	// Create a watch (the eventCh has buffer of 100)
	eventCh, err := m.Watch(context.Background(), "/test", "\x00", 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Record baseline goroutine count
	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Fill the eventCh buffer (100 events) + generate many more events
	// Without semaphore: each excess event spawns a goroutine (unbounded)
	// With semaphore: capped at 8 goroutines per watcher
	for i := 0; i < 500; i++ {
		m.notifyWatches(kvstore.WatchEvent{
			Type: kvstore.EventTypePut,
			Kv: &kvstore.KeyValue{
				Key:         []byte("/test/key"),
				Value:       []byte("value"),
				ModRevision: int64(i + 1),
			},
			Revision: int64(i + 1),
		})
	}

	// Give goroutines a moment to spawn
	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	goroutineIncrease := current - baseline

	// With semaphore cap of 8, increase should be small (< 20 to allow margin)
	// Without fix, increase would be ~400 (500 - 100 buffer)
	if goroutineIncrease > 20 {
		t.Errorf("goroutine explosion: baseline=%d, current=%d, increase=%d (expected < 20)",
			baseline, current, goroutineIncrease)
	}

	// Drain to clean up
	go func() {
		for range eventCh {
		}
	}()
	m.CancelWatch(1)
}
