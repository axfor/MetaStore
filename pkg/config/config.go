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

// Config unified configuration structure
type Config struct {
	Server ServerConfig `yaml:"server"`
}

// ServerConfig server configuration
type ServerConfig struct {
	// Cluster configuration
	ClusterID     uint64 `yaml:"cluster_id"`
	MemberID      uint64 `yaml:"member_id"`
	ListenAddress string `yaml:"listen_address"`

	// Sub-configurations
	GRPC        GRPCConfig        `yaml:"grpc"`
	Limits      LimitsConfig      `yaml:"limits"`
	Lease       LeaseConfig       `yaml:"lease"`
	Auth        AuthConfig        `yaml:"auth"`
	Maintenance MaintenanceConfig `yaml:"maintenance"`
	Reliability ReliabilityConfig `yaml:"reliability"`
	Log         LogConfig         `yaml:"log"`
	Monitoring  MonitoringConfig  `yaml:"monitoring"`
	Performance PerformanceConfig `yaml:"performance"`
	Raft        RaftConfig        `yaml:"raft"`
	RocksDB     RocksDBConfig     `yaml:"rocksdb"`
}

// GRPCConfig gRPC configuration
type GRPCConfig struct {
	// Message size limits
	MaxRecvMsgSize        int           `yaml:"max_recv_msg_size"`         // Default 1.5MB
	MaxSendMsgSize        int           `yaml:"max_send_msg_size"`         // Default 1.5MB
	MaxConcurrentStreams  uint32        `yaml:"max_concurrent_streams"`    // Default 1000

	// Flow control window
	InitialWindowSize     int32         `yaml:"initial_window_size"`       // Default 1MB
	InitialConnWindowSize int32         `yaml:"initial_conn_window_size"`  // Default 1MB

	// Keepalive configuration
	KeepaliveTime         time.Duration `yaml:"keepalive_time"`            // Default 5s
	KeepaliveTimeout      time.Duration `yaml:"keepalive_timeout"`         // Default 1s
	MaxConnectionIdle     time.Duration `yaml:"max_connection_idle"`       // Default 15s
	MaxConnectionAge      time.Duration `yaml:"max_connection_age"`        // Default 10m
	MaxConnectionAgeGrace time.Duration `yaml:"max_connection_age_grace"`  // Default 5s

	// Rate limiting configuration
	EnableRateLimit       bool          `yaml:"enable_rate_limit"`         // Whether to enable rate limiting, default false
	RateLimitQPS          int           `yaml:"rate_limit_qps"`            // Requests per second limit, default 0 (no limit)
	RateLimitBurst        int           `yaml:"rate_limit_burst"`          // Burst request token bucket size, default 0 (no limit)
}

// LimitsConfig resource limits configuration
type LimitsConfig struct {
	MaxConnections int   `yaml:"max_connections"`  // Default 1000
	MaxWatchCount  int   `yaml:"max_watch_count"`  // Default 10000
	MaxLeaseCount  int   `yaml:"max_lease_count"`  // Default 10000
	MaxRequestSize int64 `yaml:"max_request_size"` // Default 1.5MB
	MaxMemoryMB    int64 `yaml:"max_memory_mb"`    // Max memory usage (MB), default 8192 (8GB), 0 means no limit
	MaxRequests    int64 `yaml:"max_requests"`     // Max concurrent requests, default 5000
}

// LeaseConfig lease configuration
type LeaseConfig struct {
	CheckInterval time.Duration `yaml:"check_interval"` // Default 1s
	DefaultTTL    time.Duration `yaml:"default_ttl"`    // Default 60s
}

// AuthConfig authentication configuration
type AuthConfig struct {
	TokenTTL             time.Duration `yaml:"token_ttl"`              // Default 24h
	TokenCleanupInterval time.Duration `yaml:"token_cleanup_interval"` // Default 5m
	BcryptCost           int           `yaml:"bcrypt_cost"`            // Default 10
	EnableAudit          bool          `yaml:"enable_audit"`           // Default false
}

// MaintenanceConfig maintenance configuration
type MaintenanceConfig struct {
	SnapshotChunkSize int `yaml:"snapshot_chunk_size"` // Default 4MB
}

// ReliabilityConfig reliability configuration
type ReliabilityConfig struct {
	ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`      // Default 30s
	DrainTimeout        time.Duration `yaml:"drain_timeout"`         // Default 5s
	EnableCRC           bool          `yaml:"enable_crc"`            // Default false
	EnableHealthCheck   bool          `yaml:"enable_health_check"`   // Default true
	EnablePanicRecovery bool          `yaml:"enable_panic_recovery"` // Default true
}

// LogConfig log configuration
type LogConfig struct {
	Level            string   `yaml:"level"`              // Default info
	Encoding         string   `yaml:"encoding"`           // Default json
	OutputPaths      []string `yaml:"output_paths"`       // Default ["stdout"]
	ErrorOutputPaths []string `yaml:"error_output_paths"` // Default ["stderr"]
}

// MonitoringConfig monitoring configuration
type MonitoringConfig struct {
	EnablePrometheus     bool          `yaml:"enable_prometheus"`      // Default true
	PrometheusPort       int           `yaml:"prometheus_port"`        // Default 9090
	SlowRequestThreshold time.Duration `yaml:"slow_request_threshold"` // Default 100ms
}

// PerformanceConfig performance optimization configuration
type PerformanceConfig struct {
	EnableProtobuf         bool `yaml:"enable_protobuf"`          // Raft operations Protobuf serialization, default true
	EnableSnapshotProtobuf bool `yaml:"enable_snapshot_protobuf"` // Snapshot Protobuf serialization, default true
	EnableLeaseProtobuf    bool `yaml:"enable_lease_protobuf"`    // Lease Protobuf serialization, default true
}

// RaftConfig Raft consensus configuration
type RaftConfig struct {
	// Tick configuration (affects Raft processing speed)
	TickInterval  time.Duration `yaml:"tick_interval"`   // Raft tick interval, default 100ms
	ElectionTick  int           `yaml:"election_tick"`   // Election timeout tick count, default 10 (= 1s)
	HeartbeatTick int           `yaml:"heartbeat_tick"`  // Heartbeat interval tick count, default 1 (= 100ms)

	// Message size configuration
	MaxSizePerMsg uint64 `yaml:"max_size_per_msg"` // Maximum size per message, default 4MB

	// Flow control configuration (affects throughput)
	MaxInflightMsgs           int    `yaml:"max_inflight_msgs"`             // Maximum inflight messages, default 512
	MaxUncommittedEntriesSize uint64 `yaml:"max_uncommitted_entries_size"`  // Maximum uncommitted entries size, default 1GB

	// Optimization switches
	PreVote     bool `yaml:"pre_vote"`      // Enable PreVote, default true
	CheckQuorum bool `yaml:"check_quorum"`  // Enable CheckQuorum, default true

	// Batch proposal configuration (dynamic batch optimization, reference: TiKV)
	Batch RaftBatchConfig `yaml:"batch"` // Batch proposal configuration

	// Lease Read configuration (read performance optimization, reference: etcd/TiKV)
	LeaseRead LeaseReadConfig `yaml:"lease_read"` // Lease Read configuration
}

// RaftBatchConfig batch proposal configuration
// Dynamic batch proposal system that adaptively adjusts batch size and timeout based on load
// Low load: small batch + short timeout = low latency
// High load: large batch + long timeout = high throughput
type RaftBatchConfig struct {
	Enable        bool          `yaml:"enable"`          // Whether to enable batch proposals, default true
	MinBatchSize  int           `yaml:"min_batch_size"`  // Minimum batch size (low load), default 1
	MaxBatchSize  int           `yaml:"max_batch_size"`  // Maximum batch size (high load), default 256
	MinTimeout    time.Duration `yaml:"min_timeout"`     // Minimum timeout (low load), default 5ms
	MaxTimeout    time.Duration `yaml:"max_timeout"`     // Maximum timeout (high load), default 20ms
	LoadThreshold float64       `yaml:"load_threshold"`  // Load threshold (0.0-1.0), default 0.7
}

// LeaseReadConfig Lease Read configuration
// Lease Read optimization allows Leader to serve read requests directly during lease period without Raft consensus
// Performance improvement: 10-100x (read operations), especially suitable for read-heavy scenarios
// Lease Duration calculation: min(electionTimeout/2, heartbeatTick*3) - clockDrift
type LeaseReadConfig struct {
	Enable      bool          `yaml:"enable"`       // Whether to enable Lease Read, default true
	ClockDrift  time.Duration `yaml:"clock_drift"`  // Clock drift tolerance, default 100ms (same datacenter)
	                                                 // Cross-region deployment recommendation: 200ms; Cross-continent: 500ms
	ReadTimeout time.Duration `yaml:"read_timeout"` // Read timeout, default 5s
}

// RocksDBConfig RocksDB performance configuration
type RocksDBConfig struct {
	// Block Cache configuration (affects read performance)
	BlockCacheSize uint64 `yaml:"block_cache_size"` // Default 256MB

	// Write Buffer configuration (affects write performance)
	WriteBufferSize           uint64 `yaml:"write_buffer_size"`            // Default 64MB
	MaxWriteBufferNumber      int    `yaml:"max_write_buffer_number"`      // Default 3
	MinWriteBufferNumberToMerge int  `yaml:"min_write_buffer_number_to_merge"` // Default 1

	// Compaction configuration
	MaxBackgroundJobs              int `yaml:"max_background_jobs"`                // Default 4
	Level0FileNumCompactionTrigger int `yaml:"level0_file_num_compaction_trigger"` // Default 4
	Level0SlowdownWritesTrigger    int `yaml:"level0_slowdown_writes_trigger"`     // Default 20
	Level0StopWritesTrigger        int `yaml:"level0_stop_writes_trigger"`         // Default 36

	// Bloom Filter configuration
	BloomFilterBitsPerKey      int  `yaml:"bloom_filter_bits_per_key"`       // Default 10
	BlockBasedTableBloomFilter bool `yaml:"block_based_table_bloom_filter"`  // Default true

	// Other optimizations
	MaxOpenFiles  int    `yaml:"max_open_files"`   // Default 10000
	UseFsync      bool   `yaml:"use_fsync"`        // Default false (use fdatasync)
	BytesPerSync  uint64 `yaml:"bytes_per_sync"`   // Default 1MB
}

// DefaultConfig returns a configuration with recommended default values
// Use this function to get production-ready defaults when no config file is provided
func DefaultConfig(clusterID, memberID uint64, listenAddress string) *Config {
	cfg := &Config{
		Server: ServerConfig{
			ClusterID:     clusterID,
			MemberID:      memberID,
			ListenAddress: listenAddress,
		},
	}

	// Set all default values
	cfg.SetDefaults()

	return cfg
}

// LoadConfig loads configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set default values
	cfg.SetDefaults()

	// Override from environment variables
	cfg.OverrideFromEnv()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// LoadConfigOrDefault attempts to load configuration from file, uses defaults if file doesn't exist
// This allows users to run with recommended defaults when no config file is provided
func LoadConfigOrDefault(path string, clusterID, memberID uint64, listenAddress string) (*Config, error) {
	// Try loading config file
	if path != "" {
		cfg, err := LoadConfig(path)
		if err == nil {
			return cfg, nil
		}
		// If file doesn't exist, use default config
		if !os.IsNotExist(err) {
			return nil, err // File exists but has other error, return error
		}
	}

	// Use default configuration
	cfg := DefaultConfig(clusterID, memberID, listenAddress)

	// Override from environment variables
	cfg.OverrideFromEnv()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// SetDefaults sets default values
func (c *Config) SetDefaults() {
	// Server defaults
	if c.Server.ListenAddress == "" {
		c.Server.ListenAddress = ":2379"
	}

	// gRPC defaults (based on industry best practices: etcd, gRPC official, TiKV)
	if c.Server.GRPC.MaxRecvMsgSize == 0 {
		c.Server.GRPC.MaxRecvMsgSize = 4194304 // 4MB (aligned with Raft MaxSizePerMsg)
	}
	if c.Server.GRPC.MaxSendMsgSize == 0 {
		c.Server.GRPC.MaxSendMsgSize = 4194304 // 4MB
	}
	if c.Server.GRPC.MaxConcurrentStreams == 0 {
		c.Server.GRPC.MaxConcurrentStreams = 2048 // Support more concurrent Watch/Stream (TiKV uses 1024-2048)
	}
	if c.Server.GRPC.InitialWindowSize == 0 {
		c.Server.GRPC.InitialWindowSize = 8388608 // 8MB (high throughput scenario, TiKV recommends 2-8MB)
	}
	if c.Server.GRPC.InitialConnWindowSize == 0 {
		c.Server.GRPC.InitialConnWindowSize = 16777216 // 16MB (connection-level flow control, gRPC official recommendation)
	}
	if c.Server.GRPC.KeepaliveTime == 0 {
		c.Server.GRPC.KeepaliveTime = 10 * time.Second // Fast connection health detection (TiKV uses 10s)
	}
	if c.Server.GRPC.KeepaliveTimeout == 0 {
		c.Server.GRPC.KeepaliveTimeout = 10 * time.Second // Fast failure detection
	}
	if c.Server.GRPC.MaxConnectionIdle == 0 {
		c.Server.GRPC.MaxConnectionIdle = 300 * time.Second // 5 minutes, avoid frequent reconnection
	}
	if c.Server.GRPC.MaxConnectionAge == 0 {
		c.Server.GRPC.MaxConnectionAge = 10 * time.Minute // 10 minutes connection lifetime
	}
	if c.Server.GRPC.MaxConnectionAgeGrace == 0 {
		c.Server.GRPC.MaxConnectionAgeGrace = 10 * time.Second // Fast cleanup
	}

	// Limits defaults
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
	if c.Server.Limits.MaxMemoryMB == 0 {
		c.Server.Limits.MaxMemoryMB = 8192 // 8GB, suitable for performance tests
	}
	if c.Server.Limits.MaxRequests == 0 {
		c.Server.Limits.MaxRequests = 5000
	}

	// Lease defaults
	if c.Server.Lease.CheckInterval == 0 {
		c.Server.Lease.CheckInterval = 1 * time.Second
	}
	if c.Server.Lease.DefaultTTL == 0 {
		c.Server.Lease.DefaultTTL = 60 * time.Second
	}

	// Auth defaults
	if c.Server.Auth.TokenTTL == 0 {
		c.Server.Auth.TokenTTL = 24 * time.Hour
	}
	if c.Server.Auth.TokenCleanupInterval == 0 {
		c.Server.Auth.TokenCleanupInterval = 5 * time.Minute
	}
	if c.Server.Auth.BcryptCost == 0 {
		c.Server.Auth.BcryptCost = 10
	}

	// Maintenance defaults
	if c.Server.Maintenance.SnapshotChunkSize == 0 {
		c.Server.Maintenance.SnapshotChunkSize = 4 * 1024 * 1024 // 4MB
	}

	// Reliability defaults
	if c.Server.Reliability.ShutdownTimeout == 0 {
		c.Server.Reliability.ShutdownTimeout = 30 * time.Second
	}
	if c.Server.Reliability.DrainTimeout == 0 {
		c.Server.Reliability.DrainTimeout = 5 * time.Second
	}
	// EnableHealthCheck and EnablePanicRecovery default to true
	if !c.Server.Reliability.EnableHealthCheck {
		c.Server.Reliability.EnableHealthCheck = true
	}
	if !c.Server.Reliability.EnablePanicRecovery {
		c.Server.Reliability.EnablePanicRecovery = true
	}

	// Log defaults
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

	// Monitoring defaults
	if !c.Server.Monitoring.EnablePrometheus {
		c.Server.Monitoring.EnablePrometheus = true
	}
	if c.Server.Monitoring.PrometheusPort == 0 {
		c.Server.Monitoring.PrometheusPort = 9090
	}
	if c.Server.Monitoring.SlowRequestThreshold == 0 {
		c.Server.Monitoring.SlowRequestThreshold = 100 * time.Millisecond
	}

	// Performance defaults (all Protobuf optimizations enabled by default)
	// If not explicitly set in config, enable all optimizations
	c.Server.Performance.EnableProtobuf = true          // Raft operations Protobuf (3-5x improvement)
	c.Server.Performance.EnableSnapshotProtobuf = true  // Snapshot Protobuf (1.69x improvement)
	c.Server.Performance.EnableLeaseProtobuf = true     // Lease Protobuf (20.6x improvement)

	// Raft defaults (production standard config, industry best practices)
	if c.Server.Raft.TickInterval == 0 {
		c.Server.Raft.TickInterval = 100 * time.Millisecond // Standard production: 100ms (etcd default)
	}
	if c.Server.Raft.ElectionTick == 0 {
		c.Server.Raft.ElectionTick = 10 // 1000ms election timeout (10 × 100ms, industry recommended 1-3s)
	}
	if c.Server.Raft.HeartbeatTick == 0 {
		c.Server.Raft.HeartbeatTick = 1 // 100ms heartbeat interval (1 × 100ms, election_timeout/10)
	}
	if c.Server.Raft.MaxSizePerMsg == 0 {
		c.Server.Raft.MaxSizePerMsg = 4 * 1024 * 1024 // 4MB, aligned with gRPC MaxRecvMsgSize
	}
	if c.Server.Raft.MaxInflightMsgs == 0 {
		c.Server.Raft.MaxInflightMsgs = 1024 // High throughput: 1024 (2x improvement over etcd default 512)
	}
	if c.Server.Raft.MaxUncommittedEntriesSize == 0 {
		c.Server.Raft.MaxUncommittedEntriesSize = 1 << 30 // 1GB
	}
	// PreVote and CheckQuorum enabled by default
	c.Server.Raft.PreVote = true
	c.Server.Raft.CheckQuorum = true

	// Batch proposal defaults (dynamic batch optimization, reference: TiKV)
	// Enable batch proposals by default to achieve 5-50x performance improvement
	c.Server.Raft.Batch.Enable = true
	if c.Server.Raft.Batch.MinBatchSize == 0 {
		c.Server.Raft.Batch.MinBatchSize = 1 // Low load: single proposal, lowest latency
	}
	if c.Server.Raft.Batch.MaxBatchSize == 0 {
		c.Server.Raft.Batch.MaxBatchSize = 256 // High load: large batch, highest throughput (TiKV uses 256)
	}
	if c.Server.Raft.Batch.MinTimeout == 0 {
		c.Server.Raft.Batch.MinTimeout = 5 * time.Millisecond // Low load: 5ms timeout
	}
	if c.Server.Raft.Batch.MaxTimeout == 0 {
		c.Server.Raft.Batch.MaxTimeout = 20 * time.Millisecond // High load: 20ms timeout
	}
	if c.Server.Raft.Batch.LoadThreshold == 0 {
		c.Server.Raft.Batch.LoadThreshold = 0.7 // 70% load threshold
	}

	// LeaseRead defaults (read performance optimization, reference: etcd/TiKV)
	// Enable Lease Read by default to achieve 10-100x read performance improvement
	c.Server.Raft.LeaseRead.Enable = true
	if c.Server.Raft.LeaseRead.ClockDrift == 0 {
		c.Server.Raft.LeaseRead.ClockDrift = 100 * time.Millisecond // etcd recommended value (same datacenter)
		// Notes:
		// - Same datacenter: 100ms (recommended)
		// - Cross-region deployment: 200ms
		// - Cross-continent deployment: 500ms (requires increasing election_timeout)
		// - Based on current config: lease duration = min(1000/2, 100*3) - 100 = 400ms
	}
	if c.Server.Raft.LeaseRead.ReadTimeout == 0 {
		c.Server.Raft.LeaseRead.ReadTimeout = 5 * time.Second // Read timeout 5 seconds
	}

	// RocksDB defaults (based on RocksDB official recommendations)
	if c.Server.RocksDB.BlockCacheSize == 0 {
		c.Server.RocksDB.BlockCacheSize = 268435456 // 256MB
	}
	if c.Server.RocksDB.WriteBufferSize == 0 {
		c.Server.RocksDB.WriteBufferSize = 67108864 // 64MB
	}
	if c.Server.RocksDB.MaxWriteBufferNumber == 0 {
		c.Server.RocksDB.MaxWriteBufferNumber = 3
	}
	if c.Server.RocksDB.MinWriteBufferNumberToMerge == 0 {
		c.Server.RocksDB.MinWriteBufferNumberToMerge = 1
	}
	if c.Server.RocksDB.MaxBackgroundJobs == 0 {
		c.Server.RocksDB.MaxBackgroundJobs = 4
	}
	if c.Server.RocksDB.Level0FileNumCompactionTrigger == 0 {
		c.Server.RocksDB.Level0FileNumCompactionTrigger = 4
	}
	if c.Server.RocksDB.Level0SlowdownWritesTrigger == 0 {
		c.Server.RocksDB.Level0SlowdownWritesTrigger = 20
	}
	if c.Server.RocksDB.Level0StopWritesTrigger == 0 {
		c.Server.RocksDB.Level0StopWritesTrigger = 36
	}
	if c.Server.RocksDB.BloomFilterBitsPerKey == 0 {
		c.Server.RocksDB.BloomFilterBitsPerKey = 10
	}
	if !c.Server.RocksDB.BlockBasedTableBloomFilter {
		c.Server.RocksDB.BlockBasedTableBloomFilter = true
	}
	if c.Server.RocksDB.MaxOpenFiles == 0 {
		c.Server.RocksDB.MaxOpenFiles = 10000
	}
	if c.Server.RocksDB.BytesPerSync == 0 {
		c.Server.RocksDB.BytesPerSync = 1048576 // 1MB
	}
	// UseFsync defaults to false (no need to set)
}

// OverrideFromEnv overrides configuration from environment variables
func (c *Config) OverrideFromEnv() {
	// Cluster configuration
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

	// Log configuration
	if logLevel := os.Getenv("METASTORE_LOG_LEVEL"); logLevel != "" {
		c.Server.Log.Level = logLevel
	}
	if logEncoding := os.Getenv("METASTORE_LOG_ENCODING"); logEncoding != "" {
		c.Server.Log.Encoding = logEncoding
	}
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Validate cluster ID and member ID must be specified
	if c.Server.ClusterID == 0 {
		return fmt.Errorf("cluster_id is required and must be non-zero")
	}
	if c.Server.MemberID == 0 {
		return fmt.Errorf("member_id is required and must be non-zero")
	}

	// Validate listen address
	if c.Server.ListenAddress == "" {
		return fmt.Errorf("listen_address is required")
	}

	// Validate gRPC configuration
	if c.Server.GRPC.MaxRecvMsgSize < 0 {
		return fmt.Errorf("grpc.max_recv_msg_size must be >= 0")
	}
	if c.Server.GRPC.MaxSendMsgSize < 0 {
		return fmt.Errorf("grpc.max_send_msg_size must be >= 0")
	}

	// Validate resource limits
	if c.Server.Limits.MaxConnections <= 0 {
		return fmt.Errorf("limits.max_connections must be > 0")
	}
	if c.Server.Limits.MaxWatchCount <= 0 {
		return fmt.Errorf("limits.max_watch_count must be > 0")
	}
	if c.Server.Limits.MaxLeaseCount <= 0 {
		return fmt.Errorf("limits.max_lease_count must be > 0")
	}

	// Validate Lease configuration
	if c.Server.Lease.CheckInterval <= 0 {
		return fmt.Errorf("lease.check_interval must be > 0")
	}

	// Validate Auth configuration
	if c.Server.Auth.TokenTTL <= 0 {
		return fmt.Errorf("auth.token_ttl must be > 0")
	}
	if c.Server.Auth.BcryptCost < 4 || c.Server.Auth.BcryptCost > 31 {
		return fmt.Errorf("auth.bcrypt_cost must be between 4 and 31")
	}

	// Validate Maintenance configuration
	if c.Server.Maintenance.SnapshotChunkSize <= 0 {
		return fmt.Errorf("maintenance.snapshot_chunk_size must be > 0")
	}

	// Validate log level
	validLogLevels := map[string]bool{
		"debug": true, "info": true, "warn": true,
		"error": true, "dpanic": true, "panic": true, "fatal": true,
	}
	if !validLogLevels[c.Server.Log.Level] {
		return fmt.Errorf("log.level must be one of: debug, info, warn, error, dpanic, panic, fatal")
	}

	// Validate log encoding
	if c.Server.Log.Encoding != "json" && c.Server.Log.Encoding != "console" {
		return fmt.Errorf("log.encoding must be either 'json' or 'console'")
	}

	// Validate Raft configuration
	if c.Server.Raft.TickInterval <= 0 {
		return fmt.Errorf("raft.tick_interval must be > 0")
	}
	if c.Server.Raft.ElectionTick <= 0 {
		return fmt.Errorf("raft.election_tick must be > 0")
	}
	if c.Server.Raft.HeartbeatTick <= 0 {
		return fmt.Errorf("raft.heartbeat_tick must be > 0")
	}
	if c.Server.Raft.ElectionTick <= c.Server.Raft.HeartbeatTick {
		return fmt.Errorf("raft.election_tick must be > raft.heartbeat_tick")
	}
	if c.Server.Raft.MaxSizePerMsg == 0 {
		return fmt.Errorf("raft.max_size_per_msg must be > 0")
	}
	if c.Server.Raft.MaxInflightMsgs <= 0 {
		return fmt.Errorf("raft.max_inflight_msgs must be > 0")
	}

	// Validate batch proposal configuration
	if c.Server.Raft.Batch.Enable {
		if c.Server.Raft.Batch.MinBatchSize <= 0 {
			return fmt.Errorf("raft.batch.min_batch_size must be > 0")
		}
		if c.Server.Raft.Batch.MaxBatchSize <= 0 {
			return fmt.Errorf("raft.batch.max_batch_size must be > 0")
		}
		if c.Server.Raft.Batch.MinBatchSize > c.Server.Raft.Batch.MaxBatchSize {
			return fmt.Errorf("raft.batch.min_batch_size must be <= max_batch_size")
		}
		if c.Server.Raft.Batch.MinTimeout <= 0 {
			return fmt.Errorf("raft.batch.min_timeout must be > 0")
		}
		if c.Server.Raft.Batch.MaxTimeout <= 0 {
			return fmt.Errorf("raft.batch.max_timeout must be > 0")
		}
		if c.Server.Raft.Batch.MinTimeout > c.Server.Raft.Batch.MaxTimeout {
			return fmt.Errorf("raft.batch.min_timeout must be <= max_timeout")
		}
		if c.Server.Raft.Batch.LoadThreshold < 0 || c.Server.Raft.Batch.LoadThreshold > 1 {
			return fmt.Errorf("raft.batch.load_threshold must be between 0.0 and 1.0")
		}
	}

	// Validate Lease Read configuration
	if c.Server.Raft.LeaseRead.Enable {
		if c.Server.Raft.LeaseRead.ClockDrift <= 0 {
			return fmt.Errorf("raft.lease_read.clock_drift must be > 0")
		}
		if c.Server.Raft.LeaseRead.ReadTimeout <= 0 {
			return fmt.Errorf("raft.lease_read.read_timeout must be > 0")
		}
		// Clock drift should be less than election timeout, otherwise lease is unsafe
		electionTimeout := time.Duration(c.Server.Raft.ElectionTick) * c.Server.Raft.TickInterval
		if c.Server.Raft.LeaseRead.ClockDrift >= electionTimeout {
			return fmt.Errorf("raft.lease_read.clock_drift must be < election_timeout")
		}
	}

	return nil
}
