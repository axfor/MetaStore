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
	pebblestore "metaStore/internal/pebbledb"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// pebbleDBCluster represents a Pebble-backed cluster for testing
type pebbleDBCluster struct {
	peers            []string
	commitC          []<-chan *kvstore.Commit
	errorC           []<-chan error
	proposeC         []chan string
	confChangeC      []chan raftpb.ConfChange
	dbs              []*pebble.DB
	snapshotterReady []<-chan *snap.Snapshotter
}

// newPebbleCluster creates a Pebble cluster of n nodes
func newPebbleCluster(n int) *pebbleDBCluster {
	peers := make([]string, n)
	for i := range peers {
		peers[i] = fmt.Sprintf("http://127.0.0.1:%d", 11000+i)
	}

	clus := &pebbleDBCluster{
		peers:            peers,
		commitC:          make([]<-chan *kvstore.Commit, len(peers)),
		errorC:           make([]<-chan error, len(peers)),
		proposeC:         make([]chan string, len(peers)),
		confChangeC:      make([]chan raftpb.ConfChange, len(peers)),
		dbs:              make([]*pebble.DB, len(peers)),
		snapshotterReady: make([]<-chan *snap.Snapshotter, len(peers)),
	}

	for i := range clus.peers {
		// Clean up old data
		// Pebble Raft nodes expect data/pebble/{id} directory structure
		dataDir := fmt.Sprintf("data/pebble/%d", i+1)
		os.RemoveAll(dataDir)

		// Create directory for Pebble
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			panic(fmt.Sprintf("failed to create directory for node %d: %v", i+1, err))
		}

		// Open Pebble - use the standard data/pebble/{id} directory to match raft's expectations
		db, err := pebblestore.Open(dataDir)
		if err != nil {
			panic(fmt.Sprintf("failed to open Pebble for node %d: %v", i+1, err))
		}
		clus.dbs[i] = db

		clus.proposeC[i] = make(chan string, 1)
		clus.confChangeC[i] = make(chan raftpb.ConfChange, 1)

		// Create test config for this node
		cfg := NewTestConfig(uint64(i+1), 1, fmt.Sprintf(":940%d", i+1))

		// Use a dummy getSnapshot function
		getSnapshot := func() ([]byte, error) { return nil, nil }
		clus.commitC[i], clus.errorC[i], clus.snapshotterReady[i], _ = raft.NewNodePebble(
			i+1,
			clus.peers,
			false,
			getSnapshot,
			clus.proposeC[i],
			clus.confChangeC[i],
			clus.dbs[i],
			dataDir,
			cfg,
		)
	}

	return clus
}

// Close closes all Pebble cluster nodes and returns an error if any failed.
func (clus *pebbleDBCluster) Close() (err error) {
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

	// Close Pebble instances
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

// closeNoErrors closes the Pebble cluster and fails the test on any error
func (clus *pebbleDBCluster) closeNoErrors(t *testing.T) {
	t.Log("closing Pebble cluster...")
	require.NoError(t, clus.Close())
	t.Log("closing Pebble cluster [done]")
}
