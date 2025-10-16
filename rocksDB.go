// Copyright 2024 The etcd Authors
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
	"encoding/binary"
	"sync"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

var (
	// Prefixes for keys to distinguish between raft log, metadata, and application data.
	raftLogPrefix    = []byte("rlog_")
	raftMetaPrefix   = []byte("rmeta_")
	kvDataPrefix     = []byte("kv_")
	hardStateKey     = append(raftMetaPrefix, []byte("hardstate")...)
	snapshotKey      = append(raftMetaPrefix, []byte("snapshot")...)
	firstIndexKey    = append(raftMetaPrefix, []byte("firstindex")...)
	appliedIndexKey  = append(raftMetaPrefix, []byte("appliedindex")...)
)

// RocksDBStorage implements the raft.Storage interface using RocksDB.
// It also provides methods to manage application key-value data.
type RocksDBStorage struct {
	*raft.MemoryStorage
	db     *grocksdb.DB
	ro     *grocksdb.ReadOptions
	wo     *grocksdb.WriteOptions
	logger *zap.Logger
	mu     sync.RWMutex

	// Caches for frequently accessed metadata to avoid DB lookups.
	firstIndexCache    uint64
	lastIndexCache     uint64
	hardStateCache     raftpb.HardState
	snapshotCache      raftpb.Snapshot
}

// NewRocksDBStorage creates a new RocksDBStorage instance.
func NewRocksDBStorage(db *grocksdb.DB, logger *zap.Logger) (*RocksDBStorage, error) {
	ro := grocksdb.NewDefaultReadOptions()
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(true) // Ensure writes are synced to disk for durability.

	s := &RocksDBStorage{
		MemoryStorage: raft.NewMemoryStorage(),
		db:            db,
		ro:            ro,
		wo:            wo,
		logger:        logger,
	}

	if err := s.loadMetadata(); err != nil {
		return nil, err
	}

	return s, nil
}

// loadMetadata pre-fetches essential raft metadata from the database into caches.
func (s *RocksDBStorage) loadMetadata() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load HardState
	hsData, err := s.db.Get(s.ro, hardStateKey)
	if err != nil {
		return err
	}
	if hsData.Size() > 0 {
		if err := s.hardStateCache.Unmarshal(hsData.Data()); err != nil {
			hsData.Free()
			return err
		}
	}
	hsData.Free()

	// Load Snapshot
	snapData, err := s.db.Get(s.ro, snapshotKey)
	if err != nil {
		return err
	}
	if snapData.Size() > 0 {
		if err := s.snapshotCache.Unmarshal(snapData.Data()); err != nil {
			snapData.Free()
			return err
		}
	}
	snapData.Free()

	// Load first index, default to 1 if not present.
	fiData, err := s.db.Get(s.ro, firstIndexKey)
	if err != nil {
		return err
	}
	if fiData.Size() > 0 {
		s.firstIndexCache = binary.BigEndian.Uint64(fiData.Data())
	} else {
		s.firstIndexCache = 1 // Raft log starts at 1.
	}
	fiData.Free()

	// Load last index by iterating from the end.
	s.lastIndexCache, err = s.findLastIndex()
	if err != nil {
		return err
	}

	// If the log is empty, last index should be first index - 1.
	if s.lastIndexCache == 0 {
		s.lastIndexCache = s.firstIndexCache -1
	}

	return nil
}

// InitialState returns the saved HardState and ConfState from the storage.
func (s *RocksDBStorage) InitialState() (raftpb.HardState, raftpb.ConfState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hardStateCache, s.snapshotCache.Metadata.ConfState, nil
}

// Entries returns a slice of log entries in the range [lo, hi).
func (s *RocksDBStorage) Entries(lo, hi, maxSize uint64) ([]raftpb.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if lo < s.firstIndexCache {
		return nil, raft.ErrCompacted
	}
	if hi > s.lastIndexCache+1 {
		s.logger.Panic("Entries hi is out of bound", zap.Uint64("hi", hi), zap.Uint64("last-index", s.lastIndexCache))
	}
	if lo >= hi {
		return nil, nil
	}

	ents := make([]raftpb.Entry, 0, hi-lo)
	var totalSize uint64

	for i := lo; i < hi; i++ {
		key := s.logKey(i)
		val, err := s.db.Get(s.ro, key)
		if err != nil {
			return nil, err
		}

		var ent raftpb.Entry
		if err := ent.Unmarshal(val.Data()); err != nil {
			val.Free()
			return nil, err
		}
		val.Free()

		totalSize += uint64(ent.Size())
		if len(ents) > 0 && totalSize > maxSize {
			break
		}
		ents = append(ents, ent)
	}
	return ents, nil
}

// Term returns the term of the entry at the given index.
func (s *RocksDBStorage) Term(i uint64) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if i < s.firstIndexCache-1 || i > s.lastIndexCache {
		return 0, nil
	}

	if i == s.snapshotCache.Metadata.Index {
		return s.snapshotCache.Metadata.Term, nil
	}

	key := s.logKey(i)
	val, err := s.db.Get(s.ro, key)
	if err != nil {
		return 0, err
	}
	defer val.Free()

	var ent raftpb.Entry
	if err := ent.Unmarshal(val.Data()); err != nil {
		return 0, err
	}
	return ent.Term, nil
}

// LastIndex returns the index of the last entry in the log.
func (s *RocksDBStorage) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastIndexCache, nil
}

// FirstIndex returns the index of the first available entry in the log.
func (s *RocksDBStorage) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstIndexCache, nil
}

// Snapshot returns the most recent snapshot.
func (s *RocksDBStorage) Snapshot() (raftpb.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotCache, nil
}

// ApplySnapshot overwrites the storage with a new snapshot.
func (s *RocksDBStorage) ApplySnapshot(snap raftpb.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapData, err := snap.Marshal()
	if err != nil {
		return err
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()
	wb.Put(snapshotKey, snapData)

	// Update caches
	s.snapshotCache = snap
	s.firstIndexCache = snap.Metadata.Index + 1
	s.lastIndexCache = snap.Metadata.Index

	// Persist first index
	firstIndexBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(firstIndexBytes, s.firstIndexCache)
	wb.Put(firstIndexKey, firstIndexBytes)

	return s.db.Write(s.wo, wb)
}

// AppendEntries appends a slice of entries to the log.
func (s *RocksDBStorage) AppendEntries(entries []raftpb.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for _, ent := range entries {
		key := s.logKey(ent.Index)
		val, err := ent.Marshal()
		if err != nil {
			return err
		}
		wb.Put(key, val)
	}

	if err := s.db.Write(s.wo, wb); err != nil {
		return err
	}

	// Update last index cache
	s.lastIndexCache = entries[len(entries)-1].Index
	return nil
}

// SetHardState saves the current HardState.
func (s *RocksDBStorage) SetHardState(st raftpb.HardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.hardStateCache = st
	data, err := st.Marshal()
	if err != nil {
		return err
	}
	return s.db.Put(s.wo, hardStateKey, data)
}

// Compact discards log entries up to a given index.
func (s *RocksDBStorage) Compact(compactIndex uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if compactIndex <= s.firstIndexCache {
		return nil // Already compacted
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for i := s.firstIndexCache; i < compactIndex; i++ {
		wb.Delete(s.logKey(i))
	}

	firstIndexBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(firstIndexBytes, compactIndex)
	wb.Put(firstIndexKey, firstIndexBytes)

	if err := s.db.Write(s.wo, wb); err != nil {
		return err
	}

	s.firstIndexCache = compactIndex
	return nil
}

// --- Helper methods ---

// logKey generates a RocksDB key for a raft log entry.
func (s *RocksDBStorage) logKey(index uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, index)
	return append(raftLogPrefix, b...)
}

// findLastIndex iterates backwards to find the last log index.
func (s *RocksDBStorage) findLastIndex() (uint64, error) {
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	// Seek to the key that is just after the raft log prefix space.
	seekKey := append(raftLogPrefix, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}...)
	it.SeekForPrev(seekKey)

	if !it.Valid() || !bytes.HasPrefix(it.Key().Data(), raftLogPrefix) {
		return 0, nil
	}

	key := it.Key().Data()
	return binary.BigEndian.Uint64(key[len(raftLogPrefix):]), nil
}

// --- KV store methods ---

// PutKV stores a key-value pair for the application.
func (s *RocksDBStorage) PutKV(key, value []byte) error {
	return s.db.Put(s.wo, append(kvDataPrefix, key...), value)
}

// GetKV retrieves a value for a given key.
func (s *RocksDBStorage) GetKV(key []byte) ([]byte, error) {
	val, err := s.db.Get(s.ro, append(kvDataPrefix, key...))
	if err != nil {
		return nil, err
	}
	defer val.Free()
	if val.Size() == 0 {
		return nil, nil // Not found
	}
	return val.Data(), nil
}

// GetKVSnapshot returns a serialized snapshot of the current KV data.
func (s *RocksDBStorage) GetKVSnapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var buf bytes.Buffer
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	it.Seek(kvDataPrefix)
	for it.Valid() && bytes.HasPrefix(it.Key().Data(), kvDataPrefix) {
		// Write key length, key, value length, value
		key := it.Key().Data()[len(kvDataPrefix):]
		val := it.Value().Data()

		lenBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBuf, uint64(len(key)))
		buf.Write(lenBuf)
		buf.Write(key)

		binary.BigEndian.PutUint64(lenBuf, uint64(len(val)))
		buf.Write(lenBuf)
		buf.Write(val)

		it.Next()
	}
	return buf.Bytes(), nil
}

// RestoreKVSnapshot restores the KV data from a snapshot.
func (s *RocksDBStorage) RestoreKVSnapshot(snapshot []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing KV data
	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()
	it := s.db.NewIterator(s.ro)
	defer it.Close()
	it.Seek(kvDataPrefix)
	for it.Valid() && bytes.HasPrefix(it.Key().Data(), kvDataPrefix) {
		wb.Delete(it.Key().Data())
		it.Next()
	}

	// Restore from snapshot
	buf := bytes.NewBuffer(snapshot)
	lenBuf := make([]byte, 8)
	for buf.Len() > 0 {
		// Read key
		if _, err := buf.Read(lenBuf); err != nil {
			return err
		}
		keyLen := binary.BigEndian.Uint64(lenBuf)
		key := make([]byte, keyLen)
		if _, err := buf.Read(key); err != nil {
			return err
		}

		// Read value
		if _, err := buf.Read(lenBuf); err != nil {
			return err
		}
		valLen := binary.BigEndian.Uint64(lenBuf)
		val := make([]byte, valLen)
		if _, err := buf.Read(val); err != nil {
			return err
		}

		wb.Put(append(kvDataPrefix, key...), val)
	}

	return s.db.Write(s.wo, wb)
}