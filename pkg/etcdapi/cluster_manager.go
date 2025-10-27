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
	"sync"
	"time"

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

	// TODO: 实现
	// 1. 生成新的成员 ID
	// 2. 创建 ConfChange
	// 3. 发送到 confChangeC
	// 4. 等待结果
	// 5. 添加到 members map
	// 6. 返回成员信息
	return nil, nil
}

// RemoveMember 移除成员
func (cm *ClusterManager) RemoveMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// TODO: 实现
	// 1. 检查成员是否存在
	// 2. 创建 ConfChange (ConfChangeRemoveNode)
	// 3. 发送到 confChangeC
	// 4. 等待结果
	// 5. 从 members map 删除
	return nil
}

// UpdateMember 更新成员信息
func (cm *ClusterManager) UpdateMember(id uint64, peerURLs []string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// TODO: 实现
	// 1. 检查成员是否存在
	// 2. 更新 PeerURLs
	// 3. 如果需要，创建 ConfChange
	// 4. 持久化
	return nil
}

// PromoteMember 提升 learner
func (cm *ClusterManager) PromoteMember(id uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// TODO: 实现
	// 1. 检查成员是否存在且是 learner
	// 2. 创建 ConfChange (PROMOTE)
	// 3. 发送到 confChangeC
	// 4. 更新成员状态
	return nil
}

// ApplyConfChange 应用配置变更（由 Raft 回调）
func (cm *ClusterManager) ApplyConfChange(cc raftpb.ConfChange, confState raftpb.ConfState) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// TODO: 实现
	// 根据 ConfChange 类型更新 members map
	switch cc.Type {
	case raftpb.ConfChangeAddNode:
		// 添加 voting 成员
	case raftpb.ConfChangeAddLearnerNode:
		// 添加 learner 成员
	case raftpb.ConfChangeRemoveNode:
		// 移除成员
	case raftpb.ConfChangeUpdateNode:
		// 更新成员
	}
}

// generateMemberID 生成新的成员 ID
func generateMemberID() uint64 {
	// TODO: 实现更robust的ID生成算法
	// 当前使用纳秒时间戳
	return uint64(time.Now().UnixNano())
}
