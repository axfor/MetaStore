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

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// MaintenanceServer 实现 etcd Maintenance 服务
type MaintenanceServer struct {
	pb.UnimplementedMaintenanceServer
	server *Server
}

// Alarm 告警管理（暂不实现）
func (s *MaintenanceServer) Alarm(ctx context.Context, req *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	return &pb.AlarmResponse{
		Header: s.server.getResponseHeader(),
		Alarms: []*pb.AlarmMember{}, // 空列表，表示无告警
	}, nil
}

// Status 获取服务器状态
func (s *MaintenanceServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	// 获取快照以计算数据库大小
	snapshot, err := s.server.store.GetSnapshot()
	var dbSize int64
	if err == nil {
		dbSize = int64(len(snapshot))
	}

	return &pb.StatusResponse{
		Header:    s.server.getResponseHeader(),
		Version:   "3.6.0-compatible", // MetaStore 版本
		DbSize:    dbSize,
		Leader:    s.server.memberID,  // 简化：当前节点就是 leader
		RaftIndex: uint64(s.server.store.CurrentRevision()),
		RaftTerm:  1, // 简化：固定 term
	}, nil
}

// Defragment 碎片整理（暂不实现）
func (s *MaintenanceServer) Defragment(ctx context.Context, req *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	// TODO: 实现数据库碎片整理
	return &pb.DefragmentResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Hash 计算数据库哈希（暂不实现）
func (s *MaintenanceServer) Hash(ctx context.Context, req *pb.HashRequest) (*pb.HashResponse, error) {
	// TODO: 实现哈希计算
	return &pb.HashResponse{
		Header: s.server.getResponseHeader(),
		Hash:   0, // 占位
	}, nil
}

// HashKV 计算 KV 哈希（暂不实现）
func (s *MaintenanceServer) HashKV(ctx context.Context, req *pb.HashKVRequest) (*pb.HashKVResponse, error) {
	// TODO: 实现 KV 哈希计算
	return &pb.HashKVResponse{
		Header: s.server.getResponseHeader(),
		Hash:   0, // 占位
	}, nil
}

// Snapshot 创建快照
func (s *MaintenanceServer) Snapshot(req *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
	// 获取快照数据
	snapshot, err := s.server.store.GetSnapshot()
	if err != nil {
		return toGRPCError(err)
	}

	// 分块发送快照数据（每块 4MB）
	chunkSize := 4 * 1024 * 1024
	for i := 0; i < len(snapshot); i += chunkSize {
		end := i + chunkSize
		if end > len(snapshot) {
			end = len(snapshot)
		}

		// 发送快照块
		if err := stream.Send(&pb.SnapshotResponse{
			Header:        s.server.getResponseHeader(),
			RemainingBytes: uint64(len(snapshot) - end),
			Blob:          snapshot[i:end],
		}); err != nil {
			return err
		}
	}

	return nil
}

// MoveLeader 转移 leader（暂不实现）
func (s *MaintenanceServer) MoveLeader(ctx context.Context, req *pb.MoveLeaderRequest) (*pb.MoveLeaderResponse, error) {
	// TODO: 实现 leader 转移
	return &pb.MoveLeaderResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Downgrade 降级（暂不实现）
func (s *MaintenanceServer) Downgrade(ctx context.Context, req *pb.DowngradeRequest) (*pb.DowngradeResponse, error) {
	// TODO: 实现降级功能
	return &pb.DowngradeResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// MemberList 列出所有集群成员
func (s *MaintenanceServer) MemberList(ctx context.Context, req *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	// TODO: 实现
	// 1. 从 ClusterManager 获取成员列表
	// 2. 转换为 protobuf 格式
	// 3. 返回响应
	return &pb.MemberListResponse{
		Header:  s.server.getResponseHeader(),
		Members: []*pb.Member{}, // TODO: 返回成员列表
	}, nil
}

// MemberAdd 添加成员
func (s *MaintenanceServer) MemberAdd(ctx context.Context, req *pb.MemberAddRequest) (*pb.MemberAddResponse, error) {
	// TODO: 实现
	// 1. 验证权限（需要 root）
	// 2. 生成新的成员 ID
	// 3. 创建 ConfChange (ConfChangeAddNode 或 ConfChangeAddLearnerNode)
	// 4. 提交 ConfChange 到 Raft
	// 5. 等待 ConfChange 应用
	// 6. 返回新成员信息
	return &pb.MemberAddResponse{
		Header: s.server.getResponseHeader(),
		Member: &pb.Member{}, // TODO: 返回新成员
	}, nil
}

// MemberRemove 移除成员
func (s *MaintenanceServer) MemberRemove(ctx context.Context, req *pb.MemberRemoveRequest) (*pb.MemberRemoveResponse, error) {
	// TODO: 实现
	// 1. 验证权限
	// 2. 检查是否是最后一个成员（不能删除）
	// 3. 创建 ConfChange (ConfChangeRemoveNode)
	// 4. 提交 ConfChange 到 Raft
	// 5. 等待应用
	// 6. 返回响应
	return &pb.MemberRemoveResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// MemberUpdate 更新成员信息
func (s *MaintenanceServer) MemberUpdate(ctx context.Context, req *pb.MemberUpdateRequest) (*pb.MemberUpdateResponse, error) {
	// TODO: 实现
	// 1. 验证权限
	// 2. 检查成员是否存在
	// 3. 更新 PeerURLs (可能需要 ConfChange)
	// 4. 返回响应
	return &pb.MemberUpdateResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// MemberPromote 提升 learner 为 voting 成员
func (s *MaintenanceServer) MemberPromote(ctx context.Context, req *pb.MemberPromoteRequest) (*pb.MemberPromoteResponse, error) {
	// TODO: 实现
	// 1. 验证权限
	// 2. 检查成员是否是 learner
	// 3. 创建 ConfChange (ConfChangeType_PROMOTE)
	// 4. 提交 ConfChange
	// 5. 返回响应
	return &pb.MemberPromoteResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}
