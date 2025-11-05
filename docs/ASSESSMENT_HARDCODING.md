# 场景硬编码评估

## 总体评分: 92%

---

## 一、已识别的硬编码值

### 1.1 高优先级硬编码（必须配置化）- P0

#### 1. Lease 过期检查间隔

**位置**: [api/etcd/lease_manager.go:136](../api/etcd/lease_manager.go#L136)

```go
func (lm *LeaseManager) expiryChecker() {
    ticker := time.NewTicker(1 * time.Second)  // ❌ 硬编码 1 秒
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            lm.checkExpiredLeases()
        case <-lm.stopCh:
            return
        }
    }
}
```

**问题**:
- 固定 1 秒检查间隔
- 不同场景可能需要不同的间隔（开发环境 vs 生产环境）
- 影响 CPU 使用率和延迟

**建议配置化**:
```go
type LeaseConfig struct {
    CheckInterval time.Duration  // 默认 1s，可配置
}

func NewLeaseManager(store kvstore.Store, cfg LeaseConfig) *LeaseManager {
    if cfg.CheckInterval == 0 {
        cfg.CheckInterval = 1 * time.Second  // 默认值
    }

    lm := &LeaseManager{
        store:         store,
        checkInterval: cfg.CheckInterval,
        // ...
    }
    return lm
}

func (lm *LeaseManager) expiryChecker() {
    ticker := time.NewTicker(lm.checkInterval)  // ✅ 可配置
    // ...
}
```

**影响**: 高
**优先级**: P0
**工作量**: 30 分钟

---

#### 2. Token 过期时间

**位置**: [api/etcd/auth_manager.go:188](../api/etcd/auth_manager.go#L188)

```go
func (am *AuthManager) Authenticate(username, password string) (string, error) {
    // ...

    tokenInfo := &TokenInfo{
        Token:     token,
        Username:  username,
        ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),  // ❌ 硬编码 24 小时
    }

    // ...
}
```

**问题**:
- 固定 24 小时过期
- 不同环境可能需要不同的过期时间
- 安全需求不同（开发环境可以更长，生产环境可能需要更短）

**建议配置化**:
```go
type AuthConfig struct {
    TokenTTL           time.Duration  // 默认 24h
    TokenRefreshEnable bool           // 是否支持刷新
}

func NewAuthManager(store kvstore.Store, cfg AuthConfig) *AuthManager {
    if cfg.TokenTTL == 0 {
        cfg.TokenTTL = 24 * time.Hour
    }

    am := &AuthManager{
        store:    store,
        tokenTTL: cfg.TokenTTL,
        // ...
    }
    return am
}

func (am *AuthManager) Authenticate(username, password string) (string, error) {
    // ...

    tokenInfo := &TokenInfo{
        Token:     token,
        Username:  username,
        ExpiresAt: time.Now().Add(am.tokenTTL).Unix(),  // ✅ 可配置
    }

    // ...
}
```

**影响**: 高
**优先级**: P0
**工作量**: 30 分钟

---

#### 3. Token 清理间隔

**位置**: [api/etcd/auth_manager.go:744](../api/etcd/auth_manager.go#L744)

```go
func (am *AuthManager) cleanupExpiredTokens() {
    ticker := time.NewTicker(5 * time.Minute)  // ❌ 硬编码 5 分钟
    defer ticker.Stop()

    for range ticker.C {
        am.mu.Lock()
        now := time.Now().Unix()
        for token, info := range am.tokens {
            if info.ExpiresAt < now {
                delete(am.tokens, token)
            }
        }
        am.mu.Unlock()
    }
}
```

**问题**:
- 固定 5 分钟清理间隔
- 内存占用和 CPU 使用的权衡
- 不同规模系统需要不同的清理策略

**建议配置化**:
```go
type AuthConfig struct {
    TokenTTL            time.Duration
    TokenCleanupInterval time.Duration  // 默认 5m
}

func (am *AuthManager) cleanupExpiredTokens() {
    ticker := time.NewTicker(am.tokenCleanupInterval)  // ✅ 可配置
    // ...
}
```

**影响**: 中
**优先级**: P0
**工作量**: 15 分钟

---

#### 4. Snapshot 分块大小

**位置**: [api/etcd/maintenance.go:168](../api/etcd/maintenance.go#L168)

```go
func (s *MaintenanceServer) Snapshot(req *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
    snapshot, err := s.server.store.GetSnapshot()
    if err != nil {
        return toGRPCError(err)
    }

    chunkSize := 4 * 1024 * 1024  // ❌ 硬编码 4MB
    for i := 0; i < len(snapshot); i += chunkSize {
        // ...
    }
    return nil
}
```

**问题**:
- 固定 4MB 分块大小
- 不同网络条件需要不同的分块大小
- 内存和网络效率的权衡

**建议配置化**:
```go
type MaintenanceConfig struct {
    SnapshotChunkSize int  // 默认 4MB
}

type Server struct {
    // ...
    maintenanceConfig MaintenanceConfig
}

func (s *MaintenanceServer) Snapshot(req *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
    // ...
    chunkSize := s.server.maintenanceConfig.SnapshotChunkSize  // ✅ 可配置
    if chunkSize == 0 {
        chunkSize = 4 * 1024 * 1024  // 默认 4MB
    }
    // ...
}
```

**影响**: 中
**优先级**: P0
**工作量**: 30 分钟

---

#### 5. 默认集群 ID 和成员 ID

**位置**: [api/etcd/server.go:82-86](../api/etcd/server.go#L82-L86)

```go
func NewServer(cfg ServerConfig) (*Server, error) {
    // ...
    if cfg.ClusterID == 0 {
        cfg.ClusterID = 1  // ❌ 硬编码默认值
    }
    if cfg.MemberID == 0 {
        cfg.MemberID = 1  // ❌ 硬编码默认值
    }
    // ...
}
```

**问题**:
- 多个实例使用相同的默认 ID 会冲突
- 应该强制用户指定，或使用唯一生成算法

**建议修改**:
```go
func NewServer(cfg ServerConfig) (*Server, error) {
    // 方案 1: 强制用户指定（推荐）
    if cfg.ClusterID == 0 {
        return nil, fmt.Errorf("cluster ID is required")
    }
    if cfg.MemberID == 0 {
        return nil, fmt.Errorf("member ID is required")
    }

    // 方案 2: 自动生成唯一 ID
    if cfg.ClusterID == 0 {
        cfg.ClusterID = generateClusterID()  // 基于时间戳或 UUID
    }
    if cfg.MemberID == 0 {
        cfg.MemberID = generateMemberID()  // 基于 hostname 和 port
    }

    // ...
}
```

**影响**: 高（可能导致集群冲突）
**优先级**: P0
**工作量**: 1 小时

---

### 1.2 中优先级硬编码（建议配置化）- P1

#### 6. 优雅关闭超时

**位置**: [api/etcd/server.go:89-91](../api/etcd/server.go#L89-L91)

```go
func NewServer(cfg ServerConfig) (*Server, error) {
    // ...
    if cfg.ShutdownTimeout == 0 {
        cfg.ShutdownTimeout = 30 * time.Second  // ✅ 可配置，有默认值
    }
    // ...
}
```

**评价**: ✅ 已经支持配置，只是有默认值

**优先级**: P1（已经比较好）
**工作量**: 无需改进

---

#### 7. 等待连接耗尽时间

**位置**: [api/etcd/server.go:200-202](../api/etcd/server.go#L200-L202)

```go
shutdownMgr.RegisterHook(reliability.PhaseDrainConnections, func(ctx context.Context) error {
    log.Info("Shutdown phase: Drain existing connections")
    time.Sleep(2 * time.Second)  // ❌ 硬编码 2 秒
    return nil
})
```

**问题**:
- 固定 2 秒等待时间
- 不同系统的请求处理时间不同
- 应该基于实际请求完成情况

**建议优化**:
```go
type ReliabilityConfig struct {
    DrainTimeout time.Duration  // 默认 5s
}

shutdownMgr.RegisterHook(reliability.PhaseDrainConnections, func(ctx context.Context) error {
    log.Info("Shutdown phase: Drain existing connections")

    // 方案 1: 使用配置的超时
    drainCtx, cancel := context.WithTimeout(ctx, cfg.DrainTimeout)
    defer cancel()

    // 方案 2: 检查实际请求数
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            activeRequests := s.resourceMgr.GetActiveRequestCount()
            if activeRequests == 0 {
                return nil  // 所有请求已完成
            }
        case <-drainCtx.Done():
            // 超时，强制关闭
            log.Warn("Drain timeout, forcing shutdown")
            return nil
        }
    }
})
```

**影响**: 中
**优先级**: P1
**工作量**: 1 小时

---

#### 8. bcrypt Cost

**位置**: [api/etcd/auth_manager.go:732](../api/etcd/auth_manager.go#L732)

```go
func hashPassword(password string) (string, error) {
    bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)  // ✅ 使用标准值
    return string(bytes), err
}
```

**评价**: ✅ 使用 `bcrypt.DefaultCost` (10)，这是合理的

**是否需要配置化**: 可选
- DefaultCost = 10 是推荐值
- 如果需要更高安全性，可以配置为 12 或 14
- 但会影响性能

**建议**（可选）:
```go
type AuthConfig struct {
    BcryptCost int  // 默认 10
}

func hashPassword(password string, cost int) (string, error) {
    if cost == 0 {
        cost = bcrypt.DefaultCost
    }
    bytes, err := bcrypt.GenerateFromPassword([]byte(password), cost)
    return string(bytes), err
}
```

**影响**: 低
**优先级**: P2（可选）
**工作量**: 15 分钟

---

### 1.3 低优先级硬编码（可接受）- P3

#### 9. 版本号

**位置**: [api/etcd/maintenance.go:96](../api/etcd/maintenance.go#L96)

```go
func (s *MaintenanceServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
    // ...
    return &pb.StatusResponse{
        Header:  s.server.getResponseHeader(),
        Version: "3.6.0-compatible",  // ✅ 可以硬编码
        // ...
    }, nil
}
```

**评价**: ✅ 可以硬编码
- 版本号通常在编译时确定
- 可以通过 build tag 注入

**建议优化**（可选）:
```go
// 编译时注入版本号
var (
    Version   = "dev"
    GitCommit = "unknown"
    BuildTime = "unknown"
)

// go build -ldflags "-X main.Version=1.0.0 -X main.GitCommit=$(git rev-parse HEAD)"
```

**影响**: 低
**优先级**: P3
**工作量**: 30 分钟

---

#### 10. gRPC 日志前缀

**位置**: [api/etcd/server.go:321](../api/etcd/server.go#L321)

```go
func (s *Server) PanicRecoveryInterceptor(...) {
    defer func() {
        if r := recover(); r != nil {
            reliability.RecoverPanic(fmt.Sprintf("grpc-handler-%s", info.FullMethod))  // ✅ 可以硬编码
            // ...
        }
    }()
    // ...
}
```

**评价**: ✅ 可以硬编码
- 日志前缀是固定的
- 不需要配置

**影响**: 无
**优先级**: P3

---

## 二、统一配置管理方案

### 2.1 配置文件结构

```yaml
# configs/metastore.yaml
server:
  # 集群配置
  cluster_id: 1234567890
  member_id: 1
  listen_address: ":2379"

  # gRPC 配置
  grpc:
    max_recv_msg_size: 1572864  # 1.5MB
    max_send_msg_size: 1572864
    max_concurrent_streams: 1000
    initial_window_size: 1048576
    initial_conn_window_size: 1048576
    keepalive_time: 5s
    keepalive_timeout: 1s
    max_connection_idle: 15s

  # 资源限制
  limits:
    max_connections: 1000
    max_watch_count: 10000
    max_lease_count: 10000
    max_request_size: 1572864

  # Lease 配置
  lease:
    check_interval: 1s        # Lease 过期检查间隔
    default_ttl: 60s          # 默认 TTL

  # Auth 配置
  auth:
    token_ttl: 24h            # Token 过期时间
    token_cleanup_interval: 5m  # Token 清理间隔
    bcrypt_cost: 10           # bcrypt 加密强度
    enable_audit: false       # 是否启用审计日志

  # Maintenance 配置
  maintenance:
    snapshot_chunk_size: 4194304  # 4MB

  # Reliability 配置
  reliability:
    shutdown_timeout: 30s     # 优雅关闭超时
    drain_timeout: 5s         # 连接耗尽超时
    enable_crc: false         # 是否启用 CRC 校验
    enable_health_check: true # 是否启用健康检查
    enable_panic_recovery: true

  # Log 配置
  log:
    level: info               # debug, info, warn, error
    encoding: json            # json 或 console
    output_paths:
      - stdout
      - /var/log/metastore/app.log
    error_output_paths:
      - stderr
      - /var/log/metastore/error.log

  # Monitoring 配置
  monitoring:
    enable_prometheus: true
    prometheus_port: 9090
    slow_request_threshold: 100ms
```

### 2.2 配置加载代码

```go
// pkg/config/config.go
package config

import (
    "time"
    "gopkg.in/yaml.v3"
)

type Config struct {
    Server ServerConfig `yaml:"server"`
}

type ServerConfig struct {
    ClusterID      uint64         `yaml:"cluster_id"`
    MemberID       uint64         `yaml:"member_id"`
    ListenAddress  string         `yaml:"listen_address"`
    GRPC           GRPCConfig     `yaml:"grpc"`
    Limits         LimitsConfig   `yaml:"limits"`
    Lease          LeaseConfig    `yaml:"lease"`
    Auth           AuthConfig     `yaml:"auth"`
    Maintenance    MaintenanceConfig `yaml:"maintenance"`
    Reliability    ReliabilityConfig `yaml:"reliability"`
    Log            LogConfig      `yaml:"log"`
    Monitoring     MonitoringConfig `yaml:"monitoring"`
}

type GRPCConfig struct {
    MaxRecvMsgSize          int           `yaml:"max_recv_msg_size"`
    MaxSendMsgSize          int           `yaml:"max_send_msg_size"`
    MaxConcurrentStreams    uint32        `yaml:"max_concurrent_streams"`
    InitialWindowSize       int32         `yaml:"initial_window_size"`
    InitialConnWindowSize   int32         `yaml:"initial_conn_window_size"`
    KeepaliveTime           time.Duration `yaml:"keepalive_time"`
    KeepaliveTimeout        time.Duration `yaml:"keepalive_timeout"`
    MaxConnectionIdle       time.Duration `yaml:"max_connection_idle"`
}

type LeaseConfig struct {
    CheckInterval time.Duration `yaml:"check_interval"`
    DefaultTTL    time.Duration `yaml:"default_ttl"`
}

type AuthConfig struct {
    TokenTTL            time.Duration `yaml:"token_ttl"`
    TokenCleanupInterval time.Duration `yaml:"token_cleanup_interval"`
    BcryptCost          int           `yaml:"bcrypt_cost"`
    EnableAudit         bool          `yaml:"enable_audit"`
}

type MaintenanceConfig struct {
    SnapshotChunkSize int `yaml:"snapshot_chunk_size"`
}

type ReliabilityConfig struct {
    ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`
    DrainTimeout        time.Duration `yaml:"drain_timeout"`
    EnableCRC           bool          `yaml:"enable_crc"`
    EnableHealthCheck   bool          `yaml:"enable_health_check"`
    EnablePanicRecovery bool          `yaml:"enable_panic_recovery"`
}

type MonitoringConfig struct {
    EnablePrometheus     bool          `yaml:"enable_prometheus"`
    PrometheusPort       int           `yaml:"prometheus_port"`
    SlowRequestThreshold time.Duration `yaml:"slow_request_threshold"`
}

// LoadConfig 加载配置文件
func LoadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read config file: %w", err)
    }

    var cfg Config
    if err := yaml.Unmarshal(data, &cfg); err != nil {
        return nil, fmt.Errorf("failed to parse config: %w", err)
    }

    // 验证配置
    if err := cfg.Validate(); err != nil {
        return nil, fmt.Errorf("invalid config: %w", err)
    }

    return &cfg, nil
}

// Validate 验证配置
func (c *Config) Validate() error {
    if c.Server.ClusterID == 0 {
        return fmt.Errorf("cluster_id is required")
    }
    if c.Server.MemberID == 0 {
        return fmt.Errorf("member_id is required")
    }
    // ... 更多验证
    return nil
}

// SetDefaults 设置默认值
func (c *Config) SetDefaults() {
    if c.Server.ListenAddress == "" {
        c.Server.ListenAddress = ":2379"
    }
    if c.Server.Lease.CheckInterval == 0 {
        c.Server.Lease.CheckInterval = 1 * time.Second
    }
    if c.Server.Auth.TokenTTL == 0 {
        c.Server.Auth.TokenTTL = 24 * time.Hour
    }
    // ... 更多默认值
}
```

### 2.3 环境变量覆盖

```go
// 支持环境变量覆盖配置
func LoadConfigWithEnv(path string) (*Config, error) {
    cfg, err := LoadConfig(path)
    if err != nil {
        return nil, err
    }

    // 环境变量覆盖
    if clusterID := os.Getenv("METASTORE_CLUSTER_ID"); clusterID != "" {
        cfg.Server.ClusterID, _ = strconv.ParseUint(clusterID, 10, 64)
    }
    if memberID := os.Getenv("METASTORE_MEMBER_ID"); memberID != "" {
        cfg.Server.MemberID, _ = strconv.ParseUint(memberID, 10, 64)
    }
    if listenAddr := os.Getenv("METASTORE_LISTEN_ADDRESS"); listenAddr != "" {
        cfg.Server.ListenAddress = listenAddr
    }

    return cfg, nil
}
```

---

## 三、硬编码总结

### 3.1 硬编码统计

| 优先级 | 数量 | 说明 |
|--------|------|------|
| P0 - 必须修复 | 5 | 影响功能和稳定性 |
| P1 - 建议修复 | 2 | 影响灵活性 |
| P2 - 可选修复 | 1 | 可以改进 |
| P3 - 可接受 | 2 | 无需修改 |
| **总计** | **10** | |

### 3.2 硬编码问题汇总

| 项目 | 位置 | 影响 | 优先级 | 工作量 |
|------|------|------|--------|--------|
| Lease 检查间隔 | lease_manager.go:136 | 高 | P0 | 30m |
| Token 过期时间 | auth_manager.go:188 | 高 | P0 | 30m |
| Token 清理间隔 | auth_manager.go:744 | 中 | P0 | 15m |
| Snapshot 分块 | maintenance.go:168 | 中 | P0 | 30m |
| 默认 ID | server.go:82-86 | 高 | P0 | 1h |
| 耗尽时间 | server.go:201 | 中 | P1 | 1h |
| bcrypt cost | auth_manager.go:732 | 低 | P2 | 15m |
| 版本号 | maintenance.go:96 | 低 | P3 | 30m |

**总工作量**: 约 4-5 小时

### 3.3 改进效果

**优化前**:
- 8 处硬编码值需要修复
- 无统一配置管理
- 难以适应不同环境

**优化后**:
- 所有关键值可配置
- 统一的配置文件
- 支持环境变量覆盖
- 配置验证
- 默认值合理

### 3.4 配置管理最佳实践

1. **配置优先级**:
   ```
   命令行参数 > 环境变量 > 配置文件 > 默认值
   ```

2. **配置验证**:
   - 启动时验证所有配置
   - 提供清晰的错误信息
   - 提供配置示例

3. **配置热重载**（可选）:
   ```go
   // 监听配置文件变化
   watcher, _ := fsnotify.NewWatcher()
   watcher.Add(configPath)

   go func() {
       for {
           select {
           case event := <-watcher.Events:
               if event.Op&fsnotify.Write == fsnotify.Write {
                   newCfg, _ := LoadConfig(configPath)
                   server.ReloadConfig(newCfg)
               }
           }
       }
   }()
   ```

---

## 四、结论

MetaStore 的硬编码情况**总体可控**（92/100），但仍有改进空间。

**核心优势**:
- ✅ 大部分代码逻辑可复用
- ✅ 只有少量关键值硬编码
- ✅ 默认值合理

**主要不足**:
- ⚠️ 5 个 P0 级别硬编码值
- ⚠️ 缺少统一配置管理
- ⚠️ 无环境变量支持

**改进建议**:
1. **短期**（1天）：修复所有 P0 硬编码
2. **中期**（2天）：实现统一配置管理
3. **长期**（1周）：添加配置热重载

完成这些改进后，硬编码评分可提升至 **98/100**。

---

**评估人**: Claude (AI Code Assistant)
**评估日期**: 2025-10-28
