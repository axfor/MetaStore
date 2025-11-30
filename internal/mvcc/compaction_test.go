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

package mvcc

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDefaultCompactorConfig(t *testing.T) {
	config := DefaultCompactorConfig()

	if !config.Enable {
		t.Error("Enable should be true by default")
	}
	if config.Mode != CompactionModeRevision {
		t.Errorf("Mode = %v, want revision", config.Mode)
	}
	if config.Retention != 1000 {
		t.Errorf("Retention = %d, want 1000", config.Retention)
	}
	if config.Period != time.Hour {
		t.Errorf("Period = %v, want 1h", config.Period)
	}
	if config.BatchSize != 1000 {
		t.Errorf("BatchSize = %d, want 1000", config.BatchSize)
	}
	if config.BatchInterval != 10*time.Millisecond {
		t.Errorf("BatchInterval = %v, want 10ms", config.BatchInterval)
	}
}

func TestCompactorStartStop(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	config := DefaultCompactorConfig()
	compactor := NewCompactor(store, config)

	if compactor.IsRunning() {
		t.Error("Compactor should not be running before Start()")
	}

	compactor.Start()
	if !compactor.IsRunning() {
		t.Error("Compactor should be running after Start()")
	}

	// Start again should be no-op
	compactor.Start()
	if !compactor.IsRunning() {
		t.Error("Compactor should still be running")
	}

	compactor.Stop()
	if compactor.IsRunning() {
		t.Error("Compactor should not be running after Stop()")
	}

	// Stop again should be no-op
	compactor.Stop()
}

func TestCompactorDisabled(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	config := DefaultCompactorConfig()
	config.Enable = false

	var logCalled bool
	config.Logger = func(format string, args ...interface{}) {
		logCalled = true
	}

	compactor := NewCompactor(store, config)
	compactor.Start()

	if compactor.IsRunning() {
		t.Error("Disabled compactor should not be running")
	}
	if !logCalled {
		t.Error("Logger should have been called for disabled message")
	}
}

func TestCompactorForceCompact(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write 2000 revisions
	for i := 0; i < 2000; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	if store.CurrentRevision() != 2000 {
		t.Fatalf("CurrentRevision = %d, want 2000", store.CurrentRevision())
	}

	config := DefaultCompactorConfig()
	config.Retention = 500 // Keep only 500 revisions
	config.Logger = func(format string, args ...interface{}) {}

	compactor := NewCompactor(store, config)

	// Force compact
	ctx := context.Background()
	err := compactor.ForceCompact(ctx)
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	// Should have compacted to 1500 (2000 - 500)
	if store.CompactedRevision() != 1500 {
		t.Errorf("CompactedRevision = %d, want 1500", store.CompactedRevision())
	}

	// Old revision should fail
	_, err = store.Get([]byte("key"), 1000)
	if err != ErrCompacted {
		t.Errorf("Get old revision = %v, want ErrCompacted", err)
	}

	// Recent revision should work
	_, err = store.Get([]byte("key"), 1800)
	if err != nil {
		t.Errorf("Get recent revision failed: %v", err)
	}
}

func TestCompactorMetrics(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write 2000 revisions
	for i := 0; i < 2000; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	config := DefaultCompactorConfig()
	config.Retention = 500
	config.Logger = func(format string, args ...interface{}) {}

	compactor := NewCompactor(store, config)

	// Force compact
	err := compactor.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	metrics := compactor.Metrics()

	if metrics.CompactCount != 1 {
		t.Errorf("CompactCount = %d, want 1", metrics.CompactCount)
	}
	if metrics.CompactDuration <= 0 {
		t.Error("CompactDuration should be positive")
	}
	if metrics.CompactRevisions != 1500 {
		t.Errorf("CompactRevisions = %d, want 1500", metrics.CompactRevisions)
	}
	if metrics.LastCompact.IsZero() {
		t.Error("LastCompact should not be zero")
	}
	if metrics.LastError != nil {
		t.Errorf("LastError = %v, want nil", metrics.LastError)
	}
}

func TestCompactorNoOpWhenNothingToCompact(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write fewer revisions than retention
	for i := 0; i < 100; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	config := DefaultCompactorConfig()
	config.Retention = 1000 // Retention > current revisions
	config.Logger = func(format string, args ...interface{}) {}

	compactor := NewCompactor(store, config)

	// Force compact should be no-op
	err := compactor.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	// Should not have compacted
	if store.CompactedRevision() != 0 {
		t.Errorf("CompactedRevision = %d, want 0", store.CompactedRevision())
	}

	metrics := compactor.Metrics()
	if metrics.CompactCount != 0 {
		t.Errorf("CompactCount = %d, want 0", metrics.CompactCount)
	}
}

func TestRevisionCompactor(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write 2000 revisions
	for i := 0; i < 2000; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	compactor := NewRevisionCompactor(store, 500)
	compactor.config.Logger = func(format string, args ...interface{}) {}

	if compactor.Retention() != 500 {
		t.Errorf("Retention = %d, want 500", compactor.Retention())
	}

	err := compactor.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	if store.CompactedRevision() != 1500 {
		t.Errorf("CompactedRevision = %d, want 1500", store.CompactedRevision())
	}
}

func TestPeriodicCompactor(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write some revisions
	for i := 0; i < 100; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	compactor := NewPeriodicCompactor(store, time.Hour, 50)
	compactor.config.Logger = func(format string, args ...interface{}) {}

	err := compactor.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	// Periodic mode compacts to currentRev - 1
	if store.CompactedRevision() != 99 {
		t.Errorf("CompactedRevision = %d, want 99", store.CompactedRevision())
	}
}

func TestCompactorAutoRun(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write 2000 revisions
	for i := 0; i < 2000; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	var mu sync.Mutex
	var compacted bool

	config := DefaultCompactorConfig()
	config.Retention = 500
	config.Logger = func(format string, args ...interface{}) {
		mu.Lock()
		if format == "mvcc: compaction completed, target_rev=%d, duration=%v, revisions=%d" {
			compacted = true
		}
		mu.Unlock()
	}

	compactor := NewCompactor(store, config)
	compactor.Start()

	// Wait for compaction to run (check interval is 1 minute in revision mode)
	// We'll force compact instead since auto-run would take too long
	err := compactor.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact failed: %v", err)
	}

	compactor.Stop()

	mu.Lock()
	defer mu.Unlock()
	if !compacted {
		t.Error("Compaction should have run")
	}
}

func TestCompactorConcurrency(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Write 2000 revisions
	for i := 0; i < 2000; i++ {
		store.Put([]byte("key"), []byte("value"), 0)
	}

	config := DefaultCompactorConfig()
	config.Retention = 500
	config.Logger = func(format string, args ...interface{}) {}

	compactor := NewCompactor(store, config)

	// Run multiple concurrent compactions
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			compactor.ForceCompact(context.Background())
		}()
	}

	wg.Wait()

	// Should be compacted to 1500
	if store.CompactedRevision() != 1500 {
		t.Errorf("CompactedRevision = %d, want 1500", store.CompactedRevision())
	}
}

func TestCompactorDefaultsFixup(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Test with invalid config values
	config := CompactorConfig{
		Enable:        true,
		Mode:          CompactionModeRevision,
		Retention:     0,  // Invalid
		Period:        0,  // Invalid
		BatchSize:     -1, // Invalid
		BatchInterval: -1, // Invalid
		Logger:        nil,
	}

	compactor := NewCompactor(store, config)

	// Should use default values
	if compactor.config.Retention != 1000 {
		t.Errorf("Retention = %d, want 1000", compactor.config.Retention)
	}
	if compactor.config.Period != time.Hour {
		t.Errorf("Period = %v, want 1h", compactor.config.Period)
	}
	if compactor.config.BatchSize != 1000 {
		t.Errorf("BatchSize = %d, want 1000", compactor.config.BatchSize)
	}
	if compactor.config.BatchInterval != 10*time.Millisecond {
		t.Errorf("BatchInterval = %v, want 10ms", compactor.config.BatchInterval)
	}
	if compactor.config.Logger == nil {
		t.Error("Logger should not be nil")
	}
}
