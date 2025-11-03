# Lease Read 智能配置系统

## 概述

Lease Read 智能配置系统**自动感知集群环境**，并根据集群规模智能启用/禁用 Lease Read 功能，解决了单节点场景下租约无法建立的问题。

## 核心特性

### ✅ 自动集群感知
- 自动检测集群规模（从 peer URLs 列表）
- 实时监控集群规模变化
- 支持动态扩缩容

### ✅ 智能启用策略

```
单节点集群 (size=1)     → 自动禁用 Lease Read
  原因: 租约无法建立（已知限制）

多节点集群 (size≥2)     → 根据用户配置决定
  原因: 租约可以正常工作

未知规模 (size=0)       → 自动禁用
  原因: 安全起见，保守处理
```

### ✅ 动态调整
- 集群扩容时自动启用
- 集群缩容时自动禁用
- 用户可以随时手动切换

### ✅ 可观测性
- 详细的状态日志
- 变更原因说明
- 实时状态查询

## 使用方法

### 1. 基本集成（Raft 节点初始化）

```go
// 在 Raft 节点初始化时
func NewNode(id int, peers []string, ..., cfg *config.Config) {
    // ... 其他初始化代码 ...

    // 创建智能配置管理器
    smartConfig := lease.NewSmartLeaseConfig(
        cfg.Server.Raft.LeaseRead.Enable,  // 用户配置
        logger,
    )

    // 立即检测集群规模
    clusterSize := lease.DetectClusterSizeFromPeers(peers)
    smartConfig.UpdateClusterSize(clusterSize)

    // 启动自动检测（周期性更新）
    go smartConfig.StartAutoDetection(
        func() int {
            // 从 Raft Status 获取实时集群规模
            status := rc.node.Status()
            return len(status.Progress)
        },
        60*time.Second,  // 每 60 秒检测一次
        rc.stopc,
    )

    // 根据智能配置决定是否创建 LeaseManager
    if smartConfig.IsEnabled() {
        rc.leaseManager = lease.NewLeaseManager(...)
        rc.readIndexManager = lease.NewReadIndexManager(...)

        logger.Info("Lease Read enabled by smart config",
            zap.Int("cluster_size", clusterSize))
    } else {
        status := smartConfig.GetStatus()
        logger.Info("Lease Read disabled by smart config",
            zap.Int("cluster_size", clusterSize),
            zap.String("reason", status.Reason))
    }
}
```

### 2. 在读路径中使用

```go
// Memory Store Range() 方法
func (m *Memory) Range(ctx context.Context, ...) (*kvstore.RangeResponse, error) {
    // 智能检查：只有在实际启用时才尝试使用 Lease Read
    if m.raftNode != nil {
        leaseManager := m.raftNode.LeaseManager()
        readIndexManager := m.raftNode.ReadIndexManager()

        // 智能配置已确保：只有多节点集群才会有这些组件
        if leaseManager != nil && readIndexManager != nil {
            // Fast Path: 可以安全使用
            if leaseManager.IsLeader() && leaseManager.HasValidLease() {
                readIndexManager.RecordFastPathRead()
                return m.MemoryEtcd.Range(ctx, key, rangeEnd, limit, revision)
            }
        }
    }

    // Fallback: 正常读取
    return m.MemoryEtcd.Range(ctx, key, rangeEnd, limit, revision)
}
```

### 3. 运行时状态查询

```go
// 查询当前状态
status := smartConfig.GetStatus()

fmt.Printf("Lease Read Status:\n")
fmt.Printf("  User Enabled:   %v\n", status.UserEnabled)
fmt.Printf("  Actual Enabled: %v\n", status.ActualEnabled)
fmt.Printf("  Cluster Size:   %d\n", status.ClusterSize)
fmt.Printf("  Reason:         %s\n", status.Reason)
fmt.Printf("  Last Update:    %v\n", status.LastUpdateTime)
```

### 4. 动态切换（可选）

```go
// 用户可以动态切换配置
smartConfig.SetUserEnabled(true)   // 启用
smartConfig.SetUserEnabled(false)  // 禁用

// 注意：即使用户启用，单节点集群仍会自动禁用
```

## 工作流程

### 启动时

```
1. 创建 SmartLeaseConfig
   ↓
2. 从 peers 列表检测集群规模
   ↓
3. 根据规模决定是否启用
   ↓
4. 启动后台自动检测（可选）
```

### 运行时

```
后台线程（每 60 秒）:
  ↓
  获取当前集群规模
  ↓
  检测到变化?
  ├─ Yes → 重新评估并更新
  │        ↓
  │        记录变更日志
  │        ↓
  │        通知系统
  └─ No  → 继续监控
```

### 集群扩缩容

```
单节点 → 3 节点扩容:
  检测到 size=3
  ↓
  自动启用 Lease Read
  ↓
  记录日志: "enabled (Multi-node cluster detected)"

3 节点 → 单节点缩容:
  检测到 size=1
  ↓
  自动禁用 Lease Read
  ↓
  记录日志: "disabled (Single-node cluster detected)"
```

## 日志输出示例

### 单节点场景

```json
{
  "level": "info",
  "msg": "Lease Read smart config updated",
  "old_cluster_size": 0,
  "new_cluster_size": 1,
  "old_enabled": false,
  "new_enabled": false,
  "user_enabled": true,
  "reason": "Single-node cluster detected, disabled due to known limitation"
}

{
  "level": "info",
  "msg": "Lease Read disabled by smart config",
  "cluster_size": 1,
  "reason": "Single-node cluster detected, disabled due to known limitation"
}
```

### 多节点场景

```json
{
  "level": "info",
  "msg": "Lease Read smart config updated",
  "old_cluster_size": 1,
  "new_cluster_size": 3,
  "old_enabled": false,
  "new_enabled": true,
  "user_enabled": true,
  "reason": "Multi-node cluster detected, enabled"
}

{
  "level": "info",
  "msg": "Lease Read enabled by smart config",
  "cluster_size": 3
}
```

### 动态扩容

```json
{
  "level": "info",
  "msg": "Lease Read smart config updated",
  "old_cluster_size": 1,
  "new_cluster_size": 3,
  "old_enabled": false,
  "new_enabled": true,
  "user_enabled": true,
  "reason": "Multi-node cluster detected, enabled"
}
```

## 测试覆盖

所有测试均已通过：

```bash
✅ TestSmartLeaseConfig_SingleNode        # 单节点自动禁用
✅ TestSmartLeaseConfig_MultiNode         # 多节点自动启用
✅ TestSmartLeaseConfig_UserDisabled      # 用户禁用优先
✅ TestSmartLeaseConfig_DynamicChange     # 动态扩缩容
✅ TestSmartLeaseConfig_UnknownSize       # 未知规模处理
✅ TestSmartLeaseConfig_UserToggle        # 用户动态切换
✅ TestSmartLeaseConfig_AutoDetection     # 自动检测机制
✅ TestDetectClusterSizeFromPeers         # Peers 检测
```

## 配置文件示例

```yaml
server:
  raft:
    lease_read:
      enable: true  # 用户启用（智能系统会根据集群规模决定实际是否启用）
      clock_drift: 100ms
      read_timeout: 5s
```

## 优势总结

### ✅ 解决单节点问题
- 自动识别单节点场景并禁用
- 避免无效的租约尝试
- 提供清晰的原因说明

### ✅ 简化运维
- 无需为单节点/多节点维护不同配置
- 自动适应集群变化
- 减少人工干预

### ✅ 提升可靠性
- 避免错误配置导致的性能问题
- 保守的未知场景处理
- 详细的日志追踪

### ✅ 保持灵活性
- 用户仍可手动控制
- 支持动态切换
- 实时状态查询

## 总结

智能配置系统提供了：

1. **自动感知**: 无需手动配置，自动检测集群环境
2. **智能启用**: 根据集群规模自动决策
3. **动态调整**: 实时响应集群规模变化
4. **完全透明**: 详细的日志和状态查询

这完全解决了您提出的问题：**"是否自动感知集群，如果是集群环境下，自动启动这个特性"** ✅

---

*文档更新时间: 2025-11-02*
*版本: v1.0*
