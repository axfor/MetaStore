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
	"fmt"
	"hash/crc32"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// MaintenanceServer implement etcd Maintenance
type MaintenanceServer struct {
	pb.UnimplementedMaintenanceServer
	server            *Server
	snapshotChunkSize int // snapshotseparateblocksize()
}

// Alarm alarmmanagement
func (s *MaintenanceServer) Alarm(ctx context.Context, req *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	switch req.Action {
	case pb.AlarmRequest_GET:
		// getalarmlist
		alarms := s.server.alarmMgr.List()

		// ifspecified MemberID or Alarm type，rowfilter
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
		// activatealarm
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
		// cancelalarm
		s.server.alarmMgr.Deactivate(req.MemberID, req.Alarm)

		return &pb.AlarmResponse{
			Header: s.server.getResponseHeader(),
			Alarms: []*pb.AlarmMember{},
		}, nil

	default:
		return nil, toGRPCError(fmt.Errorf("unknown alarm action: %v", req.Action))
	}
}

// Status getserverstatus
func (s *MaintenanceServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	// getsnapshotcalculatedatasize
	snapshot, err := s.server.store.GetSnapshot()
	var dbSize int64
	if err == nil {
		dbSize = int64(len(snapshot))
	}

	// gettrue Raft status
	raftStatus := s.server.store.GetRaftStatus()

	return &pb.StatusResponse{
		Header:    s.server.getResponseHeader(),
		Version:   "3.6.0-compatible", // MetaStore version
		DbSize:    dbSize,
		Leader:    raftStatus.LeaderID, // true Leader ID
		RaftIndex: uint64(s.server.store.CurrentRevision()),
		RaftTerm:  raftStatus.Term, // true Raft Term
	}, nil
}

// Defragment complete(compatible etcd interface)
func (s *MaintenanceServer) Defragment(ctx context.Context, req *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	// Defragment for completedata
	// for RocksDB：storageenginehandlecompress，nomanuallytrigger
	// for Memory：memorystorageno
	// returnsuccessresponse，hold etcd API compatible

	return &pb.DefragmentResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Hash calculatedatahash(for clusterfirstcheck)
func (s *MaintenanceServer) Hash(ctx context.Context, req *pb.HashRequest) (*pb.HashResponse, error) {
	// getsnapshotandcalculate CRC32 hash
	snapshot, err := s.server.store.GetSnapshot()
	if err != nil {
		return nil, toGRPCError(err)
	}

	// calculate CRC32 hash
	hash := crc32.ChecksumIEEE(snapshot)

	return &pb.HashResponse{
		Header: s.server.getResponseHeader(),
		Hash:   uint32(hash),
	}, nil
}

// HashKV calculatespecified revision  KV hash
func (s *MaintenanceServer) HashKV(ctx context.Context, req *pb.HashKVRequest) (*pb.HashKVResponse, error) {
	// getspecified revision all KV data
	// use Range queryallkey
	resp, err := s.server.store.Range(ctx, "", "\x00", 0, req.Revision)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// calculatehash：willall KV serializeaftercalculate CRC32
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

// Snapshot createsnapshot
func (s *MaintenanceServer) Snapshot(req *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
	// getsnapshotdata
	snapshot, err := s.server.store.GetSnapshot()
	if err != nil {
		return toGRPCError(err)
	}

	// separateblocksendsnapshotdata(useconfigblocksize)
	chunkSize := s.snapshotChunkSize
	for i := 0; i < len(snapshot); i += chunkSize {
		end := i + chunkSize
		if end > len(snapshot) {
			end = len(snapshot)
		}

		// sendsnapshotblock
		if err := stream.Send(&pb.SnapshotResponse{
			Header:         s.server.getResponseHeader(),
			RemainingBytes: uint64(len(snapshot) - end),
			Blob:           snapshot[i:end],
		}); err != nil {
			return err
		}
	}

	return nil
}

// MoveLeader  leader(via Raft TransferLeadership)
func (s *MaintenanceServer) MoveLeader(ctx context.Context, req *pb.MoveLeaderRequest) (*pb.MoveLeaderResponse, error) {
	// checkcurrentnodeisnois leader
	raftStatus := s.server.store.GetRaftStatus()
	if raftStatus.LeaderID != s.server.memberID {
		return nil, toGRPCError(fmt.Errorf("not leader, current leader: %d", raftStatus.LeaderID))
	}

	// verifytargetnodeID
	if req.TargetID == 0 {
		return nil, toGRPCError(fmt.Errorf("target ID must be specified"))
	}

	// call Store  TransferLeadership methodrow leader
	if err := s.server.store.TransferLeadership(req.TargetID); err != nil {
		return nil, toGRPCError(fmt.Errorf("failed to transfer leadership: %w", err))
	}

	return &pb.MoveLeaderResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Downgrade degradation(not supported)
func (s *MaintenanceServer) Downgrade(ctx context.Context, req *pb.DowngradeRequest) (*pb.DowngradeResponse, error) {
	// Downgrade for degradationclusterversion，currentnot supported
	// return unimplemented incorrect
	return nil, toGRPCError(fmt.Errorf("downgrade is not supported"))
}

// MemberList listallclustermember
func (s *MaintenanceServer) MemberList(ctx context.Context, req *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	var pbMembers []*pb.Member

	if s.server.clusterMgr == nil {
		// ClusterManagernot initializewhen，fromclusterPeersmemberlist
		// allowinnoneConfChangeCnextcanreturnclustermemberinfo
		if len(s.server.clusterPeers) > 0 {
			pbMembers = make([]*pb.Member, 0, len(s.server.clusterPeers))
			for i, peerURL := range s.server.clusterPeers {
				memberID := uint64(i + 1)
				// Only the local member can reliably report its configured advertised
				// client URLs before ClusterManager is initialized.
				clientURLs := []string{}
				if memberID == s.server.memberID && len(s.server.clientURLs) > 0 {
					clientURLs = s.server.clientURLs
				}
				pbMembers = append(pbMembers, &pb.Member{
					ID:         memberID,
					Name:       fmt.Sprintf("node-%d", memberID),
					PeerURLs:   []string{peerURL},
					ClientURLs: clientURLs,
					IsLearner:  false,
				})
			}
		} else {
			// completelynoneclusterinfowhen，returncurrentnode
			clientURLs := s.server.clientURLs
			pbMembers = []*pb.Member{
				{
					ID:         s.server.memberID,
					Name:       fmt.Sprintf("node-%d", s.server.memberID),
					PeerURLs:   []string{fmt.Sprintf("http://127.0.0.1:902%d", s.server.memberID)},
					ClientURLs: clientURLs,
					IsLearner:  false,
				},
			}
		}
	} else {
		// 1. from ClusterManager getmemberlist
		members := s.server.clusterMgr.ListMembers()

		// 2. convertas protobuf format
		pbMembers = make([]*pb.Member, 0, len(members))
		for _, member := range members {
			clientURLs := member.ClientURLs
			// If Raft hasn't yet committed our self-published client URLs, still
			// return the configured value for the local member.
			if member.ID == s.server.memberID && len(clientURLs) == 0 && len(s.server.clientURLs) > 0 {
				clientURLs = s.server.clientURLs
			}
			pbMembers = append(pbMembers, &pb.Member{
				ID:         member.ID,
				Name:       member.Name,
				PeerURLs:   member.PeerURLs,
				ClientURLs: clientURLs,
				IsLearner:  member.IsLearner,
			})
		}
	}

	// 3. returnresponse
	return &pb.MemberListResponse{
		Header:  s.server.getResponseHeader(),
		Members: pbMembers,
	}, nil
}

// MemberAdd addmember
func (s *MaintenanceServer) MemberAdd(ctx context.Context, req *pb.MemberAddRequest) (*pb.MemberAddResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. call ClusterManager addmember
	member, err := s.server.clusterMgr.AddMember(req.PeerURLs, req.IsLearner)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// 2. returnnewmemberinfo
	return &pb.MemberAddResponse{
		Header: s.server.getResponseHeader(),
		Member: &pb.Member{
			ID:         member.ID,
			Name:       member.Name,
			PeerURLs:   member.PeerURLs,
			ClientURLs: member.ClientURLs,
			IsLearner:  member.IsLearner,
		},
		Members: nil, // optional：returnallmember
	}, nil
}

// MemberRemove member
func (s *MaintenanceServer) MemberRemove(ctx context.Context, req *pb.MemberRemoveRequest) (*pb.MemberRemoveResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. checkisnoislastfirst member
	members := s.server.clusterMgr.ListMembers()
	if len(members) <= 1 {
		return nil, toGRPCError(fmt.Errorf("cannot remove the last member"))
	}

	// 2. call ClusterManager member
	if err := s.server.clusterMgr.RemoveMember(req.ID); err != nil {
		return nil, toGRPCError(err)
	}

	// 3. returnresponse
	return &pb.MemberRemoveResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // optional：returnallmember
	}, nil
}

// MemberUpdate updatememberinfo
func (s *MaintenanceServer) MemberUpdate(ctx context.Context, req *pb.MemberUpdateRequest) (*pb.MemberUpdateResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. call ClusterManager updatemember
	if err := s.server.clusterMgr.UpdateMember(req.ID, req.PeerURLs); err != nil {
		return nil, toGRPCError(err)
	}

	// 2. returnresponse
	return &pb.MemberUpdateResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // optional：returnallmember
	}, nil
}

// MemberPromote  learner as voting member
func (s *MaintenanceServer) MemberPromote(ctx context.Context, req *pb.MemberPromoteRequest) (*pb.MemberPromoteResponse, error) {
	if s.server.clusterMgr == nil {
		return nil, toGRPCError(fmt.Errorf("cluster manager not initialized"))
	}

	// 1. call ClusterManager member
	if err := s.server.clusterMgr.PromoteMember(req.ID); err != nil {
		return nil, toGRPCError(err)
	}

	// 2. returnresponse
	return &pb.MemberPromoteResponse{
		Header:  s.server.getResponseHeader(),
		Members: nil, // optional：returnallmember
	}, nil
}
