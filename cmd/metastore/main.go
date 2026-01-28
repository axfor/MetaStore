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
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/soheilhy/cmux"

	// "metaStore/internal/batch" // 已禁用 BatchProposer
	"metaStore/api/etcd"
	"metaStore/api/etcdgateway"
	etcdhttp "metaStore/api/etcdhttp"
	"metaStore/api/mysql"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/config"
	"metaStore/pkg/log"
	"metaStore/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	// "time" // disabled BatchProposer，no longer needed
)

const (
	// proposeChanBufferSize defines the buffer size for Raft proposal channel
	// Larger buffer enables pipeline writes for better throughput (2-5x improvement)
	// Value based on typical write burst patterns and memory constraints
	proposeChanBufferSize = 10000
)

func main() {
	// configfilepath（optional）
	configFile := flag.String("config", "", "path to config file (optional, uses defaults if not provided)")

	// command line argument（used to overrideconfigfileor when noconfigfilewhen using）
	cluster := flag.String("cluster", "http://127.0.0.1:9021", "comma separated cluster peers")
	clusterID := flag.Uint64("cluster-id", 1, "cluster ID")
	memberID := flag.Int("member-id", 1, "node ID")
	grpcAddr := flag.String("grpc-addr", ":2379", "gRPC server address for etcd compatibility")
	clientURLs := flag.String("client-urls", "", "comma separated advertised client URLs")
	join := flag.Bool("join", false, "join an existing cluster")
	storageEngine := flag.String("storage", "memory", "storage engine: memory or rocksdb")

	flag.Parse()

	// load config（if providedconfigfilethen load from file，else use defaultconfig）
	cfg, err := config.LoadConfigOrDefault(*configFile, uint64(*clusterID), uint64(*memberID), *grpcAddr)
	if err != nil {
		// configloadfailurewhen using fmt output，because logsystemnot yet initialized
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(-1)
	}

	// Override ClientURLs from CLI if provided
	if *clientURLs != "" {
		cfg.Server.ClientURLs = strings.Split(*clientURLs, ",")
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

	// initializeglobalperformanceconfig
	config.InitPerformanceConfig(cfg)
	log.Info("Performance optimizations initialized",
		zap.Bool("enable_protobuf", config.GetEnableProtobuf()),
		zap.Bool("enable_snapshot_protobuf", config.GetEnableSnapshotProtobuf()),
		zap.Bool("enable_lease_protobuf", config.GetEnableLeaseProtobuf()),
		zap.String("component", "config"))

	// start Prometheus metricsserver（ifenabled）
	if cfg.Server.Monitoring.EnablePrometheus {
		prometheusAddr := fmt.Sprintf(":%d", cfg.Server.Monitoring.PrometheusPort)
		prometheusRegistry := prometheus.NewRegistry()

		// registerdefault Go runningwhenmetrics
		prometheusRegistry.MustRegister(prometheus.NewGoCollector())
		prometheusRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

		go func() {
			// use zap global logger
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

	// configfilecanbecommand line argumentoverride
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

		// use RocksDB config from config file RocksDB config
		db, err := rocksdb.Open(dbPath, &cfg.Server.RocksDB)
		if err != nil {
			log.Fatalf("Failed to open RocksDB: %v", err)
			os.Exit(-1)
			return
		}
		defer db.Close()

		// record RocksDB config
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
		commitC, errorC, snapshotterReady, raftNode := raft.NewNodeRocksDB(int(cfg.Server.MemberID), strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, db, dbPath, cfg)

		// use original constructor function（not using BatchProposer）
		kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)
		defer kvs.Close()

		// inject raft node reference，used to getstatus info
		kvs.SetRaftNode(raftNode, cfg.Server.MemberID)

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

		// Start unified listener (cmux)
		log.Info("Starting unified listener", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
		l, err := net.Listen("tcp", cfg.Server.Etcd.Address)
		if err != nil {
			log.Fatalf("Failed to listen on %s: %v", cfg.Server.Etcd.Address, err)
			os.Exit(-1)
		}

		m := cmux.New(l)
		mux := http.NewServeMux()
		etcdhttp.HandleVersion(mux)
		if cfg.Server.EtcdGateway.Enable {
			dialOpts := []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithDefaultCallOptions(
					grpc.MaxCallRecvMsgSize(cfg.Server.GRPC.MaxRecvMsgSize),
					grpc.MaxCallSendMsgSize(cfg.Server.GRPC.MaxSendMsgSize),
				),
			}
			endpoint := cfg.Server.EtcdGateway.Endpoint
			if endpoint == "" {
				endpoint = cfg.Server.Etcd.Address
			}

			gwmux, err := etcdgateway.NewHandler(context.Background(), endpoint, dialOpts)
			if err != nil {
				log.Error("Failed to create etcd gateway handler", zap.Error(err), zap.String("component", "main"))
			} else {
				log.Info("etcd gateway enabled", zap.String("endpoint", endpoint), zap.String("component", "main"))
			}

			httpMux := createMux(gwmux, mux)
			srv := &http.Server{
				Handler: httpMux,
			}
			httpl := m.Match(cmux.HTTP1())
			go srv.Serve(httpl)
		}
		grpcl := m.Match(cmux.HTTP2())

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
			ClientURLs:   cfg.Server.ClientURLs,
			ConfChangeC:  confChangeC,
			Config:       cfg,
			Listener:     grpcl, // Inject matched listener
		})
		if err != nil {
			log.Fatalf("Failed to create etcd server: %v", err)
			os.Exit(-1)
			return
		}

		// Wire Raft ConfChange callback to ClusterManager
		// This ensures all nodes receive committed ConfChanges and update their member lists
		raftNode.SetConfChangeCallback(etcdServer.GetClusterManager().ApplyConfChange)
		leaseManager := etcdServer.GetLeaseManager()
		if leaseManager != nil {
			go func() {
				for status := range raftNode.LeaderChangeC() {
					leaseManager.OnLeaderChange(status)
				}
			}()
		}

		go func() {
			if err := etcdServer.Start(); err != nil {
				log.Fatalf("etcd server failed: %v", err)
				os.Exit(-1)
			}
		}()

		// Block on cmux serving
		log.Info("Starting cmux multiplexing", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
		if err := m.Serve(); err != nil {
			log.Fatalf("cmux failed: %v", err)
			os.Exit(-1)
		}

	case "memory":
		// Memory + WAL mode with etcd compatibility
		log.Info("Starting with memory + WAL storage and etcd gRPC support", zap.String("component", "main"))
		var kvs *memory.Memory
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady, raftNode := raft.NewNode(int(cfg.Server.MemberID), strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, "memory", cfg)

		// use original constructor function（not using BatchProposer）
		kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

		// inject raft node reference，used to getstatus info
		kvs.SetRaftNode(raftNode, cfg.Server.MemberID)

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

		// Start unified listener (cmux)
		log.Info("Starting unified listener", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
		l, err := net.Listen("tcp", cfg.Server.Etcd.Address)
		if err != nil {
			log.Fatalf("Failed to listen on %s: %v", cfg.Server.Etcd.Address, err)
			os.Exit(-1)
		}
		m := cmux.New(l)
		mux := http.NewServeMux()
		etcdhttp.HandleVersion(mux)

		if cfg.Server.EtcdGateway.Enable {
			dialOpts := []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithDefaultCallOptions(
					grpc.MaxCallRecvMsgSize(cfg.Server.GRPC.MaxRecvMsgSize),
					grpc.MaxCallSendMsgSize(cfg.Server.GRPC.MaxSendMsgSize),
				),
			}
			endpoint := cfg.Server.EtcdGateway.Endpoint
			if endpoint == "" {
				endpoint = cfg.Server.Etcd.Address
			}

			gwmux, err := etcdgateway.NewHandler(context.Background(), endpoint, dialOpts)
			if err != nil {
				log.Error("Failed to create etcd gateway handler", zap.Error(err), zap.String("component", "main"))
			} else {
				log.Info("etcd gateway enabled", zap.String("endpoint", endpoint), zap.String("component", "main"))
			}
			httpMux := createMux(gwmux, mux)
			srv := &http.Server{
				Handler: httpMux,
			}
			httpl := m.Match(cmux.HTTP1())
			go srv.Serve(httpl)
		}
		grpcL := m.Match(cmux.HTTP2())
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
			ClientURLs:   cfg.Server.ClientURLs,
			ConfChangeC:  confChangeC,
			Config:       cfg,
			Listener:     grpcL, // Inject matched listener
		})
		if err != nil {
			log.Fatalf("Failed to create etcd server: %v", err)
			os.Exit(-1)
			return
		}

		// Wire Raft ConfChange callback to ClusterManager
		// This ensures all nodes receive committed ConfChanges and update their member lists
		raftNode.SetConfChangeCallback(etcdServer.GetClusterManager().ApplyConfChange)
		leaseManager := etcdServer.GetLeaseManager()
		if leaseManager != nil {
			go func() {
				for status := range raftNode.LeaderChangeC() {
					leaseManager.OnLeaderChange(status)
				}
			}()
		}

		go func() {
			if err := etcdServer.Start(); err != nil {
				log.Fatalf("etcd server failed: %v", err)
				os.Exit(-1)
			}
		}()

		// Block on cmux serving
		log.Info("Starting cmux multiplexing", zap.String("address", cfg.Server.Etcd.Address), zap.String("component", "main"))
		if err := m.Serve(); err != nil {
			log.Fatalf("cmux failed: %v", err)
			os.Exit(-1)
		}

	default:
		log.Fatalf("Unknown storage engine: %s. Supported engines: memory, rocksdb", *storageEngine)
		os.Exit(-1)
		return
	}
}

func createMux(gwmux *runtime.ServeMux, handler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/v3/", gwmux)
	mux.Handle("/", handler)

	return mux
}
