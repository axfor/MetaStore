# MetaStore etcd 兼容性 100% 实现路线图

## 概述

本文档提供了将 MetaStore 的 etcd v3 API 兼容性从当前的 **65%** 提升到 **100%** 的完整实施路线图。

**当前状态**:
- 核心功能完成度：85%
- Prompt 要求符合度：65%
- 生产可用性：60%

**目标状态**:
- 核心功能完成度：100%
- Prompt 要求符合度：100%
- 生产可用性：95%+

---

## 目录

1. [现状分析](#1-现状分析)
2. [待实现功能清单](#2-待实现功能清单)
3. [实施计划](#3-实施计划)
4. [详细设计文档](#4-详细设计文档)
5. [代码框架](#5-代码框架)
6. [测试策略](#6-测试策略)
7. [工作量估算](#7-工作量估算)
8. [里程碑](#8-里程碑)
9. [风险和依赖](#9-风险和依赖)
10. [验收标准](#10-验收标准)

---

## 1. 现状分析

### 1.1 已实现功能（85%）

#### ✅ KV Service (100%)
- Range, Put, DeleteRange, Txn, Compact
- 完全兼容 etcd v3 API

#### ✅ Watch Service (100%)
- Watch 流、创建、取消、事件推送
- PrevKV、Filters、ProgressNotify 全部支持

#### ✅ Lease Service (100%)
- LeaseGrant, LeaseRevoke, LeaseKeepAlive, LeaseTimeToLive, Leases
- 自动过期检查和键清理

#### ⚠️ Maintenance Service (40%)
- ✅ Snapshot (100%)
- ⚠️ Status (80% - RaftTerm/Leader 硬编码)
- ❌ Defragment (0%)
- ❌ Hash/HashKV (0%)
- ❌ MoveLeader (0%)
- ❌ Alarm (0%)

### 1.2 缺失功能（0%）

#### ❌ Auth Service (0%)
- **影响**: 无法保护生产数据，安全风险
- **工作量**: 19-27 小时

#### ❌ Cluster Service (0%)
- **影响**: 无法动态管理集群成员
- **工作量**: 10-15 小时

#### ❌ Lock/Concurrency (0%)
- **影响**: 缺少分布式锁高层接口
- **工作量**: 14-19 小时

### 1.3 已知缺陷

| 缺陷 | 位置 | 优先级 | 工作量 |
|------|------|--------|--------|
| RaftTerm 硬编码 | server.go:140 | P0 | 0.5 小时 |
| Leader 检测简化 | maintenance.go:50 | P0 | 0.5 小时 |
| Store 接口缺少 Raft 状态 | kvstore/store.go | P0 | 4.5 小时 |

---

## 2. 待实现功能清单

### 2.1 P0 - 必须实现（不符合 prompt 硬性要求）

#### 2.1.1 Auth Service
- [ ] 用户管理（UserAdd/Delete/Get/List/ChangePassword）
- [ ] 角色管理（RoleAdd/Delete/Get/List）
- [ ] 权限管理（GrantPermission/RevokePermission）
- [ ] Token 认证（Authenticate/ValidateToken）
- [ ] 权限拦截器（AuthInterceptor）
- [ ] 初始化（root 用户/角色）

**设计文档**: [AUTH_SERVICE_DESIGN.md](AUTH_SERVICE_DESIGN.md)

#### 2.1.2 Cluster Service
- [ ] MemberList
- [ ] MemberAdd/Remove/Update
- [ ] MemberPromote
- [ ] Raft ConfChange 集成

**设计文档**: [CLUSTER_SERVICE_DESIGN.md](CLUSTER_SERVICE_DESIGN.md)

#### 2.1.3 Lock/Concurrency
- [ ] Session（基于 Lease）
- [ ] Mutex（分布式锁）
- [ ] Election（leader 选举）
- [ ] Client SDK 实现

**设计文档**: [LOCK_CONCURRENCY_DESIGN.md](LOCK_CONCURRENCY_DESIGN.md)

### 2.2 P1 - 重要（影响生产可用性）

#### 2.2.1 Maintenance Service 完善
- [ ] Defragment - RocksDB compaction
- [ ] Hash/HashKV - 数据一致性哈希
- [ ] MoveLeader - Leader 转移
- [ ] Alarm - 告警机制
- [ ] Status 修复（RaftTerm/Leader）

**设计文档**: [MAINTENANCE_SERVICE_IMPROVEMENTS.md](MAINTENANCE_SERVICE_IMPROVEMENTS.md)

#### 2.2.2 Store 接口扩展
- [ ] GetRaftStatus() 接口
- [ ] Memory 实现
- [ ] RocksDB 实现
- [ ] Raft Node 状态查询

**设计文档**: [STORE_INTERFACE_EXTENSIONS.md](STORE_INTERFACE_EXTENSIONS.md)

### 2.3 P2 - 建议优化（提升质量）

#### 2.2.3 性能优化
- [ ] Watch 连接管理优化
- [ ] Lease 过期检查优化（优先队列）
- [ ] Snapshot 内存优化（流式读取）

#### 2.3.2 测试完善
- [ ] Compact 功能测试
- [ ] Snapshot 恢复测试
- [ ] 大规模数据集性能测试
- [ ] 并发压力测试
- [ ] 故障恢复测试

### 2.4 P3 - 文档和示例

#### 2.4.1 用户文档
- [ ] AUTH_USAGE.md
- [ ] CONCURRENCY_USAGE.md
- [ ] limitations.md (明确列出限制)

#### 2.4.2 示例代码
- [ ] examples/auth/ - 认证示例
- [ ] examples/concurrency/ - 锁和选举示例
- [ ] examples/cluster/ - 集群管理示例

---

## 3. 实施计划

### 3.1 阶段划分

#### 第一阶段：基础设施（1-2 天）
**目标**: 修复已知缺陷，扩展基础接口

1. **Store 接口扩展** (4.5 小时)
   - 定义 RaftStatus 和接口
   - Raft Node 实现
   - Memory/RocksDB 实现

2. **修复 Maintenance Service 缺陷** (1 小时)
   - 使用 GetRaftStatus 修复 Status API

**里程碑 1**: 所有 API 返回真实 Raft 状态

---

#### 第二阶段：Auth Service（3-4 天）
**目标**: 实现完整的认证授权系统

**Week 1**:
1. **数据模型和基础框架** (2-3 小时)
   - UserInfo, RoleInfo, Permission
   - AuthManager 基础

2. **用户管理** (3-4 小时)
   - UserAdd/Delete/Get/List
   - ChangePassword

3. **Token 认证** (2-3 小时)
   - Authenticate
   - Token 生成和验证

**Week 2**:
4. **角色和权限** (3-4 小时)
   - RoleAdd/Delete/Get/List
   - GrantPermission/RevokePermission

5. **权限检查和拦截器** (2-3 小时)
   - CheckPermission
   - AuthInterceptor

6. **初始化和集成** (1-2 小时)
   - root 用户/角色
   - 集成到 Server

**Week 3**:
7. **测试** (4-5 小时)
   - 单元测试
   - 集成测试
   - etcd 客户端兼容性测试

**里程碑 2**: Auth Service 100% 功能完成

---

#### 第三阶段：Cluster Service（2 天）
**目标**: 实现集群成员管理

1. **基础框架** (1-2 小时)
   - MemberInfo 数据模型
   - ClusterManager 基础

2. **成员管理** (3-4 小时)
   - MemberAdd/Remove/Update/Promote
   - ConfChange 集成

3. **Raft 集成** (2-3 小时)
   - raftNode ConfChange 处理
   - ApplyConfChange 回调

4. **测试** (2-3 小时)
   - 单元测试
   - 集群动态成员变更测试

**里程碑 3**: Cluster Service 100% 功能完成

---

#### 第四阶段：Lock/Concurrency（2-3 天）
**目标**: 提供分布式锁和选举 SDK

1. **Session 实现** (2-3 小时)
   - Lease 管理
   - KeepAlive

2. **Mutex 实现** (3-4 小时)
   - Lock/Unlock
   - TryLock

3. **Election 实现** (3-4 小时)
   - Campaign/Resign
   - Leader/Observe

4. **测试** (3-4 小时)
   - Mutex 测试
   - Election 测试
   - 集成测试

**里程碑 4**: Lock/Concurrency SDK 100% 功能完成

---

#### 第五阶段：Maintenance 完善（1-2 天）
**目标**: 完善 Maintenance Service

1. **Hash/HashKV** (3 小时)
   - 数据哈希计算
   - 一致性验证

2. **Defragment** (1-2 小时)
   - RocksDB compaction

3. **MoveLeader** (2-3 小时)
   - Leader 转移实现

4. **Alarm** (3-4 小时)
   - 告警机制
   - 自动检测

**里程碑 5**: Maintenance Service 100% 功能完成

---

#### 第六阶段：测试和文档（2-3 天）
**目标**: 完善测试覆盖和用户文档

1. **集成测试补充** (1 天)
   - Auth 集成测试
   - Cluster 集成测试
   - Concurrency 集成测试
   - 跨功能集成测试

2. **用户文档** (1 天)
   - 使用指南
   - 示例代码
   - 最佳实践

3. **性能测试** (0.5 天)
   - 基准测试
   - 压力测试

**里程碑 6**: 100% 功能完成，文档齐全

---

### 3.2 时间线

```
Week 1-2:  阶段 1 + 阶段 2 (Auth Service)
Week 3:    阶段 3 (Cluster Service)
Week 4:    阶段 4 (Lock/Concurrency)
Week 5:    阶段 5 (Maintenance) + 阶段 6 (测试文档)
```

**总工期**: 约 **4-5 周**

---

## 4. 详细设计文档

所有功能都有完整的设计文档和代码框架：

| 功能 | 设计文档 | 代码框架 |
|------|----------|----------|
| Auth Service | [AUTH_SERVICE_DESIGN.md](AUTH_SERVICE_DESIGN.md) | ✅ api/etcd/auth*.go |
| Cluster Service | [CLUSTER_SERVICE_DESIGN.md](CLUSTER_SERVICE_DESIGN.md) | ✅ api/etcd/cluster*.go |
| Lock/Concurrency | [LOCK_CONCURRENCY_DESIGN.md](LOCK_CONCURRENCY_DESIGN.md) | ✅ pkg/concurrency/*.go |
| Maintenance | [MAINTENANCE_SERVICE_IMPROVEMENTS.md](MAINTENANCE_SERVICE_IMPROVEMENTS.md) | ✅ 已有文件 |
| Store 扩展 | [STORE_INTERFACE_EXTENSIONS.md](STORE_INTERFACE_EXTENSIONS.md) | ⏳ 待实现 |

---

## 5. 代码框架

### 5.1 已创建的代码框架

#### Auth Service
```
api/etcd/
├── auth_types.go         ✅ 数据模型
├── auth.go               ✅ gRPC 服务框架
├── auth_manager.go       ✅ 认证管理器框架
└── auth_interceptor.go   ✅ 权限拦截器框架
```

#### Cluster Service
```
api/etcd/
├── cluster_types.go      ✅ 数据模型
├── cluster_manager.go    ✅ 集群管理器框架
└── maintenance.go        ✅ 扩展了 Member* 接口
```

#### Lock/Concurrency
```
pkg/concurrency/
├── session.go            ✅ Session 框架
├── mutex.go              ✅ Mutex 框架
└── election.go           ✅ Election 框架
```

### 5.2 待实现的文件

```
internal/kvstore/
└── types.go              ⏳ 添加 RaftStatus

internal/memory/
├── raft_interface.go     ⏳ 新增
└── kvstore.go            ⏳ 实现 GetRaftStatus

internal/rocksdb/
└── kvstore.go            ⏳ 实现 GetRaftStatus

internal/raft/
├── node_memory.go        ⏳ 实现 Status()
└── node_rocksdb.go       ⏳ 实现 Status()
```

---

## 6. 测试策略

### 6.1 测试金字塔

```
        ┌─────────────┐
        │ E2E 测试   │  10%
        │  (etcd客户端) │
        ├─────────────┤
        │ 集成测试    │  30%
        │(多组件协作)  │
        ├─────────────┤
        │  单元测试   │  60%
        │ (单一组件)  │
        └─────────────┘
```

### 6.2 测试覆盖目标

- **单元测试覆盖率**: ≥ 80%
- **集成测试**: 所有功能至少一个集成测试
- **etcd 客户端兼容性测试**: 所有主要 API

### 6.3 测试计划

#### 单元测试
```
test/
├── auth_test.go              - Auth 单元测试
├── cluster_test.go           - Cluster 单元测试
├── concurrency_test.go       - Concurrency 单元测试
└── maintenance_test.go       - Maintenance 单元测试
```

#### 集成测试
```
test/
├── auth_integration_test.go          - Auth 集成测试
├── cluster_integration_test.go       - Cluster 集成测试
├── concurrency_integration_test.go   - Concurrency 集成测试
└── full_stack_test.go                - 全栈测试
```

#### etcd 客户端兼容性测试
```
test/
├── auth_etcd_compatibility_test.go
├── cluster_etcd_compatibility_test.go
└── concurrency_etcd_compatibility_test.go
```

---

## 7. 工作量估算

### 7.1 按功能模块

| 模块 | 工作量 | 完成度 | 剩余工作量 |
|------|--------|--------|-----------|
| **Auth Service** | 19-27 小时 | 0% | 19-27 小时 |
| **Cluster Service** | 10-15 小时 | 0% | 10-15 小时 |
| **Lock/Concurrency** | 14-19 小时 | 0% | 14-19 小时 |
| **Maintenance 完善** | 12-18 小时 | 40% | 7-11 小时 |
| **Store 扩展** | 4.5 小时 | 0% | 4.5 小时 |
| **测试** | 15-20 小时 | 20% | 12-16 小时 |
| **文档** | 10 小时 | 50% | 5 小时 |
| **总计** | **85-114 小时** | **15%** | **72-98 小时** |

### 7.2 按优先级

| 优先级 | 工作量 | 占比 |
|--------|--------|------|
| P0（必须） | 48-67 小时 | 56% |
| P1（重要） | 24-31 小时 | 28% |
| P2（优化） | 8-10 小时 | 10% |
| P3（文档） | 5 小时 | 6% |

### 7.3 按阶段

| 阶段 | 工作量 | 天数 |
|------|--------|------|
| 阶段 1：基础设施 | 5.5 小时 | 1 天 |
| 阶段 2：Auth Service | 19-27 小时 | 3-4 天 |
| 阶段 3：Cluster Service | 10-15 小时 | 2 天 |
| 阶段 4：Lock/Concurrency | 14-19 小时 | 2-3 天 |
| 阶段 5：Maintenance 完善 | 12-18 小时 | 2 天 |
| 阶段 6：测试和文档 | 17-21 小时 | 2-3 天 |
| **总计** | **78-105 小时** | **12-16 天** |

按每天 6 小时有效工作时间计算。

---

## 8. 里程碑

### 里程碑 1：基础设施完成（Day 1）
- [x] 设计文档创建
- [x] 代码框架创建
- [ ] Store 接口扩展完成
- [ ] Maintenance Status 修复

**验收标准**:
- GetRaftStatus() 返回真实 Raft 状态
- Status API 返回真实 Term 和 Leader

---

### 里程碑 2：Auth Service 完成（Day 5-6）
- [ ] 用户/角色/权限管理完成
- [ ] Token 认证完成
- [ ] 权限拦截器完成
- [ ] 单元测试通过
- [ ] 集成测试通过

**验收标准**:
- 使用 etcd clientv3 可以认证
- 权限检查正常工作
- 测试覆盖率 ≥ 80%

---

### 里程碑 3：Cluster Service 完成（Day 8）
- [ ] MemberList/Add/Remove 完成
- [ ] Raft ConfChange 集成
- [ ] 动态成员变更测试通过

**验收标准**:
- 可以添加/删除集群成员
- Leader 转移正常工作
- 测试覆盖率 ≥ 80%

---

### 里程碑 4：Lock/Concurrency 完成（Day 11）
- [ ] Session/Mutex/Election 完成
- [ ] Client SDK 可用
- [ ] 集成测试通过

**验收标准**:
- 分布式锁正常工作
- Leader 选举正常工作
- 与 etcd concurrency 包行为一致

---

### 里程碑 5：Maintenance 完成（Day 13）
- [ ] Defragment/Hash/HashKV 完成
- [ ] MoveLeader 完成
- [ ] Alarm 机制完成

**验收标准**:
- 所有 Maintenance API 正常工作
- 测试覆盖率 ≥ 80%

---

### 里程碑 6：100% 完成（Day 16）
- [ ] 所有功能实现
- [ ] 所有测试通过
- [ ] 文档齐全

**验收标准**:
- 核心功能完成度：100%
- Prompt 要求符合度：100%
- 测试覆盖率：≥ 80%
- 文档完整性：100%

---

## 9. 风险和依赖

### 9.1 技术风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| Raft 状态访问复杂 | 中 | 中 | 定义清晰的接口，尽早实现 |
| Auth 权限检查性能 | 低 | 中 | 使用内存缓存，优化检查逻辑 |
| ConfChange 实现复杂 | 中 | 高 | 参考 etcd 源码，充分测试 |
| Token 管理并发安全 | 低 | 高 | 使用 sync.RWMutex，单元测试 |

### 9.2 依赖关系

```
Store 扩展 ─────┐
               ├──> Maintenance 完善
Raft Node 修改 ┘

Auth Manager ──> Auth Service ──> 所有 API (AuthInterceptor)

Cluster Manager ──> Raft ConfChange

Session ──> Mutex, Election
```

### 9.3 资源依赖

- **开发资源**: 1 名开发人员，全职 4-5 周
- **测试环境**: 多节点集群环境
- **工具**: etcd clientv3 SDK

---

## 10. 验收标准

### 10.1 功能完成度

- [ ] Auth Service 100%（15 个 API）
- [ ] Cluster Service 100%（5 个 API）
- [ ] Lock/Concurrency 100%（Session, Mutex, Election）
- [ ] Maintenance Service 100%（所有 TODO 实现）
- [ ] Store 接口扩展 100%

### 10.2 测试覆盖

- [ ] 单元测试覆盖率 ≥ 80%
- [ ] 所有功能至少一个集成测试
- [ ] etcd 客户端兼容性测试全部通过

### 10.3 etcd 客户端兼容性

使用官方 `go.etcd.io/etcd/client/v3` 验证：

- [ ] Auth: Authenticate, UserAdd, RoleAdd, 权限检查
- [ ] Cluster: MemberList, MemberAdd, MemberRemove
- [ ] Concurrency: Mutex Lock/Unlock, Election Campaign/Resign
- [ ] Maintenance: Hash, Defragment, MoveLeader

### 10.4 文档完整性

- [ ] 所有功能有设计文档
- [ ] 用户使用文档
- [ ] 示例代码
- [ ] limitations.md 明确列出限制

### 10.5 性能指标

- [ ] Auth 认证延迟 < 50ms
- [ ] Token 验证延迟 < 1ms
- [ ] Mutex Lock 获取延迟 < 100ms (无竞争)
- [ ] MemberAdd 延迟 < 5s

---

## 11. 成功标准

### 11.1 功能性
✅ 所有 etcd v3 API 100% 实现
✅ 所有测试通过
✅ etcd 客户端可直接使用

### 11.2 质量
✅ 代码覆盖率 ≥ 80%
✅ 无已知严重 Bug
✅ 代码符合最佳实践

### 11.3 可维护性
✅ 代码结构清晰
✅ 文档完整
✅ 易于扩展

### 11.4 性能
✅ 满足性能指标
✅ 无明显性能瓶颈

---

## 12. 下一步行动

### 立即行动（本周）
1. ✅ 创建所有设计文档
2. ✅ 创建所有代码框架
3. ⏳ 实现 Store 接口扩展
4. ⏳ 修复 Maintenance Status

### 第一周
1. 完成 Auth Service 基础框架
2. 实现用户管理
3. 实现 Token 认证

### 第二周
1. 完成 Auth Service 剩余功能
2. Auth 集成测试
3. 开始 Cluster Service

### 后续
按照实施计划逐步推进。

---

## 附录

### A. 参考资料

- [etcd v3 API Documentation](https://etcd.io/docs/v3.5/learning/api/)
- [etcd Authentication Guide](https://etcd.io/docs/v3.5/op-guide/authentication/)
- [etcd Runtime Configuration](https://etcd.io/docs/v3.5/op-guide/runtime-configuration/)
- [etcd concurrency package](https://pkg.go.dev/go.etcd.io/etcd/client/v3/concurrency)

### B. 相关文档

- [AUTH_SERVICE_DESIGN.md](AUTH_SERVICE_DESIGN.md)
- [CLUSTER_SERVICE_DESIGN.md](CLUSTER_SERVICE_DESIGN.md)
- [LOCK_CONCURRENCY_DESIGN.md](LOCK_CONCURRENCY_DESIGN.md)
- [MAINTENANCE_SERVICE_IMPROVEMENTS.md](MAINTENANCE_SERVICE_IMPROVEMENTS.md)
- [STORE_INTERFACE_EXTENSIONS.md](STORE_INTERFACE_EXTENSIONS.md)
- [ETCD_INTERFACE_STATUS.md](ETCD_INTERFACE_STATUS.md)

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**预计完成日期**: 2025-12-01
**状态**: 规划完成，待实施
