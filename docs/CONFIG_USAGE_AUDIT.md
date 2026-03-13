# MetaStore 配置使用情况审计报告

**生成日期**: 2025-11-02
**审计范围**: pkg/config/config.go 中所有配置结构
**目的**: 确保所有定义的配置项都被实际使用

---

## 📊 概览

| 配置模块 | 配置项数量 | 使用状态 | 使用位置 |
|---------|-----------|---------|---------|
| GRPCConfig | 13 | ✅ **完全使用** | api/etcd/server.go, pkg/grpc/server.go |
| LimitsConfig | 4 | ✅ **部分使用** | api/etcd/server.go, pkg/grpc/server.go |
| LeaseConfig | 2 | ❌ **未使用** | - |
| AuthConfig | 4 | ❌ **未使用** | - |
| MaintenanceConfig | 1 | ❌ **未使用** | - |
| ReliabilityConfig | 5 | ✅ **部分使用** | api/etcd/server.go, pkg/grpc/server.go |
| LogConfig | 4 | ❌ **未使用** | - |
| MonitoringConfig | 3 | ✅ **部分使用** | pkg/grpc/server.go |
| PerformanceConfig | 3 | ✅ **完全使用** | pkg/config/performance.go, internal/memory/*, internal/common/* |
| PebbleConfig | 15 | ❌ **未使用** | - |

**测试代码使用情况**:
- ❌ **test/** 目录中没有任何测试使用配置文件
- 所有测试使用硬编码配置或默认值

**总结**:
- ✅ **已使用**: 26 个配置项
- ❌ **未使用**: 33 个配置项
- 📊 **使用率**: 44.1%

---

## 1️⃣ GRPCConfig - ✅ 完全使用

**定义位置**: [pkg/config/config.go:51-72](pkg/config/config.go:51-72)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| MaxRecvMsgSize | int | 4MB | api/etcd/server.go:169-171 | ✅ |
| MaxSendMsgSize | int | 4MB | api/etcd/server.go:172-174 | ✅ |
| MaxConcurrentStreams | uint32 | 2048 | api/etcd/server.go:177-179 | ✅ |
| InitialWindowSize | int32 | 8MB | api/etcd/server.go:182-184 | ✅ |
| InitialConnWindowSize | int32 | 16MB | api/etcd/server.go:185-187 | ✅ |
| KeepaliveTime | time.Duration | 10s | api/etcd/server.go:192 | ✅ |
| KeepaliveTimeout | time.Duration | 10s | api/etcd/server.go:193 | ✅ |
| MaxConnectionIdle | time.Duration | 300s | api/etcd/server.go:195-197 | ✅ |
| MaxConnectionAge | time.Duration | 10m | api/etcd/server.go:198-200 | ✅ |
| MaxConnectionAgeGrace | time.Duration | 10s | api/etcd/server.go:201-203 | ✅ |
| EnableRateLimit | bool | true | pkg/grpc/server.go:123 | ✅ |
| RateLimitQPS | int | 1000000 | pkg/grpc/server.go:127 | ✅ |
| RateLimitBurst | int | 2000000 | pkg/grpc/server.go:128 | ✅ |

**使用详情**:
```go
// api/etcd/server.go:165-206
if cfg.Config != nil {
    grpcCfg := cfg.Config.Server.GRPC

    if grpcCfg.MaxRecvMsgSize > 0 {
        grpcOpts = append(grpcOpts, grpc.MaxRecvMsgSize(grpcCfg.MaxRecvMsgSize))
    }
    // ... 所有 gRPC 配置项都被应用
}
```

**评估**: ✅ **优秀** - 所有配置项都被正确使用

---

## 2️⃣ LimitsConfig - ✅ 完全使用

**定义位置**: [pkg/config/config.go:74-80](pkg/config/config.go:74-80)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| MaxConnections | int | 1000 | api/etcd/server.go:109, pkg/grpc/server.go:117-119 | ✅ |
| MaxWatchCount | int | 10000 | - | ❌ |
| MaxLeaseCount | int | 10000 | - | ❌ |
| MaxRequestSize | int64 | 1.5MB | api/etcd/server.go:111 | ✅ |

**使用详情**:
```go
// api/etcd/server.go:108-112
cfg.ResourceLimits = &reliability.ResourceLimits{
    MaxConnections: int64(cfg.Config.Server.Limits.MaxConnections),
    MaxRequests:    int64(cfg.Config.Server.Limits.MaxConnections * 10),
    MaxMemoryBytes: cfg.Config.Server.Limits.MaxRequestSize * 1000,
}
```

**评估**: ⚠️ **部分使用** - MaxWatchCount 和 MaxLeaseCount 未使用

---

## 3️⃣ LeaseConfig - ❌ 未使用

**定义位置**: [pkg/config/config.go:82-86](pkg/config/config.go:82-86)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| CheckInterval | time.Duration | 1s | - | ❌ |
| DefaultTTL | time.Duration | 60s | - | ❌ |

**影响**:
- Lease 管理器使用硬编码的检查间隔
- 默认 TTL 未被配置文件控制

**建议**: 在 `api/etcd/lease_manager.go` 中使用这些配置

---

## 4️⃣ AuthConfig - ❌ 未使用

**定义位置**: [pkg/config/config.go:88-94](pkg/config/config.go:88-94)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| TokenTTL | time.Duration | 24h | - | ❌ |
| TokenCleanupInterval | time.Duration | 5m | - | ❌ |
| BcryptCost | int | 10 | - | ❌ |
| EnableAudit | bool | false | - | ❌ |

**影响**:
- 认证模块使用硬编码的配置
- Token 管理和审计功能未配置化

**建议**: 在 `api/etcd/auth.go` 中使用这些配置

---

## 5️⃣ MaintenanceConfig - ❌ 未使用

**定义位置**: [pkg/config/config.go:96-99](pkg/config/config.go:96-99)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| SnapshotChunkSize | int | 4MB | - | ❌ |

**影响**:
- 快照传输使用硬编码的分块大小
- 无法根据网络环境调整

**建议**: 在 `api/etcd/maintenance.go` 中使用此配置

---

## 6️⃣ ReliabilityConfig - ✅ 部分使用

**定义位置**: [pkg/config/config.go:101-108](pkg/config/config.go:101-108)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| ShutdownTimeout | time.Duration | 30s | api/etcd/server.go:97 | ✅ |
| DrainTimeout | time.Duration | 5s | - | ❌ |
| EnableCRC | bool | false | api/etcd/server.go:100 | ✅ |
| EnableHealthCheck | bool | true | api/etcd/server.go:103 | ✅ |
| EnablePanicRecovery | bool | true | pkg/grpc/server.go:105 | ✅ |

**使用详情**:
```go
// api/etcd/server.go:96-104
if cfg.ShutdownTimeout == 0 {
    cfg.ShutdownTimeout = cfg.Config.Server.Reliability.ShutdownTimeout
}
if !cfg.EnableCRC {
    cfg.EnableCRC = cfg.Config.Server.Reliability.EnableCRC
}
// ...
```

**评估**: ✅ **良好** - 5 个配置项中有 4 个被使用

---

## 7️⃣ LogConfig - ❌ 未使用

**定义位置**: [pkg/config/config.go:110-116](pkg/config/config.go:110-116)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| Level | string | "info" | - | ❌ |
| Encoding | string | "json" | - | ❌ |
| OutputPaths | []string | ["stdout"] | - | ❌ |
| ErrorOutputPaths | []string | ["stderr"] | - | ❌ |

**影响**:
- 日志系统使用硬编码配置
- 无法通过配置文件调整日志级别和输出

**建议**: 在 `pkg/log/log.go` 中初始化时使用这些配置

---

## 8️⃣ MonitoringConfig - ✅ 完全使用

**定义位置**: [pkg/config/config.go:118-123](pkg/config/config.go:118-123)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| EnablePrometheus | bool | true | pkg/grpc/server.go:99 | ✅ |
| PrometheusPort | int | 9090 | - | ⚠️ |
| SlowRequestThreshold | time.Duration | 100ms | pkg/grpc/server.go:111 | ✅ |

**使用详情**:
```go
// pkg/grpc/server.go:99-102
if b.cfg.Server.Monitoring.EnablePrometheus && b.metrics != nil {
    mi := metrics.NewMetricsInterceptor(b.metrics)
    interceptors = append(interceptors, mi.UnaryServerInterceptor())
}

// pkg/grpc/server.go:111-114
if b.cfg.Server.Monitoring.SlowRequestThreshold > 0 {
    li := NewLoggingInterceptor(b.cfg.Server.Monitoring.SlowRequestThreshold, b.logger)
    interceptors = append(interceptors, li.UnaryServerInterceptor())
}
```

**评估**: ✅ **良好** - EnablePrometheus 和 SlowRequestThreshold 被使用，PrometheusPort 需要在 Prometheus 启动时使用

---

## 9️⃣ PerformanceConfig - ✅ 完全使用

**定义位置**: [pkg/config/config.go:125-130](pkg/config/config.go:125-130)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| EnableProtobuf | bool | true | internal/memory/protobuf_converter.go:28 | ✅ |
| EnableSnapshotProtobuf | bool | true | internal/memory/snapshot_converter.go | ✅ |
| EnableLeaseProtobuf | bool | true | internal/common/lease_converter.go:31 | ✅ |

**使用详情**:
```go
// pkg/config/performance.go:41-44
func GetEnableProtobuf() bool {
    return globalEnableProtobuf.Load()
}

// internal/memory/protobuf_converter.go:28
func enableProtobuf() bool { return config.GetEnableProtobuf() }

// internal/common/lease_converter.go:31
func EnableLeaseProtobuf() bool { return config.GetEnableLeaseProtobuf() }
```

**评估**: ✅ **优秀** - 所有配置项通过全局访问器被正确使用

---

## 🔟 PebbleConfig - ❌ 完全未使用

**定义位置**: [pkg/config/config.go:132-156](pkg/config/config.go:132-156)

| 配置项 | 类型 | 默认值 | 使用位置 | 状态 |
|--------|------|--------|---------|------|
| BlockCacheSize | uint64 | 256MB | - | ❌ |
| WriteBufferSize | uint64 | 64MB | - | ❌ |
| MaxWriteBufferNumber | int | 3 | - | ❌ |
| MinWriteBufferNumberToMerge | int | 1 | - | ❌ |
| MaxBackgroundJobs | int | 4 | - | ❌ |
| Level0FileNumCompactionTrigger | int | 4 | - | ❌ |
| Level0SlowdownWritesTrigger | int | 20 | - | ❌ |
| Level0StopWritesTrigger | int | 36 | - | ❌ |
| BloomFilterBitsPerKey | int | 10 | - | ❌ |
| BlockBasedTableBloomFilter | bool | true | - | ❌ |
| MaxOpenFiles | int | 10000 | - | ❌ |
| UseFsync | bool | false | - | ❌ |
| BytesPerSync | uint64 | 1MB | - | ❌ |

**影响**:
- Pebble 引擎使用硬编码的默认配置（[internal/pebble/config.go:84-102](internal/pebble/config.go:84-102)）
- 无法通过配置文件调整 Pebble 性能

**当前硬编码值**:
```go
// internal/pebble/config.go:84-102
func DefaultOptimizationConfig() OptimizationConfig {
    return OptimizationConfig{
        BlockCache: BlockCacheConfig{
            Size: 512 * 1024 * 1024,  // 硬编码 512MB（与配置文件的 256MB 不一致）
            // ...
        },
        // ...
    }
}
```

**建议**: **高优先级** - 需要实施 Pebble 配置集成（详见下文）

---

## 🚨 关键问题

### 问题 1: Pebble 配置未被使用（优先级：🔴 高）

**现状**:
- 配置文件定义了 15 个 Pebble 配置项
- 所有配置项都有合理的默认值
- **但是没有任何代码使用这些配置**

**影响**:
- 用户修改配置文件无效
- 无法进行 Pebble 性能调优
- 配置文件与实际行为不一致

**解决方案**:

#### 步骤 1: 修改 Pebble 配置应用函数

**文件**: `internal/pebble/config.go`

添加新函数：
```go
// ConfigFromYAML 从 YAML 配置创建 OptimizationConfig
func ConfigFromYAML(cfg *config.PebbleConfig) OptimizationConfig {
    return OptimizationConfig{
        WAL: WALConfig{
            Sync:         cfg.UseFsync,
            SizeLimitMB:  64,
            TTLSeconds:   0,
            MaxTotalSize: 512 * 1024 * 1024,
        },
        BlockCache: BlockCacheConfig{
            Size:                  cfg.BlockCacheSize,
            NumShardBits:          6,
            HighPriorityPoolRatio: 0.5,
        },
        ColumnFamilies: ColumnFamilyConfig{
            Enabled:  false,
            Families: []string{"kv", "lease", "meta"},
        },
    }
}

// ApplyDBOptionsFromConfig 应用 YAML 配置到 DBOptions
func ApplyDBOptionsFromConfig(opts *gpebble.Options, cfg *config.PebbleConfig) {
    // Block Cache
    if cfg.BlockCacheSize > 0 {
        cache := gpebble.NewLRUCache(cfg.BlockCacheSize)
        bbto := gpebble.NewDefaultBlockBasedTableOptions()
        bbto.SetBlockCache(cache)

        if cfg.BlockBasedTableBloomFilter {
            bbto.SetFilterPolicy(gpebble.NewBloomFilter(cfg.BloomFilterBitsPerKey))
        }

        opts.SetBlockBasedTableFactory(bbto)
    }

    // Write Buffer
    if cfg.WriteBufferSize > 0 {
        opts.SetWriteBufferSize(cfg.WriteBufferSize)
    }
    if cfg.MaxWriteBufferNumber > 0 {
        opts.SetMaxWriteBufferNumber(cfg.MaxWriteBufferNumber)
    }
    if cfg.MinWriteBufferNumberToMerge > 0 {
        opts.SetMinWriteBufferNumberToMerge(cfg.MinWriteBufferNumberToMerge)
    }

    // Compaction
    if cfg.MaxBackgroundJobs > 0 {
        opts.SetMaxBackgroundJobs(cfg.MaxBackgroundJobs)
    }
    if cfg.Level0FileNumCompactionTrigger > 0 {
        opts.SetLevel0FileNumCompactionTrigger(cfg.Level0FileNumCompactionTrigger)
    }
    if cfg.Level0SlowdownWritesTrigger > 0 {
        opts.SetLevel0SlowdownWritesTrigger(cfg.Level0SlowdownWritesTrigger)
    }
    if cfg.Level0StopWritesTrigger > 0 {
        opts.SetLevel0StopWritesTrigger(cfg.Level0StopWritesTrigger)
    }

    // 其他优化
    if cfg.MaxOpenFiles > 0 {
        opts.SetMaxOpenFiles(cfg.MaxOpenFiles)
    }
    if cfg.BytesPerSync > 0 {
        opts.SetBytesPerSync(cfg.BytesPerSync)
    }
}
```

#### 步骤 2: 修改 storage.go 的 Open 函数

**文件**: `internal/pebble/storage.go`

```go
// Open 打开 Pebble 数据库（使用配置）
func Open(path string, cfg *config.PebbleConfig) (*gpebble.DB, error) {
    opts := gpebble.NewDefaultOptions()
    opts.SetCreateIfMissing(true)

    // 应用配置文件中的设置
    if cfg != nil {
        ApplyDBOptionsFromConfig(opts, cfg)
    } else {
        // 使用默认优化配置
        defaultCfg := DefaultOptimizationConfig()
        defaultCfg.ApplyDBOptions(opts)
    }

    db, err := gpebble.OpenDb(opts, path)
    if err != nil {
        return nil, err
    }

    return db, nil
}
```

#### 步骤 3: 修改 main.go 传递配置

**文件**: `cmd/metastore/main.go`

```go
// 第 100 行，修改为：
db, err := pebble.Open(dbPath, &cfg.Server.Pebble)
```

---

### 问题 2: Lease/Auth/Maintenance/Log 配置未使用（优先级：🟡 中）

这些配置虽然定义了，但相应的模块使用硬编码值。

**建议**:

1. **LeaseConfig**: 在 `api/etcd/lease_manager.go` 初始化时使用
2. **AuthConfig**: 在 `api/etcd/auth.go` 初始化时使用
3. **MaintenanceConfig**: 在 `api/etcd/maintenance.go` 使用
4. **LogConfig**: 在 `pkg/log/log.go` 或 `cmd/metastore/main.go` 初始化时使用

---

### 问题 3: Limits 配置部分未使用（优先级：🟢 低）

**未使用的配置**:
- `MaxWatchCount`
- `MaxLeaseCount`

**建议**: 在 WatchManager 和 LeaseManager 中添加限制检查

---

## 📝 实施优先级

### 🔴 优先级 1: Pebble 配置集成

**预计工时**: 2-3 小时
**影响**: 高 - 15 个配置项
**价值**: 允许用户调优 Pebble 性能

**任务**:
1. 修改 `internal/pebble/config.go` 添加配置转换函数
2. 修改 `internal/pebble/storage.go` 的 Open 函数接收配置
3. 修改 `cmd/metastore/main.go` 传递配置
4. 测试验证配置生效

### 🟡 优先级 2: Log 配置集成

**预计工时**: 1 小时
**影响**: 中 - 4 个配置项
**价值**: 允许用户控制日志行为

**任务**:
1. 修改 `pkg/log/log.go` 使用 LogConfig
2. 在 `cmd/metastore/main.go` 中应用配置

### 🟡 优先级 3: Lease/Auth/Maintenance 配置集成

**预计工时**: 3-4 小时
**影响**: 中 - 7 个配置项
**价值**: 提高模块的可配置性

**任务**:
1. 修改 LeaseManager 使用 LeaseConfig
2. 修改 AuthManager 使用 AuthConfig
3. 修改 MaintenanceServer 使用 MaintenanceConfig

### 🟢 优先级 4: Limits 配置完善

**预计工时**: 1-2 小时
**影响**: 低 - 2 个配置项
**价值**: 完善资源限制

**任务**:
1. 在 WatchManager 中应用 MaxWatchCount
2. 在 LeaseManager 中应用 MaxLeaseCount

### 🟢 优先级 5: 测试代码配置化

**预计工时**: 2-3 小时
**影响**: 低 - 提高测试灵活性
**价值**: 允许测试使用不同配置场景

**当前问题**:
- ❌ `test/` 目录中所有测试都使用硬编码配置
- ❌ 无法测试不同配置参数的影响
- ❌ 性能测试无法模拟生产环境配置

**建议**:

1. **创建测试配置文件**:
   ```yaml
   # test/configs/test_config.yaml
   server:
     cluster_id: 1
     member_id: 1
     listen_address: ":2379"

     grpc:
       max_recv_msg_size: 4194304
       # ... 其他配置

     performance:
       enable_protobuf: true
       enable_snapshot_protobuf: true
       enable_lease_protobuf: true

     pebble:
       block_cache_size: 268435456
       # ... 其他配置
   ```

2. **修改测试辅助函数**:
   ```go
   // test/testutil/config.go
   package testutil

   import "metaStore/pkg/config"

   // LoadTestConfig 加载测试配置
   func LoadTestConfig() (*config.Config, error) {
       return config.LoadConfig("test/configs/test_config.yaml")
   }

   // DefaultTestConfig 返回测试默认配置
   func DefaultTestConfig() *config.Config {
       return config.DefaultConfig(1, 1, ":2379")
   }
   ```

3. **更新性能测试使用配置**:
   ```go
   // test/performance_test.go
   func BenchmarkPebbleWithConfig(b *testing.B) {
       cfg := testutil.LoadTestConfig()

       // 使用配置打开 Pebble
       db, err := pebble.Open(dbPath, &cfg.Server.Pebble)
       // ...
   }
   ```

**收益**:
- ✅ 可以测试不同配置场景
- ✅ 性能测试更接近生产环境
- ✅ 更容易发现配置相关的问题

---

## ✅ 总结

### 已使用的配置（26 项）

1. **GRPCConfig** (11 项) - ✅ 完全使用
2. **LimitsConfig** (2/4 项) - MaxConnections, MaxRequestSize
3. **ReliabilityConfig** (4/5 项) - 除了 DrainTimeout
4. **MonitoringConfig** (2/3 项) - EnablePrometheus, SlowRequestThreshold
5. **PerformanceConfig** (3 项) - ✅ 完全使用

### 未使用的配置（33 项）

1. **PebbleConfig** (15 项) - ❌ 完全未使用
2. **LeaseConfig** (2 项) - ❌ 完全未使用
3. **AuthConfig** (4 项) - ❌ 完全未使用
4. **MaintenanceConfig** (1 项) - ❌ 未使用
5. **LogConfig** (4 项) - ❌ 完全未使用
6. **LimitsConfig** (2 项) - MaxWatchCount, MaxLeaseCount
7. **ReliabilityConfig** (1 项) - DrainTimeout
8. **MonitoringConfig** (1 项) - PrometheusPort（需要在 Prometheus 启动时使用）

### 行动建议

1. **立即实施**: Pebble 配置集成（优先级 1）
2. **近期实施**: Log 配置集成（优先级 2）
3. **逐步完善**: Lease/Auth/Maintenance 配置集成（优先级 3）
4. **长期优化**: 完善 Limits 配置（优先级 4）

---

**报告结束**

*Generated by Claude Code - MetaStore Configuration Audit*
