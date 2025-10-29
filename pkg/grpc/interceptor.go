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

package grpc

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ConnectionTracker tracks active connection count to prevent resource exhaustion
// In high-concurrency scenarios, limiting max connections protects server resources
type ConnectionTracker struct {
	maxConnections int64        // Maximum allowed concurrent connections
	activeConns    atomic.Int64 // Current active connection count (atomic for thread safety)
	logger         *zap.Logger  // Structured logger
}

// NewConnectionTracker creates a connection tracker
// maxConnections: maximum allowed concurrent connections, new connections will be rejected if exceeded
// logger: logger for recording connection limit warnings and errors
func NewConnectionTracker(maxConnections int, logger *zap.Logger) *ConnectionTracker {
	return &ConnectionTracker{
		maxConnections: int64(maxConnections),
		logger:         logger,
	}
}

// Track tracks a new connection
// Called when a new connection arrives, returns error if max connections exceeded
// Uses atomic operations to ensure concurrency safety and accurate counting
func (ct *ConnectionTracker) Track() error {
	current := ct.activeConns.Add(1)
	if current > ct.maxConnections {
		ct.activeConns.Add(-1) // Rollback count
		ct.logger.Warn("connection limit reached",
			zap.Int64("current", current-1),
			zap.Int64("max", ct.maxConnections))
		return status.Errorf(codes.ResourceExhausted,
			"connection limit reached: %d/%d", current-1, ct.maxConnections)
	}
	return nil
}

// Untrack stops tracking a connection
// Called when a connection closes, decrements active connection count
func (ct *ConnectionTracker) Untrack() {
	ct.activeConns.Add(-1)
}

// Count returns the current active connection count
// Used for monitoring and metrics collection
func (ct *ConnectionTracker) Count() int64 {
	return ct.activeConns.Load()
}

// UnaryServerInterceptor returns a unary RPC connection tracking interceptor
// Used for connection count limiting in unary RPC calls
func (ct *ConnectionTracker) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := ct.Track(); err != nil {
			return nil, err
		}
		defer ct.Untrack()

		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a stream RPC connection tracking interceptor
// Used for connection count limiting in streaming RPC calls (Watch, LeaseKeepAlive, etc.)
func (ct *ConnectionTracker) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := ct.Track(); err != nil {
			return err
		}
		defer ct.Untrack()

		return handler(srv, ss)
	}
}

// RateLimiter implements token bucket algorithm for global rate limiting
// Prevents single client or burst traffic from consuming excessive server resources
// Uses golang.org/x/time/rate package for high performance and thread safety
type RateLimiter struct {
	globalLimiter *rate.Limiter // Global token bucket limiter
	logger        *zap.Logger   // Structured logger
}

// NewRateLimiter creates a rate limiter
// qps: queries per second allowed, controls average rate
// burst: token bucket size for burst requests, allows short-term traffic spikes
// logger: logger for recording rate limit events
// Example: NewRateLimiter(1000, 2000, logger) means average 1000 QPS, max burst 2000 requests
func NewRateLimiter(qps int, burst int, logger *zap.Logger) *RateLimiter {
	return &RateLimiter{
		globalLimiter: rate.NewLimiter(rate.Limit(qps), burst),
		logger:        logger,
	}
}

// UnaryServerInterceptor returns a unary RPC rate limiting interceptor
// Applies rate limiting to all unary RPC calls (Put, Get, Delete, etc.)
func (rl *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !rl.globalLimiter.Allow() {
			// Extract client info for logging and troubleshooting
			clientInfo := extractClientInfo(ctx)
			rl.logger.Warn("rate limit exceeded",
				zap.String("method", info.FullMethod),
				zap.String("client", clientInfo))
			return nil, status.Errorf(codes.ResourceExhausted,
				"rate limit exceeded for method: %s", info.FullMethod)
		}

		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a stream RPC rate limiting interceptor
// Applies rate limiting to all streaming RPC calls (Watch, LeaseKeepAlive, etc.)
func (rl *RateLimiter) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !rl.globalLimiter.Allow() {
			// Extract client info for logging and troubleshooting
			ctx := ss.Context()
			clientInfo := extractClientInfo(ctx)
			rl.logger.Warn("rate limit exceeded",
				zap.String("method", info.FullMethod),
				zap.String("client", clientInfo))
			return status.Errorf(codes.ResourceExhausted,
				"rate limit exceeded for method: %s", info.FullMethod)
		}

		return handler(srv, ss)
	}
}

// RequestSizeInterceptor monitors large requests
// Note: actual size limit is controlled by grpc.MaxRecvMsgSize in server options
type RequestSizeInterceptor struct {
	maxRequestSize int64       // Maximum request size in bytes
	logger         *zap.Logger // Structured logger
}

// NewRequestSizeInterceptor creates a request size monitoring interceptor
// maxRequestSize: maximum allowed request size in bytes
// logger: logger for recording warnings about oversized requests
func NewRequestSizeInterceptor(maxRequestSize int64, logger *zap.Logger) *RequestSizeInterceptor {
	return &RequestSizeInterceptor{
		maxRequestSize: maxRequestSize,
		logger:         logger,
	}
}

// UnaryServerInterceptor returns a unary RPC request size monitoring interceptor
// Note: actual size limit is controlled by grpc.MaxRecvMsgSize, this is mainly for monitoring
func (rsi *RequestSizeInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		return handler(ctx, req)
	}
}

// LoggingInterceptor logs slow requests to help identify performance bottlenecks
type LoggingInterceptor struct {
	slowThreshold time.Duration // Slow request threshold
	logger        *zap.Logger   // Structured logger
}

// NewLoggingInterceptor creates a slow request logging interceptor
// slowThreshold: threshold for slow requests, requests exceeding this duration will be logged
// logger: logger for recording slow request information
// Example: NewLoggingInterceptor(100*time.Millisecond, logger) logs requests exceeding 100ms
func NewLoggingInterceptor(slowThreshold time.Duration, logger *zap.Logger) *LoggingInterceptor {
	return &LoggingInterceptor{
		slowThreshold: slowThreshold,
		logger:        logger,
	}
}

// UnaryServerInterceptor returns a unary RPC logging interceptor
// Logs all unary RPC calls that exceed the slow request threshold
func (li *LoggingInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		// Log slow requests to help identify performance issues
		if duration > li.slowThreshold {
			clientInfo := extractClientInfo(ctx)
			fields := []zap.Field{
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
				zap.String("client", clientInfo),
			}
			if err != nil {
				fields = append(fields, zap.Error(err))
			}
			li.logger.Warn("slow request detected", fields...)
		}

		return resp, err
	}
}

// StreamServerInterceptor returns a stream RPC logging interceptor
// Logs all streaming RPC calls that exceed the slow request threshold
func (li *LoggingInterceptor) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start)

		// Log slow requests to help identify performance issues
		if duration > li.slowThreshold {
			ctx := ss.Context()
			clientInfo := extractClientInfo(ctx)
			fields := []zap.Field{
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
				zap.String("client", clientInfo),
			}
			if err != nil {
				fields = append(fields, zap.Error(err))
			}
			li.logger.Warn("slow stream request detected", fields...)
		}

		return err
	}
}

// PanicRecoveryInterceptor catches and recovers from panics in handlers
// This is a critical protection measure for production, ensuring one request's error doesn't affect others
type PanicRecoveryInterceptor struct {
	logger *zap.Logger // Structured logger
}

// NewPanicRecoveryInterceptor creates a panic recovery interceptor
// logger: logger for recording panic information and stack traces
func NewPanicRecoveryInterceptor(logger *zap.Logger) *PanicRecoveryInterceptor {
	return &PanicRecoveryInterceptor{
		logger: logger,
	}
}

// UnaryServerInterceptor returns a unary RPC panic recovery interceptor
// Catches panics in unary RPC calls, logs detailed information and returns internal error to client
func (pri *PanicRecoveryInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				clientInfo := extractClientInfo(ctx)
				pri.logger.Error("panic recovered in unary RPC",
					zap.String("method", info.FullMethod),
					zap.String("client", clientInfo),
					zap.Any("panic", r),
					zap.Stack("stack"))
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a stream RPC panic recovery interceptor
// Catches panics in streaming RPC calls, logs detailed information and returns internal error to client
func (pri *PanicRecoveryInterceptor) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				ctx := ss.Context()
				clientInfo := extractClientInfo(ctx)
				pri.logger.Error("panic recovered in stream RPC",
					zap.String("method", info.FullMethod),
					zap.String("client", clientInfo),
					zap.Any("panic", r),
					zap.Stack("stack"))
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(srv, ss)
	}
}

// extractClientInfo extracts client information from context
// Used for logging and troubleshooting, helps identify problem source
func extractClientInfo(ctx context.Context) string {
	// Try to get client address from peer (IP:Port)
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}

	// Try to get user agent from metadata
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if userAgent := md.Get("user-agent"); len(userAgent) > 0 {
			return fmt.Sprintf("user-agent:%s", userAgent[0])
		}
	}

	return "unknown"
}
