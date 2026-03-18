# etcd 集成测试指南

## 当前仓库现状

MetaStore 当前已经同时覆盖 **Memory** 与 **Pebble** 两种后端的 etcd 集成测试，不再是“只有 Memory、Pebble 待后续补齐”的状态。

现有 etcd 相关测试大致分为三类：

1. **后端/场景专项测试**
   - `test/etcd_memory_integration_test.go`
   - `test/etcd_memory_consistency_test.go`
   - `test/etcd_pebble_integration_test.go`
   - `test/etcd_pebble_consistency_test.go`
2. **兼容性与跨协议验证**
   - `test/etcd_compatibility_test.go`
   - `test/cross_protocol_integration_test.go`
   - 以及 `test/maintenance_*`、`test/lease_*` 等场景测试
3. **HTTP gateway 覆盖**
   - `api/etcdgateway/gateway_test.go`

这些测试共同验证 MetaStore 的 etcd 语义、集群行为、维护接口，以及 HTTP gateway 的兼容性响应格式。

## Upstream-style compatibility suite

`test/etcd_upstream_*_test.go` 这一组测试文件提供了新的 **upstream-style compatibility suite**。这些测试以黑盒方式启动真实的 MetaStore 节点，并通过官方 etcd client / concurrency API 发起请求，用于校验对外暴露的兼容行为。

它们是对仓库既有测试的补充，而不是替代：
- 不替代现有 Memory / Pebble 专项测试
- 不替代跨协议测试
- 不替代 maintenance、lease、performance 等已有场景测试
- HTTP gateway 覆盖仍然单独保留在 `api/etcdgateway/gateway_test.go`

该套件的文件入口位于 `test/etcd_upstream_*_test.go`，当前包括：
- `test/etcd_upstream_kv_test.go`
- `test/etcd_upstream_watch_test.go`
- `test/etcd_upstream_lease_test.go`
- `test/etcd_upstream_maintenance_test.go`
- `test/etcd_upstream_concurrency_test.go`
- `test/etcd_upstream_cluster_smoke_test.go`

## 新 suite 的覆盖范围

Initial scope:
- KV：Put / Get / Delete / Prefix Range / 基础事务成功与失败路径
- Watch：watch create notify 与基础 PUT 事件投递
- Lease：grant、keepalive、TTL、attached keys、revoke 后键删除
- Maintenance：Status、Hash、HashKV、Snapshot smoke
- Official etcd concurrency：官方 `client/v3/concurrency` mutex/lock smoke
- Cluster smoke：真实 3 节点集群上的跨节点写入、读取复制、leader/status 基础检查

覆盖策略重点在于：
- 使用官方客户端验证“外部可见语义”
- 同时跑过 Memory 与 Pebble 单节点后端
- 对集群路径保留小而稳的 smoke coverage，而不是一次性引入全部 upstream 内部测试矩阵

## Initial exclusions

Initial exclusions:
- etcd internal MVCC / WAL / embed 测试
- advanced auth / RBAC compatibility
- watch history replay / compact 边界语义
- 大规模故障注入、网络分区、长期 soak 与性能基准
- 依赖 etcd 内部实现细节而非公开客户端行为的测试

这些内容当前是**有意排除**，不是遗漏。当前 suite 的目标是先建立稳定、可重复运行的黑盒兼容性基线，再视运行时长与稳定性逐步扩展。

## 如何运行定向兼容性测试

Compatibility suite (manual until stable):

```bash
go test -v -timeout=20m ./test -run '^TestEtcdUpstream'
```

HTTP gateway 覆盖仍然单独运行：

```bash
go test -v -timeout=10m ./api/etcdgateway -run '^TestHTTP'
```

也就是说，upstream-style compatibility suite 当前仍走手动命令路径；而 HTTP gateway 相关验证继续保持独立，不与该命令混合。

## Known differences / failure classification

当 upstream-style compatibility suite 失败时，建议按以下类别归因：

- **product bug**：MetaStore 对外语义与预期兼容行为不一致，需要修复产品实现
- **harness mismatch**：测试 harness、启动方式、等待条件或断言方式与实际运行特征不匹配
- **known semantic difference**：当前已知且已接受的兼容差异，需要记录而不是误报为新缺陷
- **out of current scope**：失败点落在本阶段明确排除的范围内，不作为当前 suite gate

建议在 triage 时先确认失败属于哪一类，再决定是修产品、修测试、补文档，还是延期到后续兼容性阶段处理。

## 与现有测试体系的关系

当前仓库中的 etcd 测试体系可以这样理解：

- `test/etcd_memory_*` 与 `test/etcd_pebble_*`：验证后端实现与集群一致性场景
- `test/etcd_upstream_*`：验证官方 etcd 客户端视角下的黑盒兼容性
- `api/etcdgateway/gateway_test.go`：验证 `/v3/*` HTTP gateway 的兼容响应
- 其他 `test/lease_*`、`test/maintenance_*`、`test/cross_protocol_*`：补充专项行为、维护语义和跨协议一致性

因此，新的 upstream-style suite 应被视为“兼容性基线层”，而不是唯一的 etcd 测试入口。
