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
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/etcdapi"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// testNode represents a single node for testing
type testNode struct {
	id               int
	proposeC         chan string
	confChangeC      chan raftpb.ConfChange
	commitC          <-chan *kvstore.Commit
	errorC           <-chan error
	snapshotterReady <-chan *snap.Snapshotter
	kvStore          interface{} // *memory.Memory or *rocksdb.RocksDB
	server           *etcdapi.Server
	clientAddr       string
	dataDir          string
	raftNode         interface{} // *raftNode (internal type)
	db               *grocksdb.DB // Only for RocksDB nodes
}

// startMemoryNode starts a single-node cluster for testing
// This is a simplified version suitable for performance testing
func startMemoryNode(t testing.TB, nodeID int) (*testNode, func()) {
	// Create data directory
	dataDir := fmt.Sprintf("data/perf-test/%d", nodeID)
	os.RemoveAll(dataDir)

	peers := []string{fmt.Sprintf("http://127.0.0.1:%d", 10200+nodeID)}

	proposeC := make(chan string, 1)
	confChangeC := make(chan raftpb.ConfChange, 1)

	// Create Raft node
	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, raftNode := raft.NewNode(
		nodeID, peers, false, getSnapshot, proposeC, confChangeC, dataDir,
	)

	// Create KV store
	kvs = memory.NewMemory(
		<-snapshotterReady,
		proposeC,
		commitC,
		errorC,
	)

	// Set Raft node for status reporting
	kvs.SetRaftNode(raftNode, uint64(nodeID))

	// Find available port for client connections
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}
	clientAddr := listener.Addr().String()
	listener.Close()

	// Create etcd server
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   clientAddr,
		ClusterID: 1000,
		MemberID:  uint64(nodeID),
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server in background
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server start error: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	node := &testNode{
		id:               nodeID,
		proposeC:         proposeC,
		confChangeC:      confChangeC,
		commitC:          commitC,
		errorC:           errorC,
		snapshotterReady: snapshotterReady,
		kvStore:          kvs,
		server:           server,
		clientAddr:       clientAddr,
		dataDir:          dataDir,
		raftNode:         raftNode,
	}

	// Cleanup function
	cleanup := func() {
		// Stop server
		if server != nil {
			server.Stop()
		}

		// Close Raft node
		go func() {
			for range commitC {
				// drain
			}
		}()
		close(proposeC)
		<-errorC

		// Clean up data directory
		os.RemoveAll(dataDir)
	}

	return node, cleanup
}

// testRocksDBNode represents a RocksDB node for testing
type testRocksDBNode struct {
	*testNode
	rocksKVStore *rocksdb.RocksDB
}

// startRocksDBNode starts a single-node RocksDB cluster for performance testing
func startRocksDBNode(t testing.TB, nodeID int) (*testRocksDBNode, func()) {
	// Create data directory
	dataDir := fmt.Sprintf("data/perf-test-rocksdb/%d", nodeID)
	os.RemoveAll(dataDir)

	// Create RocksDB directory
	dbPath := fmt.Sprintf("%s/kv", dataDir)
	err := os.MkdirAll(dbPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create RocksDB directory: %v", err)
	}

	// Open RocksDB
	db, err := rocksdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open RocksDB: %v", err)
	}

	peers := []string{fmt.Sprintf("http://127.0.0.1:%d", 10300+nodeID)}

	proposeC := make(chan string, 1)
	confChangeC := make(chan raftpb.ConfChange, 1)

	// Create Raft node with RocksDB
	var kvs *rocksdb.RocksDB
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, raftNode := raft.NewNodeRocksDB(
		nodeID, peers, false, getSnapshot, proposeC, confChangeC, db,
	)

	// Create RocksDB KV store
	kvs = rocksdb.NewRocksDB(
		db,
		<-snapshotterReady,
		proposeC,
		commitC,
		errorC,
	)

	// Set Raft node for status reporting
	kvs.SetRaftNode(raftNode, uint64(nodeID))

	// Find available port for client connections
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}
	clientAddr := listener.Addr().String()
	listener.Close()

	// Create etcd server
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   clientAddr,
		ClusterID: 2000,
		MemberID:  uint64(nodeID),
	})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server in background
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server start error: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	baseNode := &testNode{
		id:               nodeID,
		proposeC:         proposeC,
		confChangeC:      confChangeC,
		commitC:          commitC,
		errorC:           errorC,
		snapshotterReady: snapshotterReady,
		kvStore:          kvs,
		server:           server,
		clientAddr:       clientAddr,
		dataDir:          dataDir,
		raftNode:         raftNode,
		db:               db,
	}

	node := &testRocksDBNode{
		testNode:     baseNode,
		rocksKVStore: kvs,
	}

	// Cleanup function
	cleanup := func() {
		// Stop server
		if server != nil {
			server.Stop()
		}

		// Close Raft node
		go func() {
			for range commitC {
				// drain
			}
		}()
		close(proposeC)
		<-errorC

		// Close RocksDB
		if db != nil {
			db.Close()
		}

		// Clean up data directory
		os.RemoveAll(dataDir)
	}

	return node, cleanup
}
