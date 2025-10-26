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
	"fmt"
	"log"
	"metaStore/internal/kvstore"
	"net"
	"sync"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
)

// Server etcd 兼容的 gRPC 服务器
type Server struct {
	mu       sync.RWMutex
	store    kvstore.Store    // 底层存储
	grpcSrv  *grpc.Server     // gRPC server
	listener net.Listener     // 网络监听器

	// 管理组件
	watchMgr *WatchManager    // Watch 管理器
	leaseMgr *LeaseManager    // Lease 管理器

	// 配置
	clusterID uint64          // 集群 ID
	memberID  uint64          // 成员 ID
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Store     kvstore.Store   // 底层存储（必需）
	Address   string          // 监听地址（例如 ":2379"）
	ClusterID uint64          // 集群 ID
	MemberID  uint64          // 成员 ID
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

	// 创建监听器
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %v", cfg.Address, err)
	}

	// 创建 gRPC server
	grpcSrv := grpc.NewServer()

	// 创建 Server 实例
	s := &Server{
		store:     cfg.Store,
		grpcSrv:   grpcSrv,
		listener:  listener,
		watchMgr:  NewWatchManager(cfg.Store),
		leaseMgr:  NewLeaseManager(cfg.Store),
		clusterID: cfg.ClusterID,
		memberID:  cfg.MemberID,
	}

	// 注册 gRPC 服务
	pb.RegisterKVServer(grpcSrv, &KVServer{server: s})
	pb.RegisterWatchServer(grpcSrv, &WatchServer{server: s})
	pb.RegisterLeaseServer(grpcSrv, &LeaseServer{server: s})
	pb.RegisterMaintenanceServer(grpcSrv, &MaintenanceServer{server: s})

	return s, nil
}

// Start 启动 gRPC 服务器
func (s *Server) Start() error {
	log.Printf("Starting etcd-compatible gRPC server on %s", s.listener.Addr().String())

	// 启动 Lease 管理器的过期检查
	s.leaseMgr.Start()

	// 启动 gRPC 服务
	return s.grpcSrv.Serve(s.listener)
}

// Stop 停止 gRPC 服务器
func (s *Server) Stop() {
	log.Println("Stopping etcd-compatible gRPC server")

	// 停止 Lease 管理器
	s.leaseMgr.Stop()

	// 停止 Watch 管理器
	s.watchMgr.Stop()

	// 优雅关闭 gRPC server
	s.grpcSrv.GracefulStop()

	// 关闭监听器
	if s.listener != nil {
		s.listener.Close()
	}
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
		RaftTerm:  0, // TODO: 从 Raft 获取 term
	}
}
