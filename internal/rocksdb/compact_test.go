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

package rocksdb

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"testing"
	"time"

	"metaStore/internal/kvstore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
)

// Helper: create a test RocksDB store
func createTestStore(t *testing.T, tmpDir string) (*RocksDB, func()) {
	db, err := Open(tmpDir)
	require.NoError(t, err)

	// Create snapshotter directory
	snapDir := tmpDir + "/snap"
	err = os.MkdirAll(snapDir, 0755)
	require.NoError(t, err)

	// Create snapshotter
	snapshotter := snap.New(nil, snapDir)

	proposeC := make(chan string, 10)
	commitC := make(chan *kvstore.Commit, 10)
	errorC := make(chan error, 1)

	store := NewRocksDB(db, snapshotter, proposeC, commitC, errorC)

	cleanup := func() {
		store.Close()
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestRocksDB_Compact_Basic(t *testing.T) {
	tmpDir := "test-compact-basic"
	store, cleanup := createTestStore(t, tmpDir)
	defer cleanup()

	// Simulate some operations to increase revision
	for i := 1; i <= 100; i++ {
		err := store.putUnlocked("key"+string(rune('0'+i%10)), "value", 0)
		require.NoError(t, err)
	}

	currentRev := store.CurrentRevision()
	require.Greater(t, currentRev, int64(0))

	// Compact to revision 50
	targetRev := int64(50)
	err := store.Compact(context.Background(), targetRev)
	require.NoError(t, err)

	// Verify compacted revision is recorded
	compactedRev := store.getCompactedRevisionUnlocked()
	assert.Equal(t, targetRev, compactedRev)
}

func TestRocksDB_Compact_Validation(t *testing.T) {
	tmpDir := "test-compact-validation"
	store, cleanup := createTestStore(t, tmpDir)
	defer cleanup()

	// Put some data
	for i := 1; i <= 50; i++ {
		err := store.putUnlocked("key", "value", 0)
		require.NoError(t, err)
	}

	currentRev := store.CurrentRevision()

	// Test 1: Cannot compact to revision 0
	err := store.Compact(context.Background(), 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")

	// Test 2: Cannot compact to negative revision
	err = store.Compact(context.Background(), -1)
	assert.Error(t, err)

	// Test 3: Cannot compact to future revision
	err = store.Compact(context.Background(), currentRev+100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "future")

	// Test 4: Successfully compact to valid revision
	targetRev := currentRev - 10
	err = store.Compact(context.Background(), targetRev)
	require.NoError(t, err)

	// Test 5: Cannot compact backwards
	err = store.Compact(context.Background(), targetRev-5)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already compacted")
}

func TestRocksDB_Compact_ExpiredLeases(t *testing.T) {
	tmpDir := "test-compact-leases"
	store, cleanup := createTestStore(t, tmpDir)
	defer cleanup()

	// Create some leases with short TTL
	ctx := context.Background()

	// Lease 1: Already expired (TTL 1 second, granted 10 seconds ago)
	lease1 := &kvstore.Lease{
		ID:        100,
		TTL:       1,
		GrantTime: time.Now().Add(-10 * time.Second),
		Keys:      make(map[string]bool),
	}
	err := saveLeaseForTest(store, lease1)
	require.NoError(t, err)

	// Lease 2: Still valid (TTL 1000 seconds, just granted)
	lease2 := &kvstore.Lease{
		ID:        200,
		TTL:       1000,
		GrantTime: time.Now(),
		Keys:      make(map[string]bool),
	}
	err = saveLeaseForTest(store, lease2)
	require.NoError(t, err)

	// Put some data to increase revision
	for i := 1; i <= 50; i++ {
		err := store.putUnlocked("key", "value", 0)
		require.NoError(t, err)
	}

	// Compact should clean up expired lease
	err = store.Compact(ctx, 40)
	require.NoError(t, err)

	// Verify lease1 is deleted
	l1, _ := getLeaseForTest(store, 100)
	assert.Nil(t, l1)

	// Verify lease2 still exists
	l2, err := getLeaseForTest(store, 200)
	require.NoError(t, err)
	assert.NotNil(t, l2)
	assert.Equal(t, int64(200), l2.ID)
}

func TestRocksDB_Compact_PhysicalCompaction(t *testing.T) {
	tmpDir := "test-compact-physical"
	store, cleanup := createTestStore(t, tmpDir)
	defer cleanup()

	// Write a lot of data
	for i := 1; i <= 1000; i++ {
		key := fmt.Sprintf("key%d", i%100)
		err := store.putUnlocked(key, "value", 0)
		require.NoError(t, err)
	}

	// Delete half of it
	for i := 1; i <= 500; i++ {
		key := fmt.Sprintf("key%d", i%100)
		err := store.deleteUnlocked(key, "")
		require.NoError(t, err)
	}

	currentRev := store.CurrentRevision()

	// Compact - this should trigger RocksDB physical compaction
	err := store.Compact(context.Background(), currentRev-100)
	require.NoError(t, err)

	// Verify store is still functional after compaction
	err = store.putUnlocked("test-after-compact", "value", 0)
	require.NoError(t, err)

	kv, err := store.getKeyValue("test-after-compact")
	require.NoError(t, err)
	assert.Equal(t, "value", string(kv.Value))
}

func TestRocksDB_Compact_Sequential(t *testing.T) {
	tmpDir := "test-compact-sequential"
	store, cleanup := createTestStore(t, tmpDir)
	defer cleanup()

	// Generate revisions
	for i := 1; i <= 200; i++ {
		err := store.putUnlocked("key", "value", 0)
		require.NoError(t, err)
	}

	// Sequential compactions
	err := store.Compact(context.Background(), 50)
	require.NoError(t, err)
	assert.Equal(t, int64(50), store.getCompactedRevisionUnlocked())

	err = store.Compact(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(100), store.getCompactedRevisionUnlocked())

	err = store.Compact(context.Background(), 150)
	require.NoError(t, err)
	assert.Equal(t, int64(150), store.getCompactedRevisionUnlocked())
}

// Helper: save lease for testing
func saveLeaseForTest(r *RocksDB, lease *kvstore.Lease) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		return err
	}

	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, lease.ID))
	return r.db.Put(r.wo, dbKey, buf.Bytes())
}

// Helper: get lease for testing
func getLeaseForTest(r *RocksDB, id int64) (*kvstore.Lease, error) {
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	value, err := r.db.Get(r.ro, dbKey)
	if err != nil {
		return nil, err
	}
	defer value.Free()

	if value.Size() == 0 {
		return nil, nil
	}

	var lease kvstore.Lease
	if err := gob.NewDecoder(bytes.NewReader(value.Data())).Decode(&lease); err != nil {
		return nil, err
	}

	return &lease, nil
}
