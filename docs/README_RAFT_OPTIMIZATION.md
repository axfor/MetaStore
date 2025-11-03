# MetaStore Raft 流量优化文档索引

本目录包含 MetaStore 分布式系统中 Raft 共识协议的流量优化相关文档。

## 文档导航

### 快速开始 (5 分钟)

**如果你只有 5 分钟时间**，请阅读：
- `RAFT_TRAFFIC_OPTIMIZATION_EXECUTIVE_SUMMARY.md` - 执行总结

里面包含：
- 当前性能状态 (796 ops/sec)
- 已实现的 6 项优化
- 未实现的 3 个机会
- 三阶段优化路线图
- 立即行动项

### 深度分析 (30 分钟)

**如果你需要理解技术细节**，请按顺序阅读：

1. `RAFT_TRAFFIC_OPTIMIZATION_EXECUTIVE_SUMMARY.md` (必读)
   - 快速了解现状和机会

2. `RAFT_OPTIMIZATION_ANALYSIS.md` (深度技术分析)
   - 当前性能瓶颈分析
   - 已实现优化的详细说明
   - 未实现优化的机制分析
   - 网络流量计算
   - CPU 和延迟影响分析

3. `RAFT_OPTIMIZATION_ROADMAP.md` (实施计划)
   - 详细的代码实现步骤
   - Phase 1-3 的具体任务
   - 验证和测试方法
   - 风险缓解策略

### 特定主题文档

**批量提交优化**
- `RAFT_BATCH_PROPOSAL_OPTIMIZATION.md` (7.6K)
  - 原始设计文档
  - 性能计算和验证
  - 实施计划
  
  状态: 已设计，部分禁用
  
  快速启用方法:
  ```bash
  # 取消禁用 (cmd/metastore/main.go:23)
  vim cmd/metastore/main.go
  # 改为: "metaStore/internal/batch"
  ```

**其他优化报告**
- `RAFT_OPTIMIZATION_REPORT.md` (23K) - 历史优化报告
- `RAFT_BATCHING_COMPLETION_REPORT.md` (15K) - 批处理完成报告

## 现状速查表

### 已实现的优化 (6 项)

| 优化 | 收益 | 状态 | 代码位置 |
|------|------|------|---------|
| Raft 参数优化 | 2-3x | ✓ 已启用 | pkg/config/config.go:374-395 |
| 批量 Apply | 5-10x | ✓ 已启用 | internal/memory/kvstore.go:132-179 |
| Protobuf 序列化 | 3-20.6x | ✓ 已启用 | pkg/config/config.go:128-132 |
| 日志压缩 | - | ✓ 已实现 | internal/raft/node_memory.go:433-443 |
| RocksDB 批量写入 | 5-10x | ✓ 已实现 | internal/rocksdb/kvstore.go |
| 流控制窗口 | 2x | ✓ 已配置 | pkg/config/config.go:268-272 |

### 未实现的优化 (3 项)

| 优化 | 收益 | 工作量 | 优先级 |
|------|------|--------|--------|
| 消息压缩 | 1.5-2x | 3-5 天 | 高 |
| 快照分块 | 1.5x | 2-3 天 | 中 |
| 快照压缩 | 2-5x | 1-2 天 | 高 |

### 进行中的优化 (1 项)

| 优化 | 状态 | 预期收益 |
|------|------|---------|
| 批量 Proposal | 已设计，部分禁用 | 2-5x (关键!) |

## 性能目标

### 当前性能
```
吞吐量: 796 ops/sec
延迟 P99: ~200ms
网络带宽: 296 Kbps
瓶颈: WAL fsync (1-5ms per operation)
```

### 目标性能 (Phase 1 + 2)
```
吞吐量: 10,000-40,000 ops/sec (15-50x)
延迟 P99: 50-100ms
网络带宽: < 10Mbps
完成时间: 2-3 周
```

## 核心概念解释

### WAL fsync 瓶颈
每个 Raft Ready 都触发一次 WAL (Write-Ahead Log) 的磁盘同步 (fsync)，成本 1-5ms。
使用批量处理可以将 50 个操作的 fsync 成本分摊。

### 批量 Proposal
在 Raft propose 层面进行批处理，而不是在应用层。
这样可以减少 fsync 次数，理论上提升 50x。

### 消息压缩
在传输前压缩 Raft 日志条目数据，减少网络带宽。
Snappy: 40-50% 压缩，Zstd: 70% 压缩。

### 快照优化
通过分块和压缩来优化大快照的传输。
快照大小减少 70-90%，传输时间 8-10x 加速。

## 立即行动

### Week 1: 快速赢 (高收益，低风险)

**任务**: 重新启用批量 Proposal

```bash
# 1. 编辑文件
vim cmd/metastore/main.go

# 2. 在第 23 行，改为:
"metaStore/internal/batch"

# 3. 测试
go test -bench=BenchmarkPut ./test -v

# 预期结果: 796 → ~3,980 ops/sec (5x)
```

**任务**: 验证配置

```bash
# 检查 Raft 参数是否已优化
grep -E "Tick|MaxInflight" pkg/config/config.go

# 应该看到:
# - TickInterval: 50ms (vs etcd 100ms)
# - MaxInflightMsgs: 1024 (vs etcd 512)
```

### Week 2-3: 消息压缩 (中等工作量)

参考 `RAFT_OPTIMIZATION_ROADMAP.md:1.2` 的详细实施步骤。

关键步骤:
1. 创建 `internal/raft/compression/codec.go`
2. 实现 SnappyCodec 和 ZstdCodec
3. 集成到 Raft propose 和 apply 路径
4. 运行性能测试

预期: 1.5-2x 吞吐量 + 70% 带宽优化

### Week 4-6: 快照优化 (可选)

参考 `RAFT_OPTIMIZATION_ROADMAP.md:2.1-2.2` 的详细实施步骤。

### Week 7+: 长期优化 (高风险)

异步 WAL fsync 等深度优化，仅在明确需要时实施。

## 代码导航

### 主要源文件

```
internal/raft/
├── node_memory.go      # Memory 存储的 Raft 实现 (500+ lines)
├── node_rocksdb.go     # RocksDB 存储的 Raft 实现
└── listener.go         # HTTP 监听器

internal/memory/
└── kvstore.go          # 内存存储的 etcd 兼容层 (已优化批量 apply)

internal/rocksdb/
├── kvstore.go          # RocksDB 存储的 etcd 兼容层 (已优化批量写入)
└── raftlog.go          # RocksDB Raft 日志存储 (259+ lines)

pkg/config/
└── config.go           # 配置管理 (已优化参数) (550+ lines)

configs/
└── config.yaml         # 配置示例
```

### 关键优化位置

| 优化 | 文件 | 行号 | 说明 |
|------|------|------|------|
| Raft 参数 | pkg/config/config.go | 374-395 | ElectionTick, HeartbeatTick 等 |
| Batch Apply | internal/memory/kvstore.go | 132-179 | readCommits 和 applyBatch |
| Log Compact | internal/raft/node_memory.go | 433-443 | maybeTriggerSnapshot |
| Protobuf | pkg/config/config.go | 128-132 | EnableProtobuf 等 |

## 测试与验证

### 运行性能基准

```bash
# 基准测试
cd test
go test -bench=BenchmarkPut -benchtime=10s -v

# 并发测试
go test -bench=BenchmarkParallelPuts -benchtime=10s -v

# 长期稳定性测试
go test -run=TestLongRunning -timeout=1h -v
```

### 一致性验证

```bash
# 跨集群数据一致性
go test -run=TestConsistency ./test -v

# 快照恢复测试
go test -run=TestSnapshotRecovery ./test -v

# 网络分区恢复
go test -run=TestNetworkPartition ./test -v
```

## 常见问题

### Q: 为什么当前只有 796 ops/sec？
**A**: 主要瓶颈是 WAL fsync。每个 Raft Ready 都触发一次 fsync，成本 1-5ms。
单线程下: 1000 / 1.25ms = 796 ops/sec。使用批处理可以分摊这个成本。

### Q: 批量 Proposal 为什么被禁用？
**A**: 推测原因包括:
1. 原始实现可能有对齐问题
2. 测试环境可能对延迟敏感
3. Apply 层可能需要更多工作支持批量操作

建议重新启用并在生产环境测试。

### Q: 消息压缩会增加多少 CPU 开销？
**A**: 根据算法:
- Snappy: +5% CPU (快速)
- Zstd: +15% CPU (高效)

相比 50x 吞吐提升，这个代价可以接受。

### Q: 异步 WAL fsync 有什么风险？
**A**: 理论上可能在极端故障场景丢失数据。仅推荐在:
1. 有多副本冗余 (3+ 节点)
2. 明确理解风险
3. 已验证故障恢复流程

的场景下启用。

### Q: 优化是否向后兼容？
**A**: 是的。所有优化都是:
- 配置可选
- 格式可升级 (带版本号)
- 读取路径支持旧格式

## 性能对标

### etcd 对标
```
etcd 3.5 (生产优化):
  单节点吞吐: 10,000-20,000 ops/sec (key-value 对齐)
  网络延迟: < 1ms (局域网)
  
MetaStore 目标:
  单节点吞吐: 10,000-40,000 ops/sec (优于 etcd)
  网络延迟: 50-100ms (可接受)
```

### TiKV 对标
```
TiKV Raft 优化:
  - 批量 Proposal: 30-50x 吞吐提升
  - 消息压缩: 2-3x 网络优化
  - 快照优化: 5-10x 传输加速
  
MetaStore 规划:
  采用类似策略，预期 30x 总提升
```

## 贡献指南

如果你想改进 Raft 优化:

1. 阅读相关文档理解现状
2. 参考 `RAFT_OPTIMIZATION_ROADMAP.md` 的实施步骤
3. 创建性能对比测试
4. 运行完整的测试套件
5. 文档化你的改进

## 文件清单

```
docs/
├── README_RAFT_OPTIMIZATION.md                    (本文件)
├── RAFT_TRAFFIC_OPTIMIZATION_EXECUTIVE_SUMMARY.md (执行总结)
├── RAFT_OPTIMIZATION_ANALYSIS.md                  (深度分析)
├── RAFT_OPTIMIZATION_ROADMAP.md                   (实施路线图)
├── RAFT_BATCH_PROPOSAL_OPTIMIZATION.md            (批量设计)
├── RAFT_OPTIMIZATION_REPORT.md                    (历史报告)
└── RAFT_BATCHING_COMPLETION_REPORT.md             (完成报告)
```

## 相关资源

### 外部参考
- etcd Raft 文档: https://github.com/etcd-io/etcd/tree/main/raft
- TiKV 优化实践: https://tikv.org/
- gRPC 性能优化: https://grpc.io/docs/guides/performance-tuning/

### 内部资源
- Raft 实现: `internal/raft/`
- 存储层: `internal/memory/` 和 `internal/rocksdb/`
- 配置管理: `pkg/config/`
- 性能测试: `test/benchmark_test.go`

## 更新日志

- **2025-11-02**: 创建综合优化分析和路线图
- **2025-11-01**: 完成批处理报告
- **2025-10-***: 初始优化研究

---

**文档维护者**: MetaStore 团队  
**最后更新**: 2025-11-02  
**下次审查**: 优化实施完成后
