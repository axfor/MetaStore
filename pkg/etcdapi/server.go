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

package etcdapi

import (
	"context"
	"fmt"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	"metaStore/pkg/log"
	"metaStore/pkg/reliability"
	"net"
	"sync"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Server etcd-compatible gRPC server
type Server struct {
	mu       sync.RWMutex
	store    kvstore.Store    // Underlying storage
	grpcSrv  *grpc.Server     // gRPC server
	listener net.Listener     // Network listener

	// Management components
	watchMgr   *WatchManager    // Watch manager
	leaseMgr   *LeaseManager    // Lease manager
	clusterMgr *ClusterManager  // Cluster manager
	authMgr    *AuthManager     // Auth manager
	alarmMgr   *AlarmManager    // Alarm manager

	// Reliability components
	shutdownMgr  *reliability.GracefulShutdown  // Graceful shutdown manager
	resourceMgr  *reliability.ResourceManager   // Resource manager
	healthMgr    *reliability.HealthManager     // Health manager
	dataValidator *reliability.DataValidator    // Data validator

	// Configuration
	clusterID    uint64   // Cluster ID
	memberID     uint64   // Member ID
	clusterPeers []string // Peer URLs of all cluster members
}

// ServerConfig server configuration
type ServerConfig struct {
	Store       kvstore.Store              // Underlying storage (required)
	Address     string                     // Listen address (e.g. ":2379")
	ClusterID   uint64                     // Cluster ID
	MemberID    uint64                     // Member ID
	ClusterPeers []string                  // Peer URLs of all cluster members (for member list)
	ConfChangeC chan<- raftpb.ConfChange   // Raft ConfChange channel (optional)
	Config      *config.Config             // Full configuration object (optional, values from this take precedence if provided)

	// Reliability configuration (kept for backward compatibility, but overridden if Config is provided)
	ResourceLimits    *reliability.ResourceLimits  // Resource limits configuration (optional)
	ShutdownTimeout   time.Duration                // Shutdown timeout (optional, default 30s)
	EnableCRC         bool                         // Whether to enable CRC validation (optional, default false)
	EnableHealthCheck bool                         // Whether to enable health check (optional, default true)
}

// NewServer creates a new etcd-compatible server
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if cfg.Address == "" {
		cfg.Address = ":2379"
	}
	if cfg.ClusterID == 0 {
		cfg.ClusterID = 1 // Default cluster ID
	}
	if cfg.MemberID == 0 {
		cfg.MemberID = 1 // Default member ID
	}

	// If full configuration is provided, override with config values
	if cfg.Config != nil {
		// Use reliability settings from config file
		if cfg.ShutdownTimeout == 0 {
			cfg.ShutdownTimeout = cfg.Config.Server.Reliability.ShutdownTimeout
		}
		if !cfg.EnableCRC {
			cfg.EnableCRC = cfg.Config.Server.Reliability.EnableCRC
		}
		if !cfg.EnableHealthCheck {
			cfg.EnableHealthCheck = cfg.Config.Server.Reliability.EnableHealthCheck
		}

		// Build resource limits from config file
		if cfg.ResourceLimits == nil {
			maxMemoryBytes := cfg.Config.Server.Limits.MaxMemoryMB * 1024 * 1024 // MB to Bytes
			if maxMemoryBytes == 0 {
				maxMemoryBytes = 8 * 1024 * 1024 * 1024 // Default 8GB
			}
			cfg.ResourceLimits = &reliability.ResourceLimits{
				MaxConnections: int64(cfg.Config.Server.Limits.MaxConnections),
				MaxRequests:    cfg.Config.Server.Limits.MaxRequests,
				MaxMemoryBytes: maxMemoryBytes,
			}
		}
	}

	// Set reliability configuration defaults
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}
	if cfg.ResourceLimits == nil {
		limits := reliability.DefaultLimits
		cfg.ResourceLimits = &limits
	}

	// Create listener
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %v", cfg.Address, err)
	}

	// Initialize reliability components
	var shutdownMgr *reliability.GracefulShutdown
	if cfg.Config != nil {
		shutdownMgr = reliability.NewGracefulShutdown(cfg.ShutdownTimeout, cfg.Config.Server.Reliability.DrainTimeout)
	} else {
		shutdownMgr = reliability.NewGracefulShutdown(cfg.ShutdownTimeout)
	}
	resourceMgr := reliability.NewResourceManager(*cfg.ResourceLimits)
	healthMgr := reliability.NewHealthManager()
	dataValidator := reliability.NewDataValidator(cfg.EnableCRC)

	// Create LeaseManager (using configuration)
	var leaseMgr *LeaseManager
	if cfg.Config != nil {
		leaseMgr = NewLeaseManager(cfg.Store, &cfg.Config.Server.Lease, &cfg.Config.Server.Limits)
	} else {
		leaseMgr = NewLeaseManager(cfg.Store, nil, nil)
	}

	// Create WatchManager (using configuration)
	var watchMgr *WatchManager
	if cfg.Config != nil {
		watchMgr = NewWatchManager(cfg.Store, &cfg.Config.Server.Limits)
	} else {
		watchMgr = NewWatchManager(cfg.Store)
	}

	// Create AuthManager (using configuration)
	var authMgr *AuthManager
	if cfg.Config != nil {
		authMgr = NewAuthManager(cfg.Store, &cfg.Config.Server.Auth)
	} else {
		authMgr = NewAuthManager(cfg.Store)
	}

	// Create Server instance (need to create first to use its methods)
	s := &Server{
		store:         cfg.Store,
		listener:      listener,
		watchMgr:      watchMgr,
		leaseMgr:      leaseMgr,
		authMgr:       authMgr,
		alarmMgr:      NewAlarmManager(),
		shutdownMgr:   shutdownMgr,
		resourceMgr:   resourceMgr,
		healthMgr:     healthMgr,
		dataValidator: dataValidator,
		clusterID:     cfg.ClusterID,
		memberID:      cfg.MemberID,
		clusterPeers:  cfg.ClusterPeers,
	}

	// Build gRPC server options
	grpcOpts := []grpc.ServerOption{
		// Interceptor chain
		grpc.ChainUnaryInterceptor(
			s.PanicRecoveryInterceptor,   // Panic recovery (first layer)
			resourceMgr.LimitInterceptor, // Resource limits
			s.AuthInterceptor,            // Authentication and authorization
		),
	}

	// If configuration provided, apply gRPC configuration
	if cfg.Config != nil {
		grpcCfg := cfg.Config.Server.GRPC

		// Message size limits
		if grpcCfg.MaxRecvMsgSize > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxRecvMsgSize(grpcCfg.MaxRecvMsgSize))
		}
		if grpcCfg.MaxSendMsgSize > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxSendMsgSize(grpcCfg.MaxSendMsgSize))
		}

		// Concurrent stream limits
		if grpcCfg.MaxConcurrentStreams > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxConcurrentStreams(grpcCfg.MaxConcurrentStreams))
		}

		// Flow control window size
		if grpcCfg.InitialWindowSize > 0 {
			grpcOpts = append(grpcOpts, grpc.InitialWindowSize(grpcCfg.InitialWindowSize))
		}
		if grpcCfg.InitialConnWindowSize > 0 {
			grpcOpts = append(grpcOpts, grpc.InitialConnWindowSize(grpcCfg.InitialConnWindowSize))
		}

		// Keepalive configuration
		if grpcCfg.KeepaliveTime > 0 || grpcCfg.KeepaliveTimeout > 0 {
			kaPolicy := keepalive.ServerParameters{
				Time:    grpcCfg.KeepaliveTime,
				Timeout: grpcCfg.KeepaliveTimeout,
			}
			if grpcCfg.MaxConnectionIdle > 0 {
				kaPolicy.MaxConnectionIdle = grpcCfg.MaxConnectionIdle
			}
			if grpcCfg.MaxConnectionAge > 0 {
				kaPolicy.MaxConnectionAge = grpcCfg.MaxConnectionAge
			}
			if grpcCfg.MaxConnectionAgeGrace > 0 {
				kaPolicy.MaxConnectionAgeGrace = grpcCfg.MaxConnectionAgeGrace
			}
			grpcOpts = append(grpcOpts, grpc.KeepaliveParams(kaPolicy))
		}
	}

	// Create gRPC server
	grpcSrv := grpc.NewServer(grpcOpts...)
	s.grpcSrv = grpcSrv

	// Initialize ClusterManager (if ConfChangeC is provided)
	if cfg.ConfChangeC != nil {
		s.clusterMgr = NewClusterManager(cfg.ConfChangeC)

		// Initialize all cluster members
		members := make([]*MemberInfo, 0, len(cfg.ClusterPeers))
		for i, peerURL := range cfg.ClusterPeers {
			memberID := uint64(i + 1) // Member IDs start from 1
			members = append(members, &MemberInfo{
				ID:         memberID,
				Name:       fmt.Sprintf("node-%d", memberID),
				PeerURLs:   []string{peerURL},
				ClientURLs: []string{fmt.Sprintf("http://127.0.0.1:%d", 9120+memberID)}, // Generated by convention
				IsLearner:  false,
			})
		}
		s.clusterMgr.InitialMembers(members)
	}

	// Register gRPC services
	pb.RegisterKVServer(grpcSrv, &KVServer{server: s})
	pb.RegisterWatchServer(grpcSrv, &WatchServer{server: s})
	pb.RegisterLeaseServer(grpcSrv, &LeaseServer{server: s})

	// Create Maintenance server (using configuration)
	snapshotChunkSize := 4 * 1024 * 1024 // Default 4MB
	if cfg.Config != nil {
		snapshotChunkSize = cfg.Config.Server.Maintenance.SnapshotChunkSize
	}
	maintenanceServer := &MaintenanceServer{
		server:            s,
		snapshotChunkSize: snapshotChunkSize,
	}
	pb.RegisterMaintenanceServer(grpcSrv, maintenanceServer)
	pb.RegisterAuthServer(grpcSrv, &AuthServer{server: s})

	// Register Cluster service (delegated to MaintenanceServer implementation)
	pb.RegisterClusterServer(grpcSrv, &ClusterServer{maintenance: maintenanceServer})

	// Register health check service
	if cfg.EnableHealthCheck {
		healthpb.RegisterHealthServer(grpcSrv, healthMgr.GetServer())

		// Register health checkers
		healthMgr.RegisterChecker(reliability.NewStorageHealthChecker("storage", func(ctx context.Context) error {
			// Check if storage is available
			if s.store == nil {
				return fmt.Errorf("storage is nil")
			}
			return nil
		}))

		healthMgr.RegisterChecker(reliability.NewLeaseHealthChecker("lease", func(ctx context.Context) error {
			// Check if Lease manager is healthy
			if s.leaseMgr == nil {
				return fmt.Errorf("lease manager is nil")
			}
			return nil
		}))

		// Set initial status to SERVING
		healthMgr.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	}

	// Register graceful shutdown hooks
	shutdownMgr.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
		log.Info("Shutdown phase: Stop accepting new connections",
			log.Phase("StopAccepting"),
			log.Component("server"))
		// Mark as unhealthy, stop accepting new requests
		if cfg.EnableHealthCheck {
			healthMgr.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		}
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhaseDrainConnections, func(ctx context.Context) error {
		log.Info("Shutdown phase: Drain existing connections",
			log.Phase("DrainConnections"),
			log.Component("server"))
		// Wait for existing requests to complete (controlled by context timeout)
		time.Sleep(2 * time.Second)
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhasePersistState, func(ctx context.Context) error {
		log.Info("Shutdown phase: Persist state",
			log.Phase("PersistState"),
			log.Component("server"))
		// Lease and Watch managers already handle persistence in their respective Stop() methods
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhaseCloseResources, func(ctx context.Context) error {
		log.Info("Shutdown phase: Close resources",
			log.Phase("CloseResources"),
			log.Component("server"))

		// Stop Lease manager
		if s.leaseMgr != nil {
			s.leaseMgr.Stop()
		}

		// Stop Watch manager
		if s.watchMgr != nil {
			s.watchMgr.Stop()
		}

		// Stop resource manager
		if s.resourceMgr != nil {
			s.resourceMgr.Close()
		}

		// Gracefully stop gRPC server
		if s.grpcSrv != nil {
			s.grpcSrv.GracefulStop()
		}

		// Close listener
		if s.listener != nil {
			s.listener.Close()
		}

		return nil
	})

	return s, nil
}

// Start starts the gRPC server
func (s *Server) Start() error {
	log.Info("Starting etcd-compatible gRPC server",
		log.String("address", s.listener.Addr().String()),
		log.Component("server"))

	// Start Lease manager expiry checker
	reliability.SafeGo("lease-expiry-checker", func() {
		s.leaseMgr.Start()
	})

	// Start graceful shutdown listener (waiting for signals in background)
	reliability.SafeGo("shutdown-listener", func() {
		s.shutdownMgr.Wait()
	})

	stats := s.resourceMgr.GetStats()
	log.Info("Server started with reliability features enabled",
		log.Int64("max_connections", stats.MaxConnections),
		log.Int64("max_requests", stats.MaxRequests),
		log.Int64("max_memory_mb", stats.MaxMemoryBytes/1024/1024),
		log.Bool("graceful_shutdown", true),
		log.Bool("panic_recovery", true),
		log.Bool("health_check", true),
		log.Bool("crc_validation", s.dataValidator != nil),
		log.Component("server"))

	// Start gRPC service
	return s.grpcSrv.Serve(s.listener)
}

// Stop stops the gRPC server (triggers graceful shutdown)
func (s *Server) Stop() {
	log.Info("Triggering graceful shutdown",
		log.Component("server"))
	s.shutdownMgr.Shutdown()
}

// WaitForShutdown waits for the server shutdown to complete
func (s *Server) WaitForShutdown() {
	<-s.shutdownMgr.Done()
	log.Info("Server shutdown complete",
		log.Component("server"))
}

// Address returns the server listen address
func (s *Server) Address() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// getResponseHeader creates a standard response header
func (s *Server) getResponseHeader() *pb.ResponseHeader {
	return &pb.ResponseHeader{
		ClusterId: s.clusterID,
		MemberId:  s.memberID,
		Revision:  s.store.CurrentRevision(),
		RaftTerm:  s.store.GetRaftStatus().Term,
	}
}

// PanicRecoveryInterceptor panic recovery interceptor
func (s *Server) PanicRecoveryInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			reliability.RecoverPanic(fmt.Sprintf("grpc-handler-%s", info.FullMethod))
			err = fmt.Errorf("internal server error: panic recovered")
		}
	}()

	return handler(ctx, req)
}

// GetResourceStats gets resource usage statistics
func (s *Server) GetResourceStats() reliability.ResourceStats {
	return s.resourceMgr.GetStats()
}

// GetPanicCount gets panic count
func (s *Server) GetPanicCount() int64 {
	return reliability.GetPanicCount()
}

// GetValidationErrorCount gets validation error count
func (s *Server) GetValidationErrorCount() int64 {
	return reliability.GetValidationErrorCount()
}
