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
	"hash/crc32"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// MaintenanceServer 实现 etcd Maintenance 服务
type MaintenanceServer struct {
	pb.UnimplementedMaintenanceServer
	server *Server
}

// Alarm 告警管理
func (s *MaintenanceServer) Alarm(ctx context.Context, req *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	switch req.Action {
	case pb.AlarmRequest_GET:
		// 获取告警列表
		alarms := s.server.alarmMgr.List()

		// 如果指定了 MemberID 或 Alarm 类型，进行过滤
		if req.MemberID != 0 || req.Alarm != pb.AlarmType_NONE {
			filtered := make([]*pb.AlarmMember, 0)
			for _, alarm := range alarms {
				if (req.MemberID == 0 || alarm.MemberID == req.MemberID) &&
					(req.Alarm == pb.AlarmType_NONE || alarm.Alarm == req.Alarm) {
					filtered = append(filtered, alarm)
				}
			}
			alarms = filtered
		}

		return &pb.AlarmResponse{
			Header: s.server.getResponseHeader(),
			Alarms: alarms,
		}, nil

	case pb.AlarmRequest_ACTIVATE:
		// 激活告警
		alarm := &pb.AlarmMember{
			MemberID: req.MemberID,
			Alarm:    req.Alarm,
		}
		s.server.alarmMgr.Activate(alarm)

		return &pb.AlarmResponse{
			Header: s.server.getResponseHeader(),
			Alarms: []*pb.AlarmMember{alarm},
		}, nil

	case pb.AlarmRequest_DEACTIVATE:
		// 取消告警
		s.server.alarmMgr.Deactivate(req.MemberID, req.Alarm)

		return &pb.AlarmResponse{
			Header: s.server.getResponseHeader(),
			Alarms: []*pb.AlarmMember{},
		}, nil

	default:
		return nil, toGRPCError(fmt.Errorf("unknown alarm action: %v", req.Action))
	}
}

// Status 获取服务器状态
func (s *MaintenanceServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	// 获取快照以计算数据库大小
	snapshot, err := s.server.store.GetSnapshot()
	var dbSize int64
	if err == nil {
		dbSize = int64(len(snapshot))
	}

	// 获取真实的 Raft 状态
	raftStatus := s.server.store.GetRaftStatus()

	return &pb.StatusResponse{
		Header:    s.server.getResponseHeader(),
		Version:   "3.6.0-compatible", // MetaStore 版本
		DbSize:    dbSize,
		Leader:    raftStatus.LeaderID, // 真实的 Leader ID
		RaftIndex: uint64(s.server.store.CurrentRevision()),
		RaftTerm:  raftStatus.Term, // 真实的 Raft Term
	}, nil
}

// Defragment 碎片整理（兼容 etcd 接口）
func (s *MaintenanceServer) Defragment(ctx context.Context, req *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	// Defragment 用于整理数据库碎片
	// 对于 RocksDB：由存储引擎自动处理压缩，无需手动触发
	// 对于 Memory：内存存储无碎片问题
	// 这里只需返回成功响应，保持 etcd API 兼容性

	return &pb.DefragmentResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Hash 计算数据库哈希（用于集群一致性检查）
func (s *MaintenanceServer) Hash(ctx context.Context, req *pb.HashRequest) (*pb.HashResponse, error) {
	// 获取快照并计算 CRC32 哈希
	snapshot, err := s.server.store.GetSnapshot()
	if err != nil {
		return nil, toGRPCError(err)
	}

	// 计算 CRC32 哈希
	hash := crc32.ChecksumIEEE(snapshot)

	return &pb.HashResponse{
		Header: s.server.getResponseHeader(),
		Hash:   uint32(hash),
	}, nil
}

// HashKV 计算指定 revision 的 KV 哈希
func (s *MaintenanceServer) HashKV(ctx context.Context, req *pb.HashKVRequest) (*pb.HashKVResponse, error) {
	// 获取指定 revision 的所有 KV 数据
	// 使用 Range 查询所有键
	resp, err := s.server.store.Range(ctx, "", "\x00", 0, req.Revision)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// 计算哈希：将所有 KV 序列化后计算 CRC32
	hasher := crc32.NewIEEE()
	for _, kv := range resp.Kvs {
		hasher.Write(kv.Key)
		hasher.Write(kv.Value)
	}

	hash := hasher.Sum32()
	compactRevision := s.server.store.CurrentRevision()

	return &pb.HashKVResponse{
		Header:          s.server.getResponseHeader(),
		Hash:            hash,
		CompactRevision: compactRevision,
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

// MoveLeader 转移 leader（通过 Raft TransferLeadership）
func (s *MaintenanceServer) MoveLeader(ctx context.Context, req *pb.MoveLeaderRequest) (*pb.MoveLeaderResponse, error) {
	// 检查当前节点是否是 leader
	raftStatus := s.server.store.GetRaftStatus()
	if raftStatus.LeaderID != s.server.memberID {
		return nil, toGRPCError(fmt.Errorf("not leader, current leader: %d", raftStatus.LeaderID))
	}

	// 验证目标节点ID
	if req.TargetID == 0 {
		return nil, toGRPCError(fmt.Errorf("target ID must be specified"))
	}

	// 调用 Store 的 TransferLeadership 方法进行 leader 转移
	if err := s.server.store.TransferLeadership(req.TargetID); err != nil {
		return nil, toGRPCError(fmt.Errorf("failed to transfer leadership: %w", err))
	}

	return &pb.MoveLeaderResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Downgrade 降级（暂不支持）
func (s *MaintenanceServer) Downgrade(ctx context.Context, req *pb.DowngradeRequest) (*pb.DowngradeResponse, error) {
	// Downgrade 用于降级集群版本，当前不支持
	// 返回 unimplemented 错误
	return nil, toGRPCError(fmt.Errorf("downgrade is not supported"))
}

// MemberList 列出所有集群成员
func (s *MaintenanceServer) MemberList(ctx context.Context, req *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	if s.server.clusterMgr == nil {
		return &pb.MemberListResponse{
			Header:  s.server.getResponseHeader(),
			Members: []*pb.Member{},
		}, nil
	}

	// 1. 从 ClusterManager 获取成员列表
	members := s.server.clusterMgr.ListMembers()

	// 2. 转换为 protobuf 格式
	pbMembers := make([]*pb.Member, 0, len(members))
	for _, member := range members {
		pbMembers = append(pbMembers, &pb.Member{
			ID:         member.ID,
			Name:       member.Name,
			PeerURLs:   member.PeerURLs,
			ClientURLs: member.ClientURLs,
			IsLearner:  member.IsLearner,
		})
	}

	// 3. 返回响应
	return &pb.MemberListResponse{
		Header:  s.server.getResponseHeader(),
		Members: pbMembers,
	}, nil
}

// MemberAdd 添加成员
func (s *MaintenanceServer) MemberAdd(ctx context.Context, req *pb.MemberAddRequest) (*pb.MemberAddResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. 调用 ClusterManager 添加成员
	member, err := s.server.clusterMgr.AddMember(req.PeerURLs, req.IsLearner)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// 2. 返回新成员信息
	return &pb.MemberAddResponse{
		Header: s.server.getResponseHeader(),
		Member: &pb.Member{
			ID:         member.ID,
			Name:       member.Name,
			PeerURLs:   member.PeerURLs,
			ClientURLs: member.ClientURLs,
			IsLearner:  member.IsLearner,
		},
		Members: nil, // 可选：返回所有成员
	}, nil
}

// MemberRemove 移除成员
func (s *MaintenanceServer) MemberRemove(ctx context.Context, req *pb.MemberRemoveRequest) (*pb.MemberRemoveResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. 检查是否是最后一个成员
	members := s.server.clusterMgr.ListMembers()
	if len(members) <= 1 {
		return nil, toGRPCError(fmt.Errorf("cannot remove the last member"))
	}

	// 2. 调用 ClusterManager 移除成员
	if err := s.server.clusterMgr.RemoveMember(req.ID); err != nil {
		return nil, toGRPCError(err)
	}

	// 3. 返回响应
	return &pb.MemberRemoveResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // 可选：返回所有成员
	}, nil
}

// MemberUpdate 更新成员信息
func (s *MaintenanceServer) MemberUpdate(ctx context.Context, req *pb.MemberUpdateRequest) (*pb.MemberUpdateResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. 调用 ClusterManager 更新成员
	if err := s.server.clusterMgr.UpdateMember(req.ID, req.PeerURLs); err != nil {
		return nil, toGRPCError(err)
	}

	// 2. 返回响应
	return &pb.MemberUpdateResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // 可选：返回所有成员
	}, nil
}

// MemberPromote 提升 learner 为 voting 成员
func (s *MaintenanceServer) MemberPromote(ctx context.Context, req *pb.MemberPromoteRequest) (*pb.MemberPromoteResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. 调用 ClusterManager 提升成员
	if err := s.server.clusterMgr.PromoteMember(req.ID); err != nil {
		return nil, toGRPCError(err)
	}

	// 2. 返回响应
	return &pb.MemberPromoteResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // 可选：返回所有成员
	}, nil
}
