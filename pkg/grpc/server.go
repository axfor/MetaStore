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
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"metaStore/pkg/config"
	"metaStore/pkg/metrics"
)

// ServerOptionsBuilder builds gRPC server options from configuration
// Constructs production-grade gRPC server options based on config file
type ServerOptionsBuilder struct {
	cfg     *config.Config
	logger  *zap.Logger
	metrics *metrics.Metrics
}

// NewServerOptionsBuilder creates a server options builder
func NewServerOptionsBuilder(cfg *config.Config, logger *zap.Logger) *ServerOptionsBuilder {
	return &ServerOptionsBuilder{
		cfg:    cfg,
		logger: logger,
	}
}

// WithMetrics sets the metrics collector for the builder
func (b *ServerOptionsBuilder) WithMetrics(m *metrics.Metrics) *ServerOptionsBuilder {
	b.metrics = m
	return b
}

// Build builds gRPC server options
// Returns a list of server options with all interceptors and configuration
func (b *ServerOptionsBuilder) Build() []grpc.ServerOption {
	opts := []grpc.ServerOption{
		// 1. Message size limits
		grpc.MaxRecvMsgSize(b.cfg.Server.GRPC.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(b.cfg.Server.GRPC.MaxSendMsgSize),

		// 2. Concurrent stream limits
		grpc.MaxConcurrentStreams(b.cfg.Server.GRPC.MaxConcurrentStreams),

		// 3. Flow control windows
		grpc.InitialWindowSize(b.cfg.Server.GRPC.InitialWindowSize),
		grpc.InitialConnWindowSize(b.cfg.Server.GRPC.InitialConnWindowSize),

		// 4. Keepalive parameters
		// Note: MaxConnectionIdle, MaxConnectionAge, MaxConnectionAgeGrace
		// are set via keepalive.ServerParameters in grpc-go
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:              b.cfg.Server.GRPC.KeepaliveTime,
			Timeout:           b.cfg.Server.GRPC.KeepaliveTimeout,
			MaxConnectionIdle: b.cfg.Server.GRPC.MaxConnectionIdle,
			MaxConnectionAge:  b.cfg.Server.GRPC.MaxConnectionAge,
			MaxConnectionAgeGrace: b.cfg.Server.GRPC.MaxConnectionAgeGrace,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             b.cfg.Server.GRPC.KeepaliveTime,
			PermitWithoutStream: true,
		}),
	}

	// 6. Add interceptor chains
	unaryInterceptors := b.buildUnaryInterceptors()
	streamInterceptors := b.buildStreamInterceptors()

	if len(unaryInterceptors) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))
	}
	if len(streamInterceptors) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(streamInterceptors...))
	}

	return opts
}

// buildUnaryInterceptors builds unary RPC interceptor chain
// Interceptor order matters: Metrics -> Panic Recovery -> Logging -> Connection Tracking -> Rate Limiting -> Business Logic
func (b *ServerOptionsBuilder) buildUnaryInterceptors() []grpc.UnaryServerInterceptor {
	var interceptors []grpc.UnaryServerInterceptor

	// 1. Metrics (first, to measure everything including panic recovery overhead)
	if b.cfg.Server.Monitoring.EnablePrometheus && b.metrics != nil {
		mi := metrics.NewMetricsInterceptor(b.metrics)
		interceptors = append(interceptors, mi.UnaryServerInterceptor())
	}

	// 2. Panic Recovery (outermost layer, catches all panics)
	if b.cfg.Server.Reliability.EnablePanicRecovery {
		pri := NewPanicRecoveryInterceptor(b.logger)
		interceptors = append(interceptors, pri.UnaryServerInterceptor())
	}

	// 3. Slow request logging (after connection tracking, avoids logging rejected requests)
	if b.cfg.Server.Monitoring.SlowRequestThreshold > 0 {
		li := NewLoggingInterceptor(b.cfg.Server.Monitoring.SlowRequestThreshold, b.logger)
		interceptors = append(interceptors, li.UnaryServerInterceptor())
	}

	// 4. Connection tracking (after rate limiting, avoids tracking rate-limited requests)
	if b.cfg.Server.Limits.MaxConnections > 0 {
		ct := NewConnectionTracker(b.cfg.Server.Limits.MaxConnections, b.logger)
		interceptors = append(interceptors, ct.UnaryServerInterceptor())
	}

	// 5. Rate limiting (close to business logic, quickly rejects excessive requests)
	if b.cfg.Server.GRPC.EnableRateLimit &&
		b.cfg.Server.GRPC.RateLimitQPS > 0 &&
		b.cfg.Server.GRPC.RateLimitBurst > 0 {
		rl := NewRateLimiter(
			b.cfg.Server.GRPC.RateLimitQPS,
			b.cfg.Server.GRPC.RateLimitBurst,
			b.logger)
		interceptors = append(interceptors, rl.UnaryServerInterceptor())
	}

	return interceptors
}

// buildStreamInterceptors builds streaming RPC interceptor chain
// Interceptor order is the same as unary RPC: Metrics -> Panic Recovery -> Logging -> Connection Tracking -> Rate Limiting
func (b *ServerOptionsBuilder) buildStreamInterceptors() []grpc.StreamServerInterceptor {
	var interceptors []grpc.StreamServerInterceptor

	// 1. Metrics (first, to measure everything)
	if b.cfg.Server.Monitoring.EnablePrometheus && b.metrics != nil {
		mi := metrics.NewMetricsInterceptor(b.metrics)
		interceptors = append(interceptors, mi.StreamServerInterceptor())
	}

	// 2. Panic Recovery (outermost layer, catches all panics)
	if b.cfg.Server.Reliability.EnablePanicRecovery {
		pri := NewPanicRecoveryInterceptor(b.logger)
		interceptors = append(interceptors, pri.StreamServerInterceptor())
	}

	// 3. Slow request logging
	if b.cfg.Server.Monitoring.SlowRequestThreshold > 0 {
		li := NewLoggingInterceptor(b.cfg.Server.Monitoring.SlowRequestThreshold, b.logger)
		interceptors = append(interceptors, li.StreamServerInterceptor())
	}

	// 4. Connection tracking
	if b.cfg.Server.Limits.MaxConnections > 0 {
		ct := NewConnectionTracker(b.cfg.Server.Limits.MaxConnections, b.logger)
		interceptors = append(interceptors, ct.StreamServerInterceptor())
	}

	// 5. Rate limiting
	if b.cfg.Server.GRPC.EnableRateLimit &&
		b.cfg.Server.GRPC.RateLimitQPS > 0 &&
		b.cfg.Server.GRPC.RateLimitBurst > 0 {
		rl := NewRateLimiter(
			b.cfg.Server.GRPC.RateLimitQPS,
			b.cfg.Server.GRPC.RateLimitBurst,
			b.logger)
		interceptors = append(interceptors, rl.StreamServerInterceptor())
	}

	return interceptors
}

// BuildServer builds a complete gRPC server
// This is a convenience method for creating a gRPC server with all options configured
func BuildServer(cfg *config.Config, logger *zap.Logger) *grpc.Server {
	builder := NewServerOptionsBuilder(cfg, logger)
	opts := builder.Build()

	logger.Info("creating gRPC server",
		zap.Int("max_recv_msg_size", cfg.Server.GRPC.MaxRecvMsgSize),
		zap.Int("max_send_msg_size", cfg.Server.GRPC.MaxSendMsgSize),
		zap.Uint32("max_concurrent_streams", cfg.Server.GRPC.MaxConcurrentStreams),
		zap.Duration("keepalive_time", cfg.Server.GRPC.KeepaliveTime),
		zap.Duration("keepalive_timeout", cfg.Server.GRPC.KeepaliveTimeout),
		zap.Bool("enable_rate_limit", cfg.Server.GRPC.EnableRateLimit),
		zap.Int("rate_limit_qps", cfg.Server.GRPC.RateLimitQPS),
		zap.Int("rate_limit_burst", cfg.Server.GRPC.RateLimitBurst),
		zap.Int("max_connections", cfg.Server.Limits.MaxConnections),
		zap.Bool("enable_panic_recovery", cfg.Server.Reliability.EnablePanicRecovery),
		zap.Duration("slow_request_threshold", cfg.Server.Monitoring.SlowRequestThreshold))

	return grpc.NewServer(opts...)
}
