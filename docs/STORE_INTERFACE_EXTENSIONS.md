# Store 接口扩展设计

## 概述

本文档描述对 Store 接口的扩展，添加 Raft 状态查询等新功能。

**目标**: 支持查询 Raft 状态信息，用于 Maintenance API 返回真实的 Term 和 Leader。

---

## 1. 新增接口

### 1.1 GetRaftStatus - 获取 Raft 状态

#### 数据模型

```go
// RaftStatus Raft 状态信息
type RaftStatus struct {
    NodeID   uint64 `json:"node_id"`    // 当前节点 ID
    Term     uint64 `json:"term"`       // 当前 Term
    LeaderID uint64 `json:"leader_id"`  // Leader 节点 ID (0 表示无 leader)
    State    string `json:"state"`      // "leader", "follower", "candidate", "pre-candidate"
    Applied  uint64 `json:"applied"`    // 已应用的 index
    Commit   uint64 `json:"commit"`     // 已提交的 index
}
```

#### 接口定义

```go
// internal/kvstore/store.go

type Store interface {
    // ... 现有方法

    // GetRaftStatus 返回 Raft 状态（新增）
    GetRaftStatus() RaftStatus
}
```

---

## 2. 实现方案

### 2.1 Memory 引擎实现

#### 文件：internal/memory/kvstore.go

```go
func (m *Memory) GetRaftStatus() kvstore.RaftStatus {
    // 需要访问 raftNode
    // 方案 1：将 raftNode 设为 Memory 的字段
    // 方案 2：通过接口回调获取

    if m.raftNode == nil {
        return kvstore.RaftStatus{
            NodeID:   m.id,
            Term:     0,
            LeaderID: 0,
            State:    "unknown",
        }
    }

    // TODO: 从 raftNode 获取状态
    // status := m.raftNode.Status()
    //
    // return kvstore.RaftStatus{
    //     NodeID:   status.ID,
    //     Term:     status.Term,
    //     LeaderID: status.Lead,
    //     State:    status.RaftState.String(),
    //     Applied:  status.Applied,
    //     Commit:   status.Commit,
    // }

    return kvstore.RaftStatus{}
}
```

#### 修改 Memory 结构

```go
type Memory struct {
    mu       sync.RWMutex
    kvStore  map[string]string
    proposeC chan<- string

    // 新增：Raft 节点引用
    raftNode RaftNodeInterface  // 需要定义接口
    id       uint64
}
```

#### 定义 RaftNodeInterface

```go
// internal/memory/raft_interface.go

// RaftNodeInterface Raft 节点接口
type RaftNodeInterface interface {
    Status() RaftStatusInfo
    Term() uint64
    Leader() uint64
    State() string
}

// RaftStatusInfo Raft 状态信息
type RaftStatusInfo struct {
    ID         uint64
    Term       uint64
    Lead       uint64
    RaftState  string
    Applied    uint64
    Commit     uint64
}
```

### 2.2 RocksDB 引擎实现

#### 文件：internal/rocksdb/kvstore.go

```go
func (r *RocksDB) GetRaftStatus() kvstore.RaftStatus {
    if r.raftNode == nil {
        return kvstore.RaftStatus{
            NodeID:   r.id,
            Term:     0,
            LeaderID: 0,
            State:    "unknown",
        }
    }

    // TODO: 类似 Memory 实现
    return kvstore.RaftStatus{}
}
```

---

## 3. Raft Node 修改

### 3.1 添加 Status() 方法

#### 文件：internal/raft/node_memory.go

```go
// Status 返回 Raft 状态
func (rc *raftNode) Status() RaftStatusInfo {
    status := rc.node.Status()

    return RaftStatusInfo{
        ID:        status.ID,
        Term:      status.Term,
        Lead:      status.Lead,
        RaftState: status.RaftState.String(),
        Applied:   status.Applied,
        Commit:    status.Commit,
    }
}

// Term 返回当前 Term
func (rc *raftNode) Term() uint64 {
    return rc.node.Status().Term
}

// Leader 返回当前 Leader ID
func (rc *raftNode) Leader() uint64 {
    return rc.node.Status().Lead
}

// State 返回当前状态
func (rc *raftNode) State() string {
    return rc.node.Status().RaftState.String()
}
```

---

## 4. 架构调整

### 4.1 当前架构

```
Server
  └─ Store (Memory/RocksDB)
       └─ proposeC channel
            └─ raftNode (创建时分离)
```

### 4.2 调整后架构

```
Server
  └─ Store (Memory/RocksDB)
       ├─ proposeC channel
       └─ raftNode reference (新增)
            └─ raftNode
```

### 4.3 初始化修改

#### 当前初始化流程

```go
// 创建 raft node
commitC, errorC, snapshotterReady := raft.NewNode(...)

// 创建 store
store := memory.NewMemory(proposeC, commitC)
```

#### 修改后初始化流程

```go
// 方案 A：创建后注入
commitC, errorC, snapshotterReady, raftNode := raft.NewNode(...)
store := memory.NewMemory(proposeC, commitC)
store.SetRaftNode(raftNode)  // 注入

// 方案 B：修改构造函数
commitC, errorC, snapshotterReady, raftNode := raft.NewNode(...)
store := memory.NewMemoryWithRaft(proposeC, commitC, raftNode)
```

---

## 5. 实现步骤

### 阶段 1：定义接口（30 分钟）

1. ✅ 定义 RaftStatus 结构
2. ✅ 扩展 Store 接口
3. ✅ 定义 RaftNodeInterface

### 阶段 2：Raft Node 实现（1 小时）

4. **添加 Status() 等方法到 raftNode**
   - [ ] node_memory.go
   - [ ] node_rocksdb.go

### 阶段 3：Store 实现（1-2 小时）

5. **Memory 实现**
   - [ ] 添加 raftNode 字段
   - [ ] 实现 GetRaftStatus()
   - [ ] 修改构造函数

6. **RocksDB 实现**
   - [ ] 添加 raftNode 字段
   - [ ] 实现 GetRaftStatus()
   - [ ] 修改构造函数

### 阶段 4：集成（1 小时）

7. **修改初始化代码**
   - [ ] cmd/metastore/main.go
   - [ ] 测试文件

---

## 6. 代码示例

### 6.1 扩展接口

#### internal/kvstore/types.go

```go
// RaftStatus Raft 状态信息
type RaftStatus struct {
    NodeID   uint64 `json:"node_id"`
    Term     uint64 `json:"term"`
    LeaderID uint64 `json:"leader_id"`
    State    string `json:"state"`
    Applied  uint64 `json:"applied"`
    Commit   uint64 `json:"commit"`
}
```

#### internal/kvstore/store.go

```go
type Store interface {
    // ... existing methods

    // GetRaftStatus 返回 Raft 状态
    GetRaftStatus() RaftStatus
}
```

### 6.2 Raft Node 接口

#### internal/memory/raft_interface.go (新文件)

```go
package memory

// RaftNode Raft 节点接口
type RaftNode interface {
    Status() RaftStatus
}

// RaftStatus Raft 状态
type RaftStatus struct {
    ID        uint64
    Term      uint64
    Lead      uint64
    RaftState string
    Applied   uint64
    Commit    uint64
}
```

### 6.3 Memory 实现

#### internal/memory/kvstore.go

```go
type Memory struct {
    mu       sync.RWMutex
    kvStore  map[string]string
    proposeC chan<- string

    // 新增
    raftNode RaftNode
    nodeID   uint64
}

// SetRaftNode 设置 Raft 节点（用于依赖注入）
func (m *Memory) SetRaftNode(node RaftNode) {
    m.raftNode = node
}

// GetRaftStatus 获取 Raft 状态
func (m *Memory) GetRaftStatus() kvstore.RaftStatus {
    if m.raftNode == nil {
        return kvstore.RaftStatus{
            NodeID:   m.nodeID,
            State:    "standalone",
        }
    }

    status := m.raftNode.Status()
    return kvstore.RaftStatus{
        NodeID:   status.ID,
        Term:     status.Term,
        LeaderID: status.Lead,
        State:    status.RaftState,
        Applied:  status.Applied,
        Commit:   status.Commit,
    }
}
```

### 6.4 Raft Node 实现

#### internal/raft/node_memory.go

```go
// 实现 RaftNode 接口
func (rc *raftNode) Status() memory.RaftStatus {
    status := rc.node.Status()
    return memory.RaftStatus{
        ID:        status.ID,
        Term:      status.Term,
        Lead:      status.Lead,
        RaftState: status.RaftState.String(),
        Applied:   status.Applied,
        Commit:    status.Commit,
    }
}
```

---

## 7. 测试

### 7.1 单元测试

```go
func TestGetRaftStatus(t *testing.T) {
    store := memory.NewMemory(...)
    mockRaft := &MockRaftNode{
        id:     1,
        term:   5,
        leader: 1,
        state:  "leader",
    }
    store.SetRaftNode(mockRaft)

    status := store.GetRaftStatus()
    assert.Equal(t, uint64(1), status.NodeID)
    assert.Equal(t, uint64(5), status.Term)
    assert.Equal(t, uint64(1), status.LeaderID)
    assert.Equal(t, "leader", status.State)
}
```

### 7.2 集成测试

```go
func TestRaftStatusInCluster(t *testing.T) {
    // 启动 3 节点集群
    cluster := newTestCluster(3)
    defer cluster.Close()

    // 等待 leader 选举
    time.Sleep(2 * time.Second)

    // 检查状态
    leaderCount := 0
    for _, node := range cluster.nodes {
        status := node.store.GetRaftStatus()
        if status.State == "leader" {
            leaderCount++
        }
    }

    // 应该有且仅有一个 leader
    assert.Equal(t, 1, leaderCount)
}
```

---

## 8. 影响范围

### 8.1 修改的文件

- [x] internal/kvstore/types.go - 添加 RaftStatus
- [ ] internal/kvstore/store.go - 扩展 Store 接口
- [ ] internal/memory/raft_interface.go - 新文件，定义接口
- [ ] internal/memory/kvstore.go - 实现 GetRaftStatus
- [ ] internal/rocksdb/kvstore.go - 实现 GetRaftStatus
- [ ] internal/raft/node_memory.go - 实现 Status()
- [ ] internal/raft/node_rocksdb.go - 实现 Status()
- [ ] pkg/etcdapi/maintenance.go - 使用 GetRaftStatus

### 8.2 新增的文件

- [ ] internal/memory/raft_interface.go
- [ ] internal/rocksdb/raft_interface.go (可能与 memory 共用)

---

## 9. 工作量估算

| 任务 | 估计时间 |
|------|---------|
| 定义接口和数据模型 | 30 分钟 |
| Raft Node 实现 | 1 小时 |
| Memory 实现 | 1 小时 |
| RocksDB 实现 | 1 小时 |
| 集成和测试 | 1 小时 |
| **总计** | **4.5 小时** |

约 **半个工作日**

---

## 10. 待完成清单

### 接口定义
- [x] kvstore.RaftStatus 结构
- [ ] Store.GetRaftStatus() 接口
- [ ] RaftNode 接口

### 实现
- [ ] raftNode.Status()
- [ ] Memory.GetRaftStatus()
- [ ] RocksDB.GetRaftStatus()

### 集成
- [ ] 修改初始化代码注入 raftNode
- [ ] maintenance.go 使用 GetRaftStatus

### 测试
- [ ] 单元测试
- [ ] 集成测试

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**状态**: 设计完成，待实现
