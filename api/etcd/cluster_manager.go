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
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	"go.etcd.io/raft/v3/raftpb"
)

// ClusterManager managementclustermember
type ClusterManager struct {
	mu      sync.RWMutex
	members map[uint64]*MemberInfo

	// Raft configchangechannel
	confChangeC chan<- raftpb.ConfChange
}

// NewClusterManager create Cluster manager
func NewClusterManager(confChangeC chan<- raftpb.ConfChange) *ClusterManager {
	return &ClusterManager{
		members:     make(map[uint64]*MemberInfo),
		confChangeC: confChangeC,
	}
}

// ListMembers listallmember
func (cm *ClusterManager) ListMembers() []*MemberInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	members := make([]*MemberInfo, 0, len(cm.members))
	for _, member := range cm.members {
		members = append(members, member)
	}
	return members
}

// AddMember addmember
func (cm *ClusterManager) AddMember(peerURLs []string, isLearner bool) (*MemberInfo, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. becomenewmember ID
	memberID := generateMemberID()

	// 2. creatememberinfo
	member := &MemberInfo{
		ID:         memberID,
		Name:       fmt.Sprintf("node-%d", memberID),
		PeerURLs:   peerURLs,
		ClientURLs: []string{}, // initialasempty，aftercanvia Update set
		IsLearner:  isLearner,
	}

	// 3. create ConfChange
	var ccType raftpb.ConfChangeType
	if isLearner {
		ccType = raftpb.ConfChangeAddLearnerNode
	} else {
		ccType = raftpb.ConfChangeAddNode
	}

	//  Context(PeerURLs & ClientURLs as JSON)
	ctxData := MemberContext{
		PeerURLs:   peerURLs,
		ClientURLs: []string{}, // Empty initially; node will self-publish its configured URLs
	}
	context, _ := json.Marshal(ctxData)

	cc := raftpb.ConfChange{
		Type:    ccType,
		NodeID:  memberID,
		Context: context,
	}

	// 4. sendto confChangeC(asynchronous)
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// successsend
		default:
			return nil, fmt.Errorf("confChange channel full")
		}
	}

	// 5. returnmemberinfo (map is updated only via ApplyConfChange)
	return member, nil
}

// AddWitnessMember adds a witness node to the cluster
// Witness nodes participate in Raft voting but don't store data
// They enable 2-node HA by providing the 3rd vote needed for quorum
func (cm *ClusterManager) AddWitnessMember(peerURLs []string) (*MemberInfo, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. Generate new member ID
	memberID := generateMemberID()

	// 2. Create member info with witness flag
	member := &MemberInfo{
		ID:         memberID,
		Name:       fmt.Sprintf("witness-%d", memberID),
		PeerURLs:   peerURLs,
		ClientURLs: []string{}, // Witness nodes don't serve client requests
		IsLearner:  false,      // Witness is a voter, not a learner
		IsWitness:  true,       // Mark as witness node
	}

	// 3. Create ConfChange - Witness nodes are added as regular voters
	// The witness behavior is controlled by the node's configuration, not Raft
	ctxData := MemberContext{
		PeerURLs:   peerURLs,
		ClientURLs: []string{},
	}
	context, _ := json.Marshal(ctxData)

	cc := raftpb.ConfChange{
		Type:    raftpb.ConfChangeAddNode, // Witness is a voter
		NodeID:  memberID,
		Context: context,
	}

	// 4. Send to confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// Successfully sent
		default:
			return nil, fmt.Errorf("confChange channel full")
		}
	}

	// 5. Return member info (map is updated only via ApplyConfChange)
	return member, nil
}

// RemoveMember member
func (cm *ClusterManager) RemoveMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. checkmemberisnoin
	if _, exists := cm.members[id]; !exists {
		return fmt.Errorf("member %d not found", id)
	}

	// 2. create ConfChange
	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeRemoveNode,
		NodeID: id,
	}

	// 3. sendto confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// successsend
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	// 4. map is updated only via ApplyConfChange
	return nil
}

// UpdateMember updatememberinfo
func (cm *ClusterManager) UpdateMember(id uint64, peerURLs []string) error {
	return cm.UpdateMemberWithClientURLs(id, peerURLs, nil)
}

// UpdateMemberWithClientURLs updates member info including ClientURLs
func (cm *ClusterManager) UpdateMemberWithClientURLs(id uint64, peerURLs []string, clientURLs []string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. checkmemberisnoin
	member, exists := cm.members[id]
	if !exists {
		return fmt.Errorf("member %d not found", id)
	}

	// 2. compute desired URLs (do NOT mutate local state here)
	// Local member list should reflect committed Raft state only.
	newPeerURLs := member.PeerURLs
	newClientURLs := member.ClientURLs
	if peerURLs != nil {
		newPeerURLs = peerURLs
	}
	if clientURLs != nil {
		newClientURLs = clientURLs
	}

	// 3. create ConfChange with JSON context
	ctxData := MemberContext{
		PeerURLs:   newPeerURLs,
		ClientURLs: newClientURLs,
	}
	context, _ := json.Marshal(ctxData)

	cc := raftpb.ConfChange{
		Type:    raftpb.ConfChangeUpdateNode,
		NodeID:  id,
		Context: context,
	}

	// sendto confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// successsend
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	return nil
}

// PromoteMember  learner as voting member
func (cm *ClusterManager) PromoteMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. checkmemberisnoinis learner
	member, exists := cm.members[id]
	if !exists {
		return fmt.Errorf("member %d not found", id)
	}

	if !member.IsLearner {
		return fmt.Errorf("member %d is already a voting member", id)
	}

	// 2. create ConfChange
	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddNode, //  learner use AddNode
		NodeID: id,
	}

	// 3. sendto confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// successsend
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	// 4. member status is updated only via ApplyConfChange
	return nil
}

// ApplyConfChange appliedconfigchange( Raft )
func (cm *ClusterManager) ApplyConfChange(cc raftpb.ConfChange, confState raftpb.ConfState) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Parse context: try JSON first, fallback to legacy string
	ctx := parseMemberContext(cc.Context)

	// root ConfChange typeupdate members map
	switch cc.Type {
	case raftpb.ConfChangeAddNode:
		// add voting memberor learner
		if member, exists := cm.members[cc.NodeID]; exists {
			// exists，isoperation
			member.IsLearner = false
			// Update URLs if provided
			if len(ctx.PeerURLs) > 0 {
				member.PeerURLs = ctx.PeerURLs
			}
			if len(ctx.ClientURLs) > 0 {
				member.ClientURLs = ctx.ClientURLs
			}
		} else {
			// newincreasemember
			cm.members[cc.NodeID] = &MemberInfo{
				ID:         cc.NodeID,
				Name:       fmt.Sprintf("node-%d", cc.NodeID),
				PeerURLs:   ctx.PeerURLs,
				ClientURLs: ctx.ClientURLs,
				IsLearner:  false,
			}
		}

	case raftpb.ConfChangeAddLearnerNode:
		// add learner member
		cm.members[cc.NodeID] = &MemberInfo{
			ID:         cc.NodeID,
			Name:       fmt.Sprintf("node-%d", cc.NodeID),
			PeerURLs:   ctx.PeerURLs,
			ClientURLs: ctx.ClientURLs,
			IsLearner:  true,
		}

	case raftpb.ConfChangeRemoveNode:
		// member
		delete(cm.members, cc.NodeID)

	case raftpb.ConfChangeUpdateNode:
		// updatemember
		if member, exists := cm.members[cc.NodeID]; exists {
			if len(ctx.PeerURLs) > 0 {
				member.PeerURLs = ctx.PeerURLs
			}
			if len(ctx.ClientURLs) > 0 {
				member.ClientURLs = ctx.ClientURLs
			}
		}
	}
}

// parseMemberContext parses ConfChange.Context with JSON/legacy fallback
func parseMemberContext(data []byte) MemberContext {
	if len(data) == 0 {
		return MemberContext{}
	}
	var ctx MemberContext
	if err := json.Unmarshal(data, &ctx); err == nil && (len(ctx.PeerURLs) > 0 || len(ctx.ClientURLs) > 0) {
		return ctx
	}
	// Legacy fallback: treat as single PeerURL string
	return MemberContext{
		PeerURLs:   []string{string(data)},
		ClientURLs: []string{},
	}
}

// generateMemberID becomenewmember ID(useencryptrandom)
func generateMemberID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: usesecondstime
		return uint64(binary.BigEndian.Uint64(b[:]))
	}
	return binary.BigEndian.Uint64(b[:])
}

// GetMember getmemberinfo
func (cm *ClusterManager) GetMember(id uint64) (*MemberInfo, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	member, exists := cm.members[id]
	if !exists {
		return nil, fmt.Errorf("member %d not found", id)
	}
	return member, nil
}

// InitialMembers initializememberlist(startwhenfromconfigload)
func (cm *ClusterManager) InitialMembers(members []*MemberInfo) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, member := range members {
		cm.members[member.ID] = member
	}
}
