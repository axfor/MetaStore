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

package test

import (
	"metaStore/pkg/config"
	"time"
)

// NewTestConfig 创建用于测试的配置
// 使用合理的测试默认值，可通过 opts 自定义
func NewTestConfig(nodeID, clusterID uint64, address string, opts ...func(*config.Config)) *config.Config {
	cfg := config.DefaultConfig(nodeID, clusterID, address)

	// 测试环境优化配置
	// Auth: 使用较低的 bcrypt cost 加快测试速度
	cfg.Server.Auth.BcryptCost = 4  // 默认 10，测试环境用 4 以加快速度
	cfg.Server.Auth.TokenTTL = 10 * time.Minute
	cfg.Server.Auth.TokenCleanupInterval = 1 * time.Minute

	// Limits: 设置合理的测试限制
	cfg.Server.Limits.MaxWatchCount = 1000
	cfg.Server.Limits.MaxLeaseCount = 10000
	cfg.Server.Limits.MaxConnections = 500
	cfg.Server.Limits.MaxRequestSize = 1.5 * 1024 * 1024 // 1.5MB

	// Monitoring: 默认禁用以避免端口冲突
	cfg.Server.Monitoring.EnablePrometheus = false

	// Maintenance: 使用较小的 chunk size 加快测试
	cfg.Server.Maintenance.SnapshotChunkSize = 1 * 1024 * 1024 // 1MB

	// Log: 测试环境使用简化日志
	cfg.Server.Log.Level = "info"
	cfg.Server.Log.Encoding = "console"
	cfg.Server.Log.OutputPaths = []string{"stdout"}

	// Reliability: 较短的超时时间
	cfg.Server.Reliability.ShutdownTimeout = 5 * time.Second
	cfg.Server.Reliability.DrainTimeout = 2 * time.Second

	// RocksDB: 测试环境使用较小的缓存
	cfg.Server.RocksDB.BlockCacheSize = 8 * 1024 * 1024    // 8MB
	cfg.Server.RocksDB.WriteBufferSize = 4 * 1024 * 1024   // 4MB
	cfg.Server.RocksDB.MaxWriteBufferNumber = 2
	cfg.Server.RocksDB.MaxBackgroundJobs = 2
	cfg.Server.RocksDB.BloomFilterBitsPerKey = 10

	// Raft: 测试环境使用优化配置（继承自 DefaultConfig）
	// 默认值（已优化）：
	//   - TickInterval: 50ms（快速响应，比 etcd 默认 100ms 快 2x）
	//   - ElectionTick: 10 (500ms election timeout)
	//   - HeartbeatTick: 1 (50ms heartbeat)
	//   - MaxSizePerMsg: 4MB
	//   - MaxInflightMsgs: 1024（高吞吐，比 etcd 默认 512 提升 2x）
	// 如需更激进的优化，可使用 WithRaftConfig() 自定义配置

	// 应用自定义选项
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// WithAuthConfig 自定义认证配置
func WithAuthConfig(tokenTTL time.Duration, bcryptCost int, enableAudit bool) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.TokenTTL = tokenTTL
		cfg.Server.Auth.BcryptCost = bcryptCost
		cfg.Server.Auth.EnableAudit = enableAudit
	}
}

// WithLimits 自定义限制配置
func WithLimits(maxWatch, maxLease, maxConnections int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Limits.MaxWatchCount = maxWatch
		cfg.Server.Limits.MaxLeaseCount = maxLease
		cfg.Server.Limits.MaxConnections = maxConnections
	}
}

// WithRocksDBConfig 自定义 RocksDB 配置
func WithRocksDBConfig(blockCache, writeBuffer uint64) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.RocksDB.BlockCacheSize = blockCache
		cfg.Server.RocksDB.WriteBufferSize = writeBuffer
	}
}

// WithGRPCConfig 自定义 gRPC 配置
func WithGRPCConfig(maxRecvMsgSize, maxSendMsgSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.GRPC.MaxRecvMsgSize = maxRecvMsgSize
		cfg.Server.GRPC.MaxSendMsgSize = maxSendMsgSize
	}
}

// WithMonitoring 启用监控
func WithMonitoring(prometheusPort int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Monitoring.EnablePrometheus = true
		cfg.Server.Monitoring.PrometheusPort = prometheusPort
	}
}

// WithMaintenanceConfig 自定义维护配置
func WithMaintenanceConfig(snapshotChunkSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Maintenance.SnapshotChunkSize = snapshotChunkSize
	}
}

// WithFastTest 快速测试配置（降低超时时间，加快测试速度）
func WithFastTest() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.BcryptCost = 4
		cfg.Server.Auth.TokenTTL = 1 * time.Minute
		cfg.Server.Auth.TokenCleanupInterval = 10 * time.Second
		cfg.Server.Reliability.ShutdownTimeout = 2 * time.Second
		cfg.Server.Reliability.DrainTimeout = 1 * time.Second
	}
}

// WithProductionLike 类生产环境配置（用于性能测试）
func WithProductionLike() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.BcryptCost = 10
		cfg.Server.Auth.TokenTTL = 1 * time.Hour
		cfg.Server.RocksDB.BlockCacheSize = 512 * 1024 * 1024   // 512MB
		cfg.Server.RocksDB.WriteBufferSize = 64 * 1024 * 1024   // 64MB
		cfg.Server.RocksDB.MaxWriteBufferNumber = 3
		cfg.Server.RocksDB.MaxBackgroundJobs = 4
		cfg.Server.Maintenance.SnapshotChunkSize = 16 * 1024 * 1024 // 16MB
	}
}

// WithFastRaft 快速 Raft 配置（用于加速单元测试）
// 使用更短的 tick 间隔，适合不需要真实时间行为的测试
func WithFastRaft() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = 50 * time.Millisecond  // 50ms tick（比默认 100ms 快 2x）
		cfg.Server.Raft.ElectionTick = 10  // 500ms election timeout
		cfg.Server.Raft.HeartbeatTick = 1   // 50ms heartbeat
		// 其他参数保持默认
	}
}

// WithRaftConfig 自定义 Raft 配置（用于性能调优测试）
func WithRaftConfig(tickInterval time.Duration, electionTick, heartbeatTick int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = tickInterval
		cfg.Server.Raft.ElectionTick = electionTick
		cfg.Server.Raft.HeartbeatTick = heartbeatTick
	}
}

// WithBatchProposal 自定义批量提案配置（用于批量优化测试）
// 默认情况下批量提案已启用，使用此函数可以自定义批量参数
func WithBatchProposal(minBatch, maxBatch int, minTimeout, maxTimeout time.Duration, loadThreshold float64) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.Batch.Enable = true
		cfg.Server.Raft.Batch.MinBatchSize = minBatch
		cfg.Server.Raft.Batch.MaxBatchSize = maxBatch
		cfg.Server.Raft.Batch.MinTimeout = minTimeout
		cfg.Server.Raft.Batch.MaxTimeout = maxTimeout
		cfg.Server.Raft.Batch.LoadThreshold = loadThreshold
	}
}

// WithoutBatchProposal 禁用批量提案（用于基准测试和性能对比）
// 使用此函数可以测试不启用批量优化时的性能，作为对比基准
func WithoutBatchProposal() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.Batch.Enable = false
	}
}
