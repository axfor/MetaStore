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
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseOperationsAcrossNodes_PebbleCluster(t *testing.T) {
	clus := newEtcdPebbleCluster(t, 3)
	defer clus.Close(t)

	ctx := context.Background()
	leaderIdx, followerIdx := findLeaderFollower(t, clus)

	resp, err := clus.clients[leaderIdx].Grant(ctx, 5)
	require.NoError(t, err)
	leaseID := resp.ID

	_, err = clus.clients[leaderIdx].Put(ctx, "cross-node/lease", "value", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	keepAliveResp, err := clus.clients[followerIdx].KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, leaseID, keepAliveResp.ID)
	assert.Greater(t, keepAliveResp.TTL, int64(0))

	ttlResp, err := clus.clients[followerIdx].TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))
	assert.Equal(t, int64(5), ttlResp.GrantedTTL)
	require.Len(t, ttlResp.Keys, 1)
	assert.Equal(t, []byte("cross-node/lease"), ttlResp.Keys[0])

	time.Sleep(4 * time.Second)

	getResp, err := clus.clients[leaderIdx].Get(ctx, "cross-node/lease")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	keepAliveResp, err = clus.clients[leaderIdx].KeepAliveOnce(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, leaseID, keepAliveResp.ID)

	_, err = clus.clients[followerIdx].Revoke(ctx, leaseID)
	require.NoError(t, err)

	getResp, err = clus.clients[leaderIdx].Get(ctx, "cross-node/lease")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}
