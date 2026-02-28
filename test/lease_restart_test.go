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
	"metaStore/internal/kvstore"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restartServer stops the old etcd server and creates a new one on the same store.
// This simulates a server restart where lease state must be recovered from the store.
func restartServer(t *testing.T, node *testNode) (*etcdapi.Server, string) {
	t.Helper()

	// Stop old server
	node.server.Stop()
	time.Sleep(500 * time.Millisecond)

	// Allocate a new port (old listener is closed after Stop)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	// Create new server with the SAME underlying store
	store, ok := node.kvStore.(kvstore.Store)
	require.True(t, ok, "kvStore must implement StoreInterface")

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   newAddr,
		ClusterID: 1000,
		MemberID:  uint64(node.id),
	})
	require.NoError(t, err)

	// Start new server in background
	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("Restarted server error: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	// Update node reference
	node.server = newServer
	node.clientAddr = newAddr

	return newServer, newAddr
}

// TestLeaseRecoveryAfterRestart_SingleNode tests that leases survive a single-node server restart.
// Scenario:
//  1. Start server, create leases, attach keys
//  2. Stop server (simulating crash/restart)
//  3. Start new server on same store
//  4. Verify: KeepAlive works, TimeToLive works, lease-bound keys still exist
func TestLeaseRecoveryAfterRestart_SingleNode(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	ctx := context.Background()

	// --- Phase 1: Create leases and keys before restart ---
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Create lease with 60s TTL (long enough to survive restart)
	leaseResp, err := cli.Grant(ctx, 60)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Created lease ID=%d, TTL=%d", leaseID, leaseResp.TTL)

	// Put keys with lease
	_, err = cli.Put(ctx, "service/node1", "alive", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = cli.Put(ctx, "service/node2", "alive", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Create a second lease
	leaseResp2, err := cli.Grant(ctx, 60)
	require.NoError(t, err)
	leaseID2 := leaseResp2.ID
	_, err = cli.Put(ctx, "config/key1", "value1", clientv3.WithLease(leaseID2))
	require.NoError(t, err)

	// Verify keys exist before restart
	getResp, err := cli.Get(ctx, "service/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 2, "should have 2 service keys before restart")

	// Close old client
	cli.Close()

	// --- Phase 2: Restart server ---
	t.Log("Restarting server...")
	_, newAddr := restartServer(t, node)
	t.Logf("Server restarted on %s", newAddr)

	// --- Phase 3: Verify leases survived restart ---
	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Verify keys still exist
	getResp, err = cli2.Get(ctx, "service/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 2, "service keys should survive restart")

	getResp, err = cli2.Get(ctx, "config/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1, "config key should survive restart")

	// Verify KeepAlive works on recovered leases
	kaResp, err := cli2.KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err, "KeepAlive should work after restart")
	assert.Equal(t, leaseID, kaResp.ID)
	assert.Greater(t, kaResp.TTL, int64(0), "TTL should be positive after renewal")
	t.Logf("KeepAlive on lease %d succeeded, TTL=%d", leaseID, kaResp.TTL)

	// Verify KeepAlive on second lease
	kaResp2, err := cli2.KeepAliveOnce(ctx, leaseID2)
	require.NoError(t, err, "KeepAlive on second lease should work after restart")
	assert.Equal(t, leaseID2, kaResp2.ID)

	// Verify TimeToLive works
	ttlResp, err := cli2.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	require.NoError(t, err, "TimeToLive should work after restart")
	assert.Greater(t, ttlResp.TTL, int64(0), "remaining TTL should be positive")
	assert.Equal(t, int64(60), ttlResp.GrantedTTL, "granted TTL should be preserved")
	assert.Len(t, ttlResp.Keys, 2, "lease should still have 2 attached keys")
	t.Logf("TimeToLive on lease %d: TTL=%d, GrantedTTL=%d, Keys=%d",
		leaseID, ttlResp.TTL, ttlResp.GrantedTTL, len(ttlResp.Keys))

	// Verify Revoke still works after restart
	_, err = cli2.Revoke(ctx, leaseID2)
	require.NoError(t, err, "Revoke should work after restart")

	// Verify revoked lease's key is deleted
	getResp, err = cli2.Get(ctx, "config/key1")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key should be deleted after revoking recovered lease")
}

// TestLeaseRecoveryAfterRestart_NewLeaseAfterRestart tests that new leases
// can be created after restart without ID collisions.
func TestLeaseRecoveryAfterRestart_NewLeaseAfterRestart(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	ctx := context.Background()

	// --- Phase 1: Create leases before restart ---
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Create multiple leases to advance the counter
	var leaseIDs []clientv3.LeaseID
	for i := 0; i < 5; i++ {
		resp, err := cli.Grant(ctx, 60)
		require.NoError(t, err)
		leaseIDs = append(leaseIDs, resp.ID)
	}
	t.Logf("Created %d leases before restart, IDs: %v", len(leaseIDs), leaseIDs)
	cli.Close()

	// --- Phase 2: Restart server ---
	_, newAddr := restartServer(t, node)

	// --- Phase 3: Create new leases after restart ---
	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Create new leases - should not collide with existing IDs
	for i := 0; i < 5; i++ {
		resp, err := cli2.Grant(ctx, 60)
		require.NoError(t, err)

		// Verify no collision with pre-restart lease IDs
		for _, oldID := range leaseIDs {
			assert.NotEqual(t, oldID, resp.ID, "new lease ID should not collide with pre-restart ID")
		}
		t.Logf("New lease after restart: ID=%d", resp.ID)
	}
}

// TestLeaseRecoveryAfterRestart_ExpiredDuringDowntime tests that leases
// which expired during server downtime are properly cleaned up after restart.
func TestLeaseRecoveryAfterRestart_ExpiredDuringDowntime(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	ctx := context.Background()

	// --- Phase 1: Create short-lived lease ---
	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	// Create lease with very short TTL (2 seconds)
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)
	shortLeaseID := leaseResp.ID

	_, err = cli.Put(ctx, "ephemeral/key", "temp", clientv3.WithLease(shortLeaseID))
	require.NoError(t, err)

	// Also create a long-lived lease that should survive
	longResp, err := cli.Grant(ctx, 120)
	require.NoError(t, err)
	longLeaseID := longResp.ID

	_, err = cli.Put(ctx, "persistent/key", "keep", clientv3.WithLease(longLeaseID))
	require.NoError(t, err)

	cli.Close()

	// --- Phase 2: Stop server and wait for short lease to expire ---
	node.server.Stop()
	t.Log("Server stopped, waiting for short lease TTL to pass...")
	time.Sleep(3 * time.Second) // Wait longer than the 2s TTL

	// --- Phase 3: Restart server ---
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	store, ok := node.kvStore.(kvstore.Store)
	require.True(t, ok)

	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   newAddr,
		ClusterID: 1000,
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

	// Wait for server startup AND expiry checker to run (check interval is typically 1s)
	time.Sleep(2 * time.Second)

	// --- Phase 4: Verify ---
	cli2, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	defer cli2.Close()

	// Short-lived lease's key should be cleaned up
	getResp, err := cli2.Get(ctx, "ephemeral/key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "key with expired lease should be deleted after restart")

	// Long-lived lease's key should still exist
	getResp, err = cli2.Get(ctx, "persistent/key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1, "key with long-lived lease should survive restart")

	// Long-lived lease should still be renewable
	kaResp, err := cli2.KeepAliveOnce(ctx, longLeaseID)
	require.NoError(t, err, "long-lived lease KeepAlive should work after restart")
	assert.Greater(t, kaResp.TTL, int64(0))

	// Short-lived lease KeepAlive should fail
	ttlResp, err := cli2.TimeToLive(ctx, shortLeaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL, "expired lease should have TTL=-1")
}

// TestLeaseRecoveryAfterRestart_3NodeCluster tests lease recovery in a 3-node cluster
// when one node's etcd server restarts.
func TestLeaseRecoveryAfterRestart_3NodeCluster(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// --- Phase 1: Create leases on the cluster ---
	// Use node 0 (likely the leader) to create leases
	leaseResp, err := clus.clients[0].Grant(ctx, 60)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	t.Logf("Created lease ID=%d on 3-node cluster", leaseID)

	_, err = clus.clients[0].Put(ctx, "cluster/key1", "value1", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = clus.clients[0].Put(ctx, "cluster/key2", "value2", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(2 * time.Second)

	// Verify data is visible on all nodes
	for i := 0; i < 3; i++ {
		getResp, err := clus.clients[i].Get(ctx, "cluster/", clientv3.WithPrefix())
		require.NoError(t, err, "node %d should have the keys", i)
		assert.Len(t, getResp.Kvs, 2, "node %d should have 2 keys", i)
	}

	// --- Phase 2: Restart one node's etcd server ---
	restartIdx := 2 // Restart node 2 (a follower)
	t.Logf("Restarting node %d...", restartIdx)

	// Close old client for this node
	clus.clients[restartIdx].Close()

	// Stop this node's server
	clus.servers[restartIdx].Stop()
	time.Sleep(500 * time.Millisecond)

	// Allocate new port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	newAddr := listener.Addr().String()
	listener.Close()

	// Create and start new server on the same store
	newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     clus.kvStores[restartIdx],
		Address:   newAddr,
		ClusterID: 1000,
		MemberID:  uint64(restartIdx + 1),
	})
	require.NoError(t, err)
	clus.servers[restartIdx] = newServer

	go func() {
		if err := newServer.Start(); err != nil {
			t.Logf("Restarted node %d error: %v", restartIdx, err)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	// Create new client for restarted node
	cli, err := NewEtcdClient([]string{newAddr}, 5*time.Second)
	require.NoError(t, err)
	clus.clients[restartIdx] = cli

	t.Logf("Node %d restarted on %s", restartIdx, newAddr)

	// --- Phase 3: Verify on restarted node ---

	// Keys should still be visible
	getResp, err := cli.Get(ctx, "cluster/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 2, "restarted node should have 2 keys")

	// KeepAlive should work on the restarted node
	kaResp, err := cli.KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err, "KeepAlive should work on restarted node")
	assert.Equal(t, leaseID, kaResp.ID)
	assert.Greater(t, kaResp.TTL, int64(0))
	t.Logf("KeepAlive on restarted node succeeded, TTL=%d", kaResp.TTL)

	// TimeToLive should work on the restarted node
	ttlResp, err := cli.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	require.NoError(t, err, "TimeToLive should work on restarted node")
	assert.Greater(t, ttlResp.TTL, int64(0))
	assert.Equal(t, int64(60), ttlResp.GrantedTTL)
	t.Logf("TimeToLive on restarted node: TTL=%d, Keys=%d", ttlResp.TTL, len(ttlResp.Keys))

	// Other nodes should still work
	kaResp, err = clus.clients[0].KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err, "KeepAlive should work on node 0")
	assert.Greater(t, kaResp.TTL, int64(0))
}

// TestLeaseRecoveryAfterRestart_3NodeCluster_AllRestart tests lease recovery
// when ALL nodes in a 3-node cluster restart their etcd servers.
func TestLeaseRecoveryAfterRestart_3NodeCluster_AllRestart(t *testing.T) {
	clus := newEtcdCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()

	// --- Phase 1: Create leases ---
	leaseResp, err := clus.clients[0].Grant(ctx, 60)
	require.NoError(t, err)
	leaseID := leaseResp.ID

	_, err = clus.clients[0].Put(ctx, "all-restart/key1", "v1", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// Create another lease on a different node (should go through leader via Raft)
	leaseResp2, err := clus.clients[1].Grant(ctx, 60)
	require.NoError(t, err)
	leaseID2 := leaseResp2.ID

	_, err = clus.clients[1].Put(ctx, "all-restart/key2", "v2", clientv3.WithLease(leaseID2))
	require.NoError(t, err)

	// Wait for replication
	time.Sleep(2 * time.Second)
	t.Logf("Created leases: %d, %d", leaseID, leaseID2)

	// --- Phase 2: Restart ALL etcd servers ---
	t.Log("Restarting all nodes...")
	newAddrs := make([]string, 3)

	// Close all clients first
	for i := 0; i < 3; i++ {
		clus.clients[i].Close()
	}

	// Stop all servers
	for i := 0; i < 3; i++ {
		clus.servers[i].Stop()
	}
	time.Sleep(500 * time.Millisecond)

	// Restart all servers
	for i := 0; i < 3; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		newAddrs[i] = listener.Addr().String()
		listener.Close()

		newServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:     clus.kvStores[i],
			Address:   newAddrs[i],
			ClusterID: 1000,
			MemberID:  uint64(i + 1),
		})
		require.NoError(t, err)
		clus.servers[i] = newServer

		go func(srv *etcdapi.Server, idx int) {
			if err := srv.Start(); err != nil {
				t.Logf("Restarted node %d error: %v", idx, err)
			}
		}(newServer, i)
	}
	time.Sleep(1 * time.Second)

	// Create new clients
	for i := 0; i < 3; i++ {
		cli, err := NewEtcdClient([]string{newAddrs[i]}, 5*time.Second)
		require.NoError(t, err)
		clus.clients[i] = cli
	}

	// --- Phase 3: Verify on all restarted nodes ---
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("Node%d", i), func(t *testing.T) {
			// Keys should exist
			getResp, err := clus.clients[i].Get(ctx, "all-restart/", clientv3.WithPrefix())
			require.NoError(t, err)
			assert.Len(t, getResp.Kvs, 2, "node %d should have 2 keys after all-restart", i)

			// KeepAlive should work for both leases
			kaResp, err := clus.clients[i].KeepAliveOnce(ctx, leaseID)
			require.NoError(t, err, "node %d: KeepAlive on lease1 should work", i)
			assert.Greater(t, kaResp.TTL, int64(0))

			kaResp2, err := clus.clients[i].KeepAliveOnce(ctx, leaseID2)
			require.NoError(t, err, "node %d: KeepAlive on lease2 should work", i)
			assert.Greater(t, kaResp2.TTL, int64(0))

			// TimeToLive should work
			ttlResp, err := clus.clients[i].TimeToLive(ctx, leaseID)
			require.NoError(t, err, "node %d: TimeToLive should work", i)
			assert.Greater(t, ttlResp.TTL, int64(0))
			assert.Equal(t, int64(60), ttlResp.GrantedTTL)
		})
	}
}
