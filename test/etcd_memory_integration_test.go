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
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/pkg/etcdcompat"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// etcdCluster represents a cluster of etcd-compatible nodes for testing
type etcdCluster struct {
	peers              []string
	commitC            []<-chan *kvstore.Commit
	errorC             []<-chan error
	proposeC           []chan string
	confChangeC        []chan raftpb.ConfChange
	snapshotterReady   []<-chan *snap.Snapshotter
	kvStores           []*memory.MemoryEtcdRaft
	servers            []*etcdcompat.Server
	clients            []*clientv3.Client
}

// newEtcdCluster creates a cluster of n etcd-compatible nodes
func newEtcdCluster(t *testing.T, n int) *etcdCluster {
	peers := make([]string, n)
	for i := range peers {
		peers[i] = fmt.Sprintf("http://127.0.0.1:%d", 10100+i)
	}

	clus := &etcdCluster{
		peers:            peers,
		commitC:          make([]<-chan *kvstore.Commit, len(peers)),
		errorC:           make([]<-chan error, len(peers)),
		proposeC:         make([]chan string, len(peers)),
		confChangeC:      make([]chan raftpb.ConfChange, len(peers)),
		snapshotterReady: make([]<-chan *snap.Snapshotter, len(peers)),
		kvStores:         make([]*memory.MemoryEtcdRaft, len(peers)),
		servers:          make([]*etcdcompat.Server, len(peers)),
		clients:          make([]*clientv3.Client, len(peers)),
	}

	// Create Raft nodes
	for i := range clus.peers {
		// Clean up data directory (raft.NewNode uses "data/memory/{id}" path)
		os.RemoveAll(fmt.Sprintf("data/memory/%d", i+1))
		clus.proposeC[i] = make(chan string, 1)
		clus.confChangeC[i] = make(chan raftpb.ConfChange, 1)

		var kvs *memory.MemoryEtcdRaft
		getSnapshot := func() ([]byte, error) {
			if kvs == nil {
				return nil, nil
			}
			return kvs.GetSnapshot()
		}
		clus.commitC[i], clus.errorC[i], clus.snapshotterReady[i] = raft.NewNode(
			i+1, clus.peers, false, getSnapshot, clus.proposeC[i], clus.confChangeC[i], "memory",
		)
	}

	// Create KV stores and etcd servers
	for i := range clus.peers {
		kvs := memory.NewMemoryEtcdRaft(
			<-clus.snapshotterReady[i],
			clus.proposeC[i],
			clus.commitC[i],
			clus.errorC[i],
		)
		clus.kvStores[i] = kvs

		// Find available port
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := listener.Addr().String()
		listener.Close()

		server, err := etcdcompat.NewServer(etcdcompat.ServerConfig{
			Store:     kvs,
			Address:   addr,
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = server

		// Start server in background
		go func(srv *etcdcompat.Server) {
			if err := srv.Start(); err != nil {
				t.Logf("Server start error: %v", err)
			}
		}(server)

		// Create client
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{addr},
			DialTimeout: 5 * time.Second,
		})
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// Wait for cluster to stabilize
	time.Sleep(3 * time.Second)

	return clus
}

// Close closes all cluster resources
func (clus *etcdCluster) Close(t *testing.T) {
	t.Log("closing etcd cluster...")

	// Close clients first
	for _, cli := range clus.clients {
		if cli != nil {
			cli.Close()
		}
	}

	// Stop servers
	for _, srv := range clus.servers {
		if srv != nil {
			srv.Stop()
		}
	}

	// Close Raft nodes
	for i := range clus.peers {
		// Drain pending commits
		go func(i int) {
			for range clus.commitC[i] {
				// drain
			}
		}(i)
		close(clus.proposeC[i])
		// Wait for channel to close
		<-clus.errorC[i]
		// Clean data (raft.NewNode uses "data/memory/{id}" path)
		os.RemoveAll(fmt.Sprintf("data/memory/%d", i+1))
	}

	t.Log("closing etcd cluster [done]")
}

// TestEtcdMemorySingleNodeOperations tests basic single-node operations
func TestEtcdMemorySingleNodeOperations(t *testing.T) {
	clus := newEtcdCluster(t, 1)
	defer clus.Close(t)

	ctx := context.Background()
	cli := clus.clients[0]

	t.Run("PutAndGet", func(t *testing.T) {
		_, err := cli.Put(ctx, "single-key", "single-value")
		require.NoError(t, err)

		resp, err := cli.Get(ctx, "single-key")
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		assert.Equal(t, "single-key", string(resp.Kvs[0].Key))
		assert.Equal(t, "single-value", string(resp.Kvs[0].Value))
	})

	t.Run("Delete", func(t *testing.T) {
		_, err := cli.Put(ctx, "delete-key", "delete-value")
		require.NoError(t, err)

		delResp, err := cli.Delete(ctx, "delete-key")
		require.NoError(t, err)
		assert.Equal(t, int64(1), delResp.Deleted)

		getResp, err := cli.Get(ctx, "delete-key")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0)
	})

	t.Run("RangeQuery", func(t *testing.T) {
		// Put multiple keys
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("range-key-%d", i)
			value := fmt.Sprintf("range-value-%d", i)
			_, err := cli.Put(ctx, key, value)
			require.NoError(t, err)
		}

		// Query with prefix
		resp, err := cli.Get(ctx, "range-key-", clientv3.WithPrefix())
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.Kvs), 5)
	})
}

// TestEtcdMemoryClusterBasicConsistency tests basic cluster consistency
func TestEtcdMemoryClusterBasicConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	t.Run("WriteToNode0ReadFromAll", func(t *testing.T) {
		key := "cluster-key-1"
		value := "cluster-value-1"

		// Write to node 0
		_, err := clus.clients[0].Put(ctx, key, value)
		require.NoError(t, err)

		// Wait for replication
		time.Sleep(2 * time.Second)

		// Read from all nodes and verify consistency
		for i := 0; i < numNodes; i++ {
			resp, err := clus.clients[i].Get(ctx, key)
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1, "Node %d should have the key", i)
			assert.Equal(t, value, string(resp.Kvs[0].Value),
				"Node %d should have the same value as written to node 0", i)
		}
	})

	t.Run("WriteToMultipleNodes", func(t *testing.T) {
		testData := []struct {
			node  int
			key   string
			value string
		}{
			{0, "multi-key-0", "multi-value-0"},
			{1, "multi-key-1", "multi-value-1"},
			{2, "multi-key-2", "multi-value-2"},
		}

		// Write different keys to different nodes
		for _, td := range testData {
			_, err := clus.clients[td.node].Put(ctx, td.key, td.value)
			require.NoError(t, err)
		}

		// Wait for replication
		time.Sleep(2 * time.Second)

		// Verify all nodes have all keys with correct values
		for _, td := range testData {
			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				resp, err := clus.clients[nodeIdx].Get(ctx, td.key)
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1,
					"Node %d should have key '%s'", nodeIdx, td.key)
				assert.Equal(t, td.value, string(resp.Kvs[0].Value),
					"Node %d should have key '%s' with value '%s' (written to node %d)",
					nodeIdx, td.key, td.value, td.node)
			}
		}
	})
}

// TestEtcdMemoryClusterConcurrentOperations tests concurrent operations
func TestEtcdMemoryClusterConcurrentOperations(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	t.Run("ConcurrentWrites", func(t *testing.T) {
		const numWrites = 10
		errChan := make(chan error, numWrites)

		// Concurrent writes to different nodes
		for i := 0; i < numWrites; i++ {
			go func(idx int) {
				nodeIdx := idx % numNodes
				key := fmt.Sprintf("concurrent-key-%d", idx)
				value := fmt.Sprintf("concurrent-value-%d", idx)

				_, err := clus.clients[nodeIdx].Put(ctx, key, value)
				errChan <- err
			}(i)
		}

		// Wait for all writes to complete
		for i := 0; i < numWrites; i++ {
			err := <-errChan
			require.NoError(t, err, "Concurrent write %d should succeed", i)
		}

		// Wait for replication
		time.Sleep(3 * time.Second)

		// Verify all nodes have all keys
		for i := 0; i < numWrites; i++ {
			key := fmt.Sprintf("concurrent-key-%d", i)
			expectedValue := fmt.Sprintf("concurrent-value-%d", i)

			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				resp, err := clus.clients[nodeIdx].Get(ctx, key)
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1,
					"Node %d should have key '%s' after concurrent writes", nodeIdx, key)
				assert.Equal(t, expectedValue, string(resp.Kvs[0].Value),
					"Node %d should have correct value for key '%s'", nodeIdx, key)
			}
		}
	})
}

// TestEtcdMemoryClusterUpdateOperations tests update operations on same key
func TestEtcdMemoryClusterUpdateOperations(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	t.Run("UpdateSameKeyFromDifferentNodes", func(t *testing.T) {
		key := "update-key"

		// Sequential updates from different nodes
		for i := 0; i < numNodes; i++ {
			value := fmt.Sprintf("update-value-%d", i)
			_, err := clus.clients[i].Put(ctx, key, value)
			require.NoError(t, err)

			// Wait for replication
			time.Sleep(2 * time.Second)

			// Verify all nodes have the latest value
			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				resp, err := clus.clients[nodeIdx].Get(ctx, key)
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1)
				assert.Equal(t, value, string(resp.Kvs[0].Value),
					"After update %d, node %d should have value '%s'", i, nodeIdx, value)
			}
		}
	})
}
