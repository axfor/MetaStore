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
	"net"
	"testing"
	"time"

	etcdapi "metaStore/api/etcd"
	"metaStore/internal/memory"

	clientv3 "go.etcd.io/etcd/client/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Single-node lease expiry tests (Memory)
// ============================================================================

// TestLeaseExpiry_SingleNode_Memory tests that a lease with short TTL expires on a single memory node.
func TestLeaseExpiry_SingleNode_Memory(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli.Close()

	// Grant lease with 2s TTL
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=2s", leaseID)

	// Put a key with the lease
	_, err = cli.Put(ctx, "expire-test/key1", "value1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Verify key exists immediately
	getResp, err := cli.Get(ctx, "expire-test/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 1, "key should exist before lease expires")

	// Wait for lease to expire (TTL=2s + check interval=1s + margin=1s)
	t.Log("Waiting for lease to expire...")
	time.Sleep(4 * time.Second)

	// Verify lease expired
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL, "expired lease should have TTL=-1")
	t.Logf("Lease TTL after expiry: %d", ttlResp.TTL)

	// Verify key was deleted when lease expired
	getResp, err = cli.Get(ctx, "expire-test/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key should be deleted after lease expired")
	t.Log("Single-node memory lease expiry: PASSED")
}

// ============================================================================
// Single-node lease expiry tests (Pebble)
// ============================================================================

// TestLeaseExpiry_SingleNode_Pebble tests lease expiry with the Pebble engine.
func TestLeaseExpiry_SingleNode_Pebble(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli.Close()

	// Grant lease with 2s TTL
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=2s (Pebble)", leaseID)

	// Put a key with the lease
	_, err = cli.Put(ctx, "expire-pebble/key1", "value1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Verify key exists
	getResp, err := cli.Get(ctx, "expire-pebble/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 1)

	// Wait for expiry
	t.Log("Waiting for lease to expire (Pebble)...")
	time.Sleep(4 * time.Second)

	// Verify expired
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL, "expired lease should have TTL=-1")

	// Verify key deleted
	getResp, err = cli.Get(ctx, "expire-pebble/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key should be deleted after lease expired (Pebble)")
	t.Log("Single-node Pebble lease expiry: PASSED")
}

// ============================================================================
// Single-node restart + lease expiry tests (Pebble)
// ============================================================================

// TestLeaseExpiry_AfterRestart_Pebble tests that a lease with remaining TTL
// properly expires after a Pebble server restart.
func TestLeaseExpiry_AfterRestart_Pebble(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Grant lease with 5s TTL
	leaseResp, err := cli.Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=5s (Pebble)", leaseID)

	// Put keys with the lease
	_, err = cli.Put(ctx, "restart-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = cli.Put(ctx, "restart-expire/key2", "v2", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Also create a long-lived lease
	longResp, err := cli.Grant(ctx, 120)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = cli.Put(ctx, "restart-expire/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	cli.Close()

	// Wait 2 seconds (partial TTL elapsed)
	t.Log("Waiting 2s before restart (partial TTL elapsed)...")
	time.Sleep(2 * time.Second)

	// Restart server (reuse same Pebble)
	t.Log("Restarting Pebble server...")
	node.server.Stop()
	time.Sleep(500 * time.Millisecond)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     node.pebbleKVStore,
		Address:   newAddr,
		ClusterID: 2000,
		MemberID:  uint64(node.id),
	})
	require.NoError(t, err)
	node.server = newServer
	node.clientAddr = newAddr

	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("Restarted server error: %v", err)
		}
	}()

	// Wait for remaining TTL to expire after restart
	// Original TTL=5s, already elapsed 2s before restart + 0.5s for restart
	// Remaining ~2.5s + check interval 1s + margin 1s = ~4.5s
	t.Log("Waiting for remaining TTL to expire after restart...")
	time.Sleep(5 * time.Second)

	// Verify with new client
	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Short-lived lease should have expired
	ttlResp, err := cli2.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL, "lease should have expired after restart")
	t.Logf("Short lease TTL after restart: %d", ttlResp.TTL)

	// Keys should be deleted
	getResp, err := cli2.Get(ctx, "restart-expire/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key1 should be deleted after lease expired post-restart")

	getResp, err = cli2.Get(ctx, "restart-expire/key2")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key2 should be deleted after lease expired post-restart")

	// Long-lived lease should still be alive
	ttlResp, err = cli2.TimeToLive(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0), "long-lived lease should still be alive")
	t.Logf("Long lease TTL after restart: %d", ttlResp.TTL)

	// Long-lived lease key should still exist
	getResp, err = cli2.Get(ctx, "restart-expire/long")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1, "long-lived lease key should survive")

	// KeepAlive on long lease should work
	kaResp, err := cli2.KeepAliveOnce(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, kaResp.TTL, int64(0))
	t.Log("Pebble restart + lease expiry: PASSED")
}

// ============================================================================
// 3-Node cluster lease expiry tests (Memory)
// ============================================================================

// TestLeaseExpiry_3NodeCluster_ViaLeader tests lease expiry in a 3-node cluster
// when the lease is created via the leader node.
func TestLeaseExpiry_3NodeCluster_ViaLeader(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Determine which node is the leader
	leaderIdx := -1
	for i := 0; i < 3; i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		// If no clear leader yet, default to node 0 which usually becomes leader
		leaderIdx = 0
		t.Log("No clear leader detected, using node 0")
	}
	t.Logf("Leader is node %d", leaderIdx)

	// Grant lease with 3s TTL via the LEADER
	leaseResp, err := clus.clients[leaderIdx].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease via leader (node %d), ID=%d, TTL=3s", leaderIdx, leaseID)

	// Put key with lease
	_, err = clus.clients[leaderIdx].Put(ctx, "cluster-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify key exists on all nodes
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "cluster-expire/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 1, "node %d should have the key", i)
	}

	// Wait for lease to expire (TTL=3s + check interval=1s + margin=2s)
	t.Log("Waiting for lease to expire in cluster...")
	time.Sleep(6 * time.Second)

	// Verify lease expired on leader
	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Leader (node %d) lease TTL after expiry: %d", leaderIdx, ttlResp.TTL)
	assert.Equal(t, int64(-1), ttlResp.TTL, "lease should have expired on leader")

	// Verify key was deleted on all nodes
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "cluster-expire/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key should be deleted after lease expired", i)
	}
	t.Log("3-node cluster lease expiry (via leader): PASSED")
}

// TestLeaseExpiry_3NodeCluster_ViaFollower tests lease expiry in a 3-node cluster
// when the lease is created via a FOLLOWER node.
// This test verifies whether the leader's expiry checker can detect and clean up
// leases that were not created through it.
func TestLeaseExpiry_3NodeCluster_ViaFollower(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Determine leader and pick a follower
	leaderIdx := -1
	followerIdx := -1
	for i := 0; i < 3; i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		leaderIdx = 0
	}
	// Pick a follower that is NOT the leader
	for i := 0; i < 3; i++ {
		if i != leaderIdx {
			followerIdx = i
			break
		}
	}
	t.Logf("Leader=node %d, Follower=node %d", leaderIdx, followerIdx)

	// Grant lease with 3s TTL via a FOLLOWER
	leaseResp, err := clus.clients[followerIdx].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease via FOLLOWER (node %d), ID=%d, TTL=3s", followerIdx, leaseID)

	// Put key with lease via the same follower
	_, err = clus.clients[followerIdx].Put(ctx, "follower-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify key exists on leader
	getResp, err := clus.clients[leaderIdx].Get(ctx, "follower-expire/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 1, "leader should have the key")

	// Wait for lease to expire (TTL=3s + check interval=1s + margin=3s)
	t.Log("Waiting for lease (granted via follower) to expire...")
	time.Sleep(7 * time.Second)

	// Verify lease expired
	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Leader (node %d) lease TTL after expiry: %d", leaderIdx, ttlResp.TTL)

	if ttlResp.TTL != -1 {
		t.Errorf("BUG: lease granted via follower did NOT expire! TTL=%d (expected -1)", ttlResp.TTL)
		t.Log("Root cause: leader's LeaseManager.leases in-memory cache does not contain")
		t.Log("leases granted through other nodes, so checkExpiredLeases() never finds them.")
	}

	// Verify key was deleted
	getResp, err = clus.clients[leaderIdx].Get(ctx, "follower-expire/key1")
	require.NoError(t, err)
	if len(getResp.Kvs) > 0 {
		t.Errorf("BUG: key still exists after lease should have expired (granted via follower)")
	} else {
		t.Log("3-node cluster lease expiry (via follower): PASSED")
	}
}

// ============================================================================
// 3-Node cluster lease expiry tests (Pebble)
// ============================================================================

// TestLeaseExpiry_3NodeCluster_Pebble tests lease expiry in a 3-node Pebble cluster.
func TestLeaseExpiry_3NodeCluster_Pebble(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Determine leader
	leaderIdx := -1
	for i := 0; i < 3; i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		leaderIdx = 0
	}

	// Grant lease with 3s TTL via leader
	leaseResp, err := clus.clients[leaderIdx].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease via leader (node %d), ID=%d, TTL=3s (Pebble)", leaderIdx, leaseID)

	// Put key with lease
	_, err = clus.clients[leaderIdx].Put(ctx, "pebble-cluster-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify key exists
	getResp, err := clus.clients[leaderIdx].Get(ctx, "pebble-cluster-expire/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 1)

	// Wait for expiry
	t.Log("Waiting for lease to expire (Pebble cluster)...")
	time.Sleep(6 * time.Second)

	// Verify expired
	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Lease TTL after expiry (Pebble cluster): %d", ttlResp.TTL)
	assert.Equal(t, int64(-1), ttlResp.TTL, "lease should have expired in Pebble cluster")

	// Verify key deleted on all nodes
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "pebble-cluster-expire/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key should be deleted (Pebble)", i)
	}
	t.Log("3-node Pebble cluster lease expiry: PASSED")
}

// TestLeaseExpiry_3NodeCluster_Pebble_ViaFollower tests lease expiry in a 3-node
// Pebble cluster when the lease is created via a follower.
func TestLeaseExpiry_3NodeCluster_Pebble_ViaFollower(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Determine leader and follower
	leaderIdx := -1
	followerIdx := -1
	for i := 0; i < 3; i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		leaderIdx = 0
	}
	for i := 0; i < 3; i++ {
		if i != leaderIdx {
			followerIdx = i
			break
		}
	}
	t.Logf("Leader=node %d, Follower=node %d (Pebble)", leaderIdx, followerIdx)

	// Grant lease with 3s TTL via FOLLOWER
	leaseResp, err := clus.clients[followerIdx].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease via FOLLOWER (node %d), ID=%d, TTL=3s (Pebble)", followerIdx, leaseID)

	// Put key with lease
	_, err = clus.clients[followerIdx].Put(ctx, "pebble-follower-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Wait for expiry
	t.Log("Waiting for lease (granted via follower) to expire (Pebble)...")
	time.Sleep(7 * time.Second)

	// Verify
	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Lease TTL after expiry (Pebble follower): %d", ttlResp.TTL)

	if ttlResp.TTL != -1 {
		t.Errorf("BUG: lease granted via follower did NOT expire in Pebble cluster! TTL=%d", ttlResp.TTL)
	}

	getResp, err := clus.clients[leaderIdx].Get(ctx, "pebble-follower-expire/key1")
	require.NoError(t, err)
	if len(getResp.Kvs) > 0 {
		t.Errorf("BUG: key still exists after lease should have expired (Pebble follower)")
	} else {
		t.Log("3-node Pebble cluster lease expiry (via follower): PASSED")
	}
}

// ============================================================================
// Restart + remaining TTL expiry (3-node cluster)
// ============================================================================

// TestLeaseExpiry_3NodeCluster_AfterRestart tests that leases with remaining TTL
// expire correctly after a server restart in a 3-node cluster.
func TestLeaseExpiry_3NodeCluster_AfterRestart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease with 5s TTL
	leaseResp, err := clus.clients[0].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=5s", leaseID)

	_, err = clus.clients[0].Put(ctx, "cluster-restart-expire/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Also create a long-lived lease
	longResp, err := clus.clients[0].Grant(ctx, 120)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "cluster-restart-expire/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Restart node 0's etcd server (the likely leader)
	t.Log("Restarting node 0's etcd server...")
	clus.clients[0].Close()
	clus.servers[0].Stop()
	time.Sleep(500 * time.Millisecond)

	// Wait for short lease to expire during "downtime" of node 0's etcd server
	// The underlying Raft and store are still running
	t.Log("Waiting for short lease TTL to pass...")
	time.Sleep(5 * time.Second)

	// Restart node 0's etcd server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     clus.kvStores[0],
		Address:   newAddr,
		ClusterID: 1000,
		MemberID:  1,
	})
	require.NoError(t, err)
	clus.servers[0] = newServer

	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("Restarted node 0 error: %v", err)
		}
	}()
	time.Sleep(2 * time.Second)

	// Create new client
	cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	clus.clients[0] = cli

	// Verify short-lived lease expired
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Short lease TTL after restart: %d", ttlResp.TTL)
	assert.Equal(t, int64(-1), ttlResp.TTL, "short lease should have expired after restart")

	// Verify key was cleaned up
	getResp, err := cli.Get(ctx, "cluster-restart-expire/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key should be deleted after expired lease")

	// Verify long-lived lease is still alive
	ttlResp, err = cli.TimeToLive(ctx, longID)
	require.NoError(t, err)
	t.Logf("Long lease TTL after restart: %d", ttlResp.TTL)
	assert.Greater(t, ttlResp.TTL, int64(0), "long lease should still be alive")

	// Long-lived lease key should still exist
	getResp, err = cli.Get(ctx, "cluster-restart-expire/long")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1, "long lease key should survive")

	// KeepAlive on long lease should work
	kaResp, err := cli.KeepAliveOnce(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, kaResp.TTL, int64(0))
	t.Log("3-node cluster restart + lease expiry: PASSED")
}

// ============================================================================
// 3-Node cluster: lease expired BEFORE restart, then restart cluster
// ============================================================================

// TestLeaseExpiry_3NodeCluster_ExpiredBeforeRestart tests that when a lease has
// already expired before the cluster restarts, the expired lease is properly
// cleaned up after the cluster comes back online.
func TestLeaseExpiry_3NodeCluster_ExpiredBeforeRestart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease with 10s TTL
	leaseResp, err := clus.clients[0].Grant(ctx, 10)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=10s", leaseID)

	// Put keys with the lease
	_, err = clus.clients[0].Put(ctx, "expired-before-restart/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = clus.clients[0].Put(ctx, "expired-before-restart/key2", "v2", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Also create a long-lived lease that should survive
	longResp, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "expired-before-restart/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify keys exist on all nodes before anything happens
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "expired-before-restart/key1")
		require.NoError(t, err)
		require.Len(t, getResp.Kvs, 1, "node %d should have key1 before expiry", i)
	}

	// Wait for the lease to fully expire (TTL=10s + check interval + margin)
	t.Log("Waiting 14s for lease to expire before cluster restart...")
	time.Sleep(14 * time.Second)

	// Verify lease has expired before we restart
	ttlResp, err := clus.clients[0].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Equal(t, int64(-1), ttlResp.TTL, "lease should have expired before restart")
	t.Logf("Lease TTL before restart: %d (confirmed expired)", ttlResp.TTL)

	// Verify keys are already deleted
	getResp, err := clus.clients[0].Get(ctx, "expired-before-restart/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 0, "key1 should be deleted before restart")

	// Close all clients and stop all servers
	t.Log("Stopping all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}
	time.Sleep(1 * time.Second)

	// Restart all 3 servers with new ports
	t.Log("Restarting all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		newAddr := listener.Addr().String()
		listener.Close()

		newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:     clus.kvStores[i],
			Address:   newAddr,
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = newServer

		go func(srv *etcdapi.Server) {
			if err := srv.Start(); err != nil {
				t.Logf("Restarted server error: %v", err)
			}
		}(newServer)

		// Wire up OnLeaderChange for lease expiry
		leaseManager := newServer.GetLeaseManager()
		if leaseManager != nil {
			go func(rn memory.RaftNode, lm *etcdapi.LeaseManager) {
				for status := range rn.LeaderChangeC() {
					lm.OnLeaderChange(status)
				}
			}(clus.raftNodes[i], leaseManager)
		}

		cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// Wait for cluster to stabilize after restart
	t.Log("Waiting for cluster to stabilize after restart...")
	time.Sleep(3 * time.Second)

	// Verify the expired lease is still reported as expired after restart
	for i := 0; i < 3; i++ {
		ttlResp, err := clus.clients[i].TimeToLive(ctx, leaseID)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttlResp.TTL, "node %d: expired lease should still be TTL=-1 after restart", i)
		t.Logf("Node %d: expired lease TTL after restart: %d", i, ttlResp.TTL)
	}

	// Verify expired keys are not resurrected after restart
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "expired-before-restart/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key1 should not exist after restart", i)

		getResp, err = clus.clients[i].Get(ctx, "expired-before-restart/key2")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key2 should not exist after restart", i)
	}

	// Verify long-lived lease survived the restart
	for i := 0; i < 3; i++ {
		ttlResp, err := clus.clients[i].TimeToLive(ctx, longID)
		require.NoError(t, err)
		assert.Greater(t, ttlResp.TTL, int64(0), "node %d: long-lived lease should still be alive after restart", i)
		t.Logf("Node %d: long-lived lease TTL after restart: %d", i, ttlResp.TTL)
	}

	// Verify long-lived lease key survived
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "expired-before-restart/long")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 1, "node %d: long-lived key should survive restart", i)
	}

	// Verify KeepAlive on long lease still works after restart
	kaResp, err := clus.clients[0].KeepAliveOnce(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, kaResp.TTL, int64(0))

	// Verify granting a new lease works after restart
	newResp, err := clus.clients[0].Grant(ctx, 60)
	require.NoError(t, err)
	assert.Greater(t, newResp.TTL, int64(0))
	t.Logf("New lease after restart: ID=%d, TTL=%d", newResp.ID, newResp.TTL)

	t.Log("3-node cluster expired-before-restart: PASSED")
}

// ============================================================================
// 3-Node cluster: lease granted via follower, expired before restart
// ============================================================================

// TestLeaseExpiry_3NodeCluster_FollowerGrantExpiredBeforeRestart tests that a
// lease granted via a follower node, which has expired before the cluster
// restarts, does not reappear after the cluster comes back online.
func TestLeaseExpiry_3NodeCluster_FollowerGrantExpiredBeforeRestart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Determine leader and follower
	leaderIdx := -1
	followerIdx := -1
	for i := 0; i < 3; i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		leaderIdx = 0
	}
	for i := 0; i < 3; i++ {
		if i != leaderIdx {
			followerIdx = i
			break
		}
	}
	t.Logf("Leader=node %d, Follower=node %d", leaderIdx, followerIdx)

	// Grant lease with 10s TTL via FOLLOWER
	leaseResp, err := clus.clients[followerIdx].Grant(ctx, 10)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease via follower (node %d), ID=%d, TTL=10s", followerIdx, leaseID)

	// Put keys with the lease via follower
	_, err = clus.clients[followerIdx].Put(ctx, "follower-restart/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = clus.clients[followerIdx].Put(ctx, "follower-restart/key2", "v2", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify keys exist on leader
	getResp, err := clus.clients[leaderIdx].Get(ctx, "follower-restart/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 1, "leader should have key1")

	// Wait for the lease to fully expire (TTL=10s + check interval + margin)
	t.Log("Waiting 14s for follower-granted lease to expire...")
	time.Sleep(14 * time.Second)

	// Verify lease has expired before restart
	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Equal(t, int64(-1), ttlResp.TTL, "follower-granted lease should have expired")

	// Verify keys deleted
	getResp, err = clus.clients[leaderIdx].Get(ctx, "follower-restart/key1")
	require.NoError(t, err)
	require.Len(t, getResp.Kvs, 0, "key1 should be deleted before restart")

	// Stop all servers
	t.Log("Stopping all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}
	time.Sleep(1 * time.Second)

	// Restart all servers
	t.Log("Restarting all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		newAddr := listener.Addr().String()
		listener.Close()

		newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:     clus.kvStores[i],
			Address:   newAddr,
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = newServer

		go func(srv *etcdapi.Server) {
			if err := srv.Start(); err != nil {
				t.Logf("Restarted server error: %v", err)
			}
		}(newServer)

		leaseManager := newServer.GetLeaseManager()
		if leaseManager != nil {
			go func(rn memory.RaftNode, lm *etcdapi.LeaseManager) {
				for status := range rn.LeaderChangeC() {
					lm.OnLeaderChange(status)
				}
			}(clus.raftNodes[i], leaseManager)
		}

		cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// Wait for cluster to stabilize
	time.Sleep(3 * time.Second)

	// Verify the follower-granted lease is still expired after restart
	for i := 0; i < 3; i++ {
		ttlResp, err := clus.clients[i].TimeToLive(ctx, leaseID)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttlResp.TTL,
			"node %d: follower-granted expired lease should still be TTL=-1 after restart", i)
	}

	// Verify keys not resurrected
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "follower-restart/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key1 should not exist after restart", i)

		getResp, err = clus.clients[i].Get(ctx, "follower-restart/key2")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0, "node %d: key2 should not exist after restart", i)
	}

	t.Log("3-node cluster follower-grant expired-before-restart: PASSED")
}

// ============================================================================
// 3-Node cluster: lease expires DURING restart (TTL passes while servers down)
// ============================================================================

// TestLeaseExpiry_3NodeCluster_ExpiresDuringRestart tests that a lease which
// is still alive when the servers stop, but whose TTL passes during the
// downtime, is correctly identified as expired after the cluster restarts.
func TestLeaseExpiry_3NodeCluster_ExpiresDuringRestart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease with 5s TTL
	leaseResp, err := clus.clients[0].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Granted lease ID=%d, TTL=5s", leaseID)

	// Put key with the lease
	_, err = clus.clients[0].Put(ctx, "expire-during-restart/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Also create a long-lived lease as control
	longResp, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "expire-during-restart/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Verify lease is still alive
	ttlResp, err := clus.clients[0].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Greater(t, ttlResp.TTL, int64(0), "lease should still be alive before stop")
	t.Logf("Lease TTL before stop: %d", ttlResp.TTL)

	// Stop all servers WHILE the lease is still alive
	t.Log("Stopping all 3 etcd servers (lease still alive)...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}

	// Wait long enough for the lease TTL to pass during downtime
	// TTL=5s + margin=3s = 8s
	t.Log("Waiting 8s for TTL to pass during downtime...")
	time.Sleep(8 * time.Second)

	// Restart all servers
	t.Log("Restarting all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		newAddr := listener.Addr().String()
		listener.Close()

		newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:     clus.kvStores[i],
			Address:   newAddr,
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = newServer

		go func(srv *etcdapi.Server) {
			if err := srv.Start(); err != nil {
				t.Logf("Restarted server error: %v", err)
			}
		}(newServer)

		leaseManager := newServer.GetLeaseManager()
		if leaseManager != nil {
			go func(rn memory.RaftNode, lm *etcdapi.LeaseManager) {
				for status := range rn.LeaderChangeC() {
					lm.OnLeaderChange(status)
				}
			}(clus.raftNodes[i], leaseManager)
		}

		cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// Wait for stabilization + lease expiry check (interval=1s + margin=3s)
	t.Log("Waiting for cluster to stabilize and detect expired lease...")
	time.Sleep(5 * time.Second)

	// The lease was granted with original GrantTime preserved in WAL,
	// so after replay its TTL should have already passed.
	// The expiry checker should clean it up.
	for i := 0; i < 3; i++ {
		ttlResp, err := clus.clients[i].TimeToLive(ctx, leaseID)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttlResp.TTL,
			"node %d: lease whose TTL passed during downtime should be expired", i)
	}

	// Verify key was cleaned up
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "expire-during-restart/key1")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 0,
			"node %d: key should be deleted after lease expired during downtime", i)
	}

	// Verify long-lived lease survived
	for i := 0; i < 3; i++ {
		ttlResp, err := clus.clients[i].TimeToLive(ctx, longID)
		require.NoError(t, err)
		assert.Greater(t, ttlResp.TTL, int64(0),
			"node %d: long-lived lease should still be alive", i)

		getResp, err := clus.clients[i].Get(ctx, "expire-during-restart/long")
		require.NoError(t, err)
		assert.Len(t, getResp.Kvs, 1,
			"node %d: long-lived key should survive", i)
	}

	t.Log("3-node cluster expires-during-restart: PASSED")
}

// ============================================================================
// 3-Node cluster: mixed leases with different TTLs + restart
// ============================================================================

// TestLeaseExpiry_3NodeCluster_MixedTTL_Restart tests a realistic scenario
// with multiple leases of different TTLs: some expire before restart, some
// expire during restart, and some survive the restart.
func TestLeaseExpiry_3NodeCluster_MixedTTL_Restart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Lease A: short TTL, will expire BEFORE restart
	respA, err := clus.clients[0].Grant(ctx, 3)
	require.NoError(t, err)
	leaseA := respA.ID
	_, err = clus.clients[0].Put(ctx, "mixed/short", "va", clientv3.WithLease(leaseA))
	require.NoError(t, err)
	t.Logf("Lease A (short, 3s): ID=%d", leaseA)

	// Lease B: medium TTL, will expire DURING restart downtime
	respB, err := clus.clients[0].Grant(ctx, 10)
	require.NoError(t, err)
	leaseB := respB.ID
	_, err = clus.clients[0].Put(ctx, "mixed/medium", "vb", clientv3.WithLease(leaseB))
	require.NoError(t, err)
	t.Logf("Lease B (medium, 10s): ID=%d", leaseB)

	// Lease C: long TTL, should survive everything
	respC, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	leaseC := respC.ID
	_, err = clus.clients[0].Put(ctx, "mixed/long", "vc", clientv3.WithLease(leaseC))
	require.NoError(t, err)
	t.Logf("Lease C (long, 300s): ID=%d", leaseC)

	// Wait for replication + lease A to expire (3s TTL + check + margin)
	t.Log("Waiting 7s for lease A to expire...")
	time.Sleep(7 * time.Second)

	// Verify A expired, B alive, C alive
	ttlA, err := clus.clients[0].TimeToLive(ctx, leaseA)
	require.NoError(t, err)
	require.Equal(t, int64(-1), ttlA.TTL, "lease A should be expired before restart")

	ttlB, err := clus.clients[0].TimeToLive(ctx, leaseB)
	require.NoError(t, err)
	require.Greater(t, ttlB.TTL, int64(0), "lease B should still be alive before restart")
	t.Logf("Before restart: A TTL=%d, B TTL=%d", ttlA.TTL, ttlB.TTL)

	// Stop all servers
	t.Log("Stopping all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}

	// Wait for lease B's TTL to pass during downtime
	// B was granted ~7s ago with TTL=10s, so ~3s remaining + margin
	t.Log("Waiting 6s for lease B TTL to pass during downtime...")
	time.Sleep(6 * time.Second)

	// Restart all servers
	t.Log("Restarting all 3 etcd servers...")
	for i := 0; i < 3; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		newAddr := listener.Addr().String()
		listener.Close()

		newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:     clus.kvStores[i],
			Address:   newAddr,
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = newServer

		go func(srv *etcdapi.Server) {
			if err := srv.Start(); err != nil {
				t.Logf("Restarted server error: %v", err)
			}
		}(newServer)

		leaseManager := newServer.GetLeaseManager()
		if leaseManager != nil {
			go func(rn memory.RaftNode, lm *etcdapi.LeaseManager) {
				for status := range rn.LeaderChangeC() {
					lm.OnLeaderChange(status)
				}
			}(clus.raftNodes[i], leaseManager)
		}

		cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// Wait for cluster to stabilize and expiry checker to run
	t.Log("Waiting for cluster to stabilize...")
	time.Sleep(5 * time.Second)

	// Verify lease A (expired before restart): still expired
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, leaseA)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttl.TTL,
			"node %d: lease A (expired before restart) should be TTL=-1", i)
	}

	// Verify lease B (expired during downtime): should be expired now
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, leaseB)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttl.TTL,
			"node %d: lease B (expired during downtime) should be TTL=-1", i)
	}

	// Verify lease C (long-lived): should still be alive
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, leaseC)
		require.NoError(t, err)
		assert.Greater(t, ttl.TTL, int64(0),
			"node %d: lease C (long-lived) should still be alive", i)
	}

	// Verify keys: A and B deleted, C survives
	for i := 0; i < 3; i++ {
		getA, err := clus.clients[i].Get(ctx, "mixed/short")
		require.NoError(t, err)
		assert.Len(t, getA.Kvs, 0, "node %d: short key should be deleted", i)

		getB, err := clus.clients[i].Get(ctx, "mixed/medium")
		require.NoError(t, err)
		assert.Len(t, getB.Kvs, 0, "node %d: medium key should be deleted", i)

		getC, err := clus.clients[i].Get(ctx, "mixed/long")
		require.NoError(t, err)
		assert.Len(t, getC.Kvs, 1, "node %d: long key should survive", i)
	}

	t.Log("3-node cluster mixed-TTL restart: PASSED")
}
