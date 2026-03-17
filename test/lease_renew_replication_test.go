package test

import (
	"context"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseRenewReplicationPreventsLeaderExpiryCleanup(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()
	leaderIdx, followerIdx := findLeaderFollower(t, clus)

	resp, err := clus.clients[leaderIdx].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[leaderIdx].Put(ctx, "renew/guard", "value", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	_, err = clus.clients[followerIdx].KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err)

	time.Sleep(4 * time.Second)

	getResp, err := clus.clients[leaderIdx].Get(ctx, "renew/guard")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	require.NoError(t, err)
	assert.Equal(t, int64(5), ttlResp.GrantedTTL)
	require.Len(t, ttlResp.Keys, 1)
	assert.Equal(t, []byte("renew/guard"), ttlResp.Keys[0])

	require.True(t, waitForLeaseExpiry(t, clus.clients[leaderIdx], leaseID, 8*time.Second))

	getResp, err = clus.clients[leaderIdx].Get(ctx, "renew/guard")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

func TestLeaseMissingLeaseSemantics_PebbleCluster(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()
	leaderIdx, followerIdx := findLeaderFollower(t, clus)
	missingLeaseID := clientv3.LeaseID(999999)

	_, err := clus.clients[followerIdx].KeepAliveOnce(ctx, missingLeaseID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lease not found")

	ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, missingLeaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
	assert.Equal(t, int64(0), ttlResp.GrantedTTL)
}
