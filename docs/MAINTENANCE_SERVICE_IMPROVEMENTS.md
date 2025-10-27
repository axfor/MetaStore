# Maintenance Service 完善计划

## 概述

当前 Maintenance Service 实现了部分功能，本文档详细说明需要完善的部分。

**当前完成度**: 40% (Snapshot 100%, Status 80%, 其他 0%)

**目标完成度**: 100%

---

## 1. 需要完善的功能

### 1.1 Defragment - 碎片整理

**当前状态**: ❌ 仅占位实现

**位置**: [maintenance.go:57-62](../pkg/etcdapi/maintenance.go#L57-L62)

**实现方案**:

#### RocksDB 引擎
```go
func (s *MaintenanceServer) Defragment(ctx context.Context, req *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
    // 对于 RocksDB，调用 CompactRange
    if rocksStore, ok := s.server.store.(*rocksdb.RocksDB); ok {
        // 对所有数据执行 full compaction
        if err := rocksStore.CompactRange(nil, nil); err != nil {
            return nil, toGRPCError(err)
        }
    }

    return &pb.DefragmentResponse{
        Header: s.server.getResponseHeader(),
    }, nil
}
```

#### Memory 引擎
```go
// 对于内存引擎，无需实现，直接返回成功
return &pb.DefragmentResponse{
    Header: s.server.getResponseHeader(),
}, nil
```

**工作量**: 1-2 小时

---

### 1.2 Hash - 数据库哈希

**当前状态**: ❌ 返回 0

**位置**: [maintenance.go:65-71](../pkg/etcdapi/maintenance.go#L65-L71)

**实现方案**:

```go
import "hash/crc32"

func (s *MaintenanceServer) Hash(ctx context.Context, req *pb.HashRequest) (*pb.HashResponse, error) {
    // 获取快照数据
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
```

**用途**:
- 数据一致性验证
- 集群节点间数据比对
- 备份验证

**工作量**: 1 小时

---

### 1.3 HashKV - KV 哈希

**当前状态**: ❌ 返回 0

**位置**: [maintenance.go:74-80](../pkg/etcdapi/maintenance.go#L74-L80)

**实现方案**:

```go
import (
    "hash/crc32"
    "sort"
)

func (s *MaintenanceServer) HashKV(ctx context.Context, req *pb.HashKVRequest) (*pb.HashKVResponse, error) {
    revision := req.Revision
    if revision == 0 {
        revision = s.server.store.CurrentRevision()
    }

    // 范围查询所有 KV
    resp, err := s.server.store.Range("", "\x00", 0, revision)
    if err != nil {
        return nil, toGRPCError(err)
    }

    // 对键值对排序（确保一致性）
    kvs := resp.Kvs
    sort.Slice(kvs, func(i, j int) bool {
        return string(kvs[i].Key) < string(kvs[j].Key)
    })

    // 计算哈希
    hasher := crc32.NewIEEE()
    for _, kv := range kvs {
        hasher.Write(kv.Key)
        hasher.Write(kv.Value)
        // 可选：也包含 metadata
        // binary.Write(hasher, binary.BigEndian, kv.ModRevision)
        // binary.Write(hasher, binary.BigEndian, kv.Version)
    }

    hash := hasher.Sum32()
    compactRevision := int64(0) // TODO: 从存储获取

    return &pb.HashKVResponse{
        Header:          s.server.getResponseHeader(),
        Hash:            hash,
        CompactRevision: compactRevision,
    }, nil
}
```

**工作量**: 2 小时

---

### 1.4 MoveLeader - Leader 转移

**当前状态**: ❌ 仅占位实现

**位置**: [maintenance.go:112-117](../pkg/etcdapi/maintenance.go#L112-L117)

**实现方案**:

```go
import "go.etcd.io/raft/v3"

func (s *MaintenanceServer) MoveLeader(ctx context.Context, req *pb.MoveLeaderRequest) (*pb.MoveLeaderResponse, error) {
    targetID := req.TargetID

    // TODO: 需要访问 Raft Node
    // 1. 验证当前节点是 leader
    // 2. 验证 targetID 是有效成员
    // 3. 调用 raft.Node.TransferLeadership(ctx, lead, transferee)
    // 4. 等待转移完成

    // 伪代码：
    // if s.server.raftNode != nil {
    //     s.server.raftNode.TransferLeadership(ctx, s.server.memberID, targetID)
    // }

    return &pb.MoveLeaderResponse{
        Header: s.server.getResponseHeader(),
    }, nil
}
```

**前置条件**:
- 需要在 Server 中添加对 Raft Node 的引用
- 或者通过 ClusterManager 间接调用

**工作量**: 2-3 小时

---

### 1.5 Alarm - 告警机制

**当前状态**: ⚠️ 空实现，总是返回空列表

**位置**: [maintenance.go:30-35](../pkg/etcdapi/maintenance.go#L30-L35)

**实现方案**:

#### 数据模型
```go
// AlarmInfo 告警信息
type AlarmInfo struct {
    Type     pb.AlarmType
    MemberID uint64
    Alarm    string  // 告警描述
    Time     int64   // 触发时间
}
```

#### AlarmManager
```go
// AlarmManager 管理告警
type AlarmManager struct {
    mu     sync.RWMutex
    alarms map[uint64][]*AlarmInfo  // memberID -> alarms
}

// AddAlarm 添加告警
func (am *AlarmManager) AddAlarm(memberID uint64, alarmType pb.AlarmType, message string) {
    // TODO: 实现
}

// ListAlarms 列出告警
func (am *AlarmManager) ListAlarms() []*AlarmInfo {
    // TODO: 实现
    return nil
}

// ClearAlarm 清除告警
func (am *AlarmManager) ClearAlarm(memberID uint64, alarmType pb.AlarmType) {
    // TODO: 实现
}
```

#### 告警类型
```go
const (
    AlarmType_NONE    AlarmType = 0
    AlarmType_NOSPACE AlarmType = 1  // 磁盘空间不足
    AlarmType_CORRUPT AlarmType = 2  // 数据损坏
)
```

#### 集成
```go
func (s *MaintenanceServer) Alarm(ctx context.Context, req *pb.AlarmRequest) (*pb.AlarmResponse, error) {
    switch req.Action {
    case pb.AlarmRequest_GET:
        // 列出所有告警
        alarms := s.server.alarmMgr.ListAlarms()
        pbAlarms := make([]*pb.AlarmMember, len(alarms))
        for i, alarm := range alarms {
            pbAlarms[i] = &pb.AlarmMember{
                MemberID: alarm.MemberID,
                Alarm:    alarm.Type,
            }
        }
        return &pb.AlarmResponse{
            Header: s.server.getResponseHeader(),
            Alarms: pbAlarms,
        }, nil

    case pb.AlarmRequest_ACTIVATE:
        // 激活告警
        s.server.alarmMgr.AddAlarm(req.MemberID, req.Alarm, "Manual activation")
        return &pb.AlarmResponse{
            Header: s.server.getResponseHeader(),
        }, nil

    case pb.AlarmRequest_DEACTIVATE:
        // 清除告警
        s.server.alarmMgr.ClearAlarm(req.MemberID, req.Alarm)
        return &pb.AlarmResponse{
            Header: s.server.getResponseHeader(),
        }, nil
    }

    return &pb.AlarmResponse{
        Header: s.server.getResponseHeader(),
    }, nil
}
```

#### 自动告警检测
```go
// 在后台 goroutine 中检测
func (s *Server) monitorAlarms() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        // 检查磁盘空间
        // 检查数据完整性
        // 检查性能指标
        // 触发告警
    }
}
```

**工作量**: 3-4 小时

---

### 1.6 Status - 完善返回字段

**当前状态**: ⚠️ RaftTerm 硬编码为 1，Leader 假设为当前节点

**位置**: [maintenance.go:38-54](../pkg/etcdapi/maintenance.go#L38-L54)

**需要修复**:

```go
func (s *MaintenanceServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
    // 获取快照以计算数据库大小
    snapshot, err := s.server.store.GetSnapshot()
    var dbSize int64
    if err == nil {
        dbSize = int64(len(snapshot))
    }

    // TODO: 从 Raft 获取真实状态
    raftStatus := s.server.store.GetRaftStatus()  // 需要添加此接口

    return &pb.StatusResponse{
        Header:    s.server.getResponseHeader(),
        Version:   "3.6.0-compatible",
        DbSize:    dbSize,
        Leader:    raftStatus.LeaderID,    // 真实 leader
        RaftIndex: uint64(s.server.store.CurrentRevision()),
        RaftTerm:  raftStatus.Term,        // 真实 term
    }, nil
}
```

**依赖**: 需要先完成 Store 接口扩展（见下节）

**工作量**: 1 小时

---

## 2. 扩展 Store 接口

### 2.1 添加 Raft 状态查询接口

#### 定义 RaftStatus

```go
// RaftStatus Raft 状态信息
type RaftStatus struct {
    ID       uint64
    Term     uint64
    LeaderID uint64
    State    string  // "leader", "follower", "candidate"
}
```

#### 扩展 Store 接口

```go
// internal/kvstore/store.go

type Store interface {
    // ... 现有方法

    // GetRaftStatus 返回 Raft 状态（新增）
    GetRaftStatus() RaftStatus
}
```

#### 在 Memory 和 RocksDB 实现

```go
// internal/memory/kvstore.go

func (m *Memory) GetRaftStatus() kvstore.RaftStatus {
    // TODO: 从 raftNode 获取状态
    // return kvstore.RaftStatus{
    //     ID:       m.id,
    //     Term:     m.raftNode.Term(),
    //     LeaderID: m.raftNode.Leader(),
    //     State:    m.raftNode.State().String(),
    // }
    return kvstore.RaftStatus{}
}
```

**工作量**: 2-3 小时

---

## 3. 实现优先级

### P0 - 关键功能

1. **Status 修复** - 1 小时
2. **Store.GetRaftStatus** - 2-3 小时
3. **Hash/HashKV** - 3 小时

### P1 - 重要功能

4. **Defragment** - 1-2 小时
5. **MoveLeader** - 2-3 小时

### P2 - 可选功能

6. **Alarm 机制** - 3-4 小时

---

## 4. 总体工作量估算

| 功能 | 工作量 | 优先级 |
|------|--------|--------|
| Status 修复 | 1 小时 | P0 |
| Store.GetRaftStatus | 2-3 小时 | P0 |
| Hash | 1 小时 | P0 |
| HashKV | 2 小时 | P0 |
| Defragment | 1-2 小时 | P1 |
| MoveLeader | 2-3 小时 | P1 |
| Alarm | 3-4 小时 | P2 |
| **总计** | **12-18 小时** | - |

约 **2 个工作日**

---

## 5. 测试计划

### 5.1 Defragment 测试
- RocksDB 碎片整理前后大小对比
- 整理后数据完整性验证

### 5.2 Hash/HashKV 测试
- 同一数据集哈希一致性
- 不同节点相同数据哈希相同
- 数据变化后哈希变化

### 5.3 MoveLeader 测试
- Leader 转移成功
- 新 Leader 可正常服务
- 旧 Leader 变为 Follower

### 5.4 Alarm 测试
- 添加/清除告警
- 告警列表查询
- 自动告警触发

---

## 6. 待完成清单

### 代码实现
- [ ] 修改 internal/kvstore/store.go 添加 GetRaftStatus
- [ ] 实现 Memory.GetRaftStatus
- [ ] 实现 RocksDB.GetRaftStatus
- [ ] 完善 maintenance.go 中的 TODO 方法
- [ ] 创建 pkg/etcdapi/alarm_manager.go

### 测试
- [ ] test/maintenance_test.go - 单元测试
- [ ] test/maintenance_integration_test.go - 集成测试

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**状态**: 设计完成，待实现
