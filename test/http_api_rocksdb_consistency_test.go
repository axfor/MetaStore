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
	"bytes"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	httpapi "metaStore/internal/http"
	"metaStore/internal/raft"
	rocksdbstore "metaStore/internal/rocksdb"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// TestRocksDBClusterWriteReadConsistency tests write and read operations across a 3-node cluster
// using RocksDB storage engine via HTTP API to verify data consistency across all nodes
func TestHTTPAPIRocksDBClusterWriteReadConsistency(t *testing.T) {
	const numNodes = 3
	clus := newRocksDBCluster(numNodes)
	defer clus.closeNoErrors(t)

	// Create KV stores and HTTP servers for all nodes
	kvStores := make([]*rocksdbstore.RocksDB, numNodes)
	servers := make([]*httptest.Server, numNodes)

	for i := 0; i < numNodes; i++ {
		// Use the commitC, errorC, and snapshotterReady channels from the cluster
		snapshotter := <-clus.snapshotterReady[i]
		kvs := rocksdbstore.NewRocksDB(clus.dbs[i], fmt.Sprintf("node_%d", i+1), snapshotter, clus.proposeC[i], clus.commitC[i], clus.errorC[i])
		kvStores[i] = kvs
		servers[i] = httptest.NewServer(httpapi.NewHTTPKVAPI(kvs, clus.confChangeC[i]))
		defer servers[i].Close()
	}

	// Wait for cluster to stabilize
	time.Sleep(3 * time.Second)

	// Test 1: Write to node 0, read from all nodes
	t.Run("WriteToNode0ReadFromAll", func(t *testing.T) {
		key := "rocksdb-test-key-1"
		value := "rocksdb-test-value-1"

		// Write to node 0
		url := fmt.Sprintf("%s/%s", servers[0].URL, key)
		body := bytes.NewBufferString(value)
		req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err := servers[0].Client().Do(req)
		require.NoError(t, err)
		require.Equal(t, nethttp.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		// Wait for replication
		time.Sleep(2 * time.Second)

		// Read from all nodes and verify consistency
		for i := 0; i < numNodes; i++ {
			url := fmt.Sprintf("%s/%s", servers[i].URL, key)
			resp, err := servers[i].Client().Get(url)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, value, string(data),
				"RocksDB Node %d should have the same value as written to node 0", i)
		}
	})

	// Test 2: Write to different nodes, verify consistency
	t.Run("WriteToMultipleNodes", func(t *testing.T) {
		testData := []struct {
			node  int
			key   string
			value string
		}{
			{0, "rocksdb-key-from-node-0", "rocksdb-value-0"},
			{1, "rocksdb-key-from-node-1", "rocksdb-value-1"},
			{2, "rocksdb-key-from-node-2", "rocksdb-value-2"},
		}

		// Write different keys to different nodes
		for _, td := range testData {
			url := fmt.Sprintf("%s/%s", servers[td.node].URL, td.key)
			body := bytes.NewBufferString(td.value)
			req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
			require.NoError(t, err)
			resp, err := servers[td.node].Client().Do(req)
			require.NoError(t, err)
			require.Equal(t, nethttp.StatusNoContent, resp.StatusCode)
			resp.Body.Close()
		}

		// Wait for replication
		time.Sleep(2 * time.Second)

		// Verify all nodes have all keys with correct values
		for _, td := range testData {
			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, td.key)
				resp, err := servers[nodeIdx].Client().Get(url)
				require.NoError(t, err)
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				resp.Body.Close()
				require.Equal(t, td.value, string(data),
					"RocksDB Node %d should have key '%s' with value '%s' (written to node %d)",
					nodeIdx, td.key, td.value, td.node)
			}
		}
	})

	// Test 3: Concurrent writes to different nodes
	t.Run("ConcurrentWrites", func(t *testing.T) {
		const numWrites = 10
		var wg sync.WaitGroup

		// Concurrent writes to different nodes
		for i := 0; i < numWrites; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				nodeIdx := idx % numNodes
				key := fmt.Sprintf("rocksdb-concurrent-key-%d", idx)
				value := fmt.Sprintf("rocksdb-concurrent-value-%d", idx)

				url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, key)
				body := bytes.NewBufferString(value)
				req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
				if err != nil {
					t.Errorf("Failed to create request: %v", err)
					return
				}
				resp, err := servers[nodeIdx].Client().Do(req)
				if err != nil {
					t.Errorf("Failed to write: %v", err)
					return
				}
				resp.Body.Close()
			}(i)
		}

		wg.Wait()

		// Wait for all writes to replicate
		time.Sleep(3 * time.Second)

		// Verify all nodes have all concurrent writes
		for i := 0; i < numWrites; i++ {
			key := fmt.Sprintf("rocksdb-concurrent-key-%d", i)
			expectedValue := fmt.Sprintf("rocksdb-concurrent-value-%d", i)

			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, key)
				resp, err := servers[nodeIdx].Client().Get(url)
				require.NoError(t, err)
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				resp.Body.Close()
				require.Equal(t, expectedValue, string(data),
					"RocksDB Node %d should have concurrent key '%s'", nodeIdx, key)
			}
		}
	})

	// Test 4: Update same key from different nodes
	t.Run("UpdateSameKeyFromDifferentNodes", func(t *testing.T) {
		key := "rocksdb-update-test-key"

		// Write initial value from node 0
		url := fmt.Sprintf("%s/%s", servers[0].URL, key)
		body := bytes.NewBufferString("rocksdb-initial-value")
		req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err := servers[0].Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(1 * time.Second)

		// Update from node 1
		url = fmt.Sprintf("%s/%s", servers[1].URL, key)
		body = bytes.NewBufferString("rocksdb-updated-from-node-1")
		req, err = nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err = servers[1].Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(1 * time.Second)

		// Update from node 2
		url = fmt.Sprintf("%s/%s", servers[2].URL, key)
		body = bytes.NewBufferString("rocksdb-final-value-from-node-2")
		req, err = nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err = servers[2].Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(2 * time.Second)

		// Verify all nodes have the final value
		finalValue := ""
		for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
			url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, key)
			resp, err := servers[nodeIdx].Client().Get(url)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			resp.Body.Close()

			if nodeIdx == 0 {
				finalValue = string(data)
			} else {
				require.Equal(t, finalValue, string(data),
					"All RocksDB nodes should have the same final value for key '%s'", key)
			}
		}

		// The final value should be one of the updates
		require.Contains(t, []string{
			"rocksdb-initial-value",
			"rocksdb-updated-from-node-1",
			"rocksdb-final-value-from-node-2",
		}, finalValue, "Final value should be one of the written values")
	})
}

// TestRocksDBClusterDataConsistencyAfterWrites verifies that after multiple writes,
// all nodes in the RocksDB cluster have identical data
func TestHTTPAPIRocksDBClusterDataConsistencyAfterWrites(t *testing.T) {
	const numNodes = 3
	const numKeys = 20

	clus := newRocksDBCluster(numNodes)
	defer clus.closeNoErrors(t)

	// Setup stores and servers
	kvStores := make([]*rocksdbstore.RocksDB, numNodes)
	servers := make([]*httptest.Server, numNodes)

	for i := 0; i < numNodes; i++ {
		snapshotter := <-clus.snapshotterReady[i]
		kvs := rocksdbstore.NewRocksDB(clus.dbs[i], fmt.Sprintf("node_%d", i+1), snapshotter, clus.proposeC[i], clus.commitC[i], clus.errorC[i])
		kvStores[i] = kvs
		servers[i] = httptest.NewServer(httpapi.NewHTTPKVAPI(kvs, clus.confChangeC[i]))
		defer servers[i].Close()
	}

	time.Sleep(3 * time.Second)

	// Write keys distributed across nodes
	for i := 0; i < numKeys; i++ {
		nodeIdx := i % numNodes
		key := fmt.Sprintf("rocksdb-consistency-key-%d", i)
		value := fmt.Sprintf("rocksdb-consistency-value-%d", i)

		url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, key)
		body := bytes.NewBufferString(value)
		req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err := servers[nodeIdx].Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		// Small delay between writes
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for all writes to propagate
	time.Sleep(3 * time.Second)

	// Build expected dataset
	expectedData := make(map[string]string)
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("rocksdb-consistency-key-%d", i)
		value := fmt.Sprintf("rocksdb-consistency-value-%d", i)
		expectedData[key] = value
	}

	// Verify all nodes have identical data
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		t.Run(fmt.Sprintf("VerifyRocksDBNode%d", nodeIdx), func(t *testing.T) {
			for key, expectedValue := range expectedData {
				url := fmt.Sprintf("%s/%s", servers[nodeIdx].URL, key)
				resp, err := servers[nodeIdx].Client().Get(url)
				require.NoError(t, err)
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				resp.Body.Close()
				require.Equal(t, expectedValue, string(data),
					"RocksDB Node %d: key '%s' should have value '%s'",
					nodeIdx, key, expectedValue)
			}
		})
	}
}

// TestRocksDBSingleNodeWriteRead tests basic write and read operations on a single RocksDB node via HTTP API
func TestHTTPAPIRocksDBSingleNodeWriteRead(t *testing.T) {
	// Clean up previous test data
	os.RemoveAll("data/1")
	defer os.RemoveAll("data/1")

	// Ensure data directory exists
	os.MkdirAll("data", 0755)

	clusters := []string{"http://127.0.0.1:9121"}

	proposeC := make(chan string)
	defer close(proposeC)

	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	// Open RocksDB - use data/1 to match raft's expectations
	db, err := rocksdbstore.Open("data/1")
	require.NoError(t, err)
	defer db.Close()

	var kvs *rocksdbstore.RocksDB
	getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
	commitC, errorC, snapshotterReady := raft.NewNodeRocksDB(1, clusters, false, getSnapshot, proposeC, confChangeC, db)

	snapshotter := <-snapshotterReady
	kvs = rocksdbstore.NewRocksDB(db, "node_1", snapshotter, proposeC, commitC, errorC)

	srv := httptest.NewServer(httpapi.NewHTTPKVAPI(kvs, confChangeC))
	defer srv.Close()

	time.Sleep(2 * time.Second)

	// Test multiple key-value pairs
	testData := []struct {
		key   string
		value string
	}{
		{"rocksdb-user:1:name", "Alice"},
		{"rocksdb-user:1:age", "30"},
		{"rocksdb-user:2:name", "Bob"},
		{"rocksdb-config:timeout", "5000"},
		{"rocksdb-config:retries", "3"},
	}

	// Write all key-value pairs
	for _, td := range testData {
		url := fmt.Sprintf("%s/%s", srv.URL, td.key)
		body := bytes.NewBufferString(td.value)
		req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		require.Equal(t, nethttp.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		time.Sleep(200 * time.Millisecond)
	}

	// Read and verify all key-value pairs
	for _, td := range testData {
		url := fmt.Sprintf("%s/%s", srv.URL, td.key)
		resp, err := srv.Client().Get(url)
		require.NoError(t, err)
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, td.value, string(data),
			"RocksDB Key '%s' should have value '%s'", td.key, td.value)
	}

	// Test reading non-existent key
	url := fmt.Sprintf("%s/%s", srv.URL, "rocksdb-non-existent-key")
	resp, err := srv.Client().Get(url)
	require.NoError(t, err)
	require.Equal(t, nethttp.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestRocksDBSingleNodeSequentialWrites tests sequential write operations on RocksDB via HTTP API
func TestHTTPAPIRocksDBSingleNodeSequentialWrites(t *testing.T) {
	// Clean up previous test data
	os.RemoveAll("data/1")
	defer os.RemoveAll("data/1")

	// Ensure data directory exists
	os.MkdirAll("data", 0755)

	clusters := []string{"http://127.0.0.1:9122"}

	proposeC := make(chan string)
	defer close(proposeC)

	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	// Open RocksDB - use data/1 to match raft's expectations
	db, err := rocksdbstore.Open("data/1")
	require.NoError(t, err)
	defer db.Close()

	var kvs *rocksdbstore.RocksDB
	getSnapshot := func() ([]byte, error) { return kvs.GetSnapshot() }
	commitC, errorC, snapshotterReady := raft.NewNodeRocksDB(1, clusters, false, getSnapshot, proposeC, confChangeC, db)

	snapshotter := <-snapshotterReady
	kvs = rocksdbstore.NewRocksDB(db, "node_1", snapshotter, proposeC, commitC, errorC)

	srv := httptest.NewServer(httpapi.NewHTTPKVAPI(kvs, confChangeC))
	defer srv.Close()

	time.Sleep(2 * time.Second)

	// Test sequential updates to same key
	key := "rocksdb-counter"
	values := []string{"1", "2", "3", "4", "5"}

	for _, value := range values {
		url := fmt.Sprintf("%s/%s", srv.URL, key)
		body := bytes.NewBufferString(value)
		req, err := nethttp.NewRequest(nethttp.MethodPut, url, body)
		require.NoError(t, err)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(300 * time.Millisecond)

		// Verify the value was updated
		resp, err = srv.Client().Get(url)
		require.NoError(t, err)
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, value, string(data),
			"RocksDB: After update, key should have value '%s'", value)
	}
}
