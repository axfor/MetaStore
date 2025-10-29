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

package metrics

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// MetricsInterceptor provides gRPC interceptors with metrics collection
type MetricsInterceptor struct {
	metrics *Metrics
}

// NewMetricsInterceptor creates a new metrics interceptor
func NewMetricsInterceptor(m *Metrics) *MetricsInterceptor {
	return &MetricsInterceptor{
		metrics: m,
	}
}

// UnaryServerInterceptor returns a unary RPC interceptor with metrics collection
func (mi *MetricsInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Record in-flight request
		mi.metrics.GrpcRequestInFlight.WithLabelValues(info.FullMethod).Inc()
		defer mi.metrics.GrpcRequestInFlight.WithLabelValues(info.FullMethod).Dec()

		// Record start time
		start := time.Now()

		// Execute handler
		resp, err := handler(ctx, req)

		// Record duration and status
		duration := time.Since(start)
		code := status.Code(err).String()
		mi.metrics.RecordGrpcRequest(info.FullMethod, code, duration)

		return resp, err
	}
}

// StreamServerInterceptor returns a stream RPC interceptor with metrics collection
func (mi *MetricsInterceptor) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Record in-flight request
		mi.metrics.GrpcRequestInFlight.WithLabelValues(info.FullMethod).Inc()
		defer mi.metrics.GrpcRequestInFlight.WithLabelValues(info.FullMethod).Dec()

		// Record start time
		start := time.Now()

		// Execute handler
		err := handler(srv, ss)

		// Record duration and status
		duration := time.Since(start)
		code := status.Code(err).String()
		mi.metrics.RecordGrpcRequest(info.FullMethod, code, duration)

		return err
	}
}
