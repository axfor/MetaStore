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

// TestEtcdMemoryClusterDataConsistencyAfterWrites verifies data consistency
// across all nodes after various write patterns
func TestEtcdMemoryClusterDataConsistencyAfterWrites(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Perform various write operations
	testData := []struct {
		node  int
		key   string
		value string
	}{
		{0, "consistency-key-1", "value-1"},
		{1, "consistency-key-2", "value-2"},
		{2, "consistency-key-3", "value-3"},
		{0, "consistency-key-4", "value-4"},
		{1, "consistency-key-5", "value-5"},
	}

	// Write data
	for _, td := range testData {
		_, err := clus.clients[td.node].Put(ctx, td.key, td.value)
		require.NoError(t, err)
	}

	// Wait for full replication
	time.Sleep(3 * time.Second)

	// Verify all nodes have identical data
	t.Run("VerifyNode0", func(t *testing.T) {
		for _, td := range testData {
			resp, err := clus.clients[0].Get(ctx, td.key)
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1)
			assert.Equal(t, td.value, string(resp.Kvs[0].Value))
		}
	})

	t.Run("VerifyNode1", func(t *testing.T) {
		for _, td := range testData {
			resp, err := clus.clients[1].Get(ctx, td.key)
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1)
			assert.Equal(t, td.value, string(resp.Kvs[0].Value))
		}
	})

	t.Run("VerifyNode2", func(t *testing.T) {
		for _, td := range testData {
			resp, err := clus.clients[2].Get(ctx, td.key)
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1)
			assert.Equal(t, td.value, string(resp.Kvs[0].Value))
		}
	})
}

// TestEtcdMemoryClusterSequentialWrites tests sequential writes and reads
func TestEtcdMemoryClusterSequentialWrites(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()
	const numWrites = 20

	// Sequential writes to round-robin nodes
	for i := 0; i < numWrites; i++ {
		nodeIdx := i % numNodes
		key := fmt.Sprintf("seq-key-%d", i)
		value := fmt.Sprintf("seq-value-%d", i)

		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err, "Write %d to node %d should succeed", i, nodeIdx)
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Verify all writes are visible on all nodes
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

// TestEtcdMemoryClusterRangeQueryConsistency tests range query consistency
func TestEtcdMemoryClusterRangeQueryConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Write keys with common prefix to different nodes
	prefix := "range-test-"
	for i := 0; i < 10; i++ {
		nodeIdx := i % numNodes
		key := fmt.Sprintf("%skey-%d", prefix, i)
		value := fmt.Sprintf("value-%d", i)

		_, err := clus.clients[nodeIdx].Put(ctx, key, value)
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Query range from each node and verify results match
	var allResults [][]string
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		resp, err := clus.clients[nodeIdx].Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
		require.NoError(t, err)

		results := make([]string, len(resp.Kvs))
		for i, kv := range resp.Kvs {
			results[i] = fmt.Sprintf("%s=%s", string(kv.Key), string(kv.Value))
		}
		allResults = append(allResults, results)
	}

	// Verify all nodes return the same results
	for i := 1; i < numNodes; i++ {
		assert.Equal(t, allResults[0], allResults[i],
			"Node %d should return same range query results as node 0", i)
	}
}

// TestEtcdMemoryClusterDeleteConsistency tests delete operation consistency
func TestEtcdMemoryClusterDeleteConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Create test data on all nodes
	keys := []string{"del-key-1", "del-key-2", "del-key-3"}
	for _, key := range keys {
		_, err := clus.clients[0].Put(ctx, key, "value")
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Delete keys from different nodes
	for i, key := range keys {
		nodeIdx := i % numNodes
		_, err := clus.clients[nodeIdx].Delete(ctx, key)
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Verify all keys are deleted on all nodes
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		for _, key := range keys {
			resp, err := clus.clients[nodeIdx].Get(ctx, key)
			require.NoError(t, err)
			assert.Len(t, resp.Kvs, 0,
				"Node %d should not have deleted key '%s'", nodeIdx, key)
		}
	}
}

// TestEtcdMemoryClusterTransactionConsistency tests transaction consistency
func TestEtcdMemoryClusterTransactionConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Setup initial data
	_, err := clus.clients[0].Put(ctx, "txn-key", "initial-value")
	require.NoError(t, err)

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


// TestEtcdMemoryClusterConcurrentMixedOperations tests mixed concurrent operations
func TestEtcdMemoryClusterConcurrentMixedOperations(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
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
			case 0: // Put
				key := fmt.Sprintf("mixed-key-%d", idx/3)
				value := fmt.Sprintf("mixed-value-%d", idx)
				_, err := clus.clients[nodeIdx].Put(ctx, key, value)
				errChan <- err

			case 1: // Get
				key := fmt.Sprintf("mixed-key-%d", (idx-1)/3)
				_, err := clus.clients[nodeIdx].Get(ctx, key)
				errChan <- err

			case 2: // Delete (but don't delete all)
				if idx > 15 {
					key := fmt.Sprintf("mixed-key-%d", (idx-2)/3-2)
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

	// Check for errors
	for err := range errChan {
		require.NoError(t, err, "Mixed operations should not fail")
	}

	// Wait for final state to replicate
	time.Sleep(3 * time.Second)

	// Verify final consistency - all nodes should agree on what keys exist
	for i := 0; i < numOps/3; i++ {
		key := fmt.Sprintf("mixed-key-%d", i)
		var nodeResults []bool

		for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
			resp, err := clus.clients[nodeIdx].Get(ctx, key)
			require.NoError(t, err)
			nodeResults = append(nodeResults, len(resp.Kvs) > 0)
		}

		// All nodes should agree on whether the key exists
		for j := 1; j < numNodes; j++ {
			assert.Equal(t, nodeResults[0], nodeResults[j],
				"All nodes should agree on existence of key '%s'", key)
		}
	}
}

// TestEtcdMemoryClusterRevisionConsistency tests revision number consistency
func TestEtcdMemoryClusterRevisionConsistency(t *testing.T) {
	const numNodes = 3
	clus := newEtcdCluster(t, numNodes)
	defer clus.Close(t)

	ctx := context.Background()

	// Perform several operations
	for i := 0; i < 5; i++ {
		nodeIdx := i % numNodes
		key := fmt.Sprintf("rev-key-%d", i)
		_, err := clus.clients[nodeIdx].Put(ctx, key, "value")
		require.NoError(t, err)
	}

	// Wait for replication
	time.Sleep(3 * time.Second)

	// Get revision from each node
	var revisions []int64
	for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
		resp, err := clus.clients[nodeIdx].Get(ctx, "rev-key-0")
		require.NoError(t, err)
		revisions = append(revisions, resp.Header.Revision)
	}

	// All nodes should have same or very close revision numbers
	// (might differ by 1-2 due to timing)
	maxRevision := revisions[0]
	minRevision := revisions[0]
	for _, rev := range revisions[1:] {
		if rev > maxRevision {
			maxRevision = rev
		}
		if rev < minRevision {
			minRevision = rev
		}
	}

	assert.LessOrEqual(t, maxRevision-minRevision, int64(2),
		"Revision numbers should be consistent across nodes (diff <= 2)")
}
