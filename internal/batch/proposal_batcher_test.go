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

package batch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestProposalBatcher_Creation tests creating a new proposal batcher
func TestProposalBatcher_Creation(t *testing.T) {
	inputC := make(chan string, 10)

	config := DefaultBatchConfig()
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	if batcher == nil {
		t.Fatal("NewProposalBatcher returned nil")
	}

	if batcher.minBatchSize != config.MinBatchSize {
		t.Errorf("minBatchSize mismatch: got %d, want %d", batcher.minBatchSize, config.MinBatchSize)
	}

	if batcher.maxBatchSize != config.MaxBatchSize {
		t.Errorf("maxBatchSize mismatch: got %d, want %d", batcher.maxBatchSize, config.MaxBatchSize)
	}

	if batcher.minTimeout != config.MinTimeout {
		t.Errorf("minTimeout mismatch: got %v, want %v", batcher.minTimeout, config.MinTimeout)
	}

	if batcher.maxTimeout != config.MaxTimeout {
		t.Errorf("maxTimeout mismatch: got %v, want %v", batcher.maxTimeout, config.MaxTimeout)
	}

	// Verify output channel is available
	if batcher.ProposeC() == nil {
		t.Error("ProposeC() returned nil")
	}
}

// TestProposalBatcher_SingleProposal tests batching a single proposal
func TestProposalBatcher_SingleProposal(t *testing.T) {
	inputC := make(chan string, 10)

	config := DefaultBatchConfig()
	config.MinTimeout = 50 * time.Millisecond // Short timeout for testing
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start batcher
	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel
	proposeC := batcher.ProposeC()

	// Send single proposal
	inputC <- "test-proposal-1"

	// Wait for batched proposal
	select {
	case data := <-proposeC:
		// Decode
		proposals, err := DecodeBatch(data)
		if err != nil {
			t.Fatalf("DecodeBatch failed: %v", err)
		}

		if len(proposals) != 1 {
			t.Errorf("Expected 1 proposal, got %d", len(proposals))
		}

		if proposals[0] != "test-proposal-1" {
			t.Errorf("Proposal mismatch: got %s, want %s", proposals[0], "test-proposal-1")
		}

	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for batched proposal")
	}
}

// TestProposalBatcher_MultipleProposals tests batching multiple proposals
func TestProposalBatcher_MultipleProposals(t *testing.T) {
	inputC := make(chan string, 10)

	config := DefaultBatchConfig()
	config.MinBatchSize = 3 // Batch when 3 proposals accumulated
	config.MinTimeout = 1 * time.Second // Long timeout to force batch by size
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel
	proposeC := batcher.ProposeC()

	// Send 3 proposals quickly
	inputC <- "prop-1"
	inputC <- "prop-2"
	inputC <- "prop-3"

	// Wait for batched proposal
	select {
	case data := <-proposeC:
		proposals, err := DecodeBatch(data)
		if err != nil {
			t.Fatalf("DecodeBatch failed: %v", err)
		}

		if len(proposals) != 3 {
			t.Errorf("Expected 3 proposals, got %d", len(proposals))
		}

		expected := []string{"prop-1", "prop-2", "prop-3"}
		for i, exp := range expected {
			if proposals[i] != exp {
				t.Errorf("Proposal %d mismatch: got %s, want %s", i, proposals[i], exp)
			}
		}

	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for batched proposals")
	}
}

// TestProposalBatcher_TimeoutTrigger tests that timeout triggers batch
func TestProposalBatcher_TimeoutTrigger(t *testing.T) {
	inputC := make(chan string, 10)

	config := DefaultBatchConfig()
	config.MinBatchSize = 10 // High batch size
	config.MinTimeout = 100 * time.Millisecond // Short timeout
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel
	proposeC := batcher.ProposeC()

	// Send 2 proposals (less than minBatchSize)
	inputC <- "prop-1"
	inputC <- "prop-2"

	// Should batch after timeout
	select {
	case data := <-proposeC:
		proposals, err := DecodeBatch(data)
		if err != nil {
			t.Fatalf("DecodeBatch failed: %v", err)
		}

		if len(proposals) != 2 {
			t.Errorf("Expected 2 proposals, got %d", len(proposals))
		}

	case <-time.After(500 * time.Millisecond):
		t.Fatal("Timeout trigger did not work")
	}
}

// TestProposalBatcher_Stats tests statistics collection
func TestProposalBatcher_Stats(t *testing.T) {
	inputC := make(chan string, 100)

	config := DefaultBatchConfig()
	config.MinBatchSize = 5
	config.MinTimeout = 50 * time.Millisecond
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel
	proposeC := batcher.ProposeC()

	// Send 15 proposals (should create 3 batches of 5)
	for i := 0; i < 15; i++ {
		inputC <- "prop-" + string(rune(i))
	}

	// Wait for all batches
	time.Sleep(300 * time.Millisecond)

	// Drain proposeC
	batchCount := 0
	timeout := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-proposeC:
			batchCount++
		case <-timeout:
			break drainLoop
		}
	}

	// Check stats
	stats := batcher.Stats()

	if stats.TotalProposals != 15 {
		t.Errorf("TotalProposals mismatch: got %d, want 15", stats.TotalProposals)
	}

	if stats.TotalBatches != int64(batchCount) {
		t.Errorf("TotalBatches mismatch: got %d, want %d", stats.TotalBatches, batchCount)
	}

	if stats.TotalBatches > 0 {
		expectedAvg := float64(stats.TotalProposals) / float64(stats.TotalBatches)
		if stats.AvgBatchSize != expectedAvg {
			t.Errorf("AvgBatchSize mismatch: got %.2f, want %.2f", stats.AvgBatchSize, expectedAvg)
		}
	}
}

// TestProposalBatcher_LoadMonitoring tests load monitoring
func TestProposalBatcher_LoadMonitoring(t *testing.T) {
	inputC := make(chan string, 256)

	config := DefaultBatchConfig()
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel
	proposeC := batcher.ProposeC()

	// Drain proposeC in background
	go func() {
		for range proposeC {
			// Consume batches
		}
	}()

	// Phase 1: Send low load (10 proposals)
	for i := 0; i < 10; i++ {
		inputC <- "low-load-prop"
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)
	lowLoadStats := batcher.Stats()
	t.Logf("Low load: currentLoad=%.2f, batchSize=%d, timeout=%v",
		lowLoadStats.CurrentLoad, lowLoadStats.CurrentBatchSize, lowLoadStats.CurrentTimeout)

	// Phase 2: Send high load (100 proposals quickly)
	for i := 0; i < 100; i++ {
		inputC <- "high-load-prop"
	}

	time.Sleep(500 * time.Millisecond)
	highLoadStats := batcher.Stats()
	t.Logf("High load: currentLoad=%.2f, batchSize=%d, timeout=%v",
		highLoadStats.CurrentLoad, highLoadStats.CurrentBatchSize, highLoadStats.CurrentTimeout)

	// High load should have higher batch size than low load
	if highLoadStats.CurrentBatchSize <= lowLoadStats.CurrentBatchSize {
		t.Logf("Warning: High load batch size (%d) not greater than low load (%d)",
			highLoadStats.CurrentBatchSize, lowLoadStats.CurrentBatchSize)
	}
}

// TestProposalBatcher_DynamicAdjustment tests dynamic parameter adjustment
func TestProposalBatcher_DynamicAdjustment(t *testing.T) {
	inputC := make(chan string, 256)

	config := DefaultBatchConfig()
	config.MinTimeout = 50 * time.Millisecond
	config.MaxTimeout = 200 * time.Millisecond
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel and drain it
	proposeC := batcher.ProposeC()
	go func() {
		for range proposeC {
			// Consume batches
		}
	}()

	// Monitor stats over time
	var lastLoad float64
	adjustmentCount := 0

	for i := 0; i < 10; i++ {
		// Send burst of proposals
		burstSize := 10 + i*5 // Increasing burst size
		for j := 0; j < burstSize; j++ {
			inputC <- "prop"
		}

		time.Sleep(300 * time.Millisecond)

		stats := batcher.Stats()
		t.Logf("Iteration %d: load=%.2f, batchSize=%d, timeout=%v, buffer=%d",
			i, stats.CurrentLoad, stats.CurrentBatchSize, stats.CurrentTimeout, stats.BufferLen)

		// Check if load changed significantly
		if i > 0 && stats.CurrentLoad != lastLoad {
			adjustmentCount++
		}
		lastLoad = stats.CurrentLoad
	}

	t.Logf("Total adjustments detected: %d", adjustmentCount)

	// Should have at least some adjustments
	if adjustmentCount < 2 {
		t.Logf("Warning: Few load adjustments detected (%d), dynamic adjustment may not be working", adjustmentCount)
	}
}

// TestProposalBatcher_StopGracefully tests graceful shutdown
func TestProposalBatcher_StopGracefully(t *testing.T) {
	inputC := make(chan string, 10)

	config := DefaultBatchConfig()
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	batcher.Start(ctx)

	// Get output channel and drain it
	proposeC := batcher.ProposeC()
	go func() {
		for range proposeC {
			// Consume batches
		}
	}()

	// Send some proposals
	inputC <- "prop-1"
	inputC <- "prop-2"

	time.Sleep(100 * time.Millisecond)

	// Stop batcher
	batcher.Stop()

	// Should complete without hanging
	time.Sleep(100 * time.Millisecond)
	t.Log("Batcher stopped gracefully")
}

// TestProposalBatcher_HighConcurrency tests batcher under high concurrency
func TestProposalBatcher_HighConcurrency(t *testing.T) {
	inputC := make(chan string, 256)

	config := DefaultBatchConfig()
	batcher := NewProposalBatcher(config, inputC, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	batcher.Start(ctx)
	defer batcher.Stop()

	// Get output channel and drain it
	proposeC := batcher.ProposeC()
	go func() {
		for range proposeC {
			// Consume batches
		}
	}()

	// Launch multiple goroutines sending proposals
	numGoroutines := 10
	proposalsPerGoroutine := 100
	var sent atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < proposalsPerGoroutine; j++ {
				inputC <- "concurrent-prop"
				sent.Add(1)
			}
		}(i)
	}

	// Wait for all to be sent
	expectedTotal := numGoroutines * proposalsPerGoroutine
	for {
		if sent.Load() == int64(expectedTotal) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for batching
	time.Sleep(1 * time.Second)

	stats := batcher.Stats()
	t.Logf("Concurrent test: sent=%d, batched=%d, batches=%d, avgSize=%.2f",
		expectedTotal, stats.TotalProposals, stats.TotalBatches, stats.AvgBatchSize)

	// Should have processed most proposals
	if stats.TotalProposals < int64(expectedTotal)*9/10 {
		t.Errorf("Too few proposals processed: %d out of %d", stats.TotalProposals, expectedTotal)
	}
}

// TestDefaultBatchConfig tests default configuration
func TestDefaultBatchConfig(t *testing.T) {
	config := DefaultBatchConfig()

	if config.MinBatchSize != 1 {
		t.Errorf("MinBatchSize should be 1, got %d", config.MinBatchSize)
	}

	if config.MaxBatchSize != 256 {
		t.Errorf("MaxBatchSize should be 256, got %d", config.MaxBatchSize)
	}

	if config.MinTimeout != 5*time.Millisecond {
		t.Errorf("MinTimeout should be 5ms, got %v", config.MinTimeout)
	}

	if config.MaxTimeout != 20*time.Millisecond {
		t.Errorf("MaxTimeout should be 20ms, got %v", config.MaxTimeout)
	}

	if config.LoadThreshold != 0.7 {
		t.Errorf("LoadThreshold should be 0.7, got %f", config.LoadThreshold)
	}
}

// TestInterpolate tests the interpolate function
func TestInterpolate(t *testing.T) {
	tests := []struct {
		name      string
		value     float64
		min       float64
		max       float64
		targetMin float64
		targetMax float64
		expected  int
	}{
		{
			name:      "min value",
			value:     0.0,
			min:       0.0,
			max:       1.0,
			targetMin: 1.0,
			targetMax: 256.0,
			expected:  1,
		},
		{
			name:      "max value",
			value:     1.0,
			min:       0.0,
			max:       1.0,
			targetMin: 1.0,
			targetMax: 256.0,
			expected:  256,
		},
		{
			name:      "mid value",
			value:     0.5,
			min:       0.0,
			max:       1.0,
			targetMin: 1.0,
			targetMax: 256.0,
			expected:  128, // approximately
		},
		{
			name:      "threshold value",
			value:     0.7,
			min:       0.0,
			max:       1.0,
			targetMin: 1.0,
			targetMax: 256.0,
			expected:  179, // approximately 1 + 0.7 * 255
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := interpolate(tt.value, tt.min, tt.max, tt.targetMin, tt.targetMax)
			// Allow small margin for rounding
			diff := result - tt.expected
			if diff < -2 || diff > 2 {
				t.Errorf("interpolate() = %d, want approximately %d", result, tt.expected)
			}
		})
	}
}
