# MetaStore etcd 兼容层限制说明

本文档说明 MetaStore etcd 兼容层的当前限制和与官方 etcd 的差异。

## 概述

MetaStore 提供了与 etcd v3 API 兼容的 gRPC 接口，但在某些功能和行为上与官方 etcd 存在差异。

## 当前实现状态（演示版本）

**版本**: v0.1.0-demo
**日期**: 2025-10

### ✅ 已实现的功能

1. **KV 操作**
   - ✅ Range（单键和范围查询）
   - ✅ Put（包括 lease 关联）
   - ✅ DeleteRange（单键和范围删除）
   - ✅ Transaction（Compare-Then-Else 语义）
   - ⚠️ Compact（接口存在但功能简化）

2. **Watch**
   - ✅ Watch 创建和取消
   - ✅ 事件推送（PUT/DELETE）
   - ✅ 流式 gRPC
   - ❌ 历史事件回放（startRevision > 0）

3. **Lease**
   - ✅ LeaseGrant（创建租约）
   - ✅ LeaseRevoke（撤销租约）
   - ✅ LeaseKeepAlive（续约，流式）
   - ✅ LeaseTimeToLive（查询剩余时间）
   - ✅ Leases（列出所有租约）
   - ✅ 自动过期删除关联的键

4. **Maintenance**
   - ✅ Status（服务器状态）
   - ✅ Snapshot（快照，简化实现）
   - ⚠️ Alarm（占位实现）
   - ⚠️ Defragment（占位实现）
   - ⚠️ Hash/HashKV（占位实现）

5. **基础功能**
   - ✅ gRPC 服务器
   - ✅ Revision 管理
   - ✅ 错误码映射（gRPC status codes）
   - ✅ ResponseHeader（cluster_id, member_id, revision）

### ❌ 未实现的功能

1. **Auth/RBAC**
   - ❌ 用户管理（UserAdd, UserDelete, UserList 等）
   - ❌ 角色管理（RoleAdd, RoleDelete, RoleList 等）
   - ❌ 权限管理（RoleGrantPermission, RoleRevokePermission）
   - ❌ 认证（AuthEnable, Authenticate 等）

2. **Cluster 管理**
   - ❌ MemberAdd（添加成员）
   - ❌ MemberRemove（移除成员）
   - ❌ MemberUpdate（更新成员）
   - ❌ MemberList（列出成员）
   - ❌ MemberPromote（提升成员）

3. **高级功能**
   - ❌ Lock/Concurrency 高层 API
   - ❌ Election API

## 已知限制

### 1. MVCC 和历史版本

**限制**：
- 当前实现仅维护最新版本的数据
- 不支持查询历史 revision 的数据
- Compact 操作简化实现，不会真正释放空间

**影响**：
- 无法执行时间旅行查询（time-travel queries）
- 无法查询被删除键的历史值
- Watch 不支持从历史 revision 开始回放事件

**替代方案**：
- 仅支持当前 revision 的查询
- Watch 只能从当前 revision 开始

**未来计划**：
- Phase 2: 实现简单的多版本存储（保留最近 N 个版本）
- Phase 3: 实现完整的 MVCC

### 2. Lease 过期精度

**限制**：
- Lease 过期检查基于定时器（1秒轮询）
- 不是精确的实时过期

**影响**：
- 实际过期时间可能有 ±1 秒误差
- 在高精度场景下可能不够准确

**替代方案**：
- 可接受的误差范围（对大多数应用场景足够）

**未来计划**：
- 优化过期检查机制，提高精度

### 3. Watch 可靠性

**限制**：
- 事件通道有缓冲限制（100 个事件）
- 网络断开时可能丢失事件
- 不支持历史事件回放

**影响**：
- 高频写入场景下，watch 可能丢失事件
- 客户端重连后无法恢复丢失的事件

**替代方案**：
- 客户端需要实现重连逻辑
- 使用 Range 查询补充丢失的数据

**未来计划**：
- 实现事件持久化
- 支持历史事件回放

### 4. Raft 集成（演示版本）

**限制**：
- **当前演示版本不使用 Raft 共识**
- 单节点运行，无分布式一致性保证
- 无 leader 选举

**影响**：
- 不适合生产环境
- 无高可用性
- 无数据复制

**替代方案**：
- 仅用于开发和测试
- 理解 etcd API 兼容性

**未来计划**：
- Phase 2: 集成 Raft 共识
- 支持多节点集群

### 5. 性能限制

**限制**：
- 内存存储，无持久化（演示版本）
- 单线程处理某些操作
- 无性能优化

**影响**：
- 重启后数据丢失
- 高并发性能可能不足
- 内存占用随数据量线性增长

**替代方案**：
- 适合小规模测试
- 适合开发环境

**未来计划**：
- Phase 2: 支持 RocksDB 持久化
- Phase 3: 性能优化

### 6. Snapshot 功能

**限制**：
- Snapshot 实现简化
- 序列化格式非官方格式
- 无增量快照

**影响**：
- 无法与官方 etcd 快照互操作
- 大数据量时快照性能较差

**替代方案**：
- 使用 MetaStore 自己的快照格式

**未来计划**：
- 兼容官方快照格式
- 优化快照性能

## 兼容性矩阵

| 功能 | 状态 | 兼容性 | 说明 |
|------|------|--------|------|
| Range (Get) | ✅ | 100% | 完全兼容 |
| Put | ✅ | 100% | 完全兼容 |
| Delete | ✅ | 100% | 完全兼容 |
| Txn | ✅ | 100% | 完全兼容 |
| Watch | ✅ | 80% | 不支持历史事件回放 |
| Lease | ✅ | 95% | 过期精度 ±1s |
| Compact | ⚠️ | 50% | 接口兼容但功能简化 |
| Status | ✅ | 90% | 基本信息完整 |
| Snapshot | ⚠️ | 60% | 格式不兼容 |
| Auth | ❌ | 0% | 未实现 |
| Cluster | ❌ | 0% | 未实现 |

## 错误码映射

MetaStore 正确映射了以下 gRPC 错误码：

| 内部错误 | gRPC 错误码 | 说明 |
|----------|-------------|------|
| ErrKeyNotFound | NotFound | 键不存在 |
| ErrCompacted | OutOfRange | Revision 已压缩 |
| ErrFutureRev | OutOfRange | Revision 是未来值 |
| ErrLeaseNotFound | NotFound | Lease 不存在 |
| ErrLeaseExpired | NotFound | Lease 已过期 |
| ErrTxnConflict | FailedPrecondition | 事务冲突 |
| ErrPermissionDenied | PermissionDenied | 权限不足 |
| ErrAuthFailed | Unauthenticated | 认证失败 |
| ErrInvalidArgument | InvalidArgument | 参数无效 |
| ErrWatchCanceled | Canceled | Watch 已取消 |

## 使用建议

### 适用场景

✅ **适合使用的场景**：
- 开发和测试 etcd 客户端应用
- 学习 etcd API
- 原型验证
- 单机开发环境
- CI/CD 测试环境

❌ **不适合使用的场景**：
- 生产环境（演示版本）
- 需要高可用的场景
- 需要持久化的场景
- 需要认证授权的场景
- 需要完整 MVCC 的场景

### 迁移指南

如果您想从 MetaStore 迁移到官方 etcd，或反之：

1. **数据迁移**：
   - 使用 Range 查询导出所有数据
   - 使用 Put 导入到目标系统
   - 注意：Lease 和 Watch 不会迁移

2. **代码兼容性**：
   - 使用官方 etcd client SDK（clientv3）
   - 大多数 KV 操作无需修改
   - 避免依赖高级特性（Auth、Cluster）

3. **配置调整**：
   - 修改 endpoints 配置
   - 调整超时参数
   - 适配错误处理

## 贡献和反馈

如果您发现任何问题或有改进建议，欢迎：
- 提交 GitHub Issue
- 提交 Pull Request
- 参与讨论

## 版本规划

### Phase 1（当前）：演示版本
- ✅ etcd v3 gRPC 接口框架
- ✅ 基本 KV 操作
- ✅ Watch/Lease 基础功能
- ✅ 内存存储

### Phase 2：生产就绪
- ⏳ 集成 Raft 共识
- ⏳ RocksDB 持久化
- ⏳ 完整的 MVCC
- ⏳ 历史事件回放

### Phase 3：企业级
- ⏳ Auth/RBAC
- ⏳ 集群管理
- ⏳ 性能优化
- ⏳ 监控和可观测性

---

**最后更新**: 2025-10
**文档版本**: v1.0
