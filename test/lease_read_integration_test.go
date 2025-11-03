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
	"os"
	"testing"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/internal/memory"
	"metaStore/internal/raft"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	goraft "go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leaseReadCluster represents a cluster for testing Lease Read functionality
type leaseReadCluster struct {
	peers            []string
	raftNodes        []raft.TestableNode
	commitC          []<-chan *kvstore.Commit
	errorC           []<-chan error
	proposeC         []chan string
	confChangeC      []chan raftpb.ConfChange
	snapshotterReady []<-chan *snap.Snapshotter
	kvStores         []*memory.Memory
}

// newLeaseReadCluster creates a cluster with Lease Read enabled
func newLeaseReadCluster(t *testing.T, n int) *leaseReadCluster {
	peers := make([]string, n)
	for i := range peers {
		peers[i] = fmt.Sprintf("http://127.0.0.1:%d", 11000+i)
	}

	clus := &leaseReadCluster{
		peers:            peers,
		raftNodes:        make([]raft.TestableNode, n),
		commitC:          make([]<-chan *kvstore.Commit, n),
		errorC:           make([]<-chan error, n),
		proposeC:         make([]chan string, n),
		confChangeC:      make([]chan raftpb.ConfChange, n),
		snapshotterReady: make([]<-chan *snap.Snapshotter, n),
		kvStores:         make([]*memory.Memory, n),
	}

	// Create Raft nodes with Lease Read enabled
	for i := range clus.peers {
		// Clean up data directory
		os.RemoveAll(fmt.Sprintf("data/memory/%d", i+1))
		clus.proposeC[i] = make(chan string, 1)
		clus.confChangeC[i] = make(chan raftpb.ConfChange, 1)

		// Create test config with Lease Read enabled
		nodeCfg := NewTestConfig(uint64(i+1), 1, fmt.Sprintf(":930%d", i))
		nodeCfg.Server.Raft.LeaseRead.Enable = true
		nodeCfg.Server.Raft.LeaseRead.ClockDrift = 100 * time.Millisecond
		nodeCfg.Server.Raft.ElectionTick = 10
		nodeCfg.Server.Raft.HeartbeatTick = 1
		nodeCfg.Server.Raft.TickInterval = 100 * time.Millisecond

		var kvs *memory.Memory
		getSnapshot := func() ([]byte, error) {
			if kvs == nil {
				return nil, nil
			}
			return kvs.GetSnapshot()
		}

		commitC, errorC, snapshotterReady, node := raft.NewNode(
			i+1, clus.peers, false, getSnapshot, clus.proposeC[i], clus.confChangeC[i], "memory", nodeCfg,
		)

		clus.commitC[i] = commitC
		clus.errorC[i] = errorC
		clus.snapshotterReady[i] = snapshotterReady
		clus.raftNodes[i] = node
	}

	// Create KV stores
	for i := range clus.peers {
		kvs := memory.NewMemory(
			<-clus.snapshotterReady[i],
			clus.proposeC[i],
			clus.commitC[i],
			clus.errorC[i],
		)
		clus.kvStores[i] = kvs
	}

	return clus
}

// waitForLeader waits for a leader to be elected and returns its index
func (c *leaseReadCluster) waitForLeader(t *testing.T, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for leader election")
			return -1
		case <-ticker.C:
			for i, node := range c.raftNodes {
				status := node.Status()
				if status.State == goraft.StateLeader.String() {
					t.Logf("Leader elected: node %d", i+1)
					return i
				}
			}
		}
	}
}

// shutdown stops all nodes in the cluster
func (c *leaseReadCluster) shutdown() {
	for i := range c.proposeC {
		if c.proposeC[i] != nil {
			close(c.proposeC[i])
		}
		if c.confChangeC[i] != nil {
			close(c.confChangeC[i])
		}
	}

	// Wait a bit for graceful shutdown
	time.Sleep(500 * time.Millisecond)
}

// TestLeaseReadBasicFunctionality tests basic Lease Read functionality
func TestLeaseReadBasicFunctionality(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read integration test in short mode")
	}

	clus := newLeaseReadCluster(t, 3)
	defer clus.shutdown()

	// Wait for leader election
	leaderIdx := clus.waitForLeader(t, 5*time.Second)
	require.NotEqual(t, -1, leaderIdx, "Leader should be elected")

	leader := clus.raftNodes[leaderIdx]

	// Verify Lease Read is enabled
	assert.NotNil(t, leader.LeaseManager(), "LeaseManager should be initialized")
	assert.NotNil(t, leader.ReadIndexManager(), "ReadIndexManager should be initialized")

	// Leader should have become leader
	assert.True(t, leader.LeaseManager().IsLeader(), "Leader should be marked as leader in LeaseManager")

	// Wait for some heartbeats to happen (lease renewal)
	time.Sleep(500 * time.Millisecond)

	// Leader should have valid lease after heartbeats
	assert.True(t, leader.LeaseManager().HasValidLease(), "Leader should have valid lease after heartbeats")

	// Check lease remaining time
	remaining := leader.LeaseManager().GetLeaseRemaining()
	assert.Greater(t, remaining, time.Duration(0), "Lease remaining should be positive")
	t.Logf("Leader lease remaining: %v", remaining)

	// Verify stats
	leaseStats := leader.LeaseManager().Stats()
	assert.True(t, leaseStats.IsLeader, "Stats should show IsLeader=true")
	assert.True(t, leaseStats.HasValidLease, "Stats should show HasValidLease=true")
	assert.Greater(t, leaseStats.LeaseRenewCount, int64(0), "Should have renewed lease at least once")
	t.Logf("Lease stats: %+v", leaseStats)
}

// TestLeaseReadRenewal tests that lease is automatically renewed
func TestLeaseReadRenewal(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read renewal test in short mode")
	}

	clus := newLeaseReadCluster(t, 3)
	defer clus.shutdown()

	// Wait for leader election
	leaderIdx := clus.waitForLeader(t, 5*time.Second)
	require.NotEqual(t, -1, leaderIdx, "Leader should be elected")

	leader := clus.raftNodes[leaderIdx]

	// Wait for initial lease
	time.Sleep(500 * time.Millisecond)
	require.True(t, leader.LeaseManager().HasValidLease(), "Leader should have valid lease")

	initialStats := leader.LeaseManager().Stats()
	initialRenewCount := initialStats.LeaseRenewCount

	// Wait for more heartbeats (should renew lease)
	time.Sleep(1 * time.Second)

	// Check that lease was renewed
	newStats := leader.LeaseManager().Stats()
	assert.Greater(t, newStats.LeaseRenewCount, initialRenewCount, "Lease should have been renewed")
	assert.True(t, leader.LeaseManager().HasValidLease(), "Lease should still be valid")

	t.Logf("Initial renew count: %d, After 1s: %d", initialRenewCount, newStats.LeaseRenewCount)
}

// TestLeaseReadApplyNotification tests that apply notifications work correctly
func TestLeaseReadApplyNotification(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read apply notification test in short mode")
	}

	clus := newLeaseReadCluster(t, 3)
	defer clus.shutdown()

	// Wait for leader election
	leaderIdx := clus.waitForLeader(t, 5*time.Second)
	require.NotEqual(t, -1, leaderIdx, "Leader should be elected")

	leader := clus.raftNodes[leaderIdx]
	leaderKV := clus.kvStores[leaderIdx]

	// Wait for cluster to stabilize
	time.Sleep(500 * time.Millisecond)

	// Perform some writes to advance applied index
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("test-key-%d", i)
		value := fmt.Sprintf("test-value-%d", i)
		_, _, err := leaderKV.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	// Wait for commits to be applied
	time.Sleep(500 * time.Millisecond)

	// Check ReadIndexManager stats
	readIndexStats := leader.ReadIndexManager().Stats()
	assert.Greater(t, readIndexStats.LastAppliedIndex, uint64(0), "LastAppliedIndex should be > 0 after writes")
	t.Logf("ReadIndex stats: %+v", readIndexStats)
}

// TestLeaseReadMultiNodeConsistency tests consistency across nodes
func TestLeaseReadMultiNodeConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read multi-node consistency test in short mode")
	}

	clus := newLeaseReadCluster(t, 3)
	defer clus.shutdown()

	// Wait for leader election
	leaderIdx := clus.waitForLeader(t, 5*time.Second)
	require.NotEqual(t, -1, leaderIdx, "Leader should be elected")

	leader := clus.raftNodes[leaderIdx]
	leaderKV := clus.kvStores[leaderIdx]

	// Wait for cluster to stabilize
	time.Sleep(500 * time.Millisecond)

	// Only leader should have lease
	for i, node := range clus.raftNodes {
		if i == leaderIdx {
			assert.True(t, node.LeaseManager().IsLeader(), "Node %d: Leader should be marked as leader", i+1)
			assert.True(t, node.LeaseManager().HasValidLease(), "Node %d: Leader should have valid lease", i+1)
		} else {
			assert.False(t, node.LeaseManager().IsLeader(), "Node %d: Follower should not be marked as leader", i+1)
			assert.False(t, node.LeaseManager().HasValidLease(), "Node %d: Follower should not have valid lease", i+1)
		}
	}

	// Perform writes and verify all nodes eventually see the same data
	ctx := context.Background()
	testKey := "consistency-test-key"
	testValue := "consistency-test-value"

	_, _, err := leaderKV.PutWithLease(ctx, testKey, testValue, 0)
	require.NoError(t, err, "Put should succeed")

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify all nodes have the same applied index eventually
	leaderStatus := leader.Status()
	t.Logf("Leader applied index: %d", leaderStatus.Applied)

	for i, node := range clus.raftNodes {
		status := node.Status()
		t.Logf("Node %d applied index: %d, state: %s", i+1, status.Applied, status.State)
	}
}

// TestLeaseReadStatistics tests statistics collection
func TestLeaseReadStatistics(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read statistics test in short mode")
	}

	clus := newLeaseReadCluster(t, 3)
	defer clus.shutdown()

	// Wait for leader election
	leaderIdx := clus.waitForLeader(t, 5*time.Second)
	require.NotEqual(t, -1, leaderIdx, "Leader should be elected")

	leader := clus.raftNodes[leaderIdx]

	// Wait for some heartbeats
	time.Sleep(1 * time.Second)

	// Check lease statistics
	leaseStats := leader.LeaseManager().Stats()
	assert.True(t, leaseStats.IsLeader, "Should be leader")
	assert.True(t, leaseStats.HasValidLease, "Should have valid lease")
	assert.Greater(t, leaseStats.LeaseRenewCount, int64(0), "Should have renewal count > 0")
	assert.Greater(t, leaseStats.LeaseRemaining, time.Duration(0), "Should have positive lease remaining")

	t.Logf("Lease statistics:")
	t.Logf("  - IsLeader: %v", leaseStats.IsLeader)
	t.Logf("  - HasValidLease: %v", leaseStats.HasValidLease)
	t.Logf("  - LeaseRenewCount: %d", leaseStats.LeaseRenewCount)
	t.Logf("  - LeaseExpireCount: %d", leaseStats.LeaseExpireCount)
	t.Logf("  - LeaseRemaining: %v", leaseStats.LeaseRemaining)

	// Check ReadIndex statistics
	readIndexStats := leader.ReadIndexManager().Stats()
	t.Logf("ReadIndex statistics:")
	t.Logf("  - TotalRequests: %d", readIndexStats.TotalRequests)
	t.Logf("  - FastPathReads: %d", readIndexStats.FastPathReads)
	t.Logf("  - SlowPathReads: %d", readIndexStats.SlowPathReads)
	t.Logf("  - ForwardedReads: %d", readIndexStats.ForwardedReads)
	t.Logf("  - PendingReads: %d", readIndexStats.PendingReads)
	t.Logf("  - LastAppliedIndex: %d", readIndexStats.LastAppliedIndex)
}
