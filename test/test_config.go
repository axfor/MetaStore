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

// NewTestConfig createfor testconfig
// usemergetestdefaultvalue，canvia opts custom
func NewTestConfig(nodeID, clusterID uint64, address string, opts ...func(*config.Config)) *config.Config {
	cfg := config.DefaultConfig(nodeID, clusterID, address)

	// testenvironmentoptimizeconfig
	// Auth: uselow bcrypt cost fasttest
	cfg.Server.Auth.BcryptCost = 4  // default 10，testenvironment 4 fast
	cfg.Server.Auth.TokenTTL = 10 * time.Minute
	cfg.Server.Auth.TokenCleanupInterval = 1 * time.Minute

	// Limits: setmergetestlimit
	cfg.Server.Limits.MaxWatchCount = 1000
	cfg.Server.Limits.MaxLeaseCount = 10000
	cfg.Server.Limits.MaxConnections = 500
	cfg.Server.Limits.MaxRequestSize = 1.5 * 1024 * 1024 // 1.5MB

	// Monitoring: defaultdisabledport
	cfg.Server.Monitoring.EnablePrometheus = false

	// Maintenance: usesmall chunk size fasttest
	cfg.Server.Maintenance.SnapshotChunkSize = 1 * 1024 * 1024 // 1MB

	// Log: testenvironmentusetransformlog
	cfg.Server.Log.Level = "info"
	cfg.Server.Log.Encoding = "console"
	cfg.Server.Log.OutputPaths = []string{"stdout"}

	// Reliability: shorttimeouttime
	cfg.Server.Reliability.ShutdownTimeout = 5 * time.Second
	cfg.Server.Reliability.DrainTimeout = 2 * time.Second

	// Pebble: testenvironmentusesmallcache
	cfg.Server.Pebble.BlockCacheSize = 8 * 1024 * 1024    // 8MB
	cfg.Server.Pebble.WriteBufferSize = 4 * 1024 * 1024   // 4MB
	cfg.Server.Pebble.MaxWriteBufferNumber = 2
	cfg.Server.Pebble.MaxBackgroundJobs = 2
	cfg.Server.Pebble.BloomFilterBitsPerKey = 10

	// Raft: testenvironmentuseoptimizeconfig(inherit DefaultConfig)
	// defaultvalue(already optimize)：
	//   - TickInterval: 50ms(fastresponse， etcd default 100ms fast 2x)
	//   - ElectionTick: 10 (500ms election timeout)
	//   - HeartbeatTick: 1 (50ms heartbeat)
	//   - MaxSizePerMsg: 4MB
	//   - MaxInflightMsgs: 1024(high， etcd default 512  2x)
	// optimize，canuse WithRaftConfig() customconfig

	// appliedcustomoption
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

// WithPebbleConfig custom Pebble config
func WithPebbleConfig(blockCache, writeBuffer uint64) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Pebble.BlockCacheSize = blockCache
		cfg.Server.Pebble.WriteBufferSize = writeBuffer
	}
}

// WithGRPCConfig custom gRPC config
func WithGRPCConfig(maxRecvMsgSize, maxSendMsgSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.GRPC.MaxRecvMsgSize = maxRecvMsgSize
		cfg.Server.GRPC.MaxSendMsgSize = maxSendMsgSize
	}
}

// WithMonitoring enabledmonitoring
func WithMonitoring(prometheusPort int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Monitoring.EnablePrometheus = true
		cfg.Server.Monitoring.PrometheusPort = prometheusPort
	}
}

// WithMaintenanceConfig customconfig
func WithMaintenanceConfig(snapshotChunkSize int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Maintenance.SnapshotChunkSize = snapshotChunkSize
	}
}

// WithFastTest fasttestconfig(lowtimeouttime，fasttest)
func WithFastTest() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.BcryptCost = 4
		cfg.Server.Auth.TokenTTL = 1 * time.Minute
		cfg.Server.Auth.TokenCleanupInterval = 10 * time.Second
		cfg.Server.Reliability.ShutdownTimeout = 2 * time.Second
		cfg.Server.Reliability.DrainTimeout = 1 * time.Second
	}
}

// WithProductionLike classenvironmentconfig(for performancetest)
func WithProductionLike() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Auth.BcryptCost = 10
		cfg.Server.Auth.TokenTTL = 1 * time.Hour
		cfg.Server.Pebble.BlockCacheSize = 512 * 1024 * 1024   // 512MB
		cfg.Server.Pebble.WriteBufferSize = 64 * 1024 * 1024   // 64MB
		cfg.Server.Pebble.MaxWriteBufferNumber = 3
		cfg.Server.Pebble.MaxBackgroundJobs = 4
		cfg.Server.Maintenance.SnapshotChunkSize = 16 * 1024 * 1024 // 16MB
	}
}

// WithFastRaft fast Raft config(for unittest)
// useshort tick interval，mergenotneedtruetimerowastest
func WithFastRaft() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = 50 * time.Millisecond  // 50ms tick(default 100ms fast 2x)
		cfg.Server.Raft.ElectionTick = 10  // 500ms election timeout
		cfg.Server.Raft.HeartbeatTick = 1   // 50ms heartbeat
		// argumentholddefault
	}
}

// WithRaftConfig custom Raft config(for performancetest)
func WithRaftConfig(tickInterval time.Duration, electionTick, heartbeatTick int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.TickInterval = tickInterval
		cfg.Server.Raft.ElectionTick = electionTick
		cfg.Server.Raft.HeartbeatTick = heartbeatTick
	}
}

// WithBatchProposal customconfig(for optimizetest)
// defaultnextalready enabled，usefunctioncancustomargument
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

// WithoutBatchProposal disabled(for prepare testandperformanceto)
// usefunctioncantestnotenabledoptimizewhenperformance，astoprepare
func WithoutBatchProposal() func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.Raft.Batch.Enable = false
	}
}
