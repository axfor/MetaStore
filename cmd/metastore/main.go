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
	"strings"

	"metaStore/internal/http"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"

	"go.etcd.io/raft/v3/raftpb"
)

func main() {
	cluster := flag.String("cluster", "http://127.0.0.1:9021", "comma separated cluster peers")
	id := flag.Int("id", 1, "node ID")
	kvport := flag.Int("port", 9121, "key-value server port")
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
		dbPath := fmt.Sprintf("data/%d", *id)
		db, err := rocksdb.Open(dbPath)
		if err != nil {
			log.Fatalf("Failed to open RocksDB: %v", err)
		}
		defer db.Close()

		// Create RocksDB-backed KV store
		var kvs *rocksdb.RocksDB
		nodeID := fmt.Sprintf("node_%d", *id)
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady := raft.NewNodeRocksDB(*id, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC, db)

		kvs = rocksdb.NewRocksDB(db, nodeID, <-snapshotterReady, proposeC, commitC, errorC)
		defer kvs.Close()

		// the key-value http handler will propose updates to raft
		http.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)

	case "memory":
		// Memory + WAL mode
		log.Println("Starting with memory + WAL storage")
		var kvs *memory.Memory
		getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
		commitC, errorC, snapshotterReady := raft.NewNode(*id, strings.Split(*cluster, ","), *join, getSnapshot, proposeC, confChangeC)

		kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

		// the key-value http handler will propose updates to raft
		http.ServeHTTPKVAPI(kvs, *kvport, confChangeC, errorC)

	default:
		log.Fatalf("Unknown storage engine: %s. Supported engines: memory, rocksdb", *storageEngine)
	}
}
