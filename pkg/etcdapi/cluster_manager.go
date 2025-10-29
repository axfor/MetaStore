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
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"

	"go.etcd.io/raft/v3/raftpb"
)

// ClusterManager 管理集群成员
type ClusterManager struct {
	mu      sync.RWMutex
	members map[uint64]*MemberInfo

	// Raft 配置变更通道
	confChangeC chan<- raftpb.ConfChange
}

// NewClusterManager 创建 Cluster 管理器
func NewClusterManager(confChangeC chan<- raftpb.ConfChange) *ClusterManager {
	return &ClusterManager{
		members:     make(map[uint64]*MemberInfo),
		confChangeC: confChangeC,
	}
}

// ListMembers 列出所有成员
func (cm *ClusterManager) ListMembers() []*MemberInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	members := make([]*MemberInfo, 0, len(cm.members))
	for _, member := range cm.members {
		members = append(members, member)
	}
	return members
}

// AddMember 添加成员
func (cm *ClusterManager) AddMember(peerURLs []string, isLearner bool) (*MemberInfo, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. 生成新的成员 ID
	memberID := generateMemberID()

	// 2. 创建成员信息
	member := &MemberInfo{
		ID:         memberID,
		Name:       fmt.Sprintf("node-%d", memberID),
		PeerURLs:   peerURLs,
		ClientURLs: []string{}, // 初始为空，稍后可通过 Update 设置
		IsLearner:  isLearner,
	}

	// 3. 创建 ConfChange
	var ccType raftpb.ConfChangeType
	if isLearner {
		ccType = raftpb.ConfChangeAddLearnerNode
	} else {
		ccType = raftpb.ConfChangeAddNode
	}

	// 构造 Context（PeerURLs）
	context := []byte{}
	if len(peerURLs) > 0 {
		context = []byte(peerURLs[0]) // 使用第一个 PeerURL
	}

	cc := raftpb.ConfChange{
		Type:    ccType,
		NodeID:  memberID,
		Context: context,
	}

	// 4. 发送到 confChangeC（异步）
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// 成功发送
		default:
			return nil, fmt.Errorf("confChange channel full")
		}
	}

	// 5. 添加到 members map
	cm.members[memberID] = member

	// 6. 返回成员信息
	return member, nil
}

// RemoveMember 移除成员
func (cm *ClusterManager) RemoveMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. 检查成员是否存在
	if _, exists := cm.members[id]; !exists {
		return fmt.Errorf("member %d not found", id)
	}

	// 2. 创建 ConfChange
	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeRemoveNode,
		NodeID: id,
	}

	// 3. 发送到 confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// 成功发送
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	// 4. 从 members map 删除
	delete(cm.members, id)

	return nil
}

// UpdateMember 更新成员信息
func (cm *ClusterManager) UpdateMember(id uint64, peerURLs []string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. 检查成员是否存在
	member, exists := cm.members[id]
	if !exists {
		return fmt.Errorf("member %d not found", id)
	}

	// 2. 更新 PeerURLs
	member.PeerURLs = peerURLs

	// 3. 创建 ConfChange（etcd 的 UpdateMember 也会触发 ConfChange）
	context := []byte{}
	if len(peerURLs) > 0 {
		context = []byte(peerURLs[0])
	}

	cc := raftpb.ConfChange{
		Type:    raftpb.ConfChangeUpdateNode,
		NodeID:  id,
		Context: context,
	}

	// 发送到 confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// 成功发送
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	return nil
}

// PromoteMember 提升 learner 为 voting 成员
func (cm *ClusterManager) PromoteMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. 检查成员是否存在且是 learner
	member, exists := cm.members[id]
	if !exists {
		return fmt.Errorf("member %d not found", id)
	}

	if !member.IsLearner {
		return fmt.Errorf("member %d is already a voting member", id)
	}

	// 2. 创建 ConfChange
	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddNode, // 提升 learner 使用 AddNode
		NodeID: id,
	}

	// 3. 发送到 confChangeC
	if cm.confChangeC != nil {
		select {
		case cm.confChangeC <- cc:
			// 成功发送
		default:
			return fmt.Errorf("confChange channel full")
		}
	}

	// 4. 更新成员状态
	member.IsLearner = false

	return nil
}

// ApplyConfChange 应用配置变更（由 Raft 回调）
func (cm *ClusterManager) ApplyConfChange(cc raftpb.ConfChange, confState raftpb.ConfState) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 根据 ConfChange 类型更新 members map
	switch cc.Type {
	case raftpb.ConfChangeAddNode:
		// 添加 voting 成员或提升 learner
		if member, exists := cm.members[cc.NodeID]; exists {
			// 已存在，是提升操作
			member.IsLearner = false
		} else {
			// 新增成员
			peerURL := ""
			if len(cc.Context) > 0 {
				peerURL = string(cc.Context)
			}
			cm.members[cc.NodeID] = &MemberInfo{
				ID:         cc.NodeID,
				Name:       fmt.Sprintf("node-%d", cc.NodeID),
				PeerURLs:   []string{peerURL},
				ClientURLs: []string{},
				IsLearner:  false,
			}
		}

	case raftpb.ConfChangeAddLearnerNode:
		// 添加 learner 成员
		peerURL := ""
		if len(cc.Context) > 0 {
			peerURL = string(cc.Context)
		}
		cm.members[cc.NodeID] = &MemberInfo{
			ID:         cc.NodeID,
			Name:       fmt.Sprintf("node-%d", cc.NodeID),
			PeerURLs:   []string{peerURL},
			ClientURLs: []string{},
			IsLearner:  true,
		}

	case raftpb.ConfChangeRemoveNode:
		// 移除成员
		delete(cm.members, cc.NodeID)

	case raftpb.ConfChangeUpdateNode:
		// 更新成员
		if member, exists := cm.members[cc.NodeID]; exists {
			if len(cc.Context) > 0 {
				member.PeerURLs = []string{string(cc.Context)}
			}
		}
	}
}

// generateMemberID 生成新的成员 ID（使用加密随机数）
func generateMemberID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: 使用纳秒时间戳
		return uint64(binary.BigEndian.Uint64(b[:]))
	}
	return binary.BigEndian.Uint64(b[:])
}

// GetMember 获取成员信息
func (cm *ClusterManager) GetMember(id uint64) (*MemberInfo, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	member, exists := cm.members[id]
	if !exists {
		return nil, fmt.Errorf("member %d not found", id)
	}
	return member, nil
}

// InitialMembers 初始化成员列表（启动时从配置加载）
func (cm *ClusterManager) InitialMembers(members []*MemberInfo) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, member := range members {
		cm.members[member.ID] = member
	}
}
