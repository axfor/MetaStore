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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Namespace for all metaStore metrics
const (
	namespace = "metastore"
	subsystem = "server"
)

// Metrics holds all Prometheus metrics for the metaStore server
type Metrics struct {
	// gRPC request metrics
	GrpcRequestDuration *prometheus.HistogramVec
	GrpcRequestTotal    *prometheus.CounterVec
	GrpcRequestInFlight *prometheus.GaugeVec

	// Connection metrics
	ActiveConnections  prometheus.Gauge
	TotalConnections   prometheus.Counter
	RejectedConnections *prometheus.CounterVec

	// Rate limiting metrics
	RateLimitHits *prometheus.CounterVec

	// Storage operation metrics
	StorageOperationDuration *prometheus.HistogramVec
	StorageOperationTotal    *prometheus.CounterVec
	StorageOperationErrors   *prometheus.CounterVec

	// Watch metrics
	ActiveWatches     prometheus.Gauge
	WatchEventsTotal  *prometheus.CounterVec
	WatchCreatedTotal prometheus.Counter
	WatchCanceledTotal prometheus.Counter

	// Lease metrics
	ActiveLeases      prometheus.Gauge
	LeaseGrantedTotal prometheus.Counter
	LeaseRevokedTotal prometheus.Counter
	LeaseExpiredTotal prometheus.Counter

	// Auth metrics
	AuthenticationTotal *prometheus.CounterVec
	AuthorizedTotal     *prometheus.CounterVec
	ActiveSessions      prometheus.Gauge

	// Raft metrics
	RaftAppliedIndex     prometheus.Gauge
	RaftCommittedIndex   prometheus.Gauge
	RaftProposalsTotal   prometheus.Counter
	RaftProposalsFailed  prometheus.Counter
	RaftLeaderChanges    prometheus.Counter

	// MVCC metrics
	CurrentRevision   prometheus.Gauge
	KeysTotal         prometheus.Gauge
	DeletesTotal      prometheus.Counter
	CompactionsTotal  prometheus.Counter

	// Panic recovery metrics
	PanicsRecovered *prometheus.CounterVec
}

// New creates and registers all metrics
func New(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		// gRPC request metrics
		GrpcRequestDuration: promauto.With(registry).NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "grpc",
				Name:      "request_duration_seconds",
				Help:      "Histogram of gRPC request latencies",
				Buckets:   prometheus.DefBuckets, // [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
			},
			[]string{"method", "code"},
		),

		GrpcRequestTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "grpc",
				Name:      "request_total",
				Help:      "Total number of gRPC requests",
			},
			[]string{"method", "code"},
		),

		GrpcRequestInFlight: promauto.With(registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "grpc",
				Name:      "request_in_flight",
				Help:      "Current number of in-flight gRPC requests",
			},
			[]string{"method"},
		),

		// Connection metrics
		ActiveConnections: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "active_connections",
				Help:      "Current number of active connections",
			},
		),

		TotalConnections: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "connections_total",
				Help:      "Total number of connections accepted",
			},
		),

		RejectedConnections: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "rejected_connections_total",
				Help:      "Total number of connections rejected",
			},
			[]string{"reason"}, // "limit_exceeded", "rate_limit", etc.
		),

		// Rate limiting metrics
		RateLimitHits: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "rate_limit_hits_total",
				Help:      "Total number of rate limit hits",
			},
			[]string{"method"},
		),

		// Storage operation metrics
		StorageOperationDuration: promauto.With(registry).NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "operation_duration_seconds",
				Help:      "Histogram of storage operation latencies",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"operation", "status"},
		),

		StorageOperationTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "operation_total",
				Help:      "Total number of storage operations",
			},
			[]string{"operation"},
		),

		StorageOperationErrors: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "operation_errors_total",
				Help:      "Total number of storage operation errors",
			},
			[]string{"operation", "error"},
		),

		// Watch metrics
		ActiveWatches: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "watch",
				Name:      "active_total",
				Help:      "Current number of active watches",
			},
		),

		WatchEventsTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "watch",
				Name:      "events_total",
				Help:      "Total number of watch events sent",
			},
			[]string{"event_type"}, // "put", "delete"
		),

		WatchCreatedTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "watch",
				Name:      "created_total",
				Help:      "Total number of watches created",
			},
		),

		WatchCanceledTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "watch",
				Name:      "canceled_total",
				Help:      "Total number of watches canceled",
			},
		),

		// Lease metrics
		ActiveLeases: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "lease",
				Name:      "active_total",
				Help:      "Current number of active leases",
			},
		),

		LeaseGrantedTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "lease",
				Name:      "granted_total",
				Help:      "Total number of leases granted",
			},
		),

		LeaseRevokedTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "lease",
				Name:      "revoked_total",
				Help:      "Total number of leases revoked",
			},
		),

		LeaseExpiredTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "lease",
				Name:      "expired_total",
				Help:      "Total number of leases expired",
			},
		),

		// Auth metrics
		AuthenticationTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "auth",
				Name:      "authentication_total",
				Help:      "Total number of authentication attempts",
			},
			[]string{"result"}, // "success", "failure"
		),

		AuthorizedTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "auth",
				Name:      "authorized_total",
				Help:      "Total number of authorization checks",
			},
			[]string{"result"}, // "allowed", "denied"
		),

		ActiveSessions: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "auth",
				Name:      "active_sessions",
				Help:      "Current number of active sessions",
			},
		),

		// Raft metrics
		RaftAppliedIndex: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "raft",
				Name:      "applied_index",
				Help:      "Current Raft applied index",
			},
		),

		RaftCommittedIndex: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "raft",
				Name:      "committed_index",
				Help:      "Current Raft committed index",
			},
		),

		RaftProposalsTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "raft",
				Name:      "proposals_total",
				Help:      "Total number of Raft proposals",
			},
		),

		RaftProposalsFailed: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "raft",
				Name:      "proposals_failed_total",
				Help:      "Total number of failed Raft proposals",
			},
		),

		RaftLeaderChanges: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "raft",
				Name:      "leader_changes_total",
				Help:      "Total number of Raft leader changes",
			},
		),

		// MVCC metrics
		CurrentRevision: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "mvcc",
				Name:      "current_revision",
				Help:      "Current MVCC revision",
			},
		),

		KeysTotal: promauto.With(registry).NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "mvcc",
				Name:      "keys_total",
				Help:      "Total number of keys in store",
			},
		),

		DeletesTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "mvcc",
				Name:      "deletes_total",
				Help:      "Total number of key deletions",
			},
		),

		CompactionsTotal: promauto.With(registry).NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "mvcc",
				Name:      "compactions_total",
				Help:      "Total number of compactions",
			},
		),

		// Panic recovery metrics
		PanicsRecovered: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "panics_recovered_total",
				Help:      "Total number of panics recovered",
			},
			[]string{"method"},
		),
	}

	return m
}

// RecordGrpcRequest records a gRPC request's duration and status
func (m *Metrics) RecordGrpcRequest(method string, code string, duration time.Duration) {
	m.GrpcRequestDuration.WithLabelValues(method, code).Observe(duration.Seconds())
	m.GrpcRequestTotal.WithLabelValues(method, code).Inc()
}

// RecordStorageOperation records a storage operation's duration and status
func (m *Metrics) RecordStorageOperation(operation string, status string, duration time.Duration) {
	m.StorageOperationDuration.WithLabelValues(operation, status).Observe(duration.Seconds())
	m.StorageOperationTotal.WithLabelValues(operation).Inc()
}

// RecordStorageError records a storage operation error
func (m *Metrics) RecordStorageError(operation string, errorType string) {
	m.StorageOperationErrors.WithLabelValues(operation, errorType).Inc()
}

// RecordWatchEvent records a watch event
func (m *Metrics) RecordWatchEvent(eventType string) {
	m.WatchEventsTotal.WithLabelValues(eventType).Inc()
}

// RecordAuthentication records an authentication attempt
func (m *Metrics) RecordAuthentication(success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	m.AuthenticationTotal.WithLabelValues(result).Inc()
}

// RecordAuthorization records an authorization check
func (m *Metrics) RecordAuthorization(allowed bool) {
	result := "denied"
	if allowed {
		result = "allowed"
	}
	m.AuthorizedTotal.WithLabelValues(result).Inc()
}

// RecordRateLimitHit records a rate limit hit
func (m *Metrics) RecordRateLimitHit(method string) {
	m.RateLimitHits.WithLabelValues(method).Inc()
}

// RecordConnectionRejected records a rejected connection
func (m *Metrics) RecordConnectionRejected(reason string) {
	m.RejectedConnections.WithLabelValues(reason).Inc()
}

// RecordPanicRecovered records a recovered panic
func (m *Metrics) RecordPanicRecovered(method string) {
	m.PanicsRecovered.WithLabelValues(method).Inc()
}
