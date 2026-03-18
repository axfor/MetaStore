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

func TestEtcdUpstreamKVPutGet(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		ctx := context.Background()

		_, err := tc.Client.Put(ctx, "compat/kv/basic", "value")
		require.NoError(t, err)

		resp, err := tc.Client.Get(ctx, "compat/kv/basic")
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)

		t.Run("Delete", func(t *testing.T) {
			_, err := tc.Client.Put(ctx, "compat/kv/delete", "value")
			require.NoError(t, err)

			delResp, err := tc.Client.Delete(ctx, "compat/kv/delete")
			require.NoError(t, err)
			require.Equal(t, int64(1), delResp.Deleted)

			resp, err := tc.Client.Get(ctx, "compat/kv/delete")
			require.NoError(t, err)
			require.Empty(t, resp.Kvs)
		})

		t.Run("PrefixRange", func(t *testing.T) {
			_, err := tc.Client.Put(ctx, "compat/kv/prefix/1", "one")
			require.NoError(t, err)
			_, err = tc.Client.Put(ctx, "compat/kv/prefix/2", "two")
			require.NoError(t, err)

			resp, err := tc.Client.Get(ctx, "compat/kv/prefix/", clientv3.WithPrefix())
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 2)
		})

		t.Run("TxnSuccess", func(t *testing.T) {
			_, err := tc.Client.Put(ctx, "compat/kv/txn", "old")
			require.NoError(t, err)

			txnResp, err := tc.Client.Txn(ctx).
				If(clientv3.Compare(clientv3.Value("compat/kv/txn"), "=", "old")).
				Then(clientv3.OpPut("compat/kv/txn", "new")).
				Else(clientv3.OpGet("compat/kv/txn")).
				Commit()
			require.NoError(t, err)
			require.True(t, txnResp.Succeeded)

			resp, err := tc.Client.Get(ctx, "compat/kv/txn")
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1)
			require.Equal(t, "new", string(resp.Kvs[0].Value))
		})

		t.Run("TxnFailure", func(t *testing.T) {
			txnResp, err := tc.Client.Txn(ctx).
				If(clientv3.Compare(clientv3.Value("compat/kv/txn"), "=", "missing")).
				Then(clientv3.OpPut("compat/kv/txn", "should-not-happen")).
				Else(clientv3.OpGet("compat/kv/txn")).
				Commit()
			require.NoError(t, err)
			require.False(t, txnResp.Succeeded)
			require.NotEmpty(t, txnResp.Responses)
			require.Len(t, txnResp.Responses, 1)
			require.Len(t, txnResp.Responses[0].GetResponseRange().Kvs, 1)
			require.Equal(t, "new", string(txnResp.Responses[0].GetResponseRange().Kvs[0].Value))

			resp, err := tc.Client.Get(ctx, "compat/kv/txn")
			require.NoError(t, err)
			require.Len(t, resp.Kvs, 1)
			require.Equal(t, "new", string(resp.Kvs[0].Value))
		})
	})
}
