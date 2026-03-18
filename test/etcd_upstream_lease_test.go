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

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestEtcdUpstreamLeaseSmoke(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		ctx := context.Background()

		grantResp, err := tc.Client.Grant(ctx, 10)
		require.NoError(t, err)

		_, err = tc.Client.Put(ctx, "compat/lease/key", "value", clientv3.WithLease(grantResp.ID))
		require.NoError(t, err)

		keepAliveResp, err := tc.Client.KeepAliveOnce(ctx, grantResp.ID)
		require.NoError(t, err)
		require.Equal(t, grantResp.ID, keepAliveResp.ID)

		ttlResp, err := tc.Client.TimeToLive(ctx, grantResp.ID, clientv3.WithAttachedKeys())
		require.NoError(t, err)
		require.Equal(t, grantResp.ID, ttlResp.ID)
		require.Greater(t, ttlResp.TTL, int64(0))
		require.NotEmpty(t, ttlResp.Keys)
		require.Equal(t, []byte("compat/lease/key"), ttlResp.Keys[0])

		_, err = tc.Client.Revoke(ctx, grantResp.ID)
		require.NoError(t, err)

		getResp, err := tc.Client.Get(ctx, "compat/lease/key")
		require.NoError(t, err)
		require.Len(t, getResp.Kvs, 0)
	})
}
