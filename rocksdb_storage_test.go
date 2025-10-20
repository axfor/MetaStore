//go:build rocksdb
// +build rocksdb

// Copyright 2015 The etcd Authors
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

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
)

func TestRocksDBStorage_BasicOperations(t *testing.T) {
	// Create temporary directory for test
	tmpDir := "test-rocksdb-storage"
	defer os.RemoveAll(tmpDir)

	// Open RocksDB
	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	// Create storage
	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Test InitialState
	hardState, confState, err := storage.InitialState()
	require.NoError(t, err)
	require.True(t, raft.IsEmptyHardState(hardState))
	require.Empty(t, confState.Voters)

	// Test FirstIndex and LastIndex
	firstIndex, err := storage.FirstIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(1), firstIndex)

	lastIndex, err := storage.LastIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(0), lastIndex) // No entries yet
}

func TestRocksDBStorage_AppendEntries(t *testing.T) {
	tmpDir := "test-rocksdb-append"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Append some entries
	entries := []raftpb.Entry{
		{Term: 1, Index: 1, Data: []byte("entry1")},
		{Term: 1, Index: 2, Data: []byte("entry2")},
		{Term: 2, Index: 3, Data: []byte("entry3")},
	}

	err = storage.Append(entries)
	require.NoError(t, err)

	// Verify LastIndex updated
	lastIndex, err := storage.LastIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(3), lastIndex)

	// Retrieve entries
	retrievedEntries, err := storage.Entries(1, 4, 1000000)
	require.NoError(t, err)
	require.Equal(t, 3, len(retrievedEntries))
	require.Equal(t, entries[0].Data, retrievedEntries[0].Data)
	require.Equal(t, entries[1].Data, retrievedEntries[1].Data)
	require.Equal(t, entries[2].Data, retrievedEntries[2].Data)
}

func TestRocksDBStorage_Term(t *testing.T) {
	tmpDir := "test-rocksdb-term"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Append entries
	entries := []raftpb.Entry{
		{Term: 1, Index: 1},
		{Term: 1, Index: 2},
		{Term: 2, Index: 3},
		{Term: 3, Index: 4},
	}
	err = storage.Append(entries)
	require.NoError(t, err)

	// Test Term retrieval
	term, err := storage.Term(1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), term)

	term, err = storage.Term(3)
	require.NoError(t, err)
	require.Equal(t, uint64(2), term)

	term, err = storage.Term(4)
	require.NoError(t, err)
	require.Equal(t, uint64(3), term)

	// Test error cases
	// Term(0) should return 0 for empty storage (not an error)
	term, err = storage.Term(0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), term)

	_, err = storage.Term(5)
	require.Error(t, err)
}

func TestRocksDBStorage_HardState(t *testing.T) {
	tmpDir := "test-rocksdb-hardstate"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Set HardState
	hardState := raftpb.HardState{
		Term:   5,
		Vote:   2,
		Commit: 10,
	}
	err = storage.SetHardState(hardState)
	require.NoError(t, err)

	// Retrieve HardState
	retrievedHS, _, err := storage.InitialState()
	require.NoError(t, err)
	require.Equal(t, hardState.Term, retrievedHS.Term)
	require.Equal(t, hardState.Vote, retrievedHS.Vote)
	require.Equal(t, hardState.Commit, retrievedHS.Commit)
}

func TestRocksDBStorage_Snapshot(t *testing.T) {
	tmpDir := "test-rocksdb-snapshot"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Append entries first
	entries := []raftpb.Entry{
		{Term: 1, Index: 1, Data: []byte("entry1")},
		{Term: 1, Index: 2, Data: []byte("entry2")},
		{Term: 2, Index: 3, Data: []byte("entry3")},
		{Term: 2, Index: 4, Data: []byte("entry4")},
		{Term: 2, Index: 5, Data: []byte("entry5")},
	}
	err = storage.Append(entries)
	require.NoError(t, err)

	// Create snapshot
	confState := raftpb.ConfState{
		Voters: []uint64{1, 2, 3},
	}
	snapData := []byte("snapshot_data")
	snap, err := storage.CreateSnapshot(3, &confState, snapData)
	require.NoError(t, err)
	require.Equal(t, uint64(3), snap.Metadata.Index)
	require.Equal(t, uint64(2), snap.Metadata.Term)
	require.Equal(t, snapData, snap.Data)

	// Retrieve snapshot
	retrievedSnap, err := storage.Snapshot()
	require.NoError(t, err)
	require.Equal(t, snap.Metadata.Index, retrievedSnap.Metadata.Index)
	require.Equal(t, snap.Metadata.Term, retrievedSnap.Metadata.Term)
	require.Equal(t, snap.Data, retrievedSnap.Data)
}

func TestRocksDBStorage_ApplySnapshot(t *testing.T) {
	tmpDir := "test-rocksdb-apply-snapshot"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Append initial entries
	entries := []raftpb.Entry{
		{Term: 1, Index: 1, Data: []byte("entry1")},
		{Term: 1, Index: 2, Data: []byte("entry2")},
		{Term: 2, Index: 3, Data: []byte("entry3")},
		{Term: 2, Index: 4, Data: []byte("entry4")},
		{Term: 2, Index: 5, Data: []byte("entry5")},
	}
	err = storage.Append(entries)
	require.NoError(t, err)

	// Create and apply snapshot
	snapshot := raftpb.Snapshot{
		Data: []byte("snapshot_data"),
		Metadata: raftpb.SnapshotMetadata{
			Index: 3,
			Term:  2,
			ConfState: raftpb.ConfState{
				Voters: []uint64{1, 2, 3},
			},
		},
	}

	err = storage.ApplySnapshot(snapshot)
	require.NoError(t, err)

	// Verify FirstIndex is updated
	firstIndex, err := storage.FirstIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(4), firstIndex)

	// Verify old entries are deleted
	_, err = storage.Entries(1, 4, 1000000)
	require.Error(t, err) // Should get ErrCompacted

	// Verify new entries still exist
	ents, err := storage.Entries(4, 6, 1000000)
	require.NoError(t, err)
	require.Equal(t, 2, len(ents))
}

func TestRocksDBStorage_Compact(t *testing.T) {
	tmpDir := "test-rocksdb-compact"
	defer os.RemoveAll(tmpDir)

	db, err := OpenRocksDB(tmpDir)
	require.NoError(t, err)
	defer db.Close()

	storage, err := NewRocksDBStorage(db, "test_node")
	require.NoError(t, err)
	defer storage.Close()

	// Append entries
	entries := []raftpb.Entry{
		{Term: 1, Index: 1, Data: []byte("entry1")},
		{Term: 1, Index: 2, Data: []byte("entry2")},
		{Term: 2, Index: 3, Data: []byte("entry3")},
		{Term: 2, Index: 4, Data: []byte("entry4")},
		{Term: 2, Index: 5, Data: []byte("entry5")},
	}
	err = storage.Append(entries)
	require.NoError(t, err)

	// Compact up to index 3
	err = storage.Compact(3)
	require.NoError(t, err)

	// Verify FirstIndex updated
	firstIndex, err := storage.FirstIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(3), firstIndex)

	// Verify compacted entries are inaccessible
	_, err = storage.Entries(1, 3, 1000000)
	require.Error(t, err) // Should get ErrCompacted

	// Verify non-compacted entries are still accessible
	ents, err := storage.Entries(3, 6, 1000000)
	require.NoError(t, err)
	require.Equal(t, 3, len(ents))
}

func TestRocksDBStorage_Persistence(t *testing.T) {
	tmpDir := "test-rocksdb-persistence"
	defer os.RemoveAll(tmpDir)

	// First session: write data
	{
		db, err := OpenRocksDB(tmpDir)
		require.NoError(t, err)

		storage, err := NewRocksDBStorage(db, "test_node")
		require.NoError(t, err)

		// Append entries
		entries := []raftpb.Entry{
			{Term: 1, Index: 1, Data: []byte("persistent_entry1")},
			{Term: 1, Index: 2, Data: []byte("persistent_entry2")},
		}
		err = storage.Append(entries)
		require.NoError(t, err)

		// Set HardState
		hardState := raftpb.HardState{
			Term:   3,
			Vote:   1,
			Commit: 2,
		}
		err = storage.SetHardState(hardState)
		require.NoError(t, err)

		storage.Close()
		db.Close()
	}

	// Second session: read data
	{
		db, err := OpenRocksDB(tmpDir)
		require.NoError(t, err)
		defer db.Close()

		storage, err := NewRocksDBStorage(db, "test_node")
		require.NoError(t, err)
		defer storage.Close()

		// Verify entries persisted
		lastIndex, err := storage.LastIndex()
		require.NoError(t, err)
		require.Equal(t, uint64(2), lastIndex)

		entries, err := storage.Entries(1, 3, 1000000)
		require.NoError(t, err)
		require.Equal(t, 2, len(entries))
		require.Equal(t, []byte("persistent_entry1"), entries[0].Data)
		require.Equal(t, []byte("persistent_entry2"), entries[1].Data)

		// Verify HardState persisted
		hardState, _, err := storage.InitialState()
		require.NoError(t, err)
		require.Equal(t, uint64(3), hardState.Term)
		require.Equal(t, uint64(1), hardState.Vote)
		require.Equal(t, uint64(2), hardState.Commit)
	}
}
