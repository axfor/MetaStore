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
	"sync"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// HealthChecker healthycheckinterface
type HealthChecker interface {
	// Check executehealthycheck
	Check(ctx context.Context) error
	// Name returncheck
	Name() string
}

// HealthManager healthymanager
type HealthManager struct {
	mu       sync.RWMutex
	checkers map[string]HealthChecker
	server   *health.Server
}

// NewHealthManager createhealthymanager
func NewHealthManager() *HealthManager {
	return &HealthManager{
		checkers: make(map[string]HealthChecker),
		server:   health.NewServer(),
	}
}

// RegisterChecker registerhealthycheck
func (hm *HealthManager) RegisterChecker(checker HealthChecker) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.checkers[checker.Name()] = checker
}

// Check executeallhealthycheck
func (hm *HealthManager) Check(ctx context.Context, serviceName string) healthpb.HealthCheckResponse_ServingStatus {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	// ifspecified，checkshould
	if serviceName != "" {
		if checker, exists := hm.checkers[serviceName]; exists {
			if err := checker.Check(ctx); err != nil {
				return healthpb.HealthCheckResponse_NOT_SERVING
			}
			return healthpb.HealthCheckResponse_SERVING
		}
		return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}

	// checkall
	for _, checker := range hm.checkers {
		if err := checker.Check(ctx); err != nil {
			return healthpb.HealthCheckResponse_NOT_SERVING
		}
	}

	return healthpb.HealthCheckResponse_SERVING
}

// SetServingStatus setstatus
func (hm *HealthManager) SetServingStatus(service string, status healthpb.HealthCheckResponse_ServingStatus) {
	hm.server.SetServingStatus(service, status)
}

// GetServer get gRPC healthycheckserver
func (hm *HealthManager) GetServer() *health.Server {
	return hm.server
}

// StorageHealthChecker storagehealthycheck
type StorageHealthChecker struct {
	name  string
	check func(ctx context.Context) error
}

// NewStorageHealthChecker createstoragehealthycheck
func NewStorageHealthChecker(name string, checkFunc func(ctx context.Context) error) *StorageHealthChecker {
	return &StorageHealthChecker{
		name:  name,
		check: checkFunc,
	}
}

// Name returncheck
func (s *StorageHealthChecker) Name() string {
	return s.name
}

// Check executehealthycheck
func (s *StorageHealthChecker) Check(ctx context.Context) error {
	return s.check(ctx)
}

// RaftHealthChecker Raft healthycheck
type RaftHealthChecker struct {
	name  string
	check func(ctx context.Context) error
}

// NewRaftHealthChecker create Raft healthycheck
func NewRaftHealthChecker(name string, checkFunc func(ctx context.Context) error) *RaftHealthChecker {
	return &RaftHealthChecker{
		name:  name,
		check: checkFunc,
	}
}

// Name returncheck
func (r *RaftHealthChecker) Name() string {
	return r.name
}

// Check executehealthycheck
func (r *RaftHealthChecker) Check(ctx context.Context) error {
	return r.check(ctx)
}

// LeaseHealthChecker Lease healthycheck
type LeaseHealthChecker struct {
	name  string
	check func(ctx context.Context) error
}

// NewLeaseHealthChecker create Lease healthycheck
func NewLeaseHealthChecker(name string, checkFunc func(ctx context.Context) error) *LeaseHealthChecker {
	return &LeaseHealthChecker{
		name:  name,
		check: checkFunc,
	}
}

// Name returncheck
func (l *LeaseHealthChecker) Name() string {
	return l.name
}

// Check executehealthycheck
func (l *LeaseHealthChecker) Check(ctx context.Context) error {
	return l.check(ctx)
}
