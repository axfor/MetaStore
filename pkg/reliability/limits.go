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

// ResourceLimits 资源限制配置
type ResourceLimits struct {
	MaxConnections    int64 // 最大连接数
	MaxRequests       int64 // 最大并发请求数
	MaxMemoryBytes    int64 // 最大内存使用（字节）
	MaxRequestSize    int64 // 最大请求大小（字节）
	RequestTimeout    time.Duration
	ConnectionTimeout time.Duration
}

// DefaultLimits 默认资源限制
var DefaultLimits = ResourceLimits{
	MaxConnections:    10000,
	MaxRequests:       5000,
	MaxMemoryBytes:    2 * 1024 * 1024 * 1024, // 2GB
	MaxRequestSize:    4 * 1024 * 1024,         // 4MB
	RequestTimeout:    30 * time.Second,
	ConnectionTimeout: 10 * time.Second,
}

// ResourceManager 资源管理器
type ResourceManager struct {
	limits ResourceLimits

	// 当前状态
	currentConnections int64
	currentRequests    int64

	// 连接管理
	connMu      sync.RWMutex
	connections map[string]*Connection

	// 内存监控
	memoryCheckInterval time.Duration
	memoryCheckStop     chan struct{}
}

// Connection 连接信息
type Connection struct {
	ID         string
	RemoteAddr string
	CreatedAt  time.Time
	LastActive time.Time
}

// NewResourceManager 创建资源管理器
func NewResourceManager(limits ResourceLimits) *ResourceManager {
	rm := &ResourceManager{
		limits:              limits,
		connections:         make(map[string]*Connection),
		memoryCheckInterval: 10 * time.Second,
		memoryCheckStop:     make(chan struct{}),
	}

	// 启动内存监控
	go rm.monitorMemory()

	return rm
}

// AcquireConnection 获取连接许可
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

// ReleaseConnection 释放连接
func (rm *ResourceManager) ReleaseConnection(connID string) {
	rm.connMu.Lock()
	delete(rm.connections, connID)
	rm.connMu.Unlock()

	atomic.AddInt64(&rm.currentConnections, -1)
}

// AcquireRequest 获取请求许可
func (rm *ResourceManager) AcquireRequest(ctx context.Context) (func(), error) {
	current := atomic.AddInt64(&rm.currentRequests, 1)
	if current > rm.limits.MaxRequests {
		atomic.AddInt64(&rm.currentRequests, -1)
		return nil, status.Errorf(codes.ResourceExhausted,
			"request limit exceeded: %d/%d", current, rm.limits.MaxRequests)
	}

	// 返回释放函数
	release := func() {
		atomic.AddInt64(&rm.currentRequests, -1)
	}

	return release, nil
}

// CheckRequestSize 检查请求大小
func (rm *ResourceManager) CheckRequestSize(size int64) error {
	if size > rm.limits.MaxRequestSize {
		return status.Errorf(codes.InvalidArgument,
			"request size exceeds limit: %d bytes > %d bytes", size, rm.limits.MaxRequestSize)
	}
	return nil
}

// CheckMemory 检查内存使用
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

// monitorMemory 监控内存使用
func (rm *ResourceManager) monitorMemory() {
	ticker := time.NewTicker(rm.memoryCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			usagePercent := float64(m.Alloc) / float64(rm.limits.MaxMemoryBytes) * 100

			// 当内存使用超过 80% 时触发 GC
			if usagePercent > 80 {
				runtime.GC()
			}

			// 当内存使用超过 90% 时告警
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

// UpdateConnectionActivity 更新连接活动时间
func (rm *ResourceManager) UpdateConnectionActivity(connID string) {
	rm.connMu.Lock()
	if conn, exists := rm.connections[connID]; exists {
		conn.LastActive = time.Now()
	}
	rm.connMu.Unlock()
}

// GetStats 获取资源使用统计
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

// ResourceStats 资源使用统计
type ResourceStats struct {
	CurrentConnections int64
	MaxConnections     int64
	CurrentRequests    int64
	MaxRequests        int64
	MemoryUsageBytes   int64
	MaxMemoryBytes     int64
	ActiveConnections  int64
}

// Close 关闭资源管理器
func (rm *ResourceManager) Close() {
	close(rm.memoryCheckStop)
}

// LimitInterceptor gRPC 拦截器
func (rm *ResourceManager) LimitInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// 检查内存
	if err := rm.CheckMemory(); err != nil {
		return nil, err
	}

	// 获取请求许可
	release, err := rm.AcquireRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// 应用请求超时
	if rm.limits.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rm.limits.RequestTimeout)
		defer cancel()
	}

	// 执行处理器
	return handler(ctx, req)
}
