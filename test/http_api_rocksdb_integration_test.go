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
	"os"
	"testing"

	"metaStore/internal/kvstore"
	"metaStore/internal/raft"
	rocksdbstore "metaStore/internal/rocksdb"

	"github.com/linxGnu/grocksdb"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// rocksDBCluster represents a RocksDB-backed cluster for testing
type rocksDBCluster struct {
	peers            []string
	commitC          []<-chan *kvstore.Commit
	errorC           []<-chan error
	proposeC         []chan string
	confChangeC      []chan raftpb.ConfChange
	dbs              []*grocksdb.DB
	snapshotterReady []<-chan *snap.Snapshotter
}

// newRocksDBCluster creates a RocksDB cluster of n nodes
func newRocksDBCluster(n int) *rocksDBCluster {
	peers := make([]string, n)
	for i := range peers {
		peers[i] = fmt.Sprintf("http://127.0.0.1:%d", 11000+i)
	}

	clus := &rocksDBCluster{
		peers:            peers,
		commitC:          make([]<-chan *kvstore.Commit, len(peers)),
		errorC:           make([]<-chan error, len(peers)),
		proposeC:         make([]chan string, len(peers)),
		confChangeC:      make([]chan raftpb.ConfChange, len(peers)),
		dbs:              make([]*grocksdb.DB, len(peers)),
		snapshotterReady: make([]<-chan *snap.Snapshotter, len(peers)),
	}

	for i := range clus.peers {
		// Clean up old data
		// RocksDB Raft nodes expect data/rocksdb/{id} directory structure
		dataDir := fmt.Sprintf("data/rocksdb/%d", i+1)
		os.RemoveAll(dataDir)

		// Open RocksDB - use the standard data/rocksdb/{id} directory to match raft's expectations
		db, err := rocksdbstore.Open(dataDir)
		if err != nil {
			panic(fmt.Sprintf("failed to open RocksDB for node %d: %v", i+1, err))
		}
		clus.dbs[i] = db

		clus.proposeC[i] = make(chan string, 1)
		clus.confChangeC[i] = make(chan raftpb.ConfChange, 1)

		// Use a dummy getSnapshot function
		getSnapshot := func() ([]byte, error) { return nil, nil }
		clus.commitC[i], clus.errorC[i], clus.snapshotterReady[i], _ = raft.NewNodeRocksDB(
			i+1,
			clus.peers,
			false,
			getSnapshot,
			clus.proposeC[i],
			clus.confChangeC[i],
			clus.dbs[i],
		)
	}

	return clus
}

// Close closes all RocksDB cluster nodes and returns an error if any failed.
func (clus *rocksDBCluster) Close() (err error) {
	for i := range clus.peers {
		go func(i int) {
			for range clus.commitC[i] { //revive:disable-line:empty-block
				// drain pending commits
			}
		}(i)
		if clus.proposeC != nil {
			close(clus.proposeC[i])
		}
		if clus.confChangeC != nil {
			close(clus.confChangeC[i])
		}
	}

	// Close RocksDB instances
	for i, db := range clus.dbs {
		if db != nil {
			db.Close()
		}
		// Clean up data directory
		dataDir := fmt.Sprintf("data/%d", i+1)
		os.RemoveAll(dataDir)
	}

	return nil
}

// closeNoErrors closes the RocksDB cluster and fails the test on any error
func (clus *rocksDBCluster) closeNoErrors(t *testing.T) {
	t.Log("closing RocksDB cluster...")
	require.NoError(t, clus.Close())
	t.Log("closing RocksDB cluster [done]")
}
