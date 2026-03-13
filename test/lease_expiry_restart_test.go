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
	"testing"
	"time"

	etcdapi "metaStore/api/etcd"

	clientv3 "go.etcd.io/etcd/client/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Helper: restart a single Pebble node's etcd server (keeps Raft + Pebble)
// ============================================================================

// restartPebbleServer stops the etcd server on a Pebble node and starts a new
// one on a fresh port, reusing the same KV store (Pebble + Raft keep running).
// Returns the new client address.
func restartPebbleServer(t *testing.T, node *testPebbleNode, clusterID uint64) string {
	t.Helper()
	node.server.Stop()
	time.Sleep(300 * time.Millisecond)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     node.pebbleKVStore,
		Address:   newAddr,
		ClusterID: clusterID,
		MemberID:  uint64(node.id),
	})
	require.NoError(t, err)
	node.server = newServer
	node.clientAddr = newAddr

	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("restarted server error: %v", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)
	return newAddr
}

// restartPebbleClusterServer restarts a single node's etcd server within
// a Pebble cluster.
func restartPebbleClusterServer(t *testing.T, clus *etcdPebbleCluster, idx int) string {
	t.Helper()
	if clus.clients[idx] != nil {
		clus.clients[idx].Close()
		clus.clients[idx] = nil
	}
	clus.servers[idx].Stop()
	time.Sleep(300 * time.Millisecond)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     clus.kvStores[idx],
		Address:   newAddr,
		ClusterID: 1000,
		MemberID:  uint64(idx + 1),
	})
	require.NoError(t, err)
	clus.servers[idx] = newServer

	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("restarted server %d error: %v", idx, err)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	clus.clients[idx] = cli
	return newAddr
}

// restartAllPebbleClusterServers restarts all etcd servers in a Pebble cluster.
func restartAllPebbleClusterServers(t *testing.T, clus *etcdPebbleCluster) {
	t.Helper()
	n := len(clus.peers)

	// Stop all
	for i := 0; i < n; i++ {
		if clus.clients[i] != nil {
			clus.clients[i].Close()
			clus.clients[i] = nil
		}
		clus.servers[i].Stop()
	}
	time.Sleep(500 * time.Millisecond)

	// Restart all
	for i := 0; i < n; i++ {
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
				t.Logf("restarted server error: %v", err)
			}
		}(newServer)

		cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}
	time.Sleep(2 * time.Second) // stabilize
}

// waitForLeaseExpiry polls until the lease is expired or the timeout elapses.
func waitForLeaseExpiry(t *testing.T, cli *clientv3.Client, leaseID clientv3.LeaseID, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := cli.TimeToLive(context.Background(), leaseID)
		if err == nil && resp.TTL == -1 {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// ============================================================================
// Single-node Pebble: lease expired BEFORE restart
// ============================================================================

func TestLeaseRestart_Single_ExpiredBeforeRestart(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli.Close()

	// Grant lease TTL=3s + attach key
	resp, err := cli.Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = cli.Put(ctx, "single/expired-before/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for full expiry
	t.Log("Waiting for lease to expire before restart...")
	require.True(t, waitForLeaseExpiry(t, cli, leaseID, 8*time.Second), "lease should expire")

	// Verify key deleted
	get, err := cli.Get(ctx, "single/expired-before/k1")
	require.NoError(t, err)
	require.Len(t, get.Kvs, 0, "key should be deleted after expiry")
	cli.Close()

	// Restart server
	t.Log("Restarting server...")
	newAddr := restartPebbleServer(t, node, 2000)

	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Lease must NOT come back
	ttl, err := cli2.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttl.TTL, "expired lease must not resurrect after restart")

	// Key must NOT come back
	get, err = cli2.Get(ctx, "single/expired-before/k1")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 0, "key must not resurrect after restart")

	// New lease should work
	newResp, err := cli2.Grant(ctx, 60)
	require.NoError(t, err)
	assert.Greater(t, newResp.TTL, int64(0))
	t.Log("PASSED: single-node expired-before-restart")
}

// ============================================================================
// Single-node Pebble: lease NOT expired before restart, should expire after
// ============================================================================

func TestLeaseRestart_Single_ExpiresAfterRestart(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Grant lease TTL=8s + attach key
	resp, err := cli.Grant(ctx, 8)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = cli.Put(ctx, "single/expire-after/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait 3s (lease still alive, ~5s remaining)
	time.Sleep(3 * time.Second)
	ttl, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Greater(t, ttl.TTL, int64(0), "lease should still be alive before restart")
	t.Logf("TTL before restart: %ds", ttl.TTL)
	cli.Close()

	// Restart server
	t.Log("Restarting server...")
	newAddr := restartPebbleServer(t, node, 2000)

	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Immediately after restart: lease should have reduced TTL (NOT reset)
	ttl, err = cli2.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("TTL after restart: %ds", ttl.TTL)
	// TTL must be less than original 8s (at least 3s+restart time elapsed)
	assert.Less(t, ttl.TTL, int64(8), "TTL should not be reset after restart")

	// Key still exists immediately
	get, err := cli2.Get(ctx, "single/expire-after/k1")
	require.NoError(t, err)
	if ttl.TTL > 0 {
		assert.Len(t, get.Kvs, 1, "key should still exist while lease alive")
	}

	// Wait for remaining TTL + margin
	t.Log("Waiting for remaining TTL to expire...")
	require.True(t, waitForLeaseExpiry(t, cli2, leaseID, 10*time.Second), "lease should eventually expire")

	// Key should be deleted
	get, err = cli2.Get(ctx, "single/expire-after/k1")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 0, "key should be deleted after lease expired post-restart")
	t.Log("PASSED: single-node expires-after-restart")
}

// ============================================================================
// Single-node Pebble: lease expires DURING downtime
// ============================================================================

func TestLeaseRestart_Single_ExpiresDuringDowntime(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Grant lease TTL=5s + attach key
	resp, err := cli.Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = cli.Put(ctx, "single/during-down/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Verify alive
	ttl, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Greater(t, ttl.TTL, int64(0))
	cli.Close()

	// Stop server, wait longer than TTL, then restart
	t.Log("Stopping server and waiting for TTL to pass during downtime...")
	node.server.Stop()
	time.Sleep(8 * time.Second) // TTL=5s well passed

	// Restart
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
			t.Logf("restarted server error: %v", err)
		}
	}()
	time.Sleep(1 * time.Second)

	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Wait for expiry checker to clean up
	require.True(t, waitForLeaseExpiry(t, cli2, leaseID, 8*time.Second),
		"lease whose TTL passed during downtime should expire after restart")

	// Key should be deleted
	get, err := cli2.Get(ctx, "single/during-down/k1")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 0, "key should be deleted")
	t.Log("PASSED: single-node expires-during-downtime")
}

// ============================================================================
// Single-node Pebble: mixed leases, some expire before restart, some after
// ============================================================================

func TestLeaseRestart_Single_MixedTTL(t *testing.T) {
	node, cleanup := startPebbleNode(t, 1)
	defer cleanup()

	ctx := context.Background()
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Short lease: expires before restart
	shortResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)
	shortID := shortResp.ID
	_, err = cli.Put(ctx, "single/mixed/short", "vs", clientv3.WithLease(shortID))
	require.NoError(t, err)

	// Long lease: survives restart
	longResp, err := cli.Grant(ctx, 120)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = cli.Put(ctx, "single/mixed/long", "vl", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Wait for short lease to expire
	require.True(t, waitForLeaseExpiry(t, cli, shortID, 8*time.Second))
	cli.Close()

	// Restart
	t.Log("Restarting server...")
	newAddr := restartPebbleServer(t, node, 2000)

	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Short: expired, key deleted, no resurrection
	ttl, err := cli2.TimeToLive(ctx, shortID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttl.TTL, "short lease should remain expired")
	get, err := cli2.Get(ctx, "single/mixed/short")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 0, "short key should not resurrect")

	// Long: still alive, key exists
	ttl, err = cli2.TimeToLive(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, ttl.TTL, int64(0), "long lease should survive restart")
	get, err = cli2.Get(ctx, "single/mixed/long")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 1, "long key should survive")

	// KeepAlive on long lease works after restart
	ka, err := cli2.KeepAliveOnce(ctx, longID)
	require.NoError(t, err)
	assert.Greater(t, ka.TTL, int64(0))
	t.Log("PASSED: single-node mixed-TTL restart")
}

// ============================================================================
// 3-Node Pebble cluster: lease expired before full cluster restart
// ============================================================================

func TestLeaseRestart_Cluster_ExpiredBeforeFullRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease TTL=3s + attach keys
	resp, err := clus.clients[0].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[0].Put(ctx, "cluster/expired-full/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = clus.clients[0].Put(ctx, "cluster/expired-full/k2", "v2", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Long-lived lease as control
	longResp, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "cluster/expired-full/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Wait for short lease to expire
	t.Log("Waiting for short lease to expire...")
	require.True(t, waitForLeaseExpiry(t, clus.clients[0], leaseID, 10*time.Second))

	// Verify key deleted before restart
	get, err := clus.clients[0].Get(ctx, "cluster/expired-full/k1")
	require.NoError(t, err)
	require.Len(t, get.Kvs, 0)

	// Restart ALL servers
	t.Log("Restarting all 3 servers...")
	restartAllPebbleClusterServers(t, clus)

	// Expired lease must stay expired on all nodes
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, leaseID)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttl.TTL, "node %d: expired lease must not resurrect", i)
	}

	// Keys must not resurrect
	for i := 0; i < 3; i++ {
		for _, key := range []string{"cluster/expired-full/k1", "cluster/expired-full/k2"} {
			get, err := clus.clients[i].Get(ctx, key)
			require.NoError(t, err)
			assert.Len(t, get.Kvs, 0, "node %d: %s must not resurrect", i, key)
		}
	}

	// Long lease survives
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, longID)
		require.NoError(t, err)
		assert.Greater(t, ttl.TTL, int64(0), "node %d: long lease should survive", i)

		get, err := clus.clients[i].Get(ctx, "cluster/expired-full/long")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 1, "node %d: long key should survive", i)
	}

	// New operations work after restart
	newResp, err := clus.clients[0].Grant(ctx, 60)
	require.NoError(t, err)
	assert.Greater(t, newResp.TTL, int64(0))
	t.Log("PASSED: cluster expired-before-full-restart")
}

// ============================================================================
// 3-Node Pebble cluster: lease expires DURING full cluster downtime
// ============================================================================

func TestLeaseRestart_Cluster_ExpiresDuringFullDowntime(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease TTL=5s
	resp, err := clus.clients[0].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[0].Put(ctx, "cluster/during-down/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Long-lived control
	longResp, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "cluster/during-down/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	// Verify lease alive
	ttl, err := clus.clients[0].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	require.Greater(t, ttl.TTL, int64(0))

	// Stop ALL servers while lease still alive
	t.Log("Stopping all servers (lease still alive)...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}

	// Wait for TTL to pass during downtime
	t.Log("Waiting 8s for TTL to pass during downtime...")
	time.Sleep(8 * time.Second)

	// Restart all
	t.Log("Restarting all servers...")
	restartAllPebbleClusterServers(t, clus)

	// Wait for expiry checker
	time.Sleep(3 * time.Second)

	// Lease should be expired on all nodes
	for i := 0; i < 3; i++ {
		require.True(t, waitForLeaseExpiry(t, clus.clients[i], leaseID, 5*time.Second),
			"node %d: lease should expire after restart", i)
	}

	// Key should be deleted
	for i := 0; i < 3; i++ {
		get, err := clus.clients[i].Get(ctx, "cluster/during-down/k1")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 0, "node %d: key should be deleted", i)
	}

	// Long lease survives
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, longID)
		require.NoError(t, err)
		assert.Greater(t, ttl.TTL, int64(0), "node %d: long lease should survive", i)
	}
	t.Log("PASSED: cluster expires-during-full-downtime")
}

// ============================================================================
// 3-Node Pebble cluster: lease not expired, restart single node
// ============================================================================

func TestLeaseRestart_Cluster_SingleNodeRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease TTL=10s
	resp, err := clus.clients[0].Grant(ctx, 10)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[0].Put(ctx, "cluster/single-restart/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	time.Sleep(1 * time.Second) // replication

	// Restart only node 1
	t.Log("Restarting only node 1...")
	restartPebbleClusterServer(t, clus, 1)

	// Lease should still be alive on restarted node
	ttl, err := clus.clients[1].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("Node 1 TTL after restart: %d", ttl.TTL)
	assert.Greater(t, ttl.TTL, int64(0), "lease should still be alive on restarted node")

	// Key should still exist on restarted node
	get, err := clus.clients[1].Get(ctx, "cluster/single-restart/k1")
	require.NoError(t, err)
	assert.Len(t, get.Kvs, 1, "key should exist on restarted node")

	// Wait for expiry
	t.Log("Waiting for lease to expire...")
	require.True(t, waitForLeaseExpiry(t, clus.clients[0], leaseID, 15*time.Second))

	// Key deleted on all nodes
	for i := 0; i < 3; i++ {
		get, err := clus.clients[i].Get(ctx, "cluster/single-restart/k1")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 0, "node %d: key should be deleted after expiry", i)
	}
	t.Log("PASSED: cluster single-node-restart")
}

// ============================================================================
// 3-Node Pebble cluster: rolling restart, lease expires during rolling restart
// ============================================================================

func TestLeaseRestart_Cluster_RollingRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Grant lease TTL=15s
	resp, err := clus.clients[0].Grant(ctx, 15)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[0].Put(ctx, "cluster/rolling/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Long-lived control
	longResp, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	longID := longResp.ID
	_, err = clus.clients[0].Put(ctx, "cluster/rolling/long", "persist", clientv3.WithLease(longID))
	require.NoError(t, err)

	time.Sleep(1 * time.Second)

	// Rolling restart: one node at a time
	for i := 0; i < 3; i++ {
		t.Logf("Rolling restart: restarting node %d...", i)
		restartPebbleClusterServer(t, clus, i)
		time.Sleep(2 * time.Second) // stabilize between restarts
	}

	// After rolling restart, TTL should be reduced but we may still be within window
	ttl, err := clus.clients[0].TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	t.Logf("TTL after rolling restart: %d", ttl.TTL)

	// Wait for full expiry
	require.True(t, waitForLeaseExpiry(t, clus.clients[0], leaseID, 15*time.Second))

	// Key deleted on all nodes
	for i := 0; i < 3; i++ {
		get, err := clus.clients[i].Get(ctx, "cluster/rolling/k1")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 0, "node %d: key should be deleted after expiry", i)
	}

	// Long lease survived
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, longID)
		require.NoError(t, err)
		assert.Greater(t, ttl.TTL, int64(0), "node %d: long lease should survive rolling restart", i)

		get, err := clus.clients[i].Get(ctx, "cluster/rolling/long")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 1, "node %d: long key should survive", i)
	}
	t.Log("PASSED: cluster rolling-restart")
}

// ============================================================================
// 3-Node Pebble cluster: lease via follower, expired before full restart
// ============================================================================

func TestLeaseRestart_Cluster_FollowerGrant_ExpiredBeforeRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Find leader/follower
	leaderIdx, followerIdx := findLeaderFollower(t, clus)

	// Grant lease via follower TTL=3s
	resp, err := clus.clients[followerIdx].Grant(ctx, 3)
	require.NoError(t, err)
	leaseID := resp.ID
	t.Logf("Granted lease via follower (node %d), ID=%d", followerIdx, leaseID)

	_, err = clus.clients[followerIdx].Put(ctx, "cluster/follower-expired/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for expiry
	require.True(t, waitForLeaseExpiry(t, clus.clients[leaderIdx], leaseID, 10*time.Second))

	// Restart all servers
	t.Log("Restarting all servers...")
	restartAllPebbleClusterServers(t, clus)

	// Must stay expired
	for i := 0; i < 3; i++ {
		ttl, err := clus.clients[i].TimeToLive(ctx, leaseID)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttl.TTL, "node %d: follower-granted expired lease must not resurrect", i)

		get, err := clus.clients[i].Get(ctx, "cluster/follower-expired/k1")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 0, "node %d: key must not resurrect", i)
	}
	t.Log("PASSED: cluster follower-grant expired-before-restart")
}

// ============================================================================
// 3-Node Pebble cluster: lease via follower, expires during downtime
// ============================================================================

func TestLeaseRestart_Cluster_FollowerGrant_ExpiresDuringDowntime(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	_, followerIdx := findLeaderFollower(t, clus)

	// Grant lease via follower TTL=5s
	resp, err := clus.clients[followerIdx].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[followerIdx].Put(ctx, "cluster/follower-during/k1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	time.Sleep(1 * time.Second)

	// Stop all while lease still alive
	t.Log("Stopping all servers...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}

	// Wait for TTL to pass
	t.Log("Waiting 8s for TTL to pass...")
	time.Sleep(8 * time.Second)

	// Restart
	restartAllPebbleClusterServers(t, clus)
	time.Sleep(3 * time.Second)

	for i := 0; i < 3; i++ {
		require.True(t, waitForLeaseExpiry(t, clus.clients[i], leaseID, 5*time.Second),
			"node %d: follower-granted lease should expire", i)

		get, err := clus.clients[i].Get(ctx, "cluster/follower-during/k1")
		require.NoError(t, err)
		assert.Len(t, get.Kvs, 0, "node %d: key should be deleted", i)
	}
	t.Log("PASSED: cluster follower-grant expires-during-downtime")
}

// ============================================================================
// 3-Node Pebble cluster: mixed leases + full restart
// ============================================================================

func TestLeaseRestart_Cluster_MixedTTL_FullRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Lease A: short, will expire before restart
	respA, err := clus.clients[0].Grant(ctx, 3)
	require.NoError(t, err)
	idA := respA.ID
	_, err = clus.clients[0].Put(ctx, "cluster/mixed/short", "va", clientv3.WithLease(idA))
	require.NoError(t, err)

	// Lease B: medium, will expire during downtime
	respB, err := clus.clients[0].Grant(ctx, 10)
	require.NoError(t, err)
	idB := respB.ID
	_, err = clus.clients[0].Put(ctx, "cluster/mixed/medium", "vb", clientv3.WithLease(idB))
	require.NoError(t, err)

	// Lease C: long, will survive
	respC, err := clus.clients[0].Grant(ctx, 300)
	require.NoError(t, err)
	idC := respC.ID
	_, err = clus.clients[0].Put(ctx, "cluster/mixed/long", "vc", clientv3.WithLease(idC))
	require.NoError(t, err)

	// Wait for A to expire
	t.Log("Waiting for short lease to expire...")
	require.True(t, waitForLeaseExpiry(t, clus.clients[0], idA, 10*time.Second))

	// B should still be alive
	ttl, err := clus.clients[0].TimeToLive(ctx, idB)
	require.NoError(t, err)
	require.Greater(t, ttl.TTL, int64(0), "medium lease should still be alive")

	// Stop all
	t.Log("Stopping all servers...")
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
		clus.clients[i] = nil
		clus.servers[i].Stop()
	}

	// Wait for B's TTL to pass
	t.Log("Waiting 8s for medium lease to expire during downtime...")
	time.Sleep(8 * time.Second)

	// Restart
	restartAllPebbleClusterServers(t, clus)
	time.Sleep(3 * time.Second)

	for i := 0; i < 3; i++ {
		// A: expired before restart
		ttlA, err := clus.clients[i].TimeToLive(ctx, idA)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), ttlA.TTL, "node %d: short lease should be expired", i)

		// B: expired during downtime
		require.True(t, waitForLeaseExpiry(t, clus.clients[i], idB, 5*time.Second),
			"node %d: medium lease should expire", i)

		// C: still alive
		ttlC, err := clus.clients[i].TimeToLive(ctx, idC)
		require.NoError(t, err)
		assert.Greater(t, ttlC.TTL, int64(0), "node %d: long lease should survive", i)
	}

	// Verify keys
	for i := 0; i < 3; i++ {
		get, _ := clus.clients[i].Get(ctx, "cluster/mixed/short")
		assert.Len(t, get.Kvs, 0, "node %d: short key deleted", i)

		get, _ = clus.clients[i].Get(ctx, "cluster/mixed/medium")
		assert.Len(t, get.Kvs, 0, "node %d: medium key deleted", i)

		get, _ = clus.clients[i].Get(ctx, "cluster/mixed/long")
		assert.Len(t, get.Kvs, 1, "node %d: long key survives", i)
	}
	t.Log("PASSED: cluster mixed-TTL full-restart")
}

// ============================================================================
// 3-Node Pebble cluster: multiple leases granted on different nodes + restart
// ============================================================================

func TestLeaseRestart_Cluster_MultiNodeGrant_FullRestart(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// Each node grants a lease
	leaseIDs := make([]clientv3.LeaseID, 3)
	for i := 0; i < 3; i++ {
		resp, err := clus.clients[i].Grant(ctx, 3)
		require.NoError(t, err)
		leaseIDs[i] = resp.ID

		key := fmt.Sprintf("cluster/multi-grant/node%d", i)
		_, err = clus.clients[i].Put(ctx, key, fmt.Sprintf("v%d", i), clientv3.WithLease(leaseIDs[i]))
		require.NoError(t, err)
		t.Logf("Node %d granted lease %d", i, leaseIDs[i])
	}

	// Wait for all leases to expire
	for i := 0; i < 3; i++ {
		require.True(t, waitForLeaseExpiry(t, clus.clients[0], leaseIDs[i], 10*time.Second),
			"lease from node %d should expire", i)
	}

	// Restart all
	t.Log("Restarting all servers...")
	restartAllPebbleClusterServers(t, clus)

	// None should resurrect
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			ttl, err := clus.clients[j].TimeToLive(ctx, leaseIDs[i])
			require.NoError(t, err)
			assert.Equal(t, int64(-1), ttl.TTL,
				"node %d: lease from node %d must not resurrect", j, i)
		}

		key := fmt.Sprintf("cluster/multi-grant/node%d", i)
		for j := 0; j < 3; j++ {
			get, err := clus.clients[j].Get(ctx, key)
			require.NoError(t, err)
			assert.Len(t, get.Kvs, 0, "node %d: %s must not resurrect", j, key)
		}
	}
	t.Log("PASSED: cluster multi-node-grant full-restart")
}

// ============================================================================
// Helper: find leader and follower in a Pebble cluster
// ============================================================================

func findLeaderFollower(t *testing.T, clus *etcdPebbleCluster) (leaderIdx, followerIdx int) {
	t.Helper()
	leaderIdx = -1
	followerIdx = -1
	for i := 0; i < len(clus.peers); i++ {
		status := clus.kvStores[i].GetRaftStatus()
		t.Logf("Node %d: state=%s, nodeID=%d, leaderID=%d", i, status.State, status.NodeID, status.LeaderID)
		if status.NodeID == status.LeaderID && status.LeaderID != 0 {
			leaderIdx = i
		}
	}
	if leaderIdx == -1 {
		leaderIdx = 0
	}
	for i := 0; i < len(clus.peers); i++ {
		if i != leaderIdx {
			followerIdx = i
			break
		}
	}
	t.Logf("Leader=node %d, Follower=node %d", leaderIdx, followerIdx)
	return
}
