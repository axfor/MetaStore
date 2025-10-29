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
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// MetricsServer serves Prometheus metrics over HTTP
// Provides /metrics endpoint for Prometheus scraping and /health endpoint for health checks
type MetricsServer struct {
	server   *http.Server
	registry *prometheus.Registry
	logger   *zap.Logger
}

// NewMetricsServer creates a new metrics HTTP server
// addr: Listen address (e.g., ":9090")
// registry: Prometheus registry containing all metrics
// logger: Logger for server events
func NewMetricsServer(addr string, registry *prometheus.Registry, logger *zap.Logger) *MetricsServer {
	mux := http.NewServeMux()

	// /metrics endpoint for Prometheus scraping
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,           // Enable OpenMetrics format (Prometheus 2.x+)
		MaxRequestsInFlight: 10,            // Limit concurrent scraping requests
		Timeout:             30 * time.Second, // Scraping timeout
		ErrorHandling:       promhttp.ContinueOnError, // Continue collecting on partial errors
	}))

	// /health endpoint for health checks
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
	})

	// / endpoint showing available endpoints
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<html>
<head><title>MetaStore Metrics</title></head>
<body>
<h1>MetaStore Metrics Server</h1>
<p>Available endpoints:</p>
<ul>
<li><a href="/metrics">/metrics</a> - Prometheus metrics</li>
<li><a href="/health">/health</a> - Health check</li>
</ul>
</body>
</html>`)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}

	return &MetricsServer{
		server:   server,
		registry: registry,
		logger:   logger,
	}
}

// Start starts the metrics server
// This method blocks until the server is shut down
func (ms *MetricsServer) Start() error {
	ms.logger.Info("starting metrics server",
		zap.String("addr", ms.server.Addr))

	if err := ms.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		ms.logger.Error("metrics server failed",
			zap.Error(err))
		return err
	}

	return nil
}

// Shutdown gracefully shuts down the metrics server
// ctx: Context with timeout for shutdown (recommended: 5-10 seconds)
func (ms *MetricsServer) Shutdown(ctx context.Context) error {
	ms.logger.Info("shutting down metrics server")

	if err := ms.server.Shutdown(ctx); err != nil {
		ms.logger.Error("metrics server shutdown failed",
			zap.Error(err))
		return err
	}

	ms.logger.Info("metrics server stopped")
	return nil
}

// ServeMetrics is a convenience function to start a metrics server
// This is a simplified API for common use cases
func ServeMetrics(addr string, registry *prometheus.Registry, logger *zap.Logger) *MetricsServer {
	server := NewMetricsServer(addr, registry, logger)
	go func() {
		if err := server.Start(); err != nil {
			logger.Error("metrics server error",
				zap.Error(err))
		}
	}()
	return server
}
