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
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestEtcdUpstreamWatchSmoke(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		watchCh := tc.Client.Watch(ctx, "compat/watch/basic", clientv3.WithCreatedNotify())

		select {
		case resp := <-watchCh:
			require.True(t, resp.Created)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for watch creation")
		}

		_, err := tc.Client.Put(context.Background(), "compat/watch/basic", "value")
		require.NoError(t, err)

		select {
		case resp := <-watchCh:
			require.NotEmpty(t, resp.Events)
			require.Equal(t, mvccpb.PUT, resp.Events[0].Type)
			require.Equal(t, "compat/watch/basic", string(resp.Events[0].Kv.Key))
			require.Equal(t, "value", string(resp.Events[0].Kv.Value))
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for put watch event")
		}
	})
}
