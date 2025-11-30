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
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// CompactionMode defines the auto-compaction mode.
type CompactionMode string

const (
	// CompactionModeRevision compacts based on revision count.
	CompactionModeRevision CompactionMode = "revision"
	// CompactionModePeriodic compacts based on time interval.
	CompactionModePeriodic CompactionMode = "periodic"
)

// CompactorConfig configures the auto compactor.
type CompactorConfig struct {
	// Enable enables auto compaction.
	Enable bool

	// Mode is the compaction mode: "revision" or "periodic".
	Mode CompactionMode

	// Retention is the number of revisions to retain in revision mode.
	// Default: 1000 (compatible with etcd)
	Retention int64

	// Period is the compaction check interval in periodic mode.
	// Default: 1 hour
	Period time.Duration

	// BatchSize is the number of keys to compact in each batch.
	// Default: 1000
	BatchSize int

	// BatchInterval is the interval between batches.
	// Default: 10ms
	BatchInterval time.Duration

	// Logger is the logger for compaction events.
	// If nil, log.Printf is used.
	Logger func(format string, args ...interface{})
}

// DefaultCompactorConfig returns the default compactor configuration.
func DefaultCompactorConfig() CompactorConfig {
	return CompactorConfig{
		Enable:        true,
		Mode:          CompactionModeRevision,
		Retention:     1000, // etcd default
		Period:        time.Hour,
		BatchSize:     1000,
		BatchInterval: 10 * time.Millisecond,
		Logger:        log.Printf,
	}
}

// Compactor manages automatic compaction of the MVCC store.
type Compactor struct {
	config CompactorConfig
	store  Store

	mu           sync.Mutex
	running      bool
	stopCh       chan struct{}
	doneCh       chan struct{}
	lastCompact  time.Time
	lastRevision int64

	// Metrics
	compactCount     int64 // Number of compactions performed
	compactDuration  int64 // Total compaction duration in nanoseconds
	compactRevisions int64 // Total revisions compacted
	lastError        error // Last compaction error
}

// NewCompactor creates a new auto compactor.
func NewCompactor(store Store, config CompactorConfig) *Compactor {
	if config.Retention <= 0 {
		config.Retention = 1000
	}
	if config.Period <= 0 {
		config.Period = time.Hour
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 1000
	}
	if config.BatchInterval <= 0 {
		config.BatchInterval = 10 * time.Millisecond
	}
	if config.Logger == nil {
		config.Logger = log.Printf
	}

	return &Compactor{
		config: config,
		store:  store,
	}
}

// Start starts the auto compactor.
func (c *Compactor) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return
	}

	if !c.config.Enable {
		c.config.Logger("mvcc: auto compaction disabled")
		return
	}

	c.running = true
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})

	go c.run()

	c.config.Logger("mvcc: auto compactor started, mode=%s, retention=%d",
		c.config.Mode, c.config.Retention)
}

// Stop stops the auto compactor.
func (c *Compactor) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	close(c.stopCh)
	c.mu.Unlock()

	<-c.doneCh
	c.config.Logger("mvcc: auto compactor stopped")
}

// IsRunning returns whether the compactor is running.
func (c *Compactor) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// ForceCompact triggers an immediate compaction.
func (c *Compactor) ForceCompact(ctx context.Context) error {
	return c.doCompact(ctx)
}

// Metrics returns compaction metrics.
func (c *Compactor) Metrics() CompactorMetrics {
	return CompactorMetrics{
		CompactCount:     atomic.LoadInt64(&c.compactCount),
		CompactDuration:  time.Duration(atomic.LoadInt64(&c.compactDuration)),
		CompactRevisions: atomic.LoadInt64(&c.compactRevisions),
		LastCompact:      c.lastCompact,
		LastError:        c.lastError,
	}
}

// CompactorMetrics holds compaction metrics.
type CompactorMetrics struct {
	CompactCount     int64
	CompactDuration  time.Duration
	CompactRevisions int64
	LastCompact      time.Time
	LastError        error
}

// run is the main compaction loop.
func (c *Compactor) run() {
	defer close(c.doneCh)

	// Determine check interval based on mode
	var checkInterval time.Duration
	switch c.config.Mode {
	case CompactionModePeriodic:
		checkInterval = c.config.Period
	case CompactionModeRevision:
		// Check every minute for revision-based compaction
		checkInterval = time.Minute
	default:
		checkInterval = time.Minute
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := c.doCompact(ctx); err != nil {
				c.lastError = err
				c.config.Logger("mvcc: compaction failed: %v", err)
			}
			cancel()
		}
	}
}

// doCompact performs the actual compaction based on mode.
func (c *Compactor) doCompact(ctx context.Context) error {
	currentRev := c.store.CurrentRevision()
	compactedRev := c.store.CompactedRevision()

	var targetRev int64

	switch c.config.Mode {
	case CompactionModeRevision:
		// Compact to keep only retention revisions
		targetRev = currentRev - c.config.Retention
		if targetRev <= compactedRev {
			// Nothing to compact
			return nil
		}

	case CompactionModePeriodic:
		// In periodic mode, compact to current - 1
		// This is typically used with time-based retention
		targetRev = currentRev - 1
		if targetRev <= compactedRev {
			return nil
		}

	default:
		return nil
	}

	// Don't compact if target revision is not positive
	if targetRev <= 0 {
		return nil
	}

	start := time.Now()

	c.config.Logger("mvcc: starting compaction, target_rev=%d, current_rev=%d, compacted_rev=%d",
		targetRev, currentRev, compactedRev)

	if err := c.store.Compact(targetRev); err != nil {
		if err == ErrCompacted {
			// Already compacted, not an error
			return nil
		}
		return err
	}

	duration := time.Since(start)
	revisionsCompacted := targetRev - compactedRev

	// Update metrics
	atomic.AddInt64(&c.compactCount, 1)
	atomic.AddInt64(&c.compactDuration, int64(duration))
	atomic.AddInt64(&c.compactRevisions, revisionsCompacted)
	c.lastCompact = time.Now()
	c.lastRevision = targetRev

	c.config.Logger("mvcc: compaction completed, target_rev=%d, duration=%v, revisions=%d",
		targetRev, duration, revisionsCompacted)

	return nil
}

// PeriodicCompactor is a specialized compactor for periodic mode.
type PeriodicCompactor struct {
	*Compactor
	period time.Duration
}

// NewPeriodicCompactor creates a new periodic compactor.
func NewPeriodicCompactor(store Store, period time.Duration, retention int64) *PeriodicCompactor {
	config := DefaultCompactorConfig()
	config.Mode = CompactionModePeriodic
	config.Period = period
	config.Retention = retention

	return &PeriodicCompactor{
		Compactor: NewCompactor(store, config),
		period:    period,
	}
}

// RevisionCompactor is a specialized compactor for revision-based mode.
type RevisionCompactor struct {
	*Compactor
	retention int64
}

// NewRevisionCompactor creates a new revision-based compactor.
func NewRevisionCompactor(store Store, retention int64) *RevisionCompactor {
	config := DefaultCompactorConfig()
	config.Mode = CompactionModeRevision
	config.Retention = retention

	return &RevisionCompactor{
		Compactor: NewCompactor(store, config),
		retention: retention,
	}
}

// Retention returns the retention setting.
func (c *RevisionCompactor) Retention() int64 {
	return c.retention
}
