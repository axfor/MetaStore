# 剩余未使用配置清单

**生成日期**: 2025-11-02
**当前使用率**: 43/59 = 73.1%

---

## ❌ 完全未使用的配置 (9 项)

### 1️⃣ AuthConfig (4 项)

**定义位置**: [pkg/config/config.go:88-94](pkg/config/config.go:88-94)

| 配置项 | 类型 | 默认值 | 用途 |
|--------|------|--------|------|
| TokenTTL | time.Duration | 24h | Token 过期时间 |
| TokenCleanupInterval | time.Duration | 5m | Token 清理间隔 |
| BcryptCost | int | 10 | bcrypt 加密强度 |
| EnableAudit | bool | false | 是否启用审计日志 |

**当前问题**:
- `api/etcd/auth.go` 中使用硬编码值
- 无法通过配置文件控制认证参数

**实施方案**:
```go
// api/etcd/auth.go
type AuthManager struct {
    // ... 现有字段
    tokenTTL             time.Duration
    tokenCleanupInterval time.Duration
    bcryptCost           int
    enableAudit          bool
}

func NewAuthManager(store kvstore.Store, cfg ...*config.AuthConfig) *AuthManager {
    var authCfg *config.AuthConfig
    if len(cfg) > 0 && cfg[0] != nil {
        authCfg = cfg[0]
    } else {
        defaultCfg := config.DefaultConfig(1, 1, ":2379")
        authCfg = &defaultCfg.Server.Auth
    }

    return &AuthManager{
        store:                store,
        tokenTTL:             authCfg.TokenTTL,
        tokenCleanupInterval: authCfg.TokenCleanupInterval,
        bcryptCost:           authCfg.BcryptCost,
        enableAudit:          authCfg.EnableAudit,
        // ...
    }
}
```

---

### 2️⃣ MaintenanceConfig (1 项)

**定义位置**: [pkg/config/config.go:96-99](pkg/config/config.go:96-99)

| 配置项 | 类型 | 默认值 | 用途 |
|--------|------|--------|------|
| SnapshotChunkSize | int | 4MB | 快照分块大小 |

**当前问题**:
- `api/etcd/maintenance.go` 中使用硬编码值
- 无法根据网络环境调整快照传输大小

**实施方案**:
```go
// api/etcd/maintenance.go
type MaintenanceServer struct {
    // ... 现有字段
    snapshotChunkSize int
}

// 在 Snapshot 方法中使用
func (ms *MaintenanceServer) Snapshot(req *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
    // ...
    chunkSize := ms.snapshotChunkSize  // 使用配置的值而非硬编码
    // ...
}
```

---

### 3️⃣ LogConfig (4 项)

**定义位置**: [pkg/config/config.go:110-116](pkg/config/config.go:110-116)

| 配置项 | 类型 | 默认值 | 用途 |
|--------|------|--------|------|
| Level | string | "info" | 日志级别 |
| Encoding | string | "json" | 日志编码格式 |
| OutputPaths | []string | ["stdout"] | 日志输出路径 |
| ErrorOutputPaths | []string | ["stderr"] | 错误日志输出路径 |

**当前问题**:
- 日志系统在包初始化时硬编码配置
- 无法通过配置文件控制日志行为

**实施方案**:
```go
// pkg/log/log.go
func InitLogger(cfg *config.LogConfig) error {
    var level zapcore.Level
    switch cfg.Level {
    case "debug":
        level = zapcore.DebugLevel
    case "info":
        level = zapcore.InfoLevel
    case "warn":
        level = zapcore.WarnLevel
    case "error":
        level = zapcore.ErrorLevel
    default:
        level = zapcore.InfoLevel
    }

    var encoding string
    if cfg.Encoding == "console" {
        encoding = "console"
    } else {
        encoding = "json"
    }

    config := zap.Config{
        Level:            zap.NewAtomicLevelAt(level),
        Encoding:         encoding,
        OutputPaths:      cfg.OutputPaths,
        ErrorOutputPaths: cfg.ErrorOutputPaths,
        // ...
    }

    logger, err := config.Build()
    if err != nil {
        return err
    }

    zap.ReplaceGlobals(logger)
    return nil
}

// cmd/metastore/main.go
func main() {
    // ...
    cfg, err := config.LoadConfigOrDefault(...)

    // 初始化日志系统
    if err := log.InitLogger(&cfg.Server.Log); err != nil {
        fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
        os.Exit(-1)
    }
    // ...
}
```

---

## ⚠️ 部分使用的配置 (7 项)

### 4️⃣ LimitsConfig (2/4 项 - 缺少 2 项)

**定义位置**: [pkg/config/config.go:74-80](pkg/config/config.go:74-80)

| 配置项 | 状态 | 默认值 | 用途 |
|--------|------|--------|------|
| MaxConnections | ✅ 已使用 | 1000 | 最大连接数 |
| MaxRequestSize | ✅ 已使用 | 1.5MB | 最大请求大小 |
| **MaxWatchCount** | ❌ **未使用** | 10000 | 最大 Watch 数量 |
| **MaxLeaseCount** | ❌ **未使用** | 10000 | 最大 Lease 数量 |

**实施方案**:

```go
// api/etcd/watch_manager.go
type WatchManager struct {
    // ... 现有字段
    maxWatchCount int
    watchCount    atomic.Int32
}

func NewWatchManager(store kvstore.Store, cfg ...*config.LimitsConfig) *WatchManager {
    maxWatches := 10000 // 默认值
    if len(cfg) > 0 && cfg[0] != nil {
        maxWatches = cfg[0].MaxWatchCount
    }

    return &WatchManager{
        store:         store,
        maxWatchCount: maxWatches,
        // ...
    }
}

func (wm *WatchManager) Watch(req *pb.WatchRequest) error {
    // 检查 Watch 数量限制
    if int(wm.watchCount.Load()) >= wm.maxWatchCount {
        return fmt.Errorf("too many watches: limit %d", wm.maxWatchCount)
    }
    wm.watchCount.Add(1)
    defer wm.watchCount.Add(-1)
    // ...
}

// api/etcd/lease_manager.go
type LeaseManager struct {
    // ... 现有字段
    maxLeaseCount int
}

func (lm *LeaseManager) Grant(id int64, ttl int64) (*kvstore.Lease, error) {
    // 检查 Lease 数量限制
    lm.mu.RLock()
    count := len(lm.leases)
    lm.mu.RUnlock()

    if count >= lm.maxLeaseCount {
        return nil, fmt.Errorf("too many leases: limit %d", lm.maxLeaseCount)
    }
    // ...
}
```

---

### 5️⃣ ReliabilityConfig (4/5 项 - 缺少 1 项)

**定义位置**: [pkg/config/config.go:101-108](pkg/config/config.go:101-108)

| 配置项 | 状态 | 默认值 | 用途 |
|--------|------|--------|------|
| ShutdownTimeout | ✅ 已使用 | 30s | 优雅关闭超时 |
| EnableCRC | ✅ 已使用 | false | 是否启用 CRC 校验 |
| EnableHealthCheck | ✅ 已使用 | true | 是否启用健康检查 |
| EnablePanicRecovery | ✅ 已使用 | true | 是否启用 Panic 恢复 |
| **DrainTimeout** | ❌ **未使用** | 5s | 连接耗尽超时 |

**实施方案**:
```go
// pkg/reliability/graceful_shutdown.go
type GracefulShutdown struct {
    shutdownTimeout time.Duration
    drainTimeout    time.Duration  // 新增字段
    // ...
}

func NewGracefulShutdown(shutdownTimeout, drainTimeout time.Duration) *GracefulShutdown {
    return &GracefulShutdown{
        shutdownTimeout: shutdownTimeout,
        drainTimeout:    drainTimeout,
        // ...
    }
}

func (gs *GracefulShutdown) Shutdown(ctx context.Context, server *grpc.Server) error {
    // 1. 停止接受新连接
    // 2. 等待现有连接完成（使用 drainTimeout）
    drainCtx, cancel := context.WithTimeout(ctx, gs.drainTimeout)
    defer cancel()
    // ...
}

// api/etcd/server.go
shutdownMgr := reliability.NewGracefulShutdown(
    cfg.Config.Server.Reliability.ShutdownTimeout,
    cfg.Config.Server.Reliability.DrainTimeout,  // 传递配置
)
```

---

### 6️⃣ MonitoringConfig (2/3 项 - 缺少 1 项)

**定义位置**: [pkg/config/config.go:118-123](pkg/config/config.go:118-123)

| 配置项 | 状态 | 默认值 | 用途 |
|--------|------|--------|------|
| EnablePrometheus | ✅ 已使用 | true | 是否启用 Prometheus |
| SlowRequestThreshold | ✅ 已使用 | 100ms | 慢查询阈值 |
| **PrometheusPort** | ⚠️ **部分使用** | 9090 | Prometheus 端口 |

**当前问题**:
- 配置项已定义但未在 Prometheus 服务器启动时使用
- 需要在启动 Prometheus HTTP 服务器时应用此配置

**实施方案**:
```go
// cmd/metastore/main.go 或 pkg/metrics/prometheus.go
func startPrometheusServer(cfg *config.MonitoringConfig) {
    if !cfg.EnablePrometheus {
        return
    }

    http.Handle("/metrics", promhttp.Handler())
    addr := fmt.Sprintf(":%d", cfg.PrometheusPort)

    go func() {
        log.Info("Starting Prometheus metrics server",
            zap.String("address", addr),
            zap.String("component", "prometheus"))

        if err := http.ListenAndServe(addr, nil); err != nil {
            log.Error("Prometheus server failed", zap.Error(err))
        }
    }()
}

// 在 main() 中调用
if cfg.Server.Monitoring.EnablePrometheus {
    startPrometheusServer(&cfg.Server.Monitoring)
}
```

---

## 📊 统计总结

### 当前状态

| 分类 | 配置项数量 | 百分比 |
|------|-----------|--------|
| ✅ 完全使用 | 43 | 73.1% |
| ⚠️ 部分使用需要完善 | 7 | 11.9% |
| ❌ 完全未使用 | 9 | 15.3% |
| **总计** | **59** | **100%** |

### 未使用配置明细

| 配置模块 | 未使用项 | 配置名称 |
|---------|---------|---------|
| AuthConfig | 4 | TokenTTL, TokenCleanupInterval, BcryptCost, EnableAudit |
| MaintenanceConfig | 1 | SnapshotChunkSize |
| LogConfig | 4 | Level, Encoding, OutputPaths, ErrorOutputPaths |
| LimitsConfig | 2 | MaxWatchCount, MaxLeaseCount |
| ReliabilityConfig | 1 | DrainTimeout |
| MonitoringConfig | 1 | PrometheusPort (需完善) |
| **总计** | **13** | - |

---

## 🎯 推荐实施顺序

### 优先级 1: LogConfig (4 项) 🔴
**原因**: 日志是基础设施，影响运维和调试
**工时**: 1-2 小时
**难度**: 简单
**文件**: pkg/log/log.go, cmd/metastore/main.go

### 优先级 2: LimitsConfig 完善 (2 项) 🟡
**原因**: 防止资源耗尽，保护系统稳定性
**工时**: 1-2 小时
**难度**: 简单
**文件**: api/etcd/watch_manager.go, api/etcd/lease_manager.go

### 优先级 3: AuthConfig (4 项) 🟡
**原因**: 安全相关，生产环境需要
**工时**: 2-3 小时
**难度**: 中等
**文件**: api/etcd/auth.go

### 优先级 4: MonitoringConfig 完善 (1 项) 🟢
**原因**: 监控配置完整性
**工时**: 0.5 小时
**难度**: 简单
**文件**: cmd/metastore/main.go 或 pkg/metrics/prometheus.go

### 优先级 5: MaintenanceConfig (1 项) 🟢
**原因**: 优化快照传输
**工时**: 0.5 小时
**难度**: 简单
**文件**: api/etcd/maintenance.go

### 优先级 6: ReliabilityConfig 完善 (1 项) 🟢
**原因**: 完善优雅关闭机制
**工时**: 1 小时
**难度**: 中等
**文件**: pkg/reliability/graceful_shutdown.go

---

## ✅ 完成后效果

实施所有剩余配置后：

- **配置使用率**: 73.1% → **100%** 🎉
- **可配置性**: 所有行为都可通过配置文件控制
- **可维护性**: 无硬编码值，易于调整
- **生产就绪**: 符合企业级软件标准

---

**报告结束**

*Generated by Claude Code - MetaStore Configuration Audit*
