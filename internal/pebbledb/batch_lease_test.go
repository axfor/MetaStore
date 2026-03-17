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

package pebbledb

import (
	"testing"
	"time"

	"metaStore/internal/kvstore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantThenPut(t *testing.T) {
	store, cleanup := createTestStore(t, "test-batch-lease-grant-put")
	defer cleanup()

	store.applyOperationsBatch([]*RaftOperation{
		{Type: "LEASE_GRANT", LeaseID: 101, TTL: 30},
		{Type: "PUT", Key: "batch/key", Value: "value", LeaseID: 101},
	})

	lease, err := store.getLease(101)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, lease.Keys["batch/key"], "lease should track keys written later in the same batch")

	kv, err := store.getKeyValue("batch/key")
	require.NoError(t, err)
	require.NotNil(t, kv)
	assert.Equal(t, int64(101), kv.Lease)
}

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantPutThenRevoke(t *testing.T) {
	store, cleanup := createTestStore(t, "test-batch-lease-grant-put-revoke")
	defer cleanup()

	store.applyOperationsBatch([]*RaftOperation{
		{Type: "LEASE_GRANT", LeaseID: 202, TTL: 30},
		{Type: "PUT", Key: "batch/revoke", Value: "value", LeaseID: 202},
		{Type: "LEASE_REVOKE", LeaseID: 202},
	})

	lease, err := store.getLease(202)
	assert.Error(t, err)
	assert.Nil(t, lease)

	kv, err := store.getKeyValue("batch/revoke")
	assert.NoError(t, err)
	assert.Nil(t, kv)
}

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantThenTxnPut(t *testing.T) {
	store, cleanup := createTestStore(t, "test-batch-lease-grant-txn-put")
	defer cleanup()

	store.applyOperationsBatch([]*RaftOperation{
		{Type: "LEASE_GRANT", LeaseID: 303, TTL: 30},
		{
			Type: "TXN",
			ThenOps: []kvstore.Op{
				{
					Type:    kvstore.OpPut,
					Key:     []byte("batch/txn"),
					Value:   []byte("value"),
					LeaseID: 303,
				},
			},
		},
	})

	lease, err := store.getLease(303)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, lease.Keys["batch/txn"], "lease should track keys written by a later txn in the same batch")

	kv, err := store.getKeyValue("batch/txn")
	require.NoError(t, err)
	require.NotNil(t, kv)
	assert.Equal(t, int64(303), kv.Lease)
}

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantRenewThenPut(t *testing.T) {
	store, cleanup := createTestStore(t, "test-batch-lease-grant-renew-put")
	defer cleanup()

	renewedAt := time.Unix(1_700_000_000, 123).UnixNano()
	store.applyOperationsBatch([]*RaftOperation{
		{Type: "LEASE_GRANT", LeaseID: 404, TTL: 30},
		{Type: "LEASE_RENEW", LeaseID: 404, GrantTime: renewedAt},
		{Type: "PUT", Key: "batch/renew", Value: "value", LeaseID: 404},
	})

	lease, err := store.getLease(404)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Equal(t, time.Unix(0, renewedAt), lease.GrantTime)
	assert.True(t, lease.Keys["batch/renew"])

	kv, err := store.getKeyValue("batch/renew")
	require.NoError(t, err)
	require.NotNil(t, kv)
	assert.Equal(t, int64(404), kv.Lease)
}

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantRevokeThenRenewKeepsLeaseDeleted(t *testing.T) {
	store, cleanup := createTestStore(t, "test-batch-lease-grant-revoke-renew")
	defer cleanup()

	store.applyOperationsBatch([]*RaftOperation{
		{Type: "LEASE_GRANT", LeaseID: 405, TTL: 30},
		{Type: "LEASE_REVOKE", LeaseID: 405},
		{Type: "LEASE_RENEW", LeaseID: 405, GrantTime: time.Unix(1_700_000_010, 0).UnixNano()},
	})

	lease, err := store.getLease(405)
	assert.Error(t, err)
	assert.Nil(t, lease)
}
