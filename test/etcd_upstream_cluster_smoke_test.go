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

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestEtcdUpstreamClusterSmoke(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		clus := newEtcdCluster(t, 3)
		defer clus.Close(t)

		testEtcdUpstreamClusterSmoke(t, clus.clients[0], clus.clients[1], clus.servers[0].Address(), "compat/cluster/memory", "memory-value")
	})

	t.Run("pebble", func(t *testing.T) {
		clus := newEtcdPebbleCluster(t, 3)
		defer clus.Close(t)

		testEtcdUpstreamClusterSmoke(t, clus.clients[0], clus.clients[1], clus.servers[0].Address(), "compat/cluster/pebble", "pebble-value")
	})
}

func testEtcdUpstreamClusterSmoke(t *testing.T, putClient, getClient *clientv3.Client, statusEndpoint, key, value string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := putClient.Put(ctx, key, value)
	require.NoError(t, err)

	getResp := getClusterValue(t, ctx, getClient, key)
	require.Len(t, getResp.Kvs, 1)
	require.Equal(t, key, string(getResp.Kvs[0].Key))
	require.Equal(t, value, string(getResp.Kvs[0].Value))

	statusResp, err := putClient.Status(ctx, statusEndpoint)
	require.NoError(t, err)
	require.NotZero(t, statusResp.Leader)
	require.NotEmpty(t, statusResp.Version)
}

func getClusterValue(t *testing.T, ctx context.Context, client *clientv3.Client, key string) *clientv3.GetResponse {
	t.Helper()

	var (
		resp    *clientv3.GetResponse
		err     error
		lastErr error
	)

	for attempt := 0; attempt < 20; attempt++ {
		resp, err = client.Get(ctx, key)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if len(resp.Kvs) == 1 {
			return resp
		}
		time.Sleep(250 * time.Millisecond)
	}

	if resp == nil {
		t.Fatalf("replication failed after 20 attempts for key %q: last get error: %v", key, lastErr)
	}
	t.Fatalf("replication failed after 20 attempts for key %q: expected 1 kv, got %d", key, len(resp.Kvs))
	return resp
}
