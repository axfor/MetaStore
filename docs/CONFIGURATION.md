# MetaStore Configuration Guide

MetaStore 支持灵活的配置系统，允许你通过配置文件、命令行参数或环境变量来配置服务器。

## 配置方式

### 1. 使用默认配置（推荐用于快速开始）

不提供配置文件时，MetaStore 会使用生产就绪的默认配置：

```bash
./metastore --member-id=1 --cluster-id=1
```

默认配置包括：
- gRPC 消息大小限制：1.5MB
- 最大并发流：1000
- Keepalive 时间：5s
- 资源限制：1000 个连接，10000 个 watch，10000 个 lease
- 优雅关闭超时：30s
- 启用健康检查和 panic 恢复
- 日志级别：info

### 2. 使用配置文件

创建一个 YAML 配置文件并通过 `--config` 参数指定：

```bash
./metastore --config=configs/example.yaml
```

配置文件示例参见 [configs/example.yaml](../configs/example.yaml)

### 3. 环境变量覆盖（优先级最高）

环境变量可以覆盖配置文件中的值：

```bash
export METASTORE_CLUSTER_ID=100
export METASTORE_MEMBER_ID=1
export METASTORE_LISTEN_ADDRESS=":12379"
export METASTORE_LOG_LEVEL=debug
export METASTORE_LOG_ENCODING=console

./metastore --config=configs/example.yaml
```

支持的环境变量：
- `METASTORE_CLUSTER_ID` - 集群 ID
- `METASTORE_MEMBER_ID` - 成员 ID
- `METASTORE_LISTEN_ADDRESS` - 监听地址
- `METASTORE_LOG_LEVEL` - 日志级别
- `METASTORE_LOG_ENCODING` - 日志编码格式

## 配置优先级

配置值的优先级从高到低：

1. **环境变量** - 最高优先级
2. **配置文件** - 如果提供了 `--config` 参数
3. **命令行参数** - 仅用于基本参数（cluster, member-id 等）
4. **默认值** - 生产就绪的推荐默认值

## 配置项说明

### Server 配置

```yaml
server:
  cluster_id: 1           # 集群 ID（必需，必须非零）
  member_id: 1            # 成员 ID（必需，必须非零）
  listen_address: ":2379" # gRPC 监听地址（必需）
```

### gRPC 配置

```yaml
server:
  grpc:
    # 消息大小限制 (字节)
    max_recv_msg_size: 1572864      # 接收消息最大大小 (默认 1.5MB)
    max_send_msg_size: 1572864      # 发送消息最大大小 (默认 1.5MB)
    max_concurrent_streams: 1000    # 最大并发流数量 (默认 1000)

    # 流控制窗口大小 (字节)
    initial_window_size: 1048576        # 初始流窗口大小 (默认 1MB)
    initial_conn_window_size: 1048576   # 初始连接窗口大小 (默认 1MB)

    # Keepalive 配置
    keepalive_time: 5s              # Ping 发送间隔 (默认 5s)
    keepalive_timeout: 1s           # Ping 响应超时 (默认 1s)
    max_connection_idle: 15s        # 最大空闲连接时间 (默认 15s)
    max_connection_age: 10m         # 连接最大存活时间 (默认 10m)
    max_connection_age_grace: 5s    # 连接关闭宽限期 (默认 5s)

    # 限流配置
    enable_rate_limit: false        # 是否启用限流 (默认 false)
    rate_limit_qps: 0               # 每秒请求数限制 (默认 0，不限制)
    rate_limit_burst: 0             # 突发请求令牌桶大小 (默认 0，不限制)
```

### 资源限制配置

```yaml
server:
  limits:
    max_connections: 1000     # 最大并发连接数 (默认 1000)
    max_watch_count: 10000    # 最大 watch 数量 (默认 10000)
    max_lease_count: 10000    # 最大 lease 数量 (默认 10000)
    max_request_size: 1572864 # 最大请求大小 (默认 1.5MB)
```

### Lease 配置

```yaml
server:
  lease:
    check_interval: 1s  # Lease 过期检查间隔 (默认 1s)
    default_ttl: 60s    # 默认 TTL (默认 60s)
```

### 认证配置

```yaml
server:
  auth:
    token_ttl: 24h                  # Token 有效期 (默认 24h)
    token_cleanup_interval: 5m      # Token 清理间隔 (默认 5m)
    bcrypt_cost: 10                 # Bcrypt 加密成本 (默认 10，范围 4-31)
    enable_audit: false             # 是否启用审计日志 (默认 false)
```

### 维护配置

```yaml
server:
  maintenance:
    snapshot_chunk_size: 4194304  # 快照分块大小 (默认 4MB)
```

### 可靠性配置

```yaml
server:
  reliability:
    shutdown_timeout: 30s         # 关闭超时时间 (默认 30s)
    drain_timeout: 5s             # 连接排空超时时间 (默认 5s)
    enable_crc: false             # 是否启用 CRC 校验 (默认 false)
    enable_health_check: true     # 是否启用健康检查 (默认 true)
    enable_panic_recovery: true   # 是否启用 panic 恢复 (默认 true)
```

### 日志配置

```yaml
server:
  log:
    level: "info"           # 日志级别: debug, info, warn, error, dpanic, panic, fatal
    encoding: "json"        # 编码格式: json 或 console
    output_paths:
      - "stdout"            # 输出路径（支持文件路径）
    error_output_paths:
      - "stderr"            # 错误输出路径
```

### 监控配置

```yaml
server:
  monitoring:
    enable_prometheus: true         # 是否启用 Prometheus 指标 (默认 true)
    prometheus_port: 9090           # Prometheus 指标端口 (默认 9090)
    slow_request_threshold: 100ms   # 慢请求阈值 (默认 100ms)
```

## 使用场景

### 场景 1: 开发环境（使用默认配置）

```bash
# 使用默认配置快速启动
./metastore --member-id=1 --cluster-id=1 --storage=memory
```

### 场景 2: 生产环境（使用配置文件）

```bash
# 创建生产配置文件 prod.yaml
cat > prod.yaml << EOF
server:
  cluster_id: 100
  member_id: 1
  listen_address: ":2379"

  grpc:
    max_recv_msg_size: 10485760  # 10MB for large requests
    max_send_msg_size: 10485760
    max_concurrent_streams: 5000

  limits:
    max_connections: 5000
    max_watch_count: 50000
    max_lease_count: 50000

  log:
    level: "warn"
    encoding: "json"
    output_paths:
      - "/var/log/metastore/server.log"
EOF

# 使用配置文件启动
./metastore --config=prod.yaml --storage=rocksdb
```

### 场景 3: 调试环境（环境变量覆盖）

```bash
# 临时调整日志级别进行调试
export METASTORE_LOG_LEVEL=debug
export METASTORE_LOG_ENCODING=console

./metastore --config=prod.yaml
```

## 配置验证

MetaStore 在启动时会自动验证配置：

1. **必需字段检查** - cluster_id 和 member_id 必须非零
2. **范围验证** - 例如 bcrypt_cost 必须在 4-31 之间
3. **格式验证** - 例如 log.level 必须是有效的日志级别

如果配置无效，服务器将拒绝启动并输出错误信息。

## 最佳实践

1. **生产环境使用配置文件** - 将所有配置集中管理，便于版本控制和审计
2. **使用环境变量处理敏感信息** - 例如密钥、密码等不要写入配置文件
3. **定期审查配置** - 根据实际负载调整资源限制和性能参数
4. **使用默认值** - 除非有特殊需求，推荐使用默认值开始
5. **分环境配置** - 为开发、测试、生产维护不同的配置文件

## 故障排查

### 问题：服务器无法启动

检查配置验证错误：

```bash
./metastore --config=myconfig.yaml 2>&1 | grep "invalid config"
```

### 问题：配置未生效

检查配置优先级，环境变量可能覆盖了配置文件的值：

```bash
env | grep METASTORE
```

### 问题：性能不佳

调整以下参数：

1. 增大 gRPC 消息大小限制
2. 增加并发流数量
3. 调整 Keepalive 参数
4. 增加资源限制

## 参考

- 完整配置示例：[configs/example.yaml](../configs/example.yaml)
- 配置结构定义：[pkg/config/config.go](../pkg/config/config.go)
- 默认配置值：参见 `DefaultConfig()` 函数
