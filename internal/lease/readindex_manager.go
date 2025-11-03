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

package lease

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ReadIndexRequest represents a read request waiting for confirmation
type ReadIndexRequest struct {
	RequestID string        // Unique request ID
	ReadIndex uint64        // Read index (committedIndex at request time)
	RecvTime  time.Time     // Request received time
	ResponseC chan<- uint64 // Response channel (send readIndex when ready)
}

// ReadIndexManager manages ReadIndex requests and confirmations
type ReadIndexManager struct {
	// Pending read requests
	mu            sync.RWMutex
	pendingReads  map[string]*ReadIndexRequest
	lastApplied   uint64 // Last applied index

	// Statistics
	totalReadIndexReqs atomic.Int64 // Total ReadIndex requests
	fastPathReads      atomic.Int64 // Lease reads (fast path)
	slowPathReads      atomic.Int64 // ReadIndex reads (slow path)
	forwardedReads     atomic.Int64 // Forwarded reads

	// Smart configuration (支持动态扩缩容)
	smartConfig *SmartLeaseConfig // nil 表示总是启用

	logger *zap.Logger
}

// NewReadIndexManager creates a new ReadIndex manager
// smartConfig: 传入 nil 表示总是启用，传入非 nil 则根据智能配置决定
func NewReadIndexManager(smartConfig *SmartLeaseConfig, logger *zap.Logger) *ReadIndexManager {
	return &ReadIndexManager{
		pendingReads: make(map[string]*ReadIndexRequest),
		smartConfig:  smartConfig,
		logger:       logger,
	}
}

// RequestReadIndex submits a ReadIndex request and waits for confirmation
// Returns the read index when it's safe to read
func (rm *ReadIndexManager) RequestReadIndex(ctx context.Context, readIndex uint64) (uint64, error) {
	rm.totalReadIndexReqs.Add(1)
	rm.slowPathReads.Add(1)

	// Check if already applied
	rm.mu.RLock()
	lastApplied := rm.lastApplied
	rm.mu.RUnlock()

	if readIndex <= lastApplied {
		// Already applied, can read immediately
		return readIndex, nil
	}

	// Create request
	requestID := generateRequestID()
	responseC := make(chan uint64, 1)

	req := &ReadIndexRequest{
		RequestID: requestID,
		ReadIndex: readIndex,
		RecvTime:  time.Now(),
		ResponseC: responseC,
	}

	// Register request
	rm.mu.Lock()
	rm.pendingReads[requestID] = req
	rm.mu.Unlock()

	// Wait for confirmation or timeout
	select {
	case confirmedIndex := <-responseC:
		return confirmedIndex, nil
	case <-ctx.Done():
		// Timeout or cancellation
		rm.mu.Lock()
		delete(rm.pendingReads, requestID)
		rm.mu.Unlock()
		return 0, fmt.Errorf("ReadIndex request timeout: %w", ctx.Err())
	}
}

// NotifyApplied should be called when the state machine has applied up to appliedIndex
// This will confirm all pending read requests with readIndex <= appliedIndex
func (rm *ReadIndexManager) NotifyApplied(appliedIndex uint64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Update last applied
	if appliedIndex > rm.lastApplied {
		rm.lastApplied = appliedIndex
	}

	// Confirm all eligible read requests
	for requestID, req := range rm.pendingReads {
		if req.ReadIndex <= appliedIndex {
			// Safe to read now
			select {
			case req.ResponseC <- req.ReadIndex:
				// Successfully sent
			default:
				// Channel full or closed, skip
			}

			delete(rm.pendingReads, requestID)
		}
	}
}

// RecordFastPathRead records a fast path read (lease read)
func (rm *ReadIndexManager) RecordFastPathRead() {
	// 运行时检查：如果智能配置禁用，不记录快速路径
	// 这避免了统计数据的误导性
	if rm.smartConfig != nil && !rm.smartConfig.IsEnabled() {
		return
	}
	rm.fastPathReads.Add(1)
}

// RecordForwardedRead records a forwarded read
func (rm *ReadIndexManager) RecordForwardedRead() {
	rm.forwardedReads.Add(1)
}

// GetPendingCount returns the number of pending read requests
func (rm *ReadIndexManager) GetPendingCount() int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return len(rm.pendingReads)
}

// Stats returns ReadIndex statistics
func (rm *ReadIndexManager) Stats() ReadIndexStats {
	rm.mu.RLock()
	pendingCount := len(rm.pendingReads)
	lastApplied := rm.lastApplied
	rm.mu.RUnlock()

	total := rm.totalReadIndexReqs.Load()
	fastPath := rm.fastPathReads.Load()
	slowPath := rm.slowPathReads.Load()
	forwarded := rm.forwardedReads.Load()

	// Calculate fast path hit rate (based on fast + slow reads, excluding forwarded)
	totalReads := fastPath + slowPath
	var fastPathRate float64
	if totalReads > 0 {
		fastPathRate = float64(fastPath) / float64(totalReads)
	}

	return ReadIndexStats{
		TotalRequests:    total,
		FastPathReads:    fastPath,
		SlowPathReads:    slowPath,
		ForwardedReads:   forwarded,
		PendingReads:     pendingCount,
		LastAppliedIndex: lastApplied,
		FastPathRate:     fastPathRate,
	}
}

// ReadIndexStats contains ReadIndex statistics
type ReadIndexStats struct {
	TotalRequests    int64   // Total read requests
	FastPathReads    int64   // Lease reads (fast path)
	SlowPathReads    int64   // ReadIndex reads (slow path)
	ForwardedReads   int64   // Forwarded reads
	PendingReads     int     // Current pending reads
	LastAppliedIndex uint64  // Last applied index
	FastPathRate     float64 // Fast path hit rate
}

// generateRequestID generates a unique request ID
func generateRequestID() string {
	return fmt.Sprintf("read-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
}

var requestCounter int64
