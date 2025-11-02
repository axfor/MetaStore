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

// Server etcd 兼容的 gRPC 服务器
type Server struct {
	mu       sync.RWMutex
	store    kvstore.Store    // 底层存储
	grpcSrv  *grpc.Server     // gRPC server
	listener net.Listener     // 网络监听器

	// 管理组件
	watchMgr   *WatchManager    // Watch 管理器
	leaseMgr   *LeaseManager    // Lease 管理器
	clusterMgr *ClusterManager  // Cluster 管理器
	authMgr    *AuthManager     // Auth 管理器
	alarmMgr   *AlarmManager    // Alarm 管理器

	// 可靠性组件
	shutdownMgr  *reliability.GracefulShutdown  // 优雅关闭管理器
	resourceMgr  *reliability.ResourceManager   // 资源管理器
	healthMgr    *reliability.HealthManager     // 健康管理器
	dataValidator *reliability.DataValidator    // 数据验证器

	// 配置
	clusterID    uint64   // 集群 ID
	memberID     uint64   // 成员 ID
	clusterPeers []string // 集群所有成员的 peer URLs
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Store       kvstore.Store              // 底层存储（必需）
	Address     string                     // 监听地址（例如 ":2379"）
	ClusterID   uint64                     // 集群 ID
	MemberID    uint64                     // 成员 ID
	ClusterPeers []string                  // 集群所有成员的 peer URLs（用于member list）
	ConfChangeC chan<- raftpb.ConfChange   // Raft ConfChange 通道（可选）
	Config      *config.Config             // 完整配置对象（可选，如果提供则优先使用其中的值）

	// 可靠性配置（仍保留以便向后兼容，但如果 Config 提供了则会被覆盖）
	ResourceLimits    *reliability.ResourceLimits  // 资源限制配置（可选）
	ShutdownTimeout   time.Duration                // 关闭超时时间（可选，默认 30s）
	EnableCRC         bool                         // 是否启用 CRC 验证（可选，默认 false）
	EnableHealthCheck bool                         // 是否启用健康检查（可选，默认 true）
}

// NewServer 创建新的 etcd 兼容服务器
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if cfg.Address == "" {
		cfg.Address = ":2379"
	}
	if cfg.ClusterID == 0 {
		cfg.ClusterID = 1 // 默认集群 ID
	}
	if cfg.MemberID == 0 {
		cfg.MemberID = 1 // 默认成员 ID
	}

	// 如果提供了完整配置，使用配置中的值覆盖
	if cfg.Config != nil {
		// 使用配置文件中的可靠性设置
		if cfg.ShutdownTimeout == 0 {
			cfg.ShutdownTimeout = cfg.Config.Server.Reliability.ShutdownTimeout
		}
		if !cfg.EnableCRC {
			cfg.EnableCRC = cfg.Config.Server.Reliability.EnableCRC
		}
		if !cfg.EnableHealthCheck {
			cfg.EnableHealthCheck = cfg.Config.Server.Reliability.EnableHealthCheck
		}

		// 从配置文件构建资源限制
		if cfg.ResourceLimits == nil {
			cfg.ResourceLimits = &reliability.ResourceLimits{
				MaxConnections: int64(cfg.Config.Server.Limits.MaxConnections),
				MaxRequests:    int64(cfg.Config.Server.Limits.MaxConnections * 10), // 每个连接允许10个并发请求
				MaxMemoryBytes: cfg.Config.Server.Limits.MaxRequestSize * 1000,      // 粗略估计
			}
		}
	}

	// 设置可靠性配置默认值
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}
	if cfg.ResourceLimits == nil {
		limits := reliability.DefaultLimits
		cfg.ResourceLimits = &limits
	}

	// 创建监听器
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %v", cfg.Address, err)
	}

	// 初始化可靠性组件
	var shutdownMgr *reliability.GracefulShutdown
	if cfg.Config != nil {
		shutdownMgr = reliability.NewGracefulShutdown(cfg.ShutdownTimeout, cfg.Config.Server.Reliability.DrainTimeout)
	} else {
		shutdownMgr = reliability.NewGracefulShutdown(cfg.ShutdownTimeout)
	}
	resourceMgr := reliability.NewResourceManager(*cfg.ResourceLimits)
	healthMgr := reliability.NewHealthManager()
	dataValidator := reliability.NewDataValidator(cfg.EnableCRC)

	// 创建 LeaseManager（使用配置）
	var leaseMgr *LeaseManager
	if cfg.Config != nil {
		leaseMgr = NewLeaseManager(cfg.Store, &cfg.Config.Server.Lease, &cfg.Config.Server.Limits)
	} else {
		leaseMgr = NewLeaseManager(cfg.Store, nil, nil)
	}

	// 创建 WatchManager（使用配置）
	var watchMgr *WatchManager
	if cfg.Config != nil {
		watchMgr = NewWatchManager(cfg.Store, &cfg.Config.Server.Limits)
	} else {
		watchMgr = NewWatchManager(cfg.Store)
	}

	// 创建 AuthManager（使用配置）
	var authMgr *AuthManager
	if cfg.Config != nil {
		authMgr = NewAuthManager(cfg.Store, &cfg.Config.Server.Auth)
	} else {
		authMgr = NewAuthManager(cfg.Store)
	}

	// 创建 Server 实例（需要先创建以便使用其方法）
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

	// 构建 gRPC 服务器选项
	grpcOpts := []grpc.ServerOption{
		// 拦截器链
		grpc.ChainUnaryInterceptor(
			s.PanicRecoveryInterceptor,   // Panic 恢复（第一层）
			resourceMgr.LimitInterceptor, // 资源限制
			s.AuthInterceptor,            // 认证授权
		),
	}

	// 如果提供了配置，应用 gRPC 配置
	if cfg.Config != nil {
		grpcCfg := cfg.Config.Server.GRPC

		// 消息大小限制
		if grpcCfg.MaxRecvMsgSize > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxRecvMsgSize(grpcCfg.MaxRecvMsgSize))
		}
		if grpcCfg.MaxSendMsgSize > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxSendMsgSize(grpcCfg.MaxSendMsgSize))
		}

		// 并发流限制
		if grpcCfg.MaxConcurrentStreams > 0 {
			grpcOpts = append(grpcOpts, grpc.MaxConcurrentStreams(grpcCfg.MaxConcurrentStreams))
		}

		// 流控制窗口大小
		if grpcCfg.InitialWindowSize > 0 {
			grpcOpts = append(grpcOpts, grpc.InitialWindowSize(grpcCfg.InitialWindowSize))
		}
		if grpcCfg.InitialConnWindowSize > 0 {
			grpcOpts = append(grpcOpts, grpc.InitialConnWindowSize(grpcCfg.InitialConnWindowSize))
		}

		// Keepalive 配置
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

	// 创建 gRPC server
	grpcSrv := grpc.NewServer(grpcOpts...)
	s.grpcSrv = grpcSrv

	// 初始化 ClusterManager（如果提供了 ConfChangeC）
	if cfg.ConfChangeC != nil {
		s.clusterMgr = NewClusterManager(cfg.ConfChangeC)

		// 初始化所有集群成员
		members := make([]*MemberInfo, 0, len(cfg.ClusterPeers))
		for i, peerURL := range cfg.ClusterPeers {
			memberID := uint64(i + 1) // 成员ID从1开始
			members = append(members, &MemberInfo{
				ID:         memberID,
				Name:       fmt.Sprintf("node-%d", memberID),
				PeerURLs:   []string{peerURL},
				ClientURLs: []string{fmt.Sprintf("http://127.0.0.1:%d", 9120+memberID)}, // 根据约定生成
				IsLearner:  false,
			})
		}
		s.clusterMgr.InitialMembers(members)
	}

	// 注册 gRPC 服务
	pb.RegisterKVServer(grpcSrv, &KVServer{server: s})
	pb.RegisterWatchServer(grpcSrv, &WatchServer{server: s})
	pb.RegisterLeaseServer(grpcSrv, &LeaseServer{server: s})

	// 创建 Maintenance 服务器（使用配置）
	snapshotChunkSize := 4 * 1024 * 1024 // 默认 4MB
	if cfg.Config != nil {
		snapshotChunkSize = cfg.Config.Server.Maintenance.SnapshotChunkSize
	}
	maintenanceServer := &MaintenanceServer{
		server:            s,
		snapshotChunkSize: snapshotChunkSize,
	}
	pb.RegisterMaintenanceServer(grpcSrv, maintenanceServer)
	pb.RegisterAuthServer(grpcSrv, &AuthServer{server: s})

	// 注册 Cluster 服务（委托给 MaintenanceServer 的实现）
	pb.RegisterClusterServer(grpcSrv, &ClusterServer{maintenance: maintenanceServer})

	// 注册健康检查服务
	if cfg.EnableHealthCheck {
		healthpb.RegisterHealthServer(grpcSrv, healthMgr.GetServer())

		// 注册健康检查器
		healthMgr.RegisterChecker(reliability.NewStorageHealthChecker("storage", func(ctx context.Context) error {
			// 检查存储是否可用
			if s.store == nil {
				return fmt.Errorf("storage is nil")
			}
			return nil
		}))

		healthMgr.RegisterChecker(reliability.NewLeaseHealthChecker("lease", func(ctx context.Context) error {
			// 检查 Lease 管理器是否正常
			if s.leaseMgr == nil {
				return fmt.Errorf("lease manager is nil")
			}
			return nil
		}))

		// 设置初始状态为 SERVING
		healthMgr.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	}

	// 注册优雅关闭钩子
	shutdownMgr.RegisterHook(reliability.PhaseStopAccepting, func(ctx context.Context) error {
		log.Info("Shutdown phase: Stop accepting new connections",
			log.Phase("StopAccepting"),
			log.Component("server"))
		// 标记为不健康，停止接受新请求
		if cfg.EnableHealthCheck {
			healthMgr.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		}
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhaseDrainConnections, func(ctx context.Context) error {
		log.Info("Shutdown phase: Drain existing connections",
			log.Phase("DrainConnections"),
			log.Component("server"))
		// 等待现有请求完成（通过 context 超时控制）
		time.Sleep(2 * time.Second)
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhasePersistState, func(ctx context.Context) error {
		log.Info("Shutdown phase: Persist state",
			log.Phase("PersistState"),
			log.Component("server"))
		// Lease 和 Watch 管理器已经在各自的 Stop() 中处理持久化
		return nil
	})

	shutdownMgr.RegisterHook(reliability.PhaseCloseResources, func(ctx context.Context) error {
		log.Info("Shutdown phase: Close resources",
			log.Phase("CloseResources"),
			log.Component("server"))

		// 停止 Lease 管理器
		if s.leaseMgr != nil {
			s.leaseMgr.Stop()
		}

		// 停止 Watch 管理器
		if s.watchMgr != nil {
			s.watchMgr.Stop()
		}

		// 停止资源管理器
		if s.resourceMgr != nil {
			s.resourceMgr.Close()
		}

		// 优雅关闭 gRPC server
		if s.grpcSrv != nil {
			s.grpcSrv.GracefulStop()
		}

		// 关闭监听器
		if s.listener != nil {
			s.listener.Close()
		}

		return nil
	})

	return s, nil
}

// Start 启动 gRPC 服务器
func (s *Server) Start() error {
	log.Info("Starting etcd-compatible gRPC server",
		log.String("address", s.listener.Addr().String()),
		log.Component("server"))

	// 启动 Lease 管理器的过期检查
	reliability.SafeGo("lease-expiry-checker", func() {
		s.leaseMgr.Start()
	})

	// 启动优雅关闭监听器（在后台等待信号）
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

	// 启动 gRPC 服务
	return s.grpcSrv.Serve(s.listener)
}

// Stop 停止 gRPC 服务器（触发优雅关闭）
func (s *Server) Stop() {
	log.Info("Triggering graceful shutdown",
		log.Component("server"))
	s.shutdownMgr.Shutdown()
}

// WaitForShutdown 等待服务器关闭完成
func (s *Server) WaitForShutdown() {
	<-s.shutdownMgr.Done()
	log.Info("Server shutdown complete",
		log.Component("server"))
}

// Address 返回服务器监听地址
func (s *Server) Address() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// getResponseHeader 创建标准的响应头
func (s *Server) getResponseHeader() *pb.ResponseHeader {
	return &pb.ResponseHeader{
		ClusterId: s.clusterID,
		MemberId:  s.memberID,
		Revision:  s.store.CurrentRevision(),
		RaftTerm:  s.store.GetRaftStatus().Term,
	}
}

// PanicRecoveryInterceptor Panic 恢复拦截器
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

// GetResourceStats 获取资源使用统计
func (s *Server) GetResourceStats() reliability.ResourceStats {
	return s.resourceMgr.GetStats()
}

// GetPanicCount 获取 panic 计数
func (s *Server) GetPanicCount() int64 {
	return reliability.GetPanicCount()
}

// GetValidationErrorCount 获取验证错误计数
func (s *Server) GetValidationErrorCount() int64 {
	return reliability.GetValidationErrorCount()
}
