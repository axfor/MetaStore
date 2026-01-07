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

// NewTestConfig create用attestconfig
// use合理testdefaultvalue，可via opts custom
func NewTestConfig(nodeID, clusterID uint64, address string, opts ...func(*config.Config)) *config.Config {
	cfg := config.DefaultConfig(nodeID, clusterID, address)

	// testenvironmentoptimizeconfig
	// Auth: use较low bcrypt cost 加快test速度
	cfg.Server.Auth.BcryptCost = 4  // default 10，testenvironment用 4 以加fast度
	cfg.Server.Auth.TokenTTL = 10 * time.Minute
	cfg.Server.Auth.TokenCleanupInterval = 1 * time.Minute

	// Limits: set合理testlimit
	cfg.Server.Limits.MaxWatchCount = 1000
	cfg.Server.Limits.MaxLeaseCount = 10000
	cfg.Server.Limits.MaxConnections = 500
	cfg.Server.Limits.MaxRequestSize = 1.5 * 1024 * 1024 // 1.5MB

	// Monitoring: defaultdisabled以避免port冲突
	cfg.Server.Monitoring.EnablePrometheus = false

	// Maintenance: use较小 chunk size 加快test
	cfg.Server.Maintenance.SnapshotChunkSize = 1 * 1024 * 1024 // 1MB

	// Log: testenvironmentuse简化log
	cfg.Server.Log.Level = "info"
	cfg.Server.Log.Encoding = "console"
	cfg.Server.Log.OutputPaths = []string{"stdout"}

	// Reliability: 较短timeouttime
	cfg.Server.Reliability.ShutdownTimeout = 5 * time.Second
	cfg.Server.Reliability.DrainTimeout = 2 * time.Second

	// RocksDB: testenvironmentuse较小cache
	cfg.Server.RocksDB.BlockCacheSize = 8 * 1024 * 1024    // 8MB
	cfg.Server.RocksDB.WriteBufferSize = 4 * 1024 * 1024   // 4MB
	cfg.Server.RocksDB.MaxWriteBufferNumber = 2
	cfg.Server.RocksDB.MaxBackgroundJobs = 2
	cfg.Server.RocksDB.BloomFilterBitsPerKey = 10

	// Raft: testenvironmentuseoptimizeconfig（inherit自 DefaultConfig）
	// defaultvalue（已optimize）：
	//   - TickInterval: 50ms（fastresponse，比 etcd default 100ms 快 2x）
	//   - ElectionTick: 10 (500ms election timeout)
	//   - HeartbeatTick: 1 (50ms heartbeat)
	//   - MaxSizePerMsg: 4MB
	//   - MaxInflightMsgs: 1024（high吞吐，比 etcd default 512 提升 2x）
	// 如需更激进optimize，可use WithRaftConfig() customconfig

	// 应用customoption
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// WithAuthConfig customauthenticationconfig
func WithAuthConfig(tokenTTL time.Duration, bcryptCost int, enableAudit bool) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.TokenTTL = tokenTTL
		cfg.Server.Auth.BcryptCost = bcryptCost
		cfg.Server.Auth.EnableAudit = enableAudit
	}
}

// WithLimits customlimitconfig
func WithLimits(maxWatch, maxLease, maxConnections int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Limits.MaxWatchCount = maxWatch
		cfg.Server.Limits.MaxLeaseCount = maxLease
		cfg.Server.Limits.MaxConnections = maxConnections
	}
}

// WithRocksDBConfig custom RocksDB config
func WithRocksDBConfig(blockCache, writeBuffer uint64) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.RocksDB.BlockCacheSize = blockCache
		cfg.Server.RocksDB.WriteBufferSize = writeBuffer
	}
}

// WithGRPCConfig custom gRPC config
func WithGRPCConfig(maxRecvMsgSize, maxSendMsgSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.GRPC.MaxRecvMsgSize = maxRecvMsgSize
		cfg.Server.GRPC.MaxSendMsgSize = maxSendMsgSize
	}
}

// WithMonitoring enabled监控
func WithMonitoring(prometheusPort int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Monitoring.EnablePrometheus = true
		cfg.Server.Monitoring.PrometheusPort = prometheusPort
	}
}

// WithMaintenanceConfig custom维护config
func WithMaintenanceConfig(snapshotChunkSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Maintenance.SnapshotChunkSize = snapshotChunkSize
	}
}

// WithFastTest fasttestconfig（降lowtimeouttime，加快test速度）
func WithFastTest() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.BcryptCost = 4
		cfg.Server.Auth.TokenTTL = 1 * time.Minute
		cfg.Server.Auth.TokenCleanupInterval = 10 * time.Second
		cfg.Server.Reliability.ShutdownTimeout = 2 * time.Second
		cfg.Server.Reliability.DrainTimeout = 1 * time.Second
	}
}

// WithProductionLike class生产environmentconfig（用at性能test）
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

// WithFastRaft fast Raft config（用at加速unittest）
// use更短 tick interval，适合notneedtrue实timerowastest
func WithFastRaft() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = 50 * time.Millisecond  // 50ms tick（比default 100ms 快 2x）
		cfg.Server.Raft.ElectionTick = 10  // 500ms election timeout
		cfg.Server.Raft.HeartbeatTick = 1   // 50ms heartbeat
		// 其他argument保持default
	}
}

// WithRaftConfig custom Raft config（用at性能调优test）
func WithRaftConfig(tickInterval time.Duration, electionTick, heartbeatTick int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = tickInterval
		cfg.Server.Raft.ElectionTick = electionTick
		cfg.Server.Raft.HeartbeatTick = heartbeatTick
	}
}

// WithBatchProposal custom批量提案config（用at批量optimizetest）
// default情况下批量提案已enabled，use此functioncancustom批量argument
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

// WithoutBatchProposal disabled批量提案（用at基准testand性能对比）
// use此functioncantestnotenabled批量optimize时性能，作as对比基准
func WithoutBatchProposal() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.Batch.Enable = false
	}
}
