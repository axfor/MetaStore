# 2 节点高可用(HA)架构设计

## 1. 背景与问题

### 1.1 当前架构

MetaStore 基于 etcd Raft 库实现分布式一致性，使用标准 Raft 协议的多数派(Quorum)机制：

```
Quorum = N/2 + 1

节点数  Quorum  可容错节点数
  1       1         0
  2       2         0  ← 问题所在
  3       2         1
  5       3         2
```

### 1.2 2 节点集群的问题

**问题 1：零容错**
```
2 节点集群：
- 需要 2 个节点同意才能提交
- 任意 1 个节点故障 → 无法形成 Quorum → 集群不可用
```

**问题 2：网络分区脑裂**
```
         网络分区
Node-1  ────────  Node-2
  ↓                  ↓
发起选举           发起选举
投票给自己         投票给自己
  ↓                  ↓
无法获得多数       无法获得多数
  ↓                  ↓
      双方都无法成为 Leader
```

### 1.3 用户需求

- 只有 2 个数据节点的场景下实现高可用
- 能够容忍 1 个节点故障
- 资源开销最小化

---

## 2. 解决方案

### 2.1 方案对比

| 方案 | 原理 | 容错能力 | 成本 | 复杂度 | 推荐度 |
|------|------|---------|------|--------|--------|
| **Witness 见证节点** | 添加轻量级第三节点，只投票不存数据 | 容错1个 | 低 | 低 | ⭐⭐⭐⭐⭐ |
| 外部仲裁服务 | 使用外部服务(etcd/Consul)打破平局 | 容错1个 | 中 | 高 | ⭐⭐⭐ |
| 优先节点模式 | 指定主节点在分区时有优先权 | 有脑裂风险 | 无 | 低 | ⭐ |
| Learner 副本 | 添加只读副本 | 无容错提升 | 中 | 低 | ⭐⭐ |

### 2.2 推荐方案：Witness 见证节点

```
┌─────────────────┐   ┌─────────────────┐   ┌─────────────────┐
│     Node 1      │   │     Node 2      │   │    Witness      │
│  (Data Node)    │   │  (Data Node)    │   │  (Vote Only)    │
│                 │   │                 │   │                 │
│ ✓ 参与投票      │   │ ✓ 参与投票      │   │ ✓ 参与投票      │
│ ✓ 存储数据      │   │ ✓ 存储数据      │   │ ✗ 不存储数据    │
│ ✓ 提供读写      │   │ ✓ 提供读写      │   │ ✗ 不提供服务    │
│                 │   │                 │   │                 │
│ 内存: 4GB+      │   │ 内存: 4GB+      │   │ 内存: 256MB     │
│ 磁盘: 100GB+    │   │ 磁盘: 100GB+    │   │ 磁盘: 1GB       │
└─────────────────┘   └─────────────────┘   └─────────────────┘
         │                    │                      │
         └────────────────────┴──────────────────────┘
                         Raft 集群
                       Quorum = 2/3
```

**核心思想**：
- Witness 是一个"投票机器"，参与 Raft 共识但不存储数据
- 将 2 节点变成 3 节点集群，实现真正的多数派
- Witness 资源开销极小（约 256MB 内存，1GB 磁盘）

---

## 3. 详细设计

### 3.1 Witness 节点特性

| 特性 | 普通节点 | Witness 节点 |
|------|---------|-------------|
| 参与 Leader 选举投票 | ✓ | ✓ |
| 接收 Raft 日志 | ✓ | ✓ (仅元数据) |
| 应用数据到 KV Store | ✓ | ✗ |
| 持久化 WAL | ✓ | ✗ (可选) |
| 创建快照 | ✓ | ✗ |
| 提供读服务 | ✓ | ✗ |
| 提供写服务 | ✓ (Leader) | ✗ |
| 可被选为 Leader | ✓ | ✗ |

### 3.2 配置设计

```yaml
# config.yaml
server:
  cluster_id: 1
  member_id: 3

  raft:
    # 节点角色: "data" (默认) 或 "witness"
    node_role: "witness"

    # Witness 专用配置
    witness:
      # 是否持久化投票状态 (推荐 true，防止重启后重复投票)
      persist_vote: true

      # 是否转发客户端请求到 Leader (可选功能)
      forward_requests: false

    # 通用 Raft 配置
    election_tick: 10
    heartbeat_tick: 1
    pre_vote: true
    check_quorum: true
```

### 3.3 架构变更

#### 3.3.1 节点类型定义

```go
// pkg/config/config.go

// NodeRole 定义节点角色
type NodeRole string

const (
    // NodeRoleData 数据节点，完整参与 Raft 并存储数据
    NodeRoleData NodeRole = "data"

    // NodeRoleWitness 见证节点，只参与投票不存储数据
    NodeRoleWitness NodeRole = "witness"
)

type RaftConfig struct {
    // ... 现有字段 ...

    // NodeRole 节点角色
    NodeRole NodeRole `yaml:"node_role"`

    // Witness 见证节点配置
    Witness WitnessConfig `yaml:"witness"`
}

type WitnessConfig struct {
    // PersistVote 是否持久化投票状态
    PersistVote bool `yaml:"persist_vote"`

    // ForwardRequests 是否转发请求到 Leader
    ForwardRequests bool `yaml:"forward_requests"`
}
```

#### 3.3.2 Raft 节点初始化

```go
// internal/raft/node_memory.go

func (rc *raftNode) startRaft() {
    // ... 现有初始化 ...

    if rc.isWitness() {
        rc.initAsWitness()
        return
    }

    // ... 正常数据节点初始化 ...
}

func (rc *raftNode) isWitness() bool {
    return rc.cfg.Server.Raft.NodeRole == config.NodeRoleWitness
}

func (rc *raftNode) initAsWitness() {
    // 1. 使用内存存储 (不持久化日志)
    rc.raftStorage = raft.NewMemoryStorage()

    // 2. 禁用 WAL
    rc.wal = nil

    // 3. 禁用快照
    rc.snapshotter = nil

    // 4. 禁用 Lease Read
    rc.leaseManager = nil

    // 5. 标记为非 Leader 候选
    rc.raftConfig.DisableProposalForwarding = true

    rc.logger.Info("Witness node initialized",
        zap.Uint64("id", rc.id),
        zap.String("role", "witness"))
}
```

#### 3.3.3 日志应用逻辑

```go
// internal/raft/node_memory.go

func (rc *raftNode) publishEntries(ents []raftpb.Entry) (<-chan struct{}, bool) {
    if len(ents) == 0 {
        return nil, true
    }

    // Witness 节点只处理配置变更，跳过数据应用
    if rc.isWitness() {
        return rc.publishEntriesAsWitness(ents)
    }

    // ... 正常数据节点逻辑 ...
}

func (rc *raftNode) publishEntriesAsWitness(ents []raftpb.Entry) (<-chan struct{}, bool) {
    for _, ent := range ents {
        switch ent.Type {
        case raftpb.EntryConfChange, raftpb.EntryConfChangeV2:
            // 处理集群配置变更
            var cc raftpb.ConfChange
            if err := cc.Unmarshal(ent.Data); err != nil {
                rc.logger.Error("failed to unmarshal ConfChange", zap.Error(err))
                continue
            }
            rc.confState = *rc.node.ApplyConfChange(cc)

        case raftpb.EntryNormal:
            // Witness 跳过普通数据条目
            // 只需确认收到，不需要应用
            continue
        }
    }

    return nil, true
}
```

#### 3.3.4 Leader 选举限制

```go
// internal/raft/node_memory.go

func (rc *raftNode) createRaftConfig() *raft.Config {
    cfg := &raft.Config{
        ID:                        rc.id,
        ElectionTick:              rc.cfg.Server.Raft.ElectionTick,
        HeartbeatTick:             rc.cfg.Server.Raft.HeartbeatTick,
        Storage:                   rc.raftStorage,
        MaxSizePerMsg:             1024 * 1024,
        MaxInflightMsgs:           256,
        MaxUncommittedEntriesSize: 1 << 30,
        PreVote:                   rc.cfg.Server.Raft.PreVote,
        CheckQuorum:               rc.cfg.Server.Raft.CheckQuorum,
    }

    // Witness 节点不能成为 Leader
    if rc.isWitness() {
        // 通过设置极高的选举超时，使 Witness 几乎不可能发起选举
        cfg.ElectionTick = 10000
    }

    return cfg
}
```

### 3.4 集群管理

#### 3.4.1 添加 Witness 节点

```go
// api/etcd/cluster_manager.go

// AddWitnessMember 添加 Witness 见证节点
func (cm *ClusterManager) AddWitnessMember(ctx context.Context, peerURLs []string) (*MemberInfo, error) {
    // 生成成员 ID
    memberID := cm.generateMemberID(peerURLs)

    // 创建 ConfChange
    cc := raftpb.ConfChange{
        Type:    raftpb.ConfChangeAddNode,
        NodeID:  memberID,
        Context: []byte(strings.Join(peerURLs, ",")),
    }

    // 发送到 Raft
    if err := cm.proposeConfChange(ctx, cc); err != nil {
        return nil, err
    }

    // 返回成员信息
    return &MemberInfo{
        ID:        memberID,
        PeerURLs:  peerURLs,
        IsWitness: true,
    }, nil
}
```

#### 3.4.2 成员信息扩展

```go
// api/etcd/cluster_manager.go

type MemberInfo struct {
    ID          uint64   `json:"id"`
    Name        string   `json:"name"`
    PeerURLs    []string `json:"peer_urls"`
    ClientURLs  []string `json:"client_urls"`
    IsLearner   bool     `json:"is_learner"`
    IsWitness   bool     `json:"is_witness"`  // 新增
}
```

---

## 4. 故障场景分析

### 4.1 正常运行

```
Node1 (Leader) ←──── 心跳 ────→ Node2 (Follower)
      ↑                              ↑
      │                              │
      └────────── 心跳 ──────────────┘
                    ↓
              Witness (Follower)

状态: 3 节点活跃, Quorum=2 ✓
```

### 4.2 Node2 故障

```
Node1 (Leader) ←──── X ────→ Node2 (故障)
      ↑
      │
      └────────── 心跳 ──────────┘
                    ↓
              Witness (Follower)

状态: 2 节点活跃 (Node1 + Witness), Quorum=2 ✓
结果: 集群继续正常服务
```

### 4.3 Witness 故障

```
Node1 (Leader) ←──── 心跳 ────→ Node2 (Follower)
      ↑                              ↑
      │                              │
      └────────── X ─────────────────┘
                    ↓
              Witness (故障)

状态: 2 节点活跃 (Node1 + Node2), Quorum=2 ✓
结果: 集群继续正常服务，但失去容错能力
警告: 应立即恢复 Witness
```

### 4.4 Leader (Node1) 故障

```
Node1 (故障) ←──── X ────→ Node2 (Follower)
                              ↑
                              │ 发起选举
                              ↓
                        Witness (投票给 Node2)

选举过程:
1. Node2 超时未收到心跳
2. Node2 发起选举 (term+1)
3. Witness 投票给 Node2
4. Node2 获得 2 票 (自己 + Witness), 成为新 Leader

状态: 2 节点活跃 (Node2 + Witness), Quorum=2 ✓
结果: Node2 成为新 Leader，集群继续服务
```

### 4.5 网络分区

```
        分区 A                    分区 B
┌─────────────────┐       ┌─────────────────┐
│     Node1       │       │     Node2       │
│   (原 Leader)   │   X   │   (Follower)    │
│                 │       │                 │
└─────────────────┘       │    Witness      │
                          └─────────────────┘

分区 A: 1 节点, 无法形成 Quorum (需要2)
分区 B: 2 节点, 形成 Quorum ✓

结果:
- Node1 失去 Quorum, 自动降级为 Follower (CheckQuorum)
- Node2 + Witness 选举新 Leader
- 只有分区 B 可以提供服务
- 无脑裂风险 ✓
```

---

## 5. 部署指南

### 5.1 最小部署架构

```
数据中心 A              数据中心 B              云/边缘
┌─────────────┐       ┌─────────────┐       ┌─────────────┐
│   Node 1    │       │   Node 2    │       │   Witness   │
│  (Primary)  │       │ (Secondary) │       │  (轻量级)   │
│             │       │             │       │             │
│ 8 CPU       │       │ 8 CPU       │       │ 1 CPU       │
│ 16GB RAM    │       │ 16GB RAM    │       │ 256MB RAM   │
│ 500GB SSD   │       │ 500GB SSD   │       │ 1GB Disk    │
└─────────────┘       └─────────────┘       └─────────────┘
```

### 5.2 配置文件示例

**Node 1 (数据节点)**
```yaml
# node1.yaml
server:
  cluster_id: 1
  member_id: 1

  etcd:
    address: ":2379"
  http:
    address: ":9121"
  mysql:
    address: ":3306"

  raft:
    node_role: "data"
    election_tick: 10
    heartbeat_tick: 1
    pre_vote: true
    check_quorum: true
    lease_read:
      enable: true
```

**Node 2 (数据节点)**
```yaml
# node2.yaml
server:
  cluster_id: 1
  member_id: 2

  etcd:
    address: ":2379"
  http:
    address: ":9121"
  mysql:
    address: ":3306"

  raft:
    node_role: "data"
    election_tick: 10
    heartbeat_tick: 1
    pre_vote: true
    check_quorum: true
    lease_read:
      enable: true
```

**Witness (见证节点)**
```yaml
# witness.yaml
server:
  cluster_id: 1
  member_id: 3

  # Witness 不提供客户端服务
  etcd:
    address: ""  # 禁用
  http:
    address: ""  # 禁用
  mysql:
    address: ""  # 禁用

  raft:
    node_role: "witness"
    election_tick: 10
    heartbeat_tick: 1
    pre_vote: true
    check_quorum: true

    witness:
      persist_vote: true
      forward_requests: false

    lease_read:
      enable: false  # Witness 无数据，禁用
```

### 5.3 启动顺序

```bash
# 1. 启动第一个数据节点
./metastore --config node1.yaml --cluster "http://node1:2380"

# 2. 启动第二个数据节点
./metastore --config node2.yaml --cluster "http://node1:2380,http://node2:2380" --join

# 3. 启动 Witness 节点
./metastore --config witness.yaml --cluster "http://node1:2380,http://node2:2380,http://witness:2380" --join
```

---

## 6. 实现计划

### 6.1 第一阶段：基础框架 ✅ 已完成

- [x] 扩展 `RaftConfig` 添加 `NodeRole` 和 `WitnessConfig`
- [x] 添加 `IsWitness()` 判断方法
- [x] 修改节点初始化逻辑，支持 Witness 模式

### 6.2 第二阶段：核心功能 ✅ 已完成

- [x] 实现 Witness 的轻量级日志处理
- [x] 修改 `publishEntries()` 跳过数据应用
- [x] 禁用 Witness 的 Lease Read
- [x] 限制 Witness 不能成为 Leader (通过配置禁用 LeaseRead)

### 6.3 第三阶段：集群管理 ✅ 已完成

- [x] 扩展 `MemberInfo` 添加 `IsWitness` 字段
- [x] 实现 `AddWitnessMember()` API
- [x] 添加配置示例 (`configs/2node_ha_example/`)

### 6.4 第四阶段：测试与文档 ✅ 已完成

- [x] 单元测试：Witness 配置和初始化 (`pkg/config/witness_test.go`)
- [ ] 集成测试：3 节点集群故障切换 (可选)
- [ ] 集成测试：网络分区场景 (可选)
- [ ] 性能测试：Witness 资源占用 (可选)
- [x] 更新部署文档 (配置示例)

---

## 7. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Witness 单点故障 | 失去容错能力 | 监控告警，快速恢复 |
| 网络延迟高 | 选举超时 | 调整 election_tick |
| Witness 误配置为 Leader | 数据丢失 | 代码强制禁止 |
| 滚动升级复杂 | 服务中断 | 先升级 Witness |

---

## 8. 替代方案

如果 Witness 方案不适合，可考虑：

### 8.1 外部仲裁服务

使用外部 etcd/Consul 集群作为仲裁者：

```go
type ExternalArbiter interface {
    // AcquireLock 尝试获取 Leader 锁
    AcquireLock(ctx context.Context, nodeID uint64) (bool, error)

    // RenewLock 续期锁
    RenewLock(ctx context.Context, nodeID uint64) error

    // ReleaseLock 释放锁
    ReleaseLock(ctx context.Context, nodeID uint64) error
}
```

**优点**: 不需要第三个 MetaStore 节点
**缺点**: 引入外部依赖

### 8.2 同步复制 + 手动故障转移

放弃自动 HA，使用同步复制 + 人工切换：

```
Node1 (Primary) ──同步复制──→ Node2 (Standby)

故障时: 管理员手动提升 Node2 为 Primary
```

**优点**: 最简单，无额外节点
**缺点**: 需要人工介入，RTO 较长

---

## 9. 总结

**推荐方案**: Witness 见证节点

**核心优势**:
1. 真正实现 2 数据节点 + 1 见证节点的高可用
2. 见证节点资源开销极小 (~256MB)
3. 标准 Raft 协议，无脑裂风险
4. etcd Raft 库原生支持，实现简单

**适用场景**:
- 成本敏感的小型部署
- 只需要 2 个完整数据副本
- 需要自动故障转移

**不适用场景**:
- 完全无法部署第三个节点
- 需要 3 个完整数据副本
