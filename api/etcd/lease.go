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
	"errors"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// LeaseServer implement etcd Lease 服务
type LeaseServer struct {
	pb.UnimplementedLeaseServer
	server *Server
}

// LeaseGrant createlease
func (s *LeaseServer) LeaseGrant(ctx context.Context, req *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	ttl := req.TTL
	id := req.ID

	// ifnonespecified ID，自动生成unique ID
	if id == 0 {
		id = s.server.leaseMgr.GenerateLeaseID()
	}

	// create lease
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

// LeaseRevoke revokelease
func (s *LeaseServer) LeaseRevoke(ctx context.Context, req *pb.LeaseRevokeRequest) (*pb.LeaseRevokeResponse, error) {
	id := req.ID

	// revoke lease
	if err := s.server.leaseMgr.Revoke(id); err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.LeaseRevokeResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// LeaseKeepAlive renewal（stream式）
func (s *LeaseServer) LeaseKeepAlive(stream pb.Lease_LeaseKeepAliveServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		id := req.ID

		// renewal lease
		lease, err := s.server.leaseMgr.Renew(id)
		if err != nil {
			// if lease does not existorexpiration，sendincorrect
			return toGRPCError(err)
		}

		// sendrenewalresponse
		if err := stream.Send(&pb.LeaseKeepAliveResponse{
			Header: s.server.getResponseHeader(),
			ID:     lease.ID,
			TTL:    lease.TTL,
		}); err != nil {
			return err
		}
	}
}

// LeaseTimeToLive getlease剩余time
func (s *LeaseServer) LeaseTimeToLive(ctx context.Context, req *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	id := req.ID

	// get lease info
	lease, err := s.server.leaseMgr.TimeToLive(id)
	if err != nil {
		// fordoes not exist Lease，etcd return TTL=-1 而notisincorrect
		// 这符合 etcd client期望rowas
		if errors.Is(err, ErrLeaseNotFound) {
			return &pb.LeaseTimeToLiveResponse{
				Header:     s.server.getResponseHeader(),
				ID:         id,
				TTL:        -1,
				GrantedTTL: 0,
			}, nil
		}
		return nil, toGRPCError(err)
	}

	resp := &pb.LeaseTimeToLiveResponse{
		Header:     s.server.getResponseHeader(),
		ID:         lease.ID,
		TTL:        lease.Remaining(),
		GrantedTTL: lease.TTL,
	}

	// ifrequestpackage含关联key
	if req.Keys {
		resp.Keys = make([][]byte, 0, len(lease.Keys))
		for key := range lease.Keys {
			resp.Keys = append(resp.Keys, []byte(key))
		}
	}

	return resp, nil
}

// Leases listalllease
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
