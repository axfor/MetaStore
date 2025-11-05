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

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	// "metaStore/internal/batch" // 已禁用 BatchProposer
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/config"
	"metaStore/api/etcd"
	"metaStore/api/http"
	"metaStore/pkg/log"
	"metaStore/pkg/metrics"
	"metaStore/api/mysql"

	"github.com/prometheus/client_golang/prometheus"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
	// "time" // 已禁用 BatchProposer，不再需要
)

const (
	// proposeChanBufferSize defines the buffer size for Raft proposal channel
	// Larger buffer enables pipeline writes for better throughput (2-5x improvement)
	// Value based on typical write burst patterns and memory constraints
	proposeChanBufferSize = 10000
)

func main() {
	// 配置文件路径（可选）
	configFile := flag.String("config", "", "path to config file (optional, uses defaults if not provided)")

	// 命令行参数（用于覆盖配置文件或在无配置文件时使用）
	cluster := flag.String("cluster", "http://127.0.0.1:9021", "comma separated cluster peers")
	clusterID := flag.Uint64("cluster-id", 1, "cluster ID")
	memberID := flag.Int("member-id", 1, "node ID")
	kvport := flag.Int("port", 9121, "http server port")
	grpcAddr := flag.String("grpc-addr", ":2379", "gRPC server address for etcd compatibility")
	join := flag.Bool("join", false, "join an existing cluster")
	storageEngine := flag.String("storage", "memory", "storage engine: memory or rocksdb")

	flag.Parse()

	// 加载配置（如果提供了配置文件则从文件加载，否则使用默认配置）
	cfg, err := config.LoadConfigOrDefault(*configFile, uint64(*clusterID), uint64(*memberID), *grpcAddr)
	if err != nil {
		// 配置加载失败时使用 fmt 输出，因为日志系统还未初始化
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(-1)
	}

	// 初始化日志系统（必须在其他组件之前初始化）
	if err := log.InitFromConfig(&cfg.Server.Log); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(-1)
	}
	log.Info("Logger initialized from configuration",
		zap.String("level", cfg.Server.Log.Level),
		zap.String("encoding", cfg.Server.Log.Encoding),
		zap.Strings("output_paths", cfg.Server.Log.OutputPaths),
		zap.Strings("error_output_paths", cfg.Server.Log.ErrorOutputPaths),
		zap.String("component", "main"))

	// 初始化全局性能配置
	config.InitPerformanceConfig(cfg)
	log.Info("Performance optimizations initialized",
		zap.Bool("enable_protobuf", config.GetEnableProtobuf()),
		zap.Bool("enable_snapshot_protobuf", config.GetEnableSnapshotProtobuf()),
		zap.Bool("enable_lease_protobuf", config.GetEnableLeaseProtobuf()),
		zap.String("component", "config"))

	// 启动 Prometheus 指标服务器（如果启用）
	if cfg.Server.Monitoring.EnablePrometheus {
		prometheusAddr := fmt.Sprintf(":%d", cfg.Server.Monitoring.PrometheusPort)
		prometheusRegistry := prometheus.NewRegistry()

		// 注册默认的 Go 运行时指标
		prometheusRegistry.MustRegister(prometheus.NewGoCollector())
		prometheusRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

		go func() {
			// 使用 zap 的全局 logger
			metricsServer := metrics.NewMetricsServer(prometheusAddr, prometheusRegistry, zap.L())
			log.Info("Starting Prometheus metrics server",
				zap.String("address", prometheusAddr),
				zap.String("component", "metrics"))
			if err := metricsServer.Start(); err != nil {
				log.Error("Prometheus metrics server failed",
					zap.Error(err),
					zap.String("component", "metrics"))
			}
		}()
	}

	// 配置文件可以被命令行参数覆盖
	if *configFile == "" {
		log.Info("Using default configuration with command-line parameters",
			zap.Uint64("cluster_id", cfg.Server.ClusterID),
			zap.Uint64("member_id", cfg.Server.MemberID),
			zap.String("etcd_address", cfg.Server.Etcd.Address),
			zap.String("component", "main"))
	} else {
		log.Info("Loaded configuration from file",
			zap.String("config_file", *configFile),
			zap.Uint64("cluster_id", cfg.Server.ClusterID),
			zap.Uint64("member_id", cfg.Server.MemberID),
			zap.String("etcd_address", cfg.Server.Etcd.Address),
			zap.String("component", "main"))
	}

	proposeC := make(chan string, proposeChanBufferSize)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	switch *storageEngine {
	case "rocksdb":
		// RocksDB mode - persistent storage
		log.Info("Starting with RocksDB persistent storage", zap.String("component", "main"))
		dbPath := fmt.Sprintf("data/rocksdb/%d", cfg.Server.MemberID)

		// 使用配置文件中的 RocksDB 配置
		db, err := rocksdb.Open(dbPath, &cfg.Server.RocksDB)
		if err != nil {
			log.Fatalf("Failed to open RocksDB: %v", err)
			os.Exit(-1)
			return
		}
		defer db.Close()

		// 记录 RocksDB 配置
		log.Info("RocksDB configuration applied",
			zap.Uint64("block_cache_size", cfg.Server.RocksDB.BlockCacheSize),
			zap.Uint64("write_buffer_size", cfg.Server.RocksDB.WriteBufferSize),
			zap.Int("max_background_jobs", cfg.Server.RocksDB.MaxBackgroundJobs),
			zap.Int("max_open_files", cfg.Server.RocksDB.MaxOpenFiles),
			zap.Bool("bloom_filter_enabled", cfg.Server.RocksDB.BlockBasedTableBloomFilter),
			zap.String("component", "rocksdb"))

		// Create RocksDB-backed KV store
		var kvs *rocksdb.RocksDB
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady, raftNode := raft.NewNodeRocksDB(*memberID, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, db, dbPath, cfg)

		// 使用原始构造函数（不使用 BatchProposer）
		kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)
		defer kvs.Close()

		// 注入 raft 节点引用，用于获取状态信息
		kvs.SetRaftNode(raftNode, cfg.Server.MemberID)

		// Start HTTP API server
		go func() {
			log.Info("Starting HTTP API", zap.Int("port", *kvport), zap.String("component", "main"))
			http.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)
		}()

		// Start MySQL protocol server
		mysqlServer, err := mysql.NewServer(mysql.ServerConfig{
			Store:    kvs,
			Address:  cfg.Server.MySQL.Address,
			Username: cfg.Server.MySQL.Username,
			Password: cfg.Server.MySQL.Password,
			Config:   cfg,
		})
		if err != nil {
			log.Fatalf("Failed to create MySQL server: %v", err)
			os.Exit(-1)
			return
		}

		go func() {
			log.Info("Starting MySQL protocol server",
				zap.String("address", cfg.Server.MySQL.Address),
				zap.String("component", "main"))
			if err := mysqlServer.Start(); err != nil {
				log.Error("MySQL server failed",
					zap.Error(err),
					zap.String("component", "main"))
			}
		}()

		// Start etcd gRPC server
		log.Info("Starting etcd gRPC server",
			zap.String("address", cfg.Server.Etcd.Address),
			zap.Uint64("cluster_id", cfg.Server.ClusterID),
			zap.Uint64("member_id", cfg.Server.MemberID),
			zap.String("component", "main"))
		etcdServer, err := etcd.NewServer(etcd.ServerConfig{
			Store:        kvs,
			Address:      cfg.Server.Etcd.Address,
			ClusterID:    cfg.Server.ClusterID,
			MemberID:     cfg.Server.MemberID,
			ClusterPeers: strings.Split(*cluster, ","),
			ConfChangeC:  confChangeC,
			Config:       cfg,
		})
		if err != nil {
			log.Fatalf("Failed to create etcd server: %v", err)
			os.Exit(-1)
			return
		}

		if err := etcdServer.Start(); err != nil {
			log.Fatalf("etcd server failed: %v", err)
			os.Exit(-1)
			return
		}

	case "memory":
		// Memory + WAL mode with etcd compatibility
		log.Info("Starting with memory + WAL storage and etcd gRPC support", zap.String("component", "main"))
		var kvs *memory.Memory
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady, raftNode := raft.NewNode(*memberID, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, "memory", cfg)

		// 使用原始构造函数（不使用 BatchProposer）
		kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

		// 注入 raft 节点引用，用于获取状态信息
		kvs.SetRaftNode(raftNode, cfg.Server.MemberID)

		// Start HTTP API server
		go func() {
			log.Info("Starting HTTP API", zap.Int("port", *kvport), zap.String("component", "main"))
			http.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)
		}()

		// Start MySQL protocol server
		mysqlServer, err := mysql.NewServer(mysql.ServerConfig{
			Store:    kvs,
			Address:  cfg.Server.MySQL.Address,
			Username: cfg.Server.MySQL.Username,
			Password: cfg.Server.MySQL.Password,
			Config:   cfg,
		})
		if err != nil {
			log.Fatalf("Failed to create MySQL server: %v", err)
			os.Exit(-1)
			return
		}

		go func() {
			log.Info("Starting MySQL protocol server",
				zap.String("address", cfg.Server.MySQL.Address),
				zap.String("component", "main"))
			if err := mysqlServer.Start(); err != nil {
				log.Error("MySQL server failed",
					zap.Error(err),
					zap.String("component", "main"))
			}
		}()

		// Start etcd gRPC server
		log.Info("Starting etcd gRPC server",
			zap.String("address", cfg.Server.Etcd.Address),
			zap.Uint64("cluster_id", cfg.Server.ClusterID),
			zap.Uint64("member_id", cfg.Server.MemberID),
			zap.String("component", "main"))
		etcdServer, err := etcd.NewServer(etcd.ServerConfig{
			Store:        kvs,
			Address:      cfg.Server.Etcd.Address,
			ClusterID:    cfg.Server.ClusterID,
			MemberID:     cfg.Server.MemberID,
			ClusterPeers: strings.Split(*cluster, ","),
			ConfChangeC:  confChangeC,
			Config:       cfg,
		})
		if err != nil {
			log.Fatalf("Failed to create etcd server: %v", err)
			os.Exit(-1)
			return
		}

		if err := etcdServer.Start(); err != nil {
			log.Fatalf("etcd server failed: %v", err)
			os.Exit(-1)
			return
		}

	default:
		log.Fatalf("Unknown storage engine: %s. Supported engines: memory, rocksdb", *storageEngine)
		os.Exit(-1)
		return
	}
}
