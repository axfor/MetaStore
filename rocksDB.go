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
	"bytes"
	"encoding/gob"
	"log"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

var (
	raftStateKey = []byte("raftState")
)

// RocksDBStorage implements the raft.Storage interface.
type RocksDBStorage struct {
	db       *grocksdb.DB
	logger   *zap.Logger
	useSync  bool
	wo       *grocksdb.WriteOptions
	ro       *grocksdb.ReadOptions
}

// NewRocksDBStorage creates a new RocksDBStorage.
func NewRocksDBStorage(db *grocksdb.DB, logger *zap.Logger, useSync, disableWAL bool) (*RocksDBStorage, error) {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(useSync)
	wo.SetDisableWAL(disableWAL)

	return &RocksDBStorage{
		db:      db,
		logger:  logger,
		useSync: useSync,
		wo:      wo,
		ro:      grocksdb.NewDefaultReadOptions(),
	}, nil
}

// InitialState returns the saved HardState and ConfState information.
func (s *RocksDBStorage) InitialState() (raftpb.HardState, raftpb.ConfState, error) {
	val, err := s.db.Get(s.ro, raftStateKey)
	if err != nil {
		return raftpb.HardState{}, raftpb.ConfState{}, err
	}
	defer val.Free()

	if !val.Exists() {
		return raftpb.HardState{}, raftpb.ConfState{}, nil
	}

	var state raftpb.HardState
	if err := state.Unmarshal(val.Data()); err != nil {
		return raftpb.HardState{}, raftpb.ConfState{}, err
	}
	return state, raftpb.ConfState{}, nil
}

// Entries returns a slice of log entries in the range [lo,hi).
// MaxSize limits the total size of the log entries.
func (s *RocksDBStorage) Entries(lo, hi, maxSize uint64) ([]raftpb.Entry, error) {
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	it.Seek(encodeKey(lo))
	var entries []raftpb.Entry
	var size uint64
	for it.Valid() {
		key := it.Key().Data()
		idx := decodeKey(key)
		if idx >= hi {
			break
		}

		var entry raftpb.Entry
		if err := entry.Unmarshal(it.Value().Data()); err != nil {
			return nil, err
		}

		size += uint64(entry.Size())
		if size > maxSize && len(entries) > 0 {
			break
		}
		entries = append(entries, entry)
		it.Next()
	}
	return entries, nil
}

// Term returns the term of entry i, which must be in the range
// [FirstIndex()-1, LastIndex()]. The term of the entry before
// FirstIndex is retained for matching purpose even though the
// rest of that entry may not be available.
func (s *RocksDBStorage) Term(i uint64) (uint64, error) {
	if i == 0 {
		return 0, nil
	}
	val, err := s.db.Get(s.ro, encodeKey(i))
	if err != nil {
		return 0, err
	}
	defer val.Free()

	if !val.Exists() {
		return 0, nil
	}

	var entry raftpb.Entry
	if err := entry.Unmarshal(val.Data()); err != nil {
		return 0, err
	}
	return entry.Term, nil
}

// LastIndex returns the index of the last entry in the log.
func (s *RocksDBStorage) LastIndex() (uint64, error) {
	it := s.db.NewIterator(s.ro)
	defer it.Close()
	it.SeekForPrev(encodeKey(uint64(1<<63 - 1))) // Seek to the last key
	if !it.Valid() {
		return 0, nil
	}
	return decodeKey(it.Key().Data()), nil
}

// FirstIndex returns the index of the first log entry that is
// possibly available via Entries (older entries have been incorporated
// into a snapshot; if storage only contains a snapshot, FirstIndex is
// snapshot.Metadata.Index + 1).
func (s *RocksDBStorage) FirstIndex() (uint64, error) {
	it := s.db.NewIterator(s.ro)
	defer it.Close()
	it.Seek(encodeKey(0))
	if !it.Valid() {
		return 1, nil
	}
	return decodeKey(it.Key().Data()), nil
}

// Snapshot returns the most recent snapshot.
// If there are no snapshots, it returns ErrSnapshotTemporarilyUnavailable.
func (s *RocksDBStorage) Snapshot() (raftpb.Snapshot, error) {
	val, err := s.db.Get(s.ro, raftStateKey)
	if err != nil {
		return raftpb.Snapshot{}, err
	}
	defer val.Free()

	if !val.Exists() {
		return raftpb.Snapshot{}, raft.ErrSnapshotTemporarilyUnavailable
	}

	var snap raftpb.Snapshot
	if err := snap.Unmarshal(val.Data()); err != nil {
		return raftpb.Snapshot{}, err
	}
	return snap, nil
}

// ApplySnapshot overwrites the contents of this Storage object with
// the contents of the given snapshot.
func (s *RocksDBStorage) ApplySnapshot(snap raftpb.Snapshot) error {
	b, err := snap.Marshal()
	if err != nil {
		return err
	}
	return s.db.Put(s.wo, raftStateKey, b)
}

// CreateSnapshot makes a snapshot which can be retrieved with Snapshot() and
// can be used to reconstruct the state at that point.
// If any configuration changes have been made since the last compaction,
// the configuration change entries must be retained in the log.
func (s *RocksDBStorage) CreateSnapshot(i uint64, cs *raftpb.ConfState, data []byte) (raftpb.Snapshot, error) {
	lastIdx, err := s.LastIndex()
	if err != nil {
		return raftpb.Snapshot{}, err
	}
	if i > lastIdx {
		log.Panicf("snapshot %d is out of bound lastindex(%d)", i, lastIdx)
	}

	term, err := s.Term(i)
	if err != nil {
		return raftpb.Snapshot{}, err
	}

	snap := raftpb.Snapshot{
		Data: data,
		Metadata: raftpb.SnapshotMetadata{
			ConfState: *cs,
			Index:     i,
			Term:      term,
		},
	}
	return snap, nil
}

// Compact discards all log entries prior to compactIndex.
// It is the application's responsibility to not attempt to compact an index
// greater than raftLog.applied.
func (s *RocksDBStorage) Compact(compactIndex uint64) error {
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for it.Seek(encodeKey(0)); it.Valid(); it.Next() {
		idx := decodeKey(it.Key().Data())
		if idx >= compactIndex {
			break
		}
		wb.Delete(it.Key().Data())
	}
	return s.db.Write(s.wo, wb)
}

// AppendEntries writes a slice of entries to the log.
func (s *RocksDBStorage) AppendEntries(entries []raftpb.Entry) error {
	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for _, entry := range entries {
		key := encodeKey(entry.Index)
		val, err := entry.Marshal()
		if err != nil {
			return err
		}
		wb.Put(key, val)
	}
	return s.db.Write(s.wo, wb)
}

// SetHardState saves the current HardState.
func (s *RocksDBStorage) SetHardState(st raftpb.HardState) error {
	b, err := st.Marshal()
	if err != nil {
		return err
	}
	return s.db.Put(s.wo, raftStateKey, b)
}

// PutKV stores a key-value pair.
func (s *RocksDBStorage) PutKV(key, value []byte) error {
	return s.db.Put(s.wo, key, value)
}

// GetKV retrieves a value for a given key.
func (s *RocksDBStorage) GetKV(key []byte) ([]byte, error) {
	val, err := s.db.Get(s.ro, key)
	if err != nil {
		return nil, err
	}
	defer val.Free()

	if !val.Exists() {
		return nil, nil
	}
	return val.Data(), nil
}

// GetKVSnapshot creates a snapshot of the current key-value state.
func (s *RocksDBStorage) GetKVSnapshot() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	it.SeekToFirst()
	for it.Valid() {
		if !bytes.HasPrefix(it.Key().Data(), []byte("kv_")) {
			it.Next()
			continue
		}
		if err := enc.Encode(kv{Key: string(it.Key().Data()), Val: string(it.Value().Data())}); err != nil {
			return nil, err
		}
		it.Next()
	}
	return buf.Bytes(), nil
}

// RestoreKVSnapshot restores the key-value state from a snapshot.
func (s *RocksDBStorage) RestoreKVSnapshot(data []byte) error {
	dec := gob.NewDecoder(bytes.NewBuffer(data))
	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for {
		var kvItem kv
		if err := dec.Decode(&kvItem); err != nil {
			break
		}
		wb.Put([]byte(kvItem.Key), []byte(kvItem.Val))
	}
	return s.db.Write(s.wo, wb)
}

func encodeKey(idx uint64) []byte {
	b := make([]byte, 8)
	gob.NewEncoder(bytes.NewBuffer(b)).Encode(idx)
	return b
}

func decodeKey(b []byte) uint64 {
	var idx uint64
	gob.NewDecoder(bytes.NewBuffer(b)).Decode(&idx)
	return idx
}