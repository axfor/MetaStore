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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Mock checker for testing
type mockChecker struct {
	name   string
	status Status
	msg    string
	err    error
}

func (mc *mockChecker) Name() string {
	return mc.name
}

func (mc *mockChecker) Check(ctx context.Context) (Status, string, error) {
	return mc.status, mc.msg, mc.err
}

func TestHealthServer_Check(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	// Register healthy checkers
	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    "store ok",
	})
	hs.RegisterChecker(&mockChecker{
		name:   "raft",
		status: StatusHealthy,
		msg:    "raft ok",
	})

	// Perform check
	report := hs.Check(context.Background())

	// Verify
	assert.Equal(t, StatusHealthy, report.Status)
	assert.Equal(t, 2, len(report.Checks))
	assert.Equal(t, StatusHealthy, report.Checks["store"].Status)
	assert.Equal(t, StatusHealthy, report.Checks["raft"].Status)
}

func TestHealthServer_Check_Unhealthy(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	// Register healthy and unhealthy checkers
	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    "store ok",
	})
	hs.RegisterChecker(&mockChecker{
		name:   "raft",
		status: StatusUnhealthy,
		msg:    "raft failed",
		err:    fmt.Errorf("connection lost"),
	})

	// Perform check
	report := hs.Check(context.Background())

	// Verify overall status is unhealthy
	assert.Equal(t, StatusUnhealthy, report.Status)
	assert.Equal(t, StatusHealthy, report.Checks["store"].Status)
	assert.Equal(t, StatusUnhealthy, report.Checks["raft"].Status)
}

func TestHealthServer_Check_Degraded(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	// Register healthy and degraded checkers
	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    "store ok",
	})
	hs.RegisterChecker(&mockChecker{
		name:   "disk",
		status: StatusDegraded,
		msg:    "disk space low",
	})

	// Perform check
	report := hs.Check(context.Background())

	// Verify overall status is degraded
	assert.Equal(t, StatusDegraded, report.Status)
	assert.Equal(t, StatusHealthy, report.Checks["store"].Status)
	assert.Equal(t, StatusDegraded, report.Checks["disk"].Status)
}

func TestHealthServer_HTTPHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    "store ok",
	})

	// Create HTTP request
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	// Call handler
	hs.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Parse JSON response
	var report HealthReport
	err := json.NewDecoder(w.Body).Decode(&report)
	require.NoError(t, err)

	assert.Equal(t, StatusHealthy, report.Status)
	assert.Equal(t, 1, len(report.Checks))
}

func TestHealthServer_HTTPHandler_Unhealthy(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusUnhealthy,
		msg:    "store failed",
	})

	// Create HTTP request
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	// Call handler
	hs.ServeHTTP(w, req)

	// Verify response (503)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	// Parse JSON response
	var report HealthReport
	err := json.NewDecoder(w.Body).Decode(&report)
	require.NoError(t, err)

	assert.Equal(t, StatusUnhealthy, report.Status)
}

func TestHealthServer_ReadinessHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    "store ok",
	})

	// Test readiness endpoint
	req := httptest.NewRequest("GET", "/readiness", nil)
	w := httptest.NewRecorder()

	handler := hs.ReadinessHandler()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Ready\n", w.Body.String())
}

func TestHealthServer_ReadinessHandler_NotReady(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusUnhealthy,
		msg:    "store failed",
	})

	// Test readiness endpoint
	req := httptest.NewRequest("GET", "/readiness", nil)
	w := httptest.NewRecorder()

	handler := hs.ReadinessHandler()
	handler(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "Not Ready\n", w.Body.String())
}

func TestHealthServer_LivenessHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	// Liveness should always return OK (process is alive)
	req := httptest.NewRequest("GET", "/liveness", nil)
	w := httptest.NewRecorder()

	handler := hs.LivenessHandler()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Alive\n", w.Body.String())
}

func TestStoreChecker(t *testing.T) {
	// Test healthy store
	checker := NewStoreChecker("store", func(ctx context.Context) error {
		return nil
	})

	status, msg, err := checker.Check(context.Background())
	assert.Equal(t, StatusHealthy, status)
	assert.Contains(t, msg, "operational")
	assert.NoError(t, err)

	// Test unhealthy store
	checker = NewStoreChecker("store", func(ctx context.Context) error {
		return fmt.Errorf("connection failed")
	})

	status, msg, err = checker.Check(context.Background())
	assert.Equal(t, StatusUnhealthy, status)
	assert.Contains(t, msg, "failed")
	assert.Error(t, err)
}

func TestRaftChecker(t *testing.T) {
	// Test leader with quorum
	checker := NewRaftChecker("raft", func() (bool, bool, error) {
		return true, true, nil // isLeader, hasQuorum, err
	})

	status, msg, err := checker.Check(context.Background())
	assert.Equal(t, StatusHealthy, status)
	assert.Contains(t, msg, "leader with quorum")
	assert.NoError(t, err)

	// Test follower with quorum
	checker = NewRaftChecker("raft", func() (bool, bool, error) {
		return false, true, nil
	})

	status, msg, err = checker.Check(context.Background())
	assert.Equal(t, StatusHealthy, status)
	assert.Contains(t, msg, "follower with quorum")
	assert.NoError(t, err)

	// Test no quorum
	checker = NewRaftChecker("raft", func() (bool, bool, error) {
		return false, false, nil
	})

	status, msg, err = checker.Check(context.Background())
	assert.Equal(t, StatusDegraded, status)
	assert.Contains(t, msg, "no quorum")
	assert.NoError(t, err)
}

func TestDiskSpaceChecker(t *testing.T) {
	// Test with /tmp (should have some space)
	checker := NewDiskSpaceChecker("disk", "/tmp", 1, 95)

	status, msg, err := checker.Check(context.Background())
	assert.NoError(t, err)
	assert.Contains(t, msg, "GB free")

	// Status depends on actual disk usage
	assert.Contains(t, []Status{StatusHealthy, StatusDegraded}, status)
}

func TestHealthServer_Cache(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hs := NewHealthServer(logger)

	callCount := 0
	hs.RegisterChecker(&mockChecker{
		name:   "store",
		status: StatusHealthy,
		msg:    fmt.Sprintf("call %d", callCount),
	})

	// First call
	report1 := hs.Check(context.Background())
	timestamp1 := report1.Timestamp

	// Second call (should be cached)
	report2 := hs.Check(context.Background())
	timestamp2 := report2.Timestamp

	// Timestamps should be the same (cached)
	assert.Equal(t, timestamp1, timestamp2)
}
