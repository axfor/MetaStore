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

package reliability

import (
	"context"
	"fmt"
	"metaStore/pkg/log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ResourceLimits resource limits configuration
type ResourceLimits struct {
	MaxConnections    int64 // Maximum number of connections
	MaxRequests       int64 // Maximum concurrent requests
	MaxMemoryBytes    int64 // Maximum memory usage (bytes)
	MaxRequestSize    int64 // Maximum request size (bytes)
	RequestTimeout    time.Duration
	ConnectionTimeout time.Duration
}

// DefaultLimits default resource limits
// Note: These default values should be consistent with config.DefaultConfig
var DefaultLimits = ResourceLimits{
	MaxConnections:    1000,                    // Default max connections
	MaxRequests:       5000,                    // Default max concurrent requests
	MaxMemoryBytes:    8 * 1024 * 1024 * 1024,  // 8GB, suitable for performance tests and production
	MaxRequestSize:    4 * 1024 * 1024,         // 4MB
	RequestTimeout:    30 * time.Second,
	ConnectionTimeout: 10 * time.Second,
}

// ResourceManager resource manager
type ResourceManager struct {
	limits ResourceLimits

	// Current state
	currentConnections int64
	currentRequests    int64

	// Connection management
	connMu      sync.RWMutex
	connections map[string]*Connection

	// Memory monitoring
	memoryCheckInterval time.Duration
	memoryCheckStop     chan struct{}
}

// Connection connection information
type Connection struct {
	ID         string
	RemoteAddr string
	CreatedAt  time.Time
	LastActive time.Time
}

// NewResourceManager creates a resource manager
func NewResourceManager(limits ResourceLimits) *ResourceManager {
	rm := &ResourceManager{
		limits:              limits,
		connections:         make(map[string]*Connection),
		memoryCheckInterval: 10 * time.Second,
		memoryCheckStop:     make(chan struct{}),
	}

	// Start memory monitoring
	go rm.monitorMemory()

	return rm
}

// AcquireConnection acquires a connection permit
func (rm *ResourceManager) AcquireConnection(connID, remoteAddr string) error {
	current := atomic.AddInt64(&rm.currentConnections, 1)
	if current > rm.limits.MaxConnections {
		atomic.AddInt64(&rm.currentConnections, -1)
		return status.Errorf(codes.ResourceExhausted,
			"connection limit exceeded: %d/%d", current, rm.limits.MaxConnections)
	}

	rm.connMu.Lock()
	rm.connections[connID] = &Connection{
		ID:         connID,
		RemoteAddr: remoteAddr,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	rm.connMu.Unlock()

	return nil
}

// ReleaseConnection releases a connection
func (rm *ResourceManager) ReleaseConnection(connID string) {
	rm.connMu.Lock()
	delete(rm.connections, connID)
	rm.connMu.Unlock()

	atomic.AddInt64(&rm.currentConnections, -1)
}

// AcquireRequest acquires a request permit
func (rm *ResourceManager) AcquireRequest(ctx context.Context) (func(), error) {
	current := atomic.AddInt64(&rm.currentRequests, 1)
	if current > rm.limits.MaxRequests {
		atomic.AddInt64(&rm.currentRequests, -1)
		return nil, status.Errorf(codes.ResourceExhausted,
			"request limit exceeded: %d/%d", current, rm.limits.MaxRequests)
	}

	// Return release function
	release := func() {
		atomic.AddInt64(&rm.currentRequests, -1)
	}

	return release, nil
}

// CheckRequestSize checks request size
func (rm *ResourceManager) CheckRequestSize(size int64) error {
	if size > rm.limits.MaxRequestSize {
		return status.Errorf(codes.InvalidArgument,
			"request size exceeds limit: %d bytes > %d bytes", size, rm.limits.MaxRequestSize)
	}
	return nil
}

// CheckMemory checks memory usage
func (rm *ResourceManager) CheckMemory() error {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	if int64(m.Alloc) > rm.limits.MaxMemoryBytes {
		return status.Errorf(codes.ResourceExhausted,
			"memory limit exceeded: %d MB > %d MB",
			m.Alloc/1024/1024, rm.limits.MaxMemoryBytes/1024/1024)
	}

	return nil
}

// monitorMemory monitors memory usage
func (rm *ResourceManager) monitorMemory() {
	ticker := time.NewTicker(rm.memoryCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			usagePercent := float64(m.Alloc) / float64(rm.limits.MaxMemoryBytes) * 100

			// Trigger GC when memory usage exceeds 80%
			if usagePercent > 80 {
				runtime.GC()
			}

			// Alert when memory usage exceeds 90%
			if usagePercent > 90 {
				log.Warn("High memory usage",
					log.String("usage_percent", fmt.Sprintf("%.1f%%", usagePercent)),
					log.Int64("current_mb", int64(m.Alloc/1024/1024)),
					log.Int64("max_mb", rm.limits.MaxMemoryBytes/1024/1024),
					log.Component("resource-manager"))
			}

		case <-rm.memoryCheckStop:
			return
		}
	}
}

// UpdateConnectionActivity updates connection activity time
func (rm *ResourceManager) UpdateConnectionActivity(connID string) {
	rm.connMu.Lock()
	if conn, exists := rm.connections[connID]; exists {
		conn.LastActive = time.Now()
	}
	rm.connMu.Unlock()
}

// GetStats gets resource usage statistics
func (rm *ResourceManager) GetStats() ResourceStats {
	rm.connMu.RLock()
	connCount := len(rm.connections)
	rm.connMu.RUnlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return ResourceStats{
		CurrentConnections: atomic.LoadInt64(&rm.currentConnections),
		MaxConnections:     rm.limits.MaxConnections,
		CurrentRequests:    atomic.LoadInt64(&rm.currentRequests),
		MaxRequests:        rm.limits.MaxRequests,
		MemoryUsageBytes:   int64(m.Alloc),
		MaxMemoryBytes:     rm.limits.MaxMemoryBytes,
		ActiveConnections:  int64(connCount),
	}
}

// ResourceStats resource usage statistics
type ResourceStats struct {
	CurrentConnections int64
	MaxConnections     int64
	CurrentRequests    int64
	MaxRequests        int64
	MemoryUsageBytes   int64
	MaxMemoryBytes     int64
	ActiveConnections  int64
}

// Close closes the resource manager
func (rm *ResourceManager) Close() {
	close(rm.memoryCheckStop)
}

// LimitInterceptor gRPC interceptor for resource limits
func (rm *ResourceManager) LimitInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// Check memory
	if err := rm.CheckMemory(); err != nil {
		return nil, err
	}

	// Acquire request permit
	release, err := rm.AcquireRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Apply request timeout
	if rm.limits.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rm.limits.RequestTimeout)
		defer cancel()
	}

	// Execute handler
	return handler(ctx, req)
}
