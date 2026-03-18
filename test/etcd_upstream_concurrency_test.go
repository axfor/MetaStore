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
	etcdconcurrency "go.etcd.io/etcd/client/v3/concurrency"
)

func TestEtcdUpstreamOfficialConcurrency(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		session1, err := etcdconcurrency.NewSession(tc.Client, etcdconcurrency.WithTTL(10))
		require.NoError(t, err)
		defer session1.Close()

		session2, err := etcdconcurrency.NewSession(tc.Client, etcdconcurrency.WithTTL(10))
		require.NoError(t, err)
		defer session2.Close()

		mutex1 := etcdconcurrency.NewMutex(session1, "/compat-lock")
		mutex2 := etcdconcurrency.NewMutex(session2, "/compat-lock")

		require.NoError(t, mutex1.Lock(context.Background()))
		require.ErrorIs(t, mutex2.TryLock(context.Background()), etcdconcurrency.ErrLocked)
		require.NoError(t, mutex1.Unlock(context.Background()))
		require.NoError(t, mutex2.TryLock(context.Background()))
		require.NoError(t, mutex2.Unlock(context.Background()))
	})
}
