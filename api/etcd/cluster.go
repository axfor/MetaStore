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

package etcd

import (
	"context"
	"metaStore/pkg/log"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.uber.org/zap"
)

// ClusterServer 实现 etcd Cluster 服务
// 注意: 委托给 MaintenanceServer 的实现，因为 Member 方法已在那里实现
type ClusterServer struct {
	pb.UnimplementedClusterServer
	maintenance *MaintenanceServer
}

// MemberAdd 添加新的集群成员（委托给MaintenanceServer）
func (s *ClusterServer) MemberAdd(ctx context.Context, req *pb.MemberAddRequest) (*pb.MemberAddResponse, error) {
	log.Debug("ClusterServer.MemberAdd called, delegating to MaintenanceServer",
		zap.Strings("peer_urls", req.PeerURLs),
		zap.Bool("is_learner", req.IsLearner),
		zap.String("component", "cluster"))
	return s.maintenance.MemberAdd(ctx, req)
}

// MemberRemove 删除集群成员（委托给MaintenanceServer）
func (s *ClusterServer) MemberRemove(ctx context.Context, req *pb.MemberRemoveRequest) (*pb.MemberRemoveResponse, error) {
	log.Debug("ClusterServer.MemberRemove called, delegating to MaintenanceServer",
		zap.Uint64("member_id", req.ID),
		zap.String("component", "cluster"))
	return s.maintenance.MemberRemove(ctx, req)
}

// MemberUpdate 更新集群成员信息（委托给MaintenanceServer）
func (s *ClusterServer) MemberUpdate(ctx context.Context, req *pb.MemberUpdateRequest) (*pb.MemberUpdateResponse, error) {
	log.Debug("ClusterServer.MemberUpdate called, delegating to MaintenanceServer",
		zap.Uint64("member_id", req.ID),
		zap.Strings("peer_urls", req.PeerURLs),
		zap.String("component", "cluster"))
	return s.maintenance.MemberUpdate(ctx, req)
}

// MemberList 列出所有集群成员（委托给MaintenanceServer）
func (s *ClusterServer) MemberList(ctx context.Context, req *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	log.Debug("ClusterServer.MemberList called, delegating to MaintenanceServer",
		zap.String("component", "cluster"))
	return s.maintenance.MemberList(ctx, req)
}

// MemberPromote 将学习者提升为投票成员（委托给MaintenanceServer）
func (s *ClusterServer) MemberPromote(ctx context.Context, req *pb.MemberPromoteRequest) (*pb.MemberPromoteResponse, error) {
	log.Debug("ClusterServer.MemberPromote called, delegating to MaintenanceServer",
		zap.Uint64("member_id", req.ID),
		zap.String("component", "cluster"))
	return s.maintenance.MemberPromote(ctx, req)
}
