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
)

func TestEtcdUpstreamKVPutGet(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		ctx := context.Background()

		_, err := tc.Client.Put(ctx, "compat/kv/basic", "value")
		require.NoError(t, err)

		resp, err := tc.Client.Get(ctx, "compat/kv/basic")
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
	})
}
