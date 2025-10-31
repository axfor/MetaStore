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
	"encoding/binary"
	"fmt"
	"sync"

	"metaStore/pkg/log"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

const (
	// Key prefixes for different data types
	raftLogPrefix = "raft_log_"
	hardStateKey  = "hard_state"
	confStateKey  = "conf_state"
	snapshotKey   = "snapshot_meta"
	firstIndexKey = "first_index"
	lastIndexKey  = "last_index"
)

// RocksDBStorage implements raft.Storage interface backed by RocksDB.
// It provides persistent storage for raft logs, hard state, and snapshots.
type RocksDBStorage struct {
	db     *grocksdb.DB
	wo     *grocksdb.WriteOptions
	ro     *grocksdb.ReadOptions
	nodeID string // Unique identifier for this node's data within the DB
	mu     sync.RWMutex

	// Cache for performance
	firstIndex uint64
	lastIndex  uint64
}

// NewRocksDBStorage creates a new Storage implementation using RocksDB.
// It requires an already opened, writable grocksdb.DB instance and a unique nodeID.
func NewRocksDBStorage(db *grocksdb.DB, nodeID string) (*RocksDBStorage, error) {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(true) // Ensure durability for raft operations
	ro := grocksdb.NewDefaultReadOptions()

	storage := &RocksDBStorage{
		db:     db,
		wo:     wo,
		ro:     ro,
		nodeID: nodeID,
	}

	// Initialize or load index cache
	firstIndex, err := storage.getFirstIndexUnsafe()
	if err != nil {
		// Initialize firstIndex to 1 if not found
		firstIndex = 1
		if err := storage.setFirstIndexUnsafe(firstIndex); err != nil {
			return nil, fmt.Errorf("failed to initialize first index: %v", err)
		}
	}
	storage.firstIndex = firstIndex

	lastIndex, err := storage.getLastIndexUnsafe()
	if err != nil {
		// No entries yet, lastIndex = firstIndex - 1
		lastIndex = firstIndex - 1
		if err := storage.setLastIndexUnsafe(lastIndex); err != nil {
			return nil, fmt.Errorf("failed to initialize last index: %v", err)
		}
	}
	storage.lastIndex = lastIndex

	return storage, nil
}

// Close closes the RocksDB storage and releases resources
func (s *RocksDBStorage) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wo != nil {
		s.wo.Destroy()
		s.wo = nil
	}
	if s.ro != nil {
		s.ro.Destroy()
		s.ro = nil
	}
}

// prefixedKey generates a key for a given key type and nodeID.
func (s *RocksDBStorage) prefixedKey(key string) []byte {
	return []byte(fmt.Sprintf("%s_%s", s.nodeID, key))
}

// logKey generates a key for storing a raft log entry.
func (s *RocksDBStorage) logKey(index uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return bytes.Join([][]byte{s.prefixedKey(raftLogPrefix), buf}, []byte("_"))
}

// InitialState implements the raft.Storage interface.
// It returns the hard state and conf state.
func (s *RocksDBStorage) InitialState() (raftpb.HardState, raftpb.ConfState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var hardState raftpb.HardState
	var confState raftpb.ConfState

	// Load HardState
	hsData, err := s.db.Get(s.ro, s.prefixedKey(hardStateKey))
	if err != nil {
		return hardState, confState, err
	}
	defer hsData.Free()

	if hsData.Size() > 0 {
		if err := hardState.Unmarshal(hsData.Data()); err != nil {
			return hardState, confState, fmt.Errorf("failed to unmarshal hard state: %v", err)
		}
	}

	// Load ConfState
	csData, err := s.db.Get(s.ro, s.prefixedKey(confStateKey))
	if err != nil {
		return hardState, confState, err
	}
	defer csData.Free()

	if csData.Size() > 0 {
		if err := confState.Unmarshal(csData.Data()); err != nil {
			return hardState, confState, fmt.Errorf("failed to unmarshal conf state: %v", err)
		}
	}

	return hardState, confState, nil
}

// Entries implements the raft.Storage interface.
// It returns a slice of log entries in the range [lo, hi), limited by maxSize.
func (s *RocksDBStorage) Entries(lo, hi, maxSize uint64) ([]raftpb.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if lo > hi {
		return nil, fmt.Errorf("invalid range: lo(%d) > hi(%d)", lo, hi)
	}

	firstIndex := s.firstIndex
	lastIndex := s.lastIndex

	if lo < firstIndex {
		return nil, raft.ErrCompacted
	}
	if hi > lastIndex+1 {
		return nil, raft.ErrUnavailable
	}
	if lo == hi {
		return nil, nil
	}

	var ents []raftpb.Entry
	size := uint64(0)

	for i := lo; i < hi; i++ {
		key := s.logKey(i)
		data, err := s.db.Get(s.ro, key)
		if err != nil {
			return nil, fmt.Errorf("failed to get entry %d: %v", i, err)
		}

		if data.Size() == 0 {
			data.Free()
			return nil, raft.ErrUnavailable
		}

		var ent raftpb.Entry
		if err := ent.Unmarshal(data.Data()); err != nil {
			data.Free()
			return nil, fmt.Errorf("failed to unmarshal entry %d: %v", i, err)
		}
		data.Free()

		entSize := uint64(ent.Size())
		if size > 0 && size+entSize > maxSize {
			// We've exceeded maxSize, return what we have
			break
		}

		ents = append(ents, ent)
		size += entSize
	}

	return ents, nil
}

// Term implements the raft.Storage interface.
// It returns the term of entry i, which must be in the range [FirstIndex()-1, LastIndex()].
func (s *RocksDBStorage) Term(index uint64) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	firstIndex := s.firstIndex
	lastIndex := s.lastIndex

	if index < firstIndex-1 {
		return 0, raft.ErrCompacted
	}
	if index > lastIndex {
		return 0, raft.ErrUnavailable
	}

	// Special case: asking for term of firstIndex-1
	// This is typically from a snapshot
	if index == firstIndex-1 {
		snap, err := s.loadSnapshotUnsafe()
		if err != nil {
			return 0, err
		}
		if !raft.IsEmptySnap(snap) && snap.Metadata.Index == index {
			return snap.Metadata.Term, nil
		}
		// For empty storage (no snapshot, no logs), return term 0
		if index == 0 {
			return 0, nil
		}
		return 0, raft.ErrCompacted
	}

	key := s.logKey(index)
	data, err := s.db.Get(s.ro, key)
	if err != nil {
		return 0, fmt.Errorf("failed to get entry %d: %v", index, err)
	}
	defer data.Free()

	if data.Size() == 0 {
		return 0, raft.ErrUnavailable
	}

	var ent raftpb.Entry
	if err := ent.Unmarshal(data.Data()); err != nil {
		return 0, fmt.Errorf("failed to unmarshal entry %d: %v", index, err)
	}

	return ent.Term, nil
}

// LastIndex implements the raft.Storage interface.
// It returns the index of the last entry in the log.
func (s *RocksDBStorage) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastIndex, nil
}

// FirstIndex implements the raft.Storage interface.
// It returns the index of the first log entry that is available.
func (s *RocksDBStorage) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstIndex, nil
}

// Snapshot implements the raft.Storage interface.
// It returns the most recent snapshot.
func (s *RocksDBStorage) Snapshot() (raftpb.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadSnapshotUnsafe()
}

// loadSnapshotUnsafe loads snapshot without acquiring lock (caller must hold lock)
func (s *RocksDBStorage) loadSnapshotUnsafe() (raftpb.Snapshot, error) {
	var snapshot raftpb.Snapshot

	snapData, err := s.db.Get(s.ro, s.prefixedKey(snapshotKey))
	if err != nil {
		return snapshot, err
	}
	defer snapData.Free()

	if snapData.Size() > 0 {
		if err := snapshot.Unmarshal(snapData.Data()); err != nil {
			return snapshot, fmt.Errorf("failed to unmarshal snapshot: %v", err)
		}
	} else {
		// No stored snapshot - create a valid empty snapshot
		// This prevents "need non-empty snapshot" panic in raft
		snapshot.Metadata.Index = s.firstIndex - 1
		snapshot.Metadata.Term = 0
		// Set Data to empty slice (not nil) to indicate a valid snapshot
		snapshot.Data = []byte{}
	}

	return snapshot, nil
}

// --- Additional Methods for Raft Log Management ---

// Append appends new entries to the log. It may delete conflicting entries.
func (s *RocksDBStorage) Append(entries []raftpb.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	first := entries[0].Index
	last := entries[len(entries)-1].Index

	// Check for conflicts with existing entries
	// If we have entries, we might need to truncate the log
	if first <= s.lastIndex {
		// Truncate any conflicting entries
		// Delete entries from [first, lastIndex]
		for i := first; i <= s.lastIndex; i++ {
			wb.Delete(s.logKey(i))
		}
	}

	// Store all new entries
	for _, ent := range entries {
		key := s.logKey(ent.Index)
		data, err := ent.Marshal()
		if err != nil {
			return fmt.Errorf("failed to marshal entry %d: %v", ent.Index, err)
		}
		wb.Put(key, data)
	}

	// Update lastIndex if needed
	if last > s.lastIndex {
		if err := s.setLastIndexWithWB(wb, last); err != nil {
			return err
		}
		s.lastIndex = last
	}

	// Update firstIndex if this is the first append
	if s.firstIndex > s.lastIndex && len(entries) > 0 {
		s.firstIndex = first
		if err := s.setFirstIndexWithWB(wb, first); err != nil {
			return err
		}
	}

	return s.db.Write(s.wo, wb)
}

// SetHardState saves the current HardState.
func (s *RocksDBStorage) SetHardState(st raftpb.HardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := st.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal hard state: %v", err)
	}
	return s.db.Put(s.wo, s.prefixedKey(hardStateKey), data)
}

// SetConfState saves the current ConfState.
func (s *RocksDBStorage) SetConfState(cs raftpb.ConfState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := cs.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal conf state: %v", err)
	}
	return s.db.Put(s.wo, s.prefixedKey(confStateKey), data)
}

// CreateSnapshot creates a snapshot with the given index, conf state, and data.
func (s *RocksDBStorage) CreateSnapshot(index uint64, cs *raftpb.ConfState, data []byte) (raftpb.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Allow creating snapshot at firstIndex-1 (for initial snapshot)
	if index < s.firstIndex-1 {
		return raftpb.Snapshot{}, raft.ErrSnapOutOfDate
	}

	if index > s.lastIndex {
		return raftpb.Snapshot{}, fmt.Errorf("snapshot index %d > last index %d", index, s.lastIndex)
	}

	// Get the term for this index
	var term uint64
	if index == s.firstIndex-1 {
		// Edge case: creating snapshot at firstIndex-1
		snap, err := s.loadSnapshotUnsafe()
		if err != nil {
			return raftpb.Snapshot{}, err
		}
		if !raft.IsEmptySnap(snap) {
			term = snap.Metadata.Term
		}
	} else {
		key := s.logKey(index)
		entData, err := s.db.Get(s.ro, key)
		if err != nil {
			return raftpb.Snapshot{}, fmt.Errorf("failed to get entry %d: %v", index, err)
		}
		defer entData.Free()

		if entData.Size() == 0 {
			return raftpb.Snapshot{}, fmt.Errorf("entry %d not found", index)
		}

		var ent raftpb.Entry
		if err := ent.Unmarshal(entData.Data()); err != nil {
			return raftpb.Snapshot{}, fmt.Errorf("failed to unmarshal entry %d: %v", index, err)
		}
		term = ent.Term
	}

	snapshot := raftpb.Snapshot{
		Data: data,
		Metadata: raftpb.SnapshotMetadata{
			Index:     index,
			Term:      term,
			ConfState: *cs,
		},
	}

	// Save the snapshot
	snapData, err := snapshot.Marshal()
	if err != nil {
		return raftpb.Snapshot{}, fmt.Errorf("failed to marshal snapshot: %v", err)
	}

	if err := s.db.Put(s.wo, s.prefixedKey(snapshotKey), snapData); err != nil {
		return raftpb.Snapshot{}, fmt.Errorf("failed to save snapshot: %v", err)
	}

	return snapshot, nil
}

// ApplySnapshot applies a snapshot to the storage.
// It updates the first index and deletes old log entries.
func (s *RocksDBStorage) ApplySnapshot(snap raftpb.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if raft.IsEmptySnap(snap) {
		return nil
	}

	index := snap.Metadata.Index

	// Check if snapshot is out of date
	if index <= s.firstIndex-1 {
		return raft.ErrSnapOutOfDate
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	// Save snapshot metadata
	snapData, err := snap.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %v", err)
	}
	wb.Put(s.prefixedKey(snapshotKey), snapData)

	// Delete old log entries [firstIndex, index]
	for i := s.firstIndex; i <= index && i <= s.lastIndex; i++ {
		wb.Delete(s.logKey(i))
	}

	// Update first index to index + 1
	newFirstIndex := index + 1
	if err := s.setFirstIndexWithWB(wb, newFirstIndex); err != nil {
		return err
	}

	// Update last index if snapshot is beyond current last index
	if index > s.lastIndex {
		if err := s.setLastIndexWithWB(wb, index); err != nil {
			return err
		}
		s.lastIndex = index
	}

	// Update ConfState
	csData, err := snap.Metadata.ConfState.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal conf state: %v", err)
	}
	wb.Put(s.prefixedKey(confStateKey), csData)

	// Write all changes atomically
	if err := s.db.Write(s.wo, wb); err != nil {
		return fmt.Errorf("failed to write snapshot: %v", err)
	}

	s.firstIndex = newFirstIndex

	log.Info("Applied Raft snapshot",
		zap.Uint64("snapshotIndex", index),
		zap.Uint64("newFirstIndex", s.firstIndex),
		zap.String("component", "raft-storage"))

	return nil
}

// Compact discards all log entries prior to compactIndex.
// It updates the first index marker.
func (s *RocksDBStorage) Compact(compactIndex uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if compactIndex <= s.firstIndex {
		// Already compacted
		return raft.ErrCompacted
	}

	if compactIndex > s.lastIndex {
		return fmt.Errorf("compact index %d > last index %d", compactIndex, s.lastIndex)
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	// Delete entries [firstIndex, compactIndex)
	for i := s.firstIndex; i < compactIndex; i++ {
		wb.Delete(s.logKey(i))
	}

	// Update first index
	if err := s.setFirstIndexWithWB(wb, compactIndex); err != nil {
		return err
	}

	if err := s.db.Write(s.wo, wb); err != nil {
		return fmt.Errorf("failed to compact: %v", err)
	}

	s.firstIndex = compactIndex

	log.Info("Compacted Raft log",
		zap.Uint64("compactIndex", compactIndex),
		zap.String("component", "raft-storage"))

	return nil
}

// --- Helper Functions ---

// getFirstIndexUnsafe retrieves the first index without acquiring the lock.
func (s *RocksDBStorage) getFirstIndexUnsafe() (uint64, error) {
	fiData, err := s.db.Get(s.ro, s.prefixedKey(firstIndexKey))
	if err != nil {
		return 0, err
	}
	defer fiData.Free()

	if fiData.Size() == 0 {
		return 0, fmt.Errorf("first index not found")
	}

	return binaryReadUint64BigEndian(fiData.Data())
}

// setFirstIndexUnsafe sets the first index without acquiring lock.
func (s *RocksDBStorage) setFirstIndexUnsafe(index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return s.db.Put(s.wo, s.prefixedKey(firstIndexKey), buf)
}

// setFirstIndexWithWB sets the first index using a provided WriteBatch.
func (s *RocksDBStorage) setFirstIndexWithWB(wb *grocksdb.WriteBatch, index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	wb.Put(s.prefixedKey(firstIndexKey), buf)
	return nil
}

// getLastIndexUnsafe retrieves the last index without acquiring the lock.
func (s *RocksDBStorage) getLastIndexUnsafe() (uint64, error) {
	liData, err := s.db.Get(s.ro, s.prefixedKey(lastIndexKey))
	if err != nil {
		return 0, err
	}
	defer liData.Free()

	if liData.Size() == 0 {
		return 0, fmt.Errorf("last index not found")
	}

	return binaryReadUint64BigEndian(liData.Data())
}

// setLastIndexUnsafe sets the last index without acquiring lock.
func (s *RocksDBStorage) setLastIndexUnsafe(index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return s.db.Put(s.wo, s.prefixedKey(lastIndexKey), buf)
}

// setLastIndexWithWB sets the last index using a provided WriteBatch.
func (s *RocksDBStorage) setLastIndexWithWB(wb *grocksdb.WriteBatch, index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	wb.Put(s.prefixedKey(lastIndexKey), buf)
	return nil
}

func binaryReadUint64BigEndian(b []byte) (uint64, error) {
	if len(b) < 8 {
		return 0, fmt.Errorf("buffer too small to read uint64")
	}
	return binary.BigEndian.Uint64(b), nil
}

// OpenRocksDB opens a RocksDB database with optimal settings for raft storage
func Open(path string) (*grocksdb.DB, error) {
	bbto := grocksdb.NewDefaultBlockBasedTableOptions()
	bbto.SetBlockCache(grocksdb.NewLRUCache(512 << 20)) // 512MB block cache
	bbto.SetFilterPolicy(grocksdb.NewBloomFilter(10))
	defer bbto.Destroy()

	opts := grocksdb.NewDefaultOptions()
	opts.SetBlockBasedTableFactory(bbto)
	opts.SetCreateIfMissing(true)
	opts.SetCreateIfMissingColumnFamilies(true)

	// Write settings for durability (WAL is enabled by default in RocksDB)
	opts.SetManualWALFlush(false)

	// Performance settings
	opts.SetMaxBackgroundJobs(4)
	opts.SetMaxOpenFiles(1000)
	opts.SetWriteBufferSize(64 << 20) // 64MB write buffer

	// Compression
	opts.SetCompression(grocksdb.SnappyCompression)

	db, err := grocksdb.OpenDb(opts, path)
	if err != nil {
		opts.Destroy()
		return nil, fmt.Errorf("failed to open RocksDB at %s: %v", path, err)
	}

	return db, nil
}
