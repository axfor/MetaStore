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

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// LeaseServer 实现 etcd Lease 服务
type LeaseServer struct {
	pb.UnimplementedLeaseServer
	server *Server
}

// LeaseGrant 创建租约
func (s *LeaseServer) LeaseGrant(ctx context.Context, req *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	ttl := req.TTL
	id := req.ID

	// 如果没有指定 ID，自动生成
	if id == 0 {
		id = s.server.store.CurrentRevision() + 1
	}

	// 创建 lease
	lease, err := s.server.leaseMgr.Grant(id, ttl)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.LeaseGrantResponse{
		Header: s.server.getResponseHeader(),
		ID:     lease.ID,
		TTL:    lease.TTL,
	}, nil
}

// LeaseRevoke 撤销租约
func (s *LeaseServer) LeaseRevoke(ctx context.Context, req *pb.LeaseRevokeRequest) (*pb.LeaseRevokeResponse, error) {
	id := req.ID

	// 撤销 lease
	if err := s.server.leaseMgr.Revoke(id); err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.LeaseRevokeResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// LeaseKeepAlive 续约（流式）
func (s *LeaseServer) LeaseKeepAlive(stream pb.Lease_LeaseKeepAliveServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		id := req.ID

		// 续约 lease
		lease, err := s.server.leaseMgr.Renew(id)
		if err != nil {
			// 如果 lease 不存在或过期，发送错误
			return toGRPCError(err)
		}

		// 发送续约响应
		if err := stream.Send(&pb.LeaseKeepAliveResponse{
			Header: s.server.getResponseHeader(),
			ID:     lease.ID,
			TTL:    lease.TTL,
		}); err != nil {
			return err
		}
	}
}

// LeaseTimeToLive 获取租约剩余时间
func (s *LeaseServer) LeaseTimeToLive(ctx context.Context, req *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	id := req.ID

	// 获取 lease 信息
	lease, err := s.server.leaseMgr.TimeToLive(id)
	if err != nil {
		return nil, toGRPCError(err)
	}

	resp := &pb.LeaseTimeToLiveResponse{
		Header:    s.server.getResponseHeader(),
		ID:        lease.ID,
		TTL:       lease.Remaining(),
		GrantedTTL: lease.TTL,
	}

	// 如果请求包含关联的键
	if req.Keys {
		resp.Keys = make([][]byte, 0, len(lease.Keys))
		for key := range lease.Keys {
			resp.Keys = append(resp.Keys, []byte(key))
		}
	}

	return resp, nil
}

// Leases 列出所有租约
func (s *LeaseServer) Leases(ctx context.Context, req *pb.LeaseLeasesRequest) (*pb.LeaseLeasesResponse, error) {
	leases, err := s.server.leaseMgr.Leases()
	if err != nil {
		return nil, toGRPCError(err)
	}

	leaseStatuses := make([]*pb.LeaseStatus, len(leases))
	for i, lease := range leases {
		leaseStatuses[i] = &pb.LeaseStatus{
			ID: lease.ID,
		}
	}

	return &pb.LeaseLeasesResponse{
		Header: s.server.getResponseHeader(),
		Leases: leaseStatuses,
	}, nil
}
