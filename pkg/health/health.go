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

package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Status represents the health status
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
	StatusDegraded  Status = "degraded"
)

// CheckResult represents the result of a single health check
type CheckResult struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
	Latency int64  `json:"latency_ms,omitempty"` // Check latency in milliseconds
}

// HealthReport represents the overall health status
type HealthReport struct {
	Status    Status                  `json:"status"`
	Timestamp string                  `json:"timestamp"`
	Checks    map[string]CheckResult  `json:"checks"`
}

// Checker is an interface for health checks
type Checker interface {
	// Check performs the health check
	// Returns status, message, and error (if any)
	Check(ctx context.Context) (Status, string, error)

	// Name returns the check name
	Name() string
}

// HealthServer provides health check HTTP endpoints
type HealthServer struct {
	mu       sync.RWMutex
	checkers []Checker
	logger   *zap.Logger

	// Cached health status (updated periodically)
	cachedReport   *HealthReport
	cacheValidUntil time.Time
	cacheDuration   time.Duration
}

// NewHealthServer creates a new health check server
func NewHealthServer(logger *zap.Logger) *HealthServer {
	return &HealthServer{
		checkers:      make([]Checker, 0),
		logger:        logger,
		cacheDuration: 5 * time.Second, // Cache health checks for 5 seconds
	}
}

// RegisterChecker adds a health checker
func (hs *HealthServer) RegisterChecker(checker Checker) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.checkers = append(hs.checkers, checker)
	hs.logger.Info("registered health checker", zap.String("name", checker.Name()))
}

// Check performs all health checks
func (hs *HealthServer) Check(ctx context.Context) *HealthReport {
	// Check cache first
	hs.mu.RLock()
	if hs.cachedReport != nil && time.Now().Before(hs.cacheValidUntil) {
		cached := hs.cachedReport
		hs.mu.RUnlock()
		return cached
	}
	hs.mu.RUnlock()

	// Perform checks
	hs.mu.Lock()
	defer hs.mu.Unlock()

	report := &HealthReport{
		Status:    StatusHealthy,
		Timestamp: time.Now().Format(time.RFC3339),
		Checks:    make(map[string]CheckResult),
	}

	// Run all checkers
	for _, checker := range hs.checkers {
		startTime := time.Now()
		status, message, err := checker.Check(ctx)
		latency := time.Since(startTime).Milliseconds()

		if err != nil {
			status = StatusUnhealthy
			message = err.Error()
		}

		report.Checks[checker.Name()] = CheckResult{
			Status:  status,
			Message: message,
			Latency: latency,
		}

		// Update overall status
		if status == StatusUnhealthy {
			report.Status = StatusUnhealthy
		} else if status == StatusDegraded && report.Status != StatusUnhealthy {
			report.Status = StatusDegraded
		}
	}

	// Cache the report
	hs.cachedReport = report
	hs.cacheValidUntil = time.Now().Add(hs.cacheDuration)

	return report
}

// ServeHTTP implements http.Handler for /health endpoint
func (hs *HealthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	report := hs.Check(ctx)

	w.Header().Set("Content-Type", "application/json")

	// Set HTTP status based on health
	if report.Status == StatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if report.Status == StatusDegraded {
		w.WriteHeader(http.StatusOK) // 200 but degraded
	} else {
		w.WriteHeader(http.StatusOK)
	}

	json.NewEncoder(w).Encode(report)
}

// ReadinessHandler returns a handler for Kubernetes readiness probes
// Returns 200 if ready, 503 if not
func (hs *HealthServer) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		report := hs.Check(ctx)

		// Ready only if healthy or degraded (not unhealthy)
		if report.Status == StatusUnhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Not Ready\n"))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ready\n"))
	}
}

// LivenessHandler returns a handler for Kubernetes liveness probes
// Returns 200 if alive, 503 if not
func (hs *HealthServer) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// For liveness, we just check if the process is responsive
		// We don't perform expensive checks
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Alive\n"))
	}
}

// StartHealthServer starts the health check HTTP server
func StartHealthServer(addr string, hs *HealthServer, logger *zap.Logger) error {
	mux := http.NewServeMux()

	// Register endpoints
	mux.Handle("/health", hs)
	mux.HandleFunc("/readiness", hs.ReadinessHandler())
	mux.HandleFunc("/liveness", hs.LivenessHandler())

	// Root endpoint with links
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html>
<head><title>MetaStore Health</title></head>
<body>
<h1>MetaStore Health Check Server</h1>
<p>Available endpoints:</p>
<ul>
<li><a href="/health">/health</a> - Detailed health status (JSON)</li>
<li><a href="/readiness">/readiness</a> - Kubernetes readiness probe</li>
<li><a href="/liveness">/liveness</a> - Kubernetes liveness probe</li>
</ul>
</body>
</html>`)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("starting health check server", zap.String("addr", addr))
	return server.ListenAndServe()
}

// Common health checkers

// StoreChecker checks if the KV store is operational
type StoreChecker struct {
	name        string
	checkFunc   func(context.Context) error
}

// NewStoreChecker creates a store health checker
func NewStoreChecker(name string, checkFunc func(context.Context) error) *StoreChecker {
	return &StoreChecker{
		name:      name,
		checkFunc: checkFunc,
	}
}

func (sc *StoreChecker) Name() string {
	return sc.name
}

func (sc *StoreChecker) Check(ctx context.Context) (Status, string, error) {
	if err := sc.checkFunc(ctx); err != nil {
		return StatusUnhealthy, fmt.Sprintf("store check failed: %v", err), err
	}
	return StatusHealthy, "store is operational", nil
}

// RaftChecker checks Raft consensus status
type RaftChecker struct {
	name      string
	raftStatusFunc func() (isLeader bool, hasQuorum bool, err error)
}

// NewRaftChecker creates a Raft health checker
func NewRaftChecker(name string, raftStatusFunc func() (bool, bool, error)) *RaftChecker {
	return &RaftChecker{
		name:           name,
		raftStatusFunc: raftStatusFunc,
	}
}

func (rc *RaftChecker) Name() string {
	return rc.name
}

func (rc *RaftChecker) Check(ctx context.Context) (Status, string, error) {
	isLeader, hasQuorum, err := rc.raftStatusFunc()
	if err != nil {
		return StatusUnhealthy, fmt.Sprintf("raft check failed: %v", err), err
	}

	if !hasQuorum {
		return StatusDegraded, "no quorum", nil
	}

	if isLeader {
		return StatusHealthy, "leader with quorum", nil
	}

	return StatusHealthy, "follower with quorum", nil
}

// DiskSpaceChecker checks available disk space
type DiskSpaceChecker struct {
	name          string
	path          string
	minFreeGB     int64
	warnThreshold int64 // Warning threshold in percentage (e.g., 80 for 80%)
}

// NewDiskSpaceChecker creates a disk space checker
func NewDiskSpaceChecker(name string, path string, minFreeGB int64, warnThreshold int64) *DiskSpaceChecker {
	return &DiskSpaceChecker{
		name:          name,
		path:          path,
		minFreeGB:     minFreeGB,
		warnThreshold: warnThreshold,
	}
}

func (dsc *DiskSpaceChecker) Name() string {
	return dsc.name
}

func (dsc *DiskSpaceChecker) Check(ctx context.Context) (Status, string, error) {
	// Get disk usage
	totalGB, freeGB, usedPercent, err := getDiskUsage(dsc.path)
	if err != nil {
		return StatusUnhealthy, fmt.Sprintf("failed to get disk usage: %v", err), err
	}

	message := fmt.Sprintf("%.1fGB free of %.1fGB (%.1f%% used)", freeGB, totalGB, usedPercent)

	// Check critical threshold
	if freeGB < float64(dsc.minFreeGB) {
		return StatusUnhealthy, fmt.Sprintf("disk space critical: %s", message), nil
	}

	// Check warning threshold
	if usedPercent > float64(dsc.warnThreshold) {
		return StatusDegraded, fmt.Sprintf("disk space low: %s", message), nil
	}

	return StatusHealthy, message, nil
}
