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
	"log"
	"os"
	"strings"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/etcdcompat"
	"metaStore/pkg/httpapi"

	"go.etcd.io/raft/v3/raftpb"
)

func main() {
	cluster := flag.String("cluster", "http://127.0.0.1:9021", "comma separated cluster peers")
	clusterID := flag.Uint64("cluster-id", 1, "cluster ID")
	id := flag.Int("id", 1, "node ID") // memberID
	kvport := flag.Int("port", 9121, "http server port")
	grpcAddr := flag.String("grpc-addr", ":2379", "gRPC server address for etcd compatibility")
	join := flag.Bool("join", false, "join an existing cluster")
	storageEngine := flag.String("storage", "memory", "storage engine: memory or rocksdb")

	flag.Parse()

	proposeC := make(chan string)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	switch *storageEngine {
	case "rocksdb":
		// RocksDB mode - persistent storage
		log.Println("Starting with RocksDB persistent storage")
		dbPath := fmt.Sprintf("data/rocksdb/%d", *id)
		db, err := rocksdb.Open(dbPath)
		if err != nil {
			log.Fatalf("Failed to open RocksDB: %v", err)
			os.Exit(-1)
			return
		}
		defer db.Close()

		// Create RocksDB-backed KV store

		// nodeID := fmt.Sprintf("node_%d", *id)
		var kvs *rocksdb.RocksDBEtcdRaft
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady := raft.NewNodeRocksDB(*id, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, db)

		kvs = rocksdb.NewRocksDBEtcdRaft(db, <-snapshotterReady, proposeC, commitC, errorC)
		defer kvs.Close()

		// Start HTTP API server
		go func() {
			log.Printf("Starting HTTP API on port %d", *kvport)
			httpapi.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)
		}()

		// Start etcd gRPC server
		log.Printf("Starting etcd gRPC server on %s", *grpcAddr)
		etcdServer, err := etcdcompat.NewServer(etcdcompat.ServerConfig{
			Store:     kvs,
			Address:   *grpcAddr,
			ClusterID: uint64(*clusterID),
			MemberID:  uint64(*id),
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
		log.Println("Starting with memory + WAL storage and etcd gRPC support")
		var kvs *memory.MemoryEtcdRaft
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady := raft.NewNode(*id, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, "memory")

		kvs = memory.NewMemoryEtcdRaft(<-snapshotterReady, proposeC, commitC, errorC)

		// Start HTTP API server
		go func() {
			log.Printf("Starting HTTP API on port %d", *kvport)
			httpapi.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)
		}()

		// Start etcd gRPC server
		log.Printf("Starting etcd gRPC server on %s", *grpcAddr)
		etcdServer, err := etcdcompat.NewServer(etcdcompat.ServerConfig{
			Store:     kvs,
			Address:   *grpcAddr,
			ClusterID: uint64(*clusterID),
			MemberID:  uint64(*id),
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
