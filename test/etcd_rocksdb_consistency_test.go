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
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEtcdRocksDBClusterDataConsistencyAfterWrites tests data consistency after writes
func TestEtcdRocksDBClusterDataConsistencyAfterWrites(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Write keys to different nodes
	testData := map[string]string{
		"consistency-key-1": "consistency-value-1",
		"consistency-key-2": "consistency-value-2",
		"consistency-key-3": "consistency-value-3",
		"consistency-key-4": "consistency-value-4",
		"consistency-key-5": "consistency-value-5",
	}

	nodeIdx := 0
	for key, value := range testData {
		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err)
		nodeIdx = (nodeIdx + 1) % numNodes
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Verify all nodes have the same data
	for i := 0; i < numNodes; i++ {
		t.Run(fmt.Sprintf("VerifyNode%d", i), func(t *testing.T) {
			for key, expectedValue := range testData {
				resp, err := clus.clients[i].Get(ctx, key)
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1,
					"Node %d should have key '%s'", i, key)
				assert.Equal(t, expectedValue, string(resp.Kvs[0].Value),
					"Node %d should have correct value for key '%s'", i, key)
			}
		})
	}
}

// TestEtcdRocksDBClusterSequentialWrites tests sequential writes to cluster
func TestEtcdRocksDBClusterSequentialWrites(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()
	const numWrites = 20

	// Perform sequential writes to different nodes (round-robin)
	for i := 0; i < numWrites; i++ {
		nodeIdx := i % numNodes
		key := fmt.Sprintf("seq-key-%d", i)
		value := fmt.Sprintf("seq-value-%d", i)

		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Wait for all writes to replicate
	time.Sleep(3 * time.Second)

	// Verify all nodes see all writes
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		t.Run(fmt.Sprintf("VerifyNode%d", nodeIdx), func(t *testing.T) {
			for i := 0; i < numWrites; i++ {
				key := fmt.Sprintf("seq-key-%d", i)
				expectedValue := fmt.Sprintf("seq-value-%d", i)

				resp, err := clus.clients[nodeIdx].Get(ctx, key)
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1,
					"Node %d should have key '%s'", nodeIdx, key)
				assert.Equal(t, expectedValue, string(resp.Kvs[0].Value),
					"Node %d should have correct value for key '%s'", nodeIdx, key)
			}
		})
	}
}

// TestEtcdRocksDBClusterRangeQueryConsistency tests range query consistency
func TestEtcdRocksDBClusterRangeQueryConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Write range of keys with common prefix
	const numKeys = 10
	prefix := "range-"
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("%skey-%02d", prefix, i)
		value := fmt.Sprintf("value-%02d", i)
		nodeIdx := i % numNodes
		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Query with prefix from each node and verify results are consistent
	var results [][]string
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		resp, err := clus.clients[nodeIdx].Get(ctx, prefix, clientv3.WithPrefix())
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(resp.Kvs), numKeys,
			"Node %d should have at least %d keys with prefix '%s'", nodeIdx, numKeys, prefix)

		// Collect keys for comparison
		keys := make([]string, len(resp.Kvs))
		for i, kv := range resp.Kvs {
			keys[i] = string(kv.Key)
		}
		results = append(results, keys)
	}

	// Verify all nodes return the same keys
	for i := 1; i < numNodes; i++ {
		assert.ElementsMatch(t, results[0], results[i],
			"Node %d should return same keys as node 0", i)
	}
}

// TestEtcdRocksDBClusterDeleteConsistency tests delete operation consistency
func TestEtcdRocksDBClusterDeleteConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Setup: Write keys to all nodes
	testKeys := []string{"del-key-1", "del-key-2", "del-key-3"}
	for _, key := range testKeys {
		_, err := clus.clients[0].Put(ctx, key, "value")
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Verify all nodes have the keys
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		for _, key := range testKeys {
			resp, err := clus.clients[nodeIdx].Get(ctx, key)
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1,
				"Node %d should have key '%s' before deletion", nodeIdx, key)
		}
	}

	// Delete keys from different nodes
	for i, key := range testKeys {
		nodeIdx := i % numNodes
		_, err := clus.clients[nodeIdx].Delete(ctx, key)
		require.NoError(t, err)
	}

	// Wait for delete replication
	time.Sleep(2 * time.Second)

	// Verify all nodes no longer have the keys
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		for _, key := range testKeys {
			resp, err := clus.clients[nodeIdx].Get(ctx, key)
			require.NoError(t, err)
			assert.Len(t, resp.Kvs, 0,
				"Node %d should not have deleted key '%s'", nodeIdx, key)
		}
	}
}

// TestEtcdRocksDBClusterConcurrentMixedOperations tests mixed concurrent operations
func TestEtcdRocksDBClusterConcurrentMixedOperations(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()
	const numOps = 30
	var wg sync.WaitGroup
	errChan := make(chan error, numOps)

	// Concurrent mixed operations (Put, Get, Delete)
	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			nodeIdx := idx % numNodes

			switch idx % 3 {
			case 0:
				// Put operation
				key := fmt.Sprintf("mixed-key-%d", idx)
				value := fmt.Sprintf("mixed-value-%d", idx)
				_, err := clus.clients[nodeIdx].Put(ctx, key, value)
				errChan <- err

			case 1:
				// Get operation (on previously written keys)
				if idx >= 3 {
					key := fmt.Sprintf("mixed-key-%d", idx-3)
					_, err := clus.clients[nodeIdx].Get(ctx, key)
					errChan <- err
				} else {
					errChan <- nil
				}

			case 2:
				// Delete operation (on even older keys)
				if idx >= 6 {
					key := fmt.Sprintf("mixed-key-%d", idx-6)
					_, err := clus.clients[nodeIdx].Delete(ctx, key)
					errChan <- err
				} else {
					errChan <- nil
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check all operations succeeded
	for err := range errChan {
		require.NoError(t, err, "Mixed concurrent operation should succeed")
	}

	// Wait for operations to propagate
	time.Sleep(3 * time.Second)

	// Basic sanity check: verify cluster is still consistent
	// Write a test key to one node and verify all nodes can read it
	testKey := "sanity-check-key"
	testValue := "sanity-check-value"
	_, err := clus.clients[0].Put(ctx, testKey, testValue)
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		resp, err := clus.clients[nodeIdx].Get(ctx, testKey)
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1,
			"Node %d should have sanity check key after mixed operations", nodeIdx)
		assert.Equal(t, testValue, string(resp.Kvs[0].Value),
			"Node %d should have correct sanity check value", nodeIdx)
	}
}

// TestEtcdRocksDBClusterRevisionConsistency tests revision consistency across nodes
func TestEtcdRocksDBClusterRevisionConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Perform several writes
	const numWrites = 5
	for i := 0; i < numWrites; i++ {
		key := fmt.Sprintf("rev-key-%d", i)
		value := fmt.Sprintf("rev-value-%d", i)
		nodeIdx := i % numNodes
		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Get revision from each node
	var revisions []int64
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		// Get any key to retrieve current revision
		resp, err := clus.clients[nodeIdx].Get(ctx, "rev-key-0")
		require.NoError(t, err)
		revisions = append(revisions, resp.Header.Revision)
		t.Logf("Node %d revision: %d", nodeIdx, resp.Header.Revision)
	}

	// Verify revisions are close (within small delta due to timing)
	// All nodes should have at least numWrites revisions
	for nodeIdx, rev := range revisions {
		assert.GreaterOrEqual(t, rev, int64(numWrites),
			"Node %d should have revision >= %d", nodeIdx, numWrites)
	}

	// Revisions should be very close (within 2 of each other)
	minRev := revisions[0]
	maxRev := revisions[0]
	for _, rev := range revisions[1:] {
		if rev < minRev {
			minRev = rev
		}
		if rev > maxRev {
			maxRev = rev
		}
	}

	assert.LessOrEqual(t, maxRev-minRev, int64(2),
		"Revision difference between nodes should be <= 2")
}

// TestEtcdRocksDBClusterTransactionConsistency tests transaction consistency across cluster
func TestEtcdRocksDBClusterTransactionConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdRocksDBCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Setup initial data
	_, err := clus.clients[0].Put(ctx, "txn-key", "initial-value")
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Execute transaction on node 1
	txn := clus.clients[1].Txn(ctx).
		If(clientv3.Compare(clientv3.Value("txn-key"), "=", "initial-value")).
		Then(clientv3.OpPut("txn-key", "updated-value")).
		Else(clientv3.OpPut("txn-key", "failed-value"))

	txnResp, err := txn.Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded, "Transaction should succeed")

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Verify all nodes see the updated value
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		resp, err := clus.clients[nodeIdx].Get(ctx, "txn-key")
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		assert.Equal(t, "updated-value", string(resp.Kvs[0].Value),
			"Node %d should have transaction result", nodeIdx)
	}
}
