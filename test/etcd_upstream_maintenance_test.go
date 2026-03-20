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
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func waitForMaintenanceReady(t *testing.T, client pb.MaintenanceClient) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		resp, err := client.Status(ctx, &pb.StatusRequest{})
		return err == nil && resp != nil && resp.Header != nil
	}, 3*time.Second, 100*time.Millisecond)
}

func TestEtcdUpstreamMaintenanceSmoke(t *testing.T) {
	forEachEtcdUpstreamSingleNodeBackend(t, func(t *testing.T, tc etcdUpstreamSingleNodeCase) {
		ctx := context.Background()

		_, err := tc.Client.Put(ctx, "compat/maintenance/key", "value")
		require.NoError(t, err)

		conn, err := grpc.Dial(tc.Endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		defer conn.Close()

		maintenanceClient := pb.NewMaintenanceClient(conn)
		waitForMaintenanceReady(t, maintenanceClient)

		statusResp, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
		require.NoError(t, err)
		require.Greater(t, statusResp.Header.Revision, int64(0))

		hashResp, err := maintenanceClient.Hash(ctx, &pb.HashRequest{})
		require.NoError(t, err)
		require.NotZero(t, hashResp.Hash)

		hashKVResp, err := maintenanceClient.HashKV(ctx, &pb.HashKVRequest{Revision: statusResp.Header.Revision})
		require.NoError(t, err)
		require.Equal(t, statusResp.Header.Revision, hashKVResp.Header.Revision)

		snapshotClient, err := maintenanceClient.Snapshot(ctx, &pb.SnapshotRequest{})
		require.NoError(t, err)

		snapshotChunk, err := snapshotClient.Recv()
		require.NoError(t, err)
		require.NotEmpty(t, snapshotChunk.Blob)
	})
}
