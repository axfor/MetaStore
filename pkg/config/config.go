// Copyright 2025 The axfor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 统一配置结构
type Config struct {
	Server ServerConfig `yaml:"server"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	// 集群配置
	ClusterID     uint64 `yaml:"cluster_id"`
	MemberID      uint64 `yaml:"member_id"`
	ListenAddress string `yaml:"listen_address"`

	// 子配置
	GRPC        GRPCConfig        `yaml:"grpc"`
	Limits      LimitsConfig      `yaml:"limits"`
	Lease       LeaseConfig       `yaml:"lease"`
	Auth        AuthConfig        `yaml:"auth"`
	Maintenance MaintenanceConfig `yaml:"maintenance"`
	Reliability ReliabilityConfig `yaml:"reliability"`
	Log         LogConfig         `yaml:"log"`
	Monitoring  MonitoringConfig  `yaml:"monitoring"`
}

// GRPCConfig gRPC 配置
type GRPCConfig struct {
	// 消息大小限制
	MaxRecvMsgSize        int           `yaml:"max_recv_msg_size"`         // 默认 1.5MB
	MaxSendMsgSize        int           `yaml:"max_send_msg_size"`         // 默认 1.5MB
	MaxConcurrentStreams  uint32        `yaml:"max_concurrent_streams"`    // 默认 1000

	// 流控制窗口
	InitialWindowSize     int32         `yaml:"initial_window_size"`       // 默认 1MB
	InitialConnWindowSize int32         `yaml:"initial_conn_window_size"`  // 默认 1MB

	// Keepalive 配置
	KeepaliveTime         time.Duration `yaml:"keepalive_time"`            // 默认 5s
	KeepaliveTimeout      time.Duration `yaml:"keepalive_timeout"`         // 默认 1s
	MaxConnectionIdle     time.Duration `yaml:"max_connection_idle"`       // 默认 15s
	MaxConnectionAge      time.Duration `yaml:"max_connection_age"`        // 默认 10m
	MaxConnectionAgeGrace time.Duration `yaml:"max_connection_age_grace"`  // 默认 5s

	// 限流配置
	EnableRateLimit       bool          `yaml:"enable_rate_limit"`         // 是否启用限流，默认 false
	RateLimitQPS          int           `yaml:"rate_limit_qps"`            // 每秒请求数限制，默认 0（不限制）
	RateLimitBurst        int           `yaml:"rate_limit_burst"`          // 突发请求令牌桶大小，默认 0（不限制）
}

// LimitsConfig 资源限制配置
type LimitsConfig struct {
	MaxConnections int   `yaml:"max_connections"`  // 默认 1000
	MaxWatchCount  int   `yaml:"max_watch_count"`  // 默认 10000
	MaxLeaseCount  int   `yaml:"max_lease_count"`  // 默认 10000
	MaxRequestSize int64 `yaml:"max_request_size"` // 默认 1.5MB
}

// LeaseConfig Lease 配置
type LeaseConfig struct {
	CheckInterval time.Duration `yaml:"check_interval"` // 默认 1s
	DefaultTTL    time.Duration `yaml:"default_ttl"`    // 默认 60s
}

// AuthConfig 认证配置
type AuthConfig struct {
	TokenTTL             time.Duration `yaml:"token_ttl"`              // 默认 24h
	TokenCleanupInterval time.Duration `yaml:"token_cleanup_interval"` // 默认 5m
	BcryptCost           int           `yaml:"bcrypt_cost"`            // 默认 10
	EnableAudit          bool          `yaml:"enable_audit"`           // 默认 false
}

// MaintenanceConfig 维护配置
type MaintenanceConfig struct {
	SnapshotChunkSize int `yaml:"snapshot_chunk_size"` // 默认 4MB
}

// ReliabilityConfig 可靠性配置
type ReliabilityConfig struct {
	ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`      // 默认 30s
	DrainTimeout        time.Duration `yaml:"drain_timeout"`         // 默认 5s
	EnableCRC           bool          `yaml:"enable_crc"`            // 默认 false
	EnableHealthCheck   bool          `yaml:"enable_health_check"`   // 默认 true
	EnablePanicRecovery bool          `yaml:"enable_panic_recovery"` // 默认 true
}

// LogConfig 日志配置
type LogConfig struct {
	Level            string   `yaml:"level"`              // 默认 info
	Encoding         string   `yaml:"encoding"`           // 默认 json
	OutputPaths      []string `yaml:"output_paths"`       // 默认 ["stdout"]
	ErrorOutputPaths []string `yaml:"error_output_paths"` // 默认 ["stderr"]
}

// MonitoringConfig 监控配置
type MonitoringConfig struct {
	EnablePrometheus     bool          `yaml:"enable_prometheus"`      // 默认 true
	PrometheusPort       int           `yaml:"prometheus_port"`        // 默认 9090
	SlowRequestThreshold time.Duration `yaml:"slow_request_threshold"` // 默认 100ms
}

// LoadConfig 从文件加载配置
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// 设置默认值
	cfg.SetDefaults()

	// 环境变量覆盖
	cfg.OverrideFromEnv()

	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// SetDefaults 设置默认值
func (c *Config) SetDefaults() {
	// Server 默认值
	if c.Server.ListenAddress == "" {
		c.Server.ListenAddress = ":2379"
	}

	// gRPC 默认值
	if c.Server.GRPC.MaxRecvMsgSize == 0 {
		c.Server.GRPC.MaxRecvMsgSize = 1572864 // 1.5MB
	}
	if c.Server.GRPC.MaxSendMsgSize == 0 {
		c.Server.GRPC.MaxSendMsgSize = 1572864 // 1.5MB
	}
	if c.Server.GRPC.MaxConcurrentStreams == 0 {
		c.Server.GRPC.MaxConcurrentStreams = 1000
	}
	if c.Server.GRPC.InitialWindowSize == 0 {
		c.Server.GRPC.InitialWindowSize = 1048576 // 1MB
	}
	if c.Server.GRPC.InitialConnWindowSize == 0 {
		c.Server.GRPC.InitialConnWindowSize = 1048576 // 1MB
	}
	if c.Server.GRPC.KeepaliveTime == 0 {
		c.Server.GRPC.KeepaliveTime = 5 * time.Second
	}
	if c.Server.GRPC.KeepaliveTimeout == 0 {
		c.Server.GRPC.KeepaliveTimeout = 1 * time.Second
	}
	if c.Server.GRPC.MaxConnectionIdle == 0 {
		c.Server.GRPC.MaxConnectionIdle = 15 * time.Second
	}
	if c.Server.GRPC.MaxConnectionAge == 0 {
		c.Server.GRPC.MaxConnectionAge = 10 * time.Minute
	}
	if c.Server.GRPC.MaxConnectionAgeGrace == 0 {
		c.Server.GRPC.MaxConnectionAgeGrace = 5 * time.Second
	}

	// Limits 默认值
	if c.Server.Limits.MaxConnections == 0 {
		c.Server.Limits.MaxConnections = 1000
	}
	if c.Server.Limits.MaxWatchCount == 0 {
		c.Server.Limits.MaxWatchCount = 10000
	}
	if c.Server.Limits.MaxLeaseCount == 0 {
		c.Server.Limits.MaxLeaseCount = 10000
	}
	if c.Server.Limits.MaxRequestSize == 0 {
		c.Server.Limits.MaxRequestSize = 1572864 // 1.5MB
	}

	// Lease 默认值
	if c.Server.Lease.CheckInterval == 0 {
		c.Server.Lease.CheckInterval = 1 * time.Second
	}
	if c.Server.Lease.DefaultTTL == 0 {
		c.Server.Lease.DefaultTTL = 60 * time.Second
	}

	// Auth 默认值
	if c.Server.Auth.TokenTTL == 0 {
		c.Server.Auth.TokenTTL = 24 * time.Hour
	}
	if c.Server.Auth.TokenCleanupInterval == 0 {
		c.Server.Auth.TokenCleanupInterval = 5 * time.Minute
	}
	if c.Server.Auth.BcryptCost == 0 {
		c.Server.Auth.BcryptCost = 10
	}

	// Maintenance 默认值
	if c.Server.Maintenance.SnapshotChunkSize == 0 {
		c.Server.Maintenance.SnapshotChunkSize = 4 * 1024 * 1024 // 4MB
	}

	// Reliability 默认值
	if c.Server.Reliability.ShutdownTimeout == 0 {
		c.Server.Reliability.ShutdownTimeout = 30 * time.Second
	}
	if c.Server.Reliability.DrainTimeout == 0 {
		c.Server.Reliability.DrainTimeout = 5 * time.Second
	}
	// EnableHealthCheck 和 EnablePanicRecovery 默认为 true
	if !c.Server.Reliability.EnableHealthCheck {
		c.Server.Reliability.EnableHealthCheck = true
	}
	if !c.Server.Reliability.EnablePanicRecovery {
		c.Server.Reliability.EnablePanicRecovery = true
	}

	// Log 默认值
	if c.Server.Log.Level == "" {
		c.Server.Log.Level = "info"
	}
	if c.Server.Log.Encoding == "" {
		c.Server.Log.Encoding = "json"
	}
	if len(c.Server.Log.OutputPaths) == 0 {
		c.Server.Log.OutputPaths = []string{"stdout"}
	}
	if len(c.Server.Log.ErrorOutputPaths) == 0 {
		c.Server.Log.ErrorOutputPaths = []string{"stderr"}
	}

	// Monitoring 默认值
	if !c.Server.Monitoring.EnablePrometheus {
		c.Server.Monitoring.EnablePrometheus = true
	}
	if c.Server.Monitoring.PrometheusPort == 0 {
		c.Server.Monitoring.PrometheusPort = 9090
	}
	if c.Server.Monitoring.SlowRequestThreshold == 0 {
		c.Server.Monitoring.SlowRequestThreshold = 100 * time.Millisecond
	}
}

// OverrideFromEnv 从环境变量覆盖配置
func (c *Config) OverrideFromEnv() {
	// 集群配置
	if clusterID := os.Getenv("METASTORE_CLUSTER_ID"); clusterID != "" {
		if id, err := strconv.ParseUint(clusterID, 10, 64); err == nil {
			c.Server.ClusterID = id
		}
	}
	if memberID := os.Getenv("METASTORE_MEMBER_ID"); memberID != "" {
		if id, err := strconv.ParseUint(memberID, 10, 64); err == nil {
			c.Server.MemberID = id
		}
	}
	if listenAddr := os.Getenv("METASTORE_LISTEN_ADDRESS"); listenAddr != "" {
		c.Server.ListenAddress = listenAddr
	}

	// 日志配置
	if logLevel := os.Getenv("METASTORE_LOG_LEVEL"); logLevel != "" {
		c.Server.Log.Level = logLevel
	}
	if logEncoding := os.Getenv("METASTORE_LOG_ENCODING"); logEncoding != "" {
		c.Server.Log.Encoding = logEncoding
	}
}

// Validate 验证配置
func (c *Config) Validate() error {
	// 验证集群 ID 和成员 ID 必须指定
	if c.Server.ClusterID == 0 {
		return fmt.Errorf("cluster_id is required and must be non-zero")
	}
	if c.Server.MemberID == 0 {
		return fmt.Errorf("member_id is required and must be non-zero")
	}

	// 验证监听地址
	if c.Server.ListenAddress == "" {
		return fmt.Errorf("listen_address is required")
	}

	// 验证 gRPC 配置
	if c.Server.GRPC.MaxRecvMsgSize < 0 {
		return fmt.Errorf("grpc.max_recv_msg_size must be >= 0")
	}
	if c.Server.GRPC.MaxSendMsgSize < 0 {
		return fmt.Errorf("grpc.max_send_msg_size must be >= 0")
	}

	// 验证资源限制
	if c.Server.Limits.MaxConnections <= 0 {
		return fmt.Errorf("limits.max_connections must be > 0")
	}
	if c.Server.Limits.MaxWatchCount <= 0 {
		return fmt.Errorf("limits.max_watch_count must be > 0")
	}
	if c.Server.Limits.MaxLeaseCount <= 0 {
		return fmt.Errorf("limits.max_lease_count must be > 0")
	}

	// 验证 Lease 配置
	if c.Server.Lease.CheckInterval <= 0 {
		return fmt.Errorf("lease.check_interval must be > 0")
	}

	// 验证 Auth 配置
	if c.Server.Auth.TokenTTL <= 0 {
		return fmt.Errorf("auth.token_ttl must be > 0")
	}
	if c.Server.Auth.BcryptCost < 4 || c.Server.Auth.BcryptCost > 31 {
		return fmt.Errorf("auth.bcrypt_cost must be between 4 and 31")
	}

	// 验证 Maintenance 配置
	if c.Server.Maintenance.SnapshotChunkSize <= 0 {
		return fmt.Errorf("maintenance.snapshot_chunk_size must be > 0")
	}

	// 验证日志级别
	validLogLevels := map[string]bool{
		"debug": true, "info": true, "warn": true,
		"error": true, "dpanic": true, "panic": true, "fatal": true,
	}
	if !validLogLevels[c.Server.Log.Level] {
		return fmt.Errorf("log.level must be one of: debug, info, warn, error, dpanic, panic, fatal")
	}

	// 验证日志编码
	if c.Server.Log.Encoding != "json" && c.Server.Log.Encoding != "console" {
		return fmt.Errorf("log.encoding must be either 'json' or 'console'")
	}

	return nil
}
