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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type etcdUpstreamSingleNodeCase struct {
	Name     string
	Client   *clientv3.Client
	Endpoint string
}

func forEachEtcdUpstreamSingleNodeBackend(t *testing.T, fn func(t *testing.T, tc etcdUpstreamSingleNodeCase)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		node, cleanup := startMemoryNode(t, 1)
		client, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() {
			client.Close()
			cleanup()
		})
		fn(t, etcdUpstreamSingleNodeCase{Name: "memory", Client: client, Endpoint: node.clientAddr})
	})

	t.Run("pebble", func(t *testing.T) {
		node, cleanup := startPebbleNode(t, 1)
		client, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() {
			client.Close()
			cleanup()
		})
		fn(t, etcdUpstreamSingleNodeCase{Name: "pebble", Client: client, Endpoint: node.clientAddr})
	})
}
