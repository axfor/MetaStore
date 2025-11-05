# Cluster Service 实现设计文档

## 概述

本文档详细描述了 MetaStore 的 etcd v3 兼容 Cluster Service 的设计方案、接口定义和实现步骤。

**目标**: 实现 100% etcd v3 Cluster API 兼容，提供完整的集群成员管理功能。

**优先级**: P0（必须实现，不符合 prompt 要求）

---

## 1. 功能需求

### 1.1 核心功能

根据 etcd v3 API 规范，Cluster Service 需要实现以下功能：

#### 成员管理（通过 Maintenance Service）
- ✅ **MemberList** - 列出所有集群成员
- ✅ **MemberAdd** - 添加新成员到集群
- ✅ **MemberRemove** - 从集群中移除成员
- ✅ **MemberUpdate** - 更新成员信息
- ✅ **MemberPromote** - 提升 learner 为 voting 成员

### 1.2 成员状态

```
Member (成员)
  ├─ ID (uint64) - 成员 ID
  ├─ Name (string) - 成员名称
  ├─ PeerURLs ([]string) - Raft peer 通信地址
  ├─ ClientURLs ([]string) - 客户端访问地址
  └─ IsLearner (bool) - 是否是 learner
```

### 1.3 集群配置变更

- 使用 Raft ConfChange 机制
- 保证配置变更的一致性
- 支持动态添加/删除成员

---

## 2. 架构设计

### 2.1 组件架构

```
┌─────────────────────────────────────────────────────────┐
│                Maintenance gRPC API                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │ MemberList   │  │ MemberAdd    │  │ MemberRemove │ │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘ │
│         │                  │                  │         │
│         └──────────────────┴──────────────────┘         │
│                         │                                │
│                   ┌─────▼─────┐                         │
│                   │  Cluster  │                         │
│                   │  Manager  │                         │
│                   └─────┬─────┘                         │
└─────────────────────────┼───────────────────────────────┘
                          │
                   ┌──────▼──────┐
                   │    Raft     │
                   │  ConfChange │
                   └──────┬──────┘
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                  │
   ┌────▼────┐       ┌────▼────┐       ┌────▼────┐
   │ Node 1  │       │ Node 2  │       │ Node 3  │
   │(Leader) │       │(Follower)│      │(Follower)│
   └─────────┘       └─────────┘       └─────────┘
```

### 2.2 数据存储

集群成员信息存储在 Raft 配置中，同时可以在 KV Store 中缓存：

```
/__cluster/members/{id}  -> MemberInfo (JSON)
```

但主要依赖 **Raft ConfState** 来管理成员。

---

## 3. 数据模型

### 3.1 Go 结构定义

```go
// MemberInfo 成员信息
type MemberInfo struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name"`
	PeerURLs   []string `json:"peer_urls"`
	ClientURLs []string `json:"client_urls"`
	IsLearner  bool     `json:"is_learner"`
}
```

---

## 4. 接口实现

### 4.1 添加到 Maintenance Service

由于 etcd v3 API 将集群管理放在 Maintenance Service 中，我们需要扩展现有的 MaintenanceServer。

#### 文件：api/etcd/maintenance.go

```go
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
```

### 4.2 Cluster Manager 实现

#### 文件：api/etcd/cluster_manager.go

```go
package etcdapi

import (
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
	// TODO: 实现
	// 使用时间戳 + 随机数 或者其他方法生成唯一 ID
	return 0
}
```

---

## 5. 实现步骤

### 阶段 1：基础框架（估计 1-2 小时）

1. ✅ **创建数据模型**
   - [x] 定义 MemberInfo 结构

2. ✅ **实现 ClusterManager 基础**
   - [x] NewClusterManager 构造函数
   - [x] ListMembers 方法

3. ✅ **扩展 Maintenance Service**
   - [x] MemberList 接口（TODO 实现）
   - [x] MemberAdd/Remove/Update/Promote 接口签名

### 阶段 2：成员管理功能（估计 3-4 小时）

4. **实现 MemberAdd**
   - [ ] 生成成员 ID
   - [ ] 创建 ConfChange
   - [ ] 提交到 Raft
   - [ ] 等待应用结果

5. **实现 MemberRemove**
   - [ ] 验证可以删除
   - [ ] 创建 ConfChange
   - [ ] 提交和等待

6. **实现 MemberUpdate**
   - [ ] 更新 PeerURLs
   - [ ] 可能需要 ConfChange

7. **实现 MemberPromote**
   - [ ] 提升 learner

### 阶段 3：Raft 集成（估计 2-3 小时）

8. **与 Raft Node 集成**
   - [ ] 在 raftNode 中处理 ConfChange
   - [ ] 回调 ClusterManager.ApplyConfChange
   - [ ] 更新成员列表

9. **启动时加载成员**
   - [ ] 从 Raft ConfState 恢复成员列表
   - [ ] 初始化 ClusterManager

### 阶段 4：测试（估计 2-3 小时）

10. **单元测试**
    - [ ] ClusterManager 测试
    - [ ] MemberAdd/Remove 测试

11. **集成测试**
    - [ ] 3节点集群添加第4个节点
    - [ ] 删除节点后集群仍正常工作
    - [ ] Leader 迁移测试

---

## 6. 关键技术点

### 6.1 Raft ConfChange

etcd/raft 提供了配置变更 API：

```go
// ConfChange 类型
type ConfChange struct {
    Type    ConfChangeType
    NodeID  uint64
    Context []byte  // 可以存储 MemberInfo JSON
}

// ConfChangeType
const (
    ConfChangeAddNode        ConfChangeType = 0
    ConfChangeRemoveNode     ConfChangeType = 1
    ConfChangeUpdateNode     ConfChangeType = 2
    ConfChangeAddLearnerNode ConfChangeType = 3
    // v3.5+ 新增
    ConfChangeType_PROMOTE   ConfChangeType = 4
)
```

### 6.2 配置变更流程

```
1. 客户端调用 MemberAdd
2. MaintenanceServer 创建 ConfChange
3. 发送到 confChangeC channel
4. raftNode 接收并调用 node.ProposeConfChange()
5. Raft 达成共识
6. raftNode 从 Ready.CommittedEntries 接收
7. 回调 ClusterManager.ApplyConfChange()
8. 更新成员列表
9. 返回客户端
```

### 6.3 成员 ID 生成

```go
// 使用当前时间戳 (纳秒) 作为 ID
func generateMemberID() uint64 {
    return uint64(time.Now().UnixNano())
}
```

### 6.4 初始成员配置

在启动时需要指定初始成员：

```go
// 启动参数
--cluster=node1=http://127.0.0.1:12379,node2=http://127.0.0.1:22379,node3=http://127.0.0.1:32379
```

解析后创建初始 ConfState：

```go
confState := raftpb.ConfState{
    Voters:    []uint64{1, 2, 3},
    Learners:  []uint64{},
}
```

---

## 7. 与现有 Raft Node 集成

### 7.1 修改 raftNode 结构

在 `internal/raft/node_memory.go` 和 `node_rocksdb.go` 中：

```go
type raftNode struct {
    // ... 现有字段

    confChangeC <-chan raftpb.ConfChange  // 已有

    // 新增：Cluster Manager 回调
    applyConfChange func(raftpb.ConfChange, raftpb.ConfState)
}
```

### 7.2 处理 ConfChange

在 `serveChannels()` 方法中：

```go
func (rc *raftNode) serveChannels() {
    // ...

    select {
    case cc := <-rc.confChangeC:
        rc.node.ProposeConfChange(context.TODO(), cc)
    case <-rc.stopc:
        return
    }
}
```

在 `readCommits()` 中：

```go
for ready := range rc.node.Ready() {
    // ...

    // 应用 ConfChange
    for _, cc := range ready.CommittedEntries {
        if cc.Type == raftpb.EntryConfChange {
            var confChange raftpb.ConfChange
            confChange.Unmarshal(cc.Data)
            rc.confState = *rc.node.ApplyConfChange(confChange)

            // 回调 ClusterManager
            if rc.applyConfChange != nil {
                rc.applyConfChange(confChange, rc.confState)
            }
        }
    }
}
```

---

## 8. 配置和部署

### 8.1 启动参数

```bash
# 节点 1
metaStore --member-id=1 \
    --cluster=http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
    --port=12380

# 节点 2
metaStore --member-id=2 \
    --cluster=http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
    --port=22380

# 节点 3
metaStore --member-id=3 \
    --cluster=http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
    --port=32380
```

### 8.2 动态添加节点

```bash
# 在任一运行节点上执行
etcdctl member add node4 --peer-urls=http://127.0.0.1:42379

# 启动新节点（使用 --join 标志）
metaStore --member-id=4 \
    --cluster=http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379,http://127.0.0.1:42379 \
    --port=42380 \
    --join
```

---

## 9. 测试计划

### 9.1 单元测试

```
TestClusterManager
├── TestListMembers
├── TestAddMember
│   ├── 添加 voting 成员
│   ├── 添加 learner 成员
│   └── 重复添加（应失败）
├── TestRemoveMember
│   ├── 正常移除
│   └── 移除最后一个成员（应失败）
├── TestUpdateMember
└── TestPromoteMember
```

### 9.2 集成测试

```
TestClusterIntegration
├── TestDynamicMembershipChange
│   ├── 3节点集群启动
│   ├── 添加第4个节点
│   ├── 验证新节点数据同步
│   ├── 删除一个节点
│   └── 验证集群仍正常工作
├── TestLeaderTransfer
│   ├── 移除 leader 节点
│   └── 新 leader 选举成功
└── TestLearnerPromotion
    ├── 添加 learner
    ├── 等待数据同步
    └── 提升为 voting 成员
```

---

## 10. 性能考虑

- **ConfChange 是串行的**：一次只能有一个配置变更
- **等待超时**：配置变更需要等待 Raft 共识，设置合理超时（建议 30s）
- **成员数量限制**：建议不超过 7 个 voting 成员

---

## 11. 安全考虑

- **权限验证**：MemberAdd/Remove/Update 需要 root 权限
- **防止脑裂**：移除成员时确保不破坏 quorum
- **数据一致性**：配置变更必须通过 Raft 共识

---

## 12. 待完成清单

### 代码实现
- [ ] api/etcd/cluster_manager.go - Cluster 管理器
- [ ] 扩展 api/etcd/maintenance.go - 添加 Member* 接口
- [ ] 修改 internal/raft/node_*.go - 集成 ConfChange
- [ ] api/etcd/cluster_types.go - 数据模型

### 测试
- [ ] api/etcd/cluster_test.go - 单元测试
- [ ] test/cluster_integration_test.go - 集成测试

### 集成
- [ ] 修改 api/etcd/server.go 集成 ClusterManager
- [ ] 修改 NewNode 添加 applyConfChange 回调

---

## 13. 估算工作量

| 任务 | 估计时间 | 优先级 |
|------|---------|--------|
| 数据模型和基础框架 | 1-2 小时 | P0 |
| ClusterManager 实现 | 2-3 小时 | P0 |
| Maintenance API 扩展 | 1-2 小时 | P0 |
| Raft Node 集成 | 2-3 小时 | P0 |
| 单元测试 | 2 小时 | P0 |
| 集成测试 | 2-3 小时 | P0 |
| **总计** | **10-15 小时** | - |

约 **2 个工作日**

---

## 14. 参考资料

- [etcd Runtime Configuration](https://etcd.io/docs/v3.5/op-guide/runtime-configuration/)
- [Raft ConfChange](https://github.com/etcd-io/raft/blob/main/raftpb/raft.proto#L128)
- [etcd Member API](https://etcd.io/docs/v3.5/dev-guide/api_reference_v3/)

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**状态**: 设计完成，待实现
