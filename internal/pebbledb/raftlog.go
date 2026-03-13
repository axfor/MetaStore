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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"metaStore/pkg/config"
	"metaStore/pkg/log"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
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

// PebbleStorage implements raft.Storage interface backed by Pebble.
// It provides persistent storage for raft logs, hard state, and snapshots.
type PebbleStorage struct {
	db     *pebble.DB
	wo     *pebble.WriteOptions
	nodeID string // Unique identifier for this node's data within the DB
	mu     sync.RWMutex

	// Cache for performance
	firstIndex uint64
	lastIndex  uint64
}

// NewPebbleStorage creates a new Storage implementation using Pebble.
func NewPebbleStorage(db *pebble.DB, nodeID string) (*PebbleStorage, error) {
	storage := &PebbleStorage{
		db:     db,
		wo:     pebble.Sync, // Ensure durability for raft operations
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

// Close closes the storage and releases resources
func (s *PebbleStorage) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Pebble WriteOptions are simple values, no cleanup needed
}

// prefixedKey generates a key for a given key type and nodeID.
func (s *PebbleStorage) prefixedKey(key string) []byte {
	return []byte(fmt.Sprintf("%s_%s", s.nodeID, key))
}

// logKey generates a key for storing a raft log entry.
func (s *PebbleStorage) logKey(index uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return bytes.Join([][]byte{s.prefixedKey(raftLogPrefix), buf}, []byte("_"))
}

// pebbleGet is a helper that wraps pebble's Get and returns (data, error).
// Returns nil, ErrNotFound-style error if key doesn't exist.
func (s *PebbleStorage) pebbleGet(key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
	if err != nil {
		return nil, err
	}
	// Copy value before closing
	result := make([]byte, len(val))
	copy(result, val)
	closer.Close()
	return result, nil
}

// InitialState implements the raft.Storage interface.
func (s *PebbleStorage) InitialState() (raftpb.HardState, raftpb.ConfState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var hardState raftpb.HardState
	var confState raftpb.ConfState

	// Load HardState
	hsData, err := s.pebbleGet(s.prefixedKey(hardStateKey))
	if err != nil && err != pebble.ErrNotFound {
		return hardState, confState, err
	}
	if len(hsData) > 0 {
		if err := hardState.Unmarshal(hsData); err != nil {
			return hardState, confState, fmt.Errorf("failed to unmarshal hard state: %v", err)
		}
	}

	// Load ConfState
	csData, err := s.pebbleGet(s.prefixedKey(confStateKey))
	if err != nil && err != pebble.ErrNotFound {
		return hardState, confState, err
	}
	if len(csData) > 0 {
		if err := confState.Unmarshal(csData); err != nil {
			return hardState, confState, fmt.Errorf("failed to unmarshal conf state: %v", err)
		}
	}

	return hardState, confState, nil
}

// Entries implements the raft.Storage interface.
func (s *PebbleStorage) Entries(lo, hi, maxSize uint64) ([]raftpb.Entry, error) {
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
		data, err := s.pebbleGet(key)
		if err != nil {
			if err == pebble.ErrNotFound {
				return nil, raft.ErrUnavailable
			}
			return nil, fmt.Errorf("failed to get entry %d: %v", i, err)
		}

		if len(data) == 0 {
			return nil, raft.ErrUnavailable
		}

		var ent raftpb.Entry
		if err := ent.Unmarshal(data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry %d: %v", i, err)
		}

		entSize := uint64(ent.Size())
		if size > 0 && size+entSize > maxSize {
			break
		}

		ents = append(ents, ent)
		size += entSize
	}

	return ents, nil
}

// Term implements the raft.Storage interface.
func (s *PebbleStorage) Term(index uint64) (uint64, error) {
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

	if index == firstIndex-1 {
		snap, err := s.loadSnapshotUnsafe()
		if err != nil {
			return 0, err
		}
		if !raft.IsEmptySnap(snap) && snap.Metadata.Index == index {
			return snap.Metadata.Term, nil
		}
		if index == 0 {
			return 0, nil
		}
		return 0, raft.ErrCompacted
	}

	key := s.logKey(index)
	data, err := s.pebbleGet(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return 0, raft.ErrUnavailable
		}
		return 0, fmt.Errorf("failed to get entry %d: %v", index, err)
	}

	if len(data) == 0 {
		return 0, raft.ErrUnavailable
	}

	var ent raftpb.Entry
	if err := ent.Unmarshal(data); err != nil {
		return 0, fmt.Errorf("failed to unmarshal entry %d: %v", index, err)
	}

	return ent.Term, nil
}

// LastIndex implements the raft.Storage interface.
func (s *PebbleStorage) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastIndex, nil
}

// FirstIndex implements the raft.Storage interface.
func (s *PebbleStorage) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstIndex, nil
}

// Snapshot implements the raft.Storage interface.
func (s *PebbleStorage) Snapshot() (raftpb.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadSnapshotUnsafe()
}

// loadSnapshotUnsafe loads snapshot without acquiring lock (caller must hold lock)
func (s *PebbleStorage) loadSnapshotUnsafe() (raftpb.Snapshot, error) {
	var snapshot raftpb.Snapshot

	snapData, err := s.pebbleGet(s.prefixedKey(snapshotKey))
	if err != nil && err != pebble.ErrNotFound {
		return snapshot, err
	}

	if len(snapData) > 0 {
		if err := snapshot.Unmarshal(snapData); err != nil {
			return snapshot, fmt.Errorf("failed to unmarshal snapshot: %v", err)
		}
	} else {
		snapshot.Metadata.Index = s.firstIndex - 1
		snapshot.Metadata.Term = 0
		snapshot.Data = []byte{}
	}

	return snapshot, nil
}

// --- Additional Methods for Raft Log Management ---

// Append appends new entries to the log.
func (s *PebbleStorage) Append(entries []raftpb.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}

	wb := s.db.NewBatch()
	defer wb.Close()

	first := entries[0].Index
	last := entries[len(entries)-1].Index

	// Truncate any conflicting entries
	if first <= s.lastIndex {
		for i := first; i <= s.lastIndex; i++ {
			wb.Delete(s.logKey(i), s.wo)
		}
	}

	// Store all new entries
	for _, ent := range entries {
		key := s.logKey(ent.Index)
		data, err := ent.Marshal()
		if err != nil {
			return fmt.Errorf("failed to marshal entry %d: %v", ent.Index, err)
		}
		wb.Set(key, data, s.wo)
	}

	// Update lastIndex if needed
	if last > s.lastIndex {
		s.setLastIndexWithBatch(wb, last)
		s.lastIndex = last
	}

	// Update firstIndex if this is the first append
	if s.firstIndex > s.lastIndex && len(entries) > 0 {
		s.firstIndex = first
		s.setFirstIndexWithBatch(wb, first)
	}

	return wb.Commit(s.wo)
}

// SetHardState saves the current HardState.
func (s *PebbleStorage) SetHardState(st raftpb.HardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := st.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal hard state: %v", err)
	}
	return s.db.Set(s.prefixedKey(hardStateKey), data, s.wo)
}

// SetConfState saves the current ConfState.
func (s *PebbleStorage) SetConfState(cs raftpb.ConfState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := cs.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal conf state: %v", err)
	}
	return s.db.Set(s.prefixedKey(confStateKey), data, s.wo)
}

// CreateSnapshot creates a snapshot with the given index, conf state, and data.
func (s *PebbleStorage) CreateSnapshot(index uint64, cs *raftpb.ConfState, data []byte) (raftpb.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < s.firstIndex-1 {
		return raftpb.Snapshot{}, raft.ErrSnapOutOfDate
	}

	if index > s.lastIndex {
		return raftpb.Snapshot{}, fmt.Errorf("snapshot index %d > last index %d", index, s.lastIndex)
	}

	var term uint64
	if index == s.firstIndex-1 {
		snap, err := s.loadSnapshotUnsafe()
		if err != nil {
			return raftpb.Snapshot{}, err
		}
		if !raft.IsEmptySnap(snap) {
			term = snap.Metadata.Term
		}
	} else {
		key := s.logKey(index)
		entData, err := s.pebbleGet(key)
		if err != nil {
			return raftpb.Snapshot{}, fmt.Errorf("failed to get entry %d: %v", index, err)
		}

		if len(entData) == 0 {
			return raftpb.Snapshot{}, fmt.Errorf("entry %d not found", index)
		}

		var ent raftpb.Entry
		if err := ent.Unmarshal(entData); err != nil {
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

	snapData, err := snapshot.Marshal()
	if err != nil {
		return raftpb.Snapshot{}, fmt.Errorf("failed to marshal snapshot: %v", err)
	}

	if err := s.db.Set(s.prefixedKey(snapshotKey), snapData, s.wo); err != nil {
		return raftpb.Snapshot{}, fmt.Errorf("failed to save snapshot: %v", err)
	}

	return snapshot, nil
}

// ApplySnapshot applies a snapshot to the storage.
func (s *PebbleStorage) ApplySnapshot(snap raftpb.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if raft.IsEmptySnap(snap) {
		return nil
	}

	index := snap.Metadata.Index

	if index <= s.firstIndex-1 {
		return raft.ErrSnapOutOfDate
	}

	wb := s.db.NewBatch()
	defer wb.Close()

	// Save snapshot metadata
	snapData, err := snap.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %v", err)
	}
	wb.Set(s.prefixedKey(snapshotKey), snapData, s.wo)

	// Delete old log entries [firstIndex, index]
	for i := s.firstIndex; i <= index && i <= s.lastIndex; i++ {
		wb.Delete(s.logKey(i), s.wo)
	}

	// Update first index to index + 1
	newFirstIndex := index + 1
	s.setFirstIndexWithBatch(wb, newFirstIndex)

	// Update last index if snapshot is beyond current last index
	if index > s.lastIndex {
		s.setLastIndexWithBatch(wb, index)
		s.lastIndex = index
	}

	// Update ConfState
	csData, err := snap.Metadata.ConfState.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal conf state: %v", err)
	}
	wb.Set(s.prefixedKey(confStateKey), csData, s.wo)

	// Write all changes atomically
	if err := wb.Commit(s.wo); err != nil {
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
func (s *PebbleStorage) Compact(compactIndex uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if compactIndex <= s.firstIndex {
		return raft.ErrCompacted
	}

	if compactIndex > s.lastIndex {
		return fmt.Errorf("compact index %d > last index %d", compactIndex, s.lastIndex)
	}

	wb := s.db.NewBatch()
	defer wb.Close()

	// Delete entries [firstIndex, compactIndex)
	for i := s.firstIndex; i < compactIndex; i++ {
		wb.Delete(s.logKey(i), s.wo)
	}

	// Update first index
	s.setFirstIndexWithBatch(wb, compactIndex)

	if err := wb.Commit(s.wo); err != nil {
		return fmt.Errorf("failed to compact: %v", err)
	}

	s.firstIndex = compactIndex

	log.Info("Compacted Raft log",
		zap.Uint64("compactIndex", compactIndex),
		zap.String("component", "raft-storage"))

	return nil
}

// --- Helper Functions ---

func (s *PebbleStorage) getFirstIndexUnsafe() (uint64, error) {
	fiData, err := s.pebbleGet(s.prefixedKey(firstIndexKey))
	if err != nil {
		return 0, err
	}
	if len(fiData) == 0 {
		return 0, fmt.Errorf("first index not found")
	}
	return binaryReadUint64BigEndian(fiData)
}

func (s *PebbleStorage) setFirstIndexUnsafe(index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return s.db.Set(s.prefixedKey(firstIndexKey), buf, s.wo)
}

func (s *PebbleStorage) setFirstIndexWithBatch(wb *pebble.Batch, index uint64) {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	wb.Set(s.prefixedKey(firstIndexKey), buf, s.wo)
}

func (s *PebbleStorage) getLastIndexUnsafe() (uint64, error) {
	liData, err := s.pebbleGet(s.prefixedKey(lastIndexKey))
	if err != nil {
		return 0, err
	}
	if len(liData) == 0 {
		return 0, fmt.Errorf("last index not found")
	}
	return binaryReadUint64BigEndian(liData)
}

func (s *PebbleStorage) setLastIndexUnsafe(index uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	return s.db.Set(s.prefixedKey(lastIndexKey), buf, s.wo)
}

func (s *PebbleStorage) setLastIndexWithBatch(wb *pebble.Batch, index uint64) {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, index)
	wb.Set(s.prefixedKey(lastIndexKey), buf, s.wo)
}

func binaryReadUint64BigEndian(b []byte) (uint64, error) {
	if len(b) < 8 {
		return 0, fmt.Errorf("buffer too small to read uint64")
	}
	return binary.BigEndian.Uint64(b), nil
}

// prefixUpperBound returns the upper bound for prefix iteration.
// e.g., "abc" -> "abd"
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil // prefix was all 0xff
}

// Open opens a Pebble database with optimal settings for raft storage
func Open(path string, cfg ...*config.PebbleConfig) (*pebble.DB, error) {
	var pebbleCfg *config.PebbleConfig
	if len(cfg) > 0 && cfg[0] != nil {
		pebbleCfg = cfg[0]
	} else {
		defaultCfg := config.DefaultConfig(1, 1, ":2379")
		pebbleCfg = &defaultCfg.Server.Pebble
	}

	opts := &pebble.Options{}

	// Block cache
	if pebbleCfg.BlockCacheSize > 0 {
		opts.Cache = pebble.NewCache(int64(pebbleCfg.BlockCacheSize))
	}

	// Performance settings
	if pebbleCfg.WriteBufferSize > 0 {
		opts.MemTableSize = pebbleCfg.WriteBufferSize
	}
	if pebbleCfg.MaxWriteBufferNumber > 0 {
		opts.MemTableStopWritesThreshold = pebbleCfg.MaxWriteBufferNumber + 1
	}
	if pebbleCfg.MaxBackgroundJobs > 0 {
		jobs := pebbleCfg.MaxBackgroundJobs
		opts.MaxConcurrentCompactions = func() int { return jobs }
	}
	if pebbleCfg.MaxOpenFiles > 0 {
		opts.MaxOpenFiles = pebbleCfg.MaxOpenFiles
	}
	if pebbleCfg.BytesPerSync > 0 {
		opts.BytesPerSync = int(pebbleCfg.BytesPerSync)
	}

	// Configure levels with bloom filter and compression
	compression := parseCompression(pebbleCfg.Compression)
	opts.Levels = make([]pebble.LevelOptions, 7)
	for i := range opts.Levels {
		opts.Levels[i].BlockSize = 16 * 1024 // 16KB
		if pebbleCfg.BlockBasedTableBloomFilter {
			opts.Levels[i].FilterPolicy = bloom.FilterPolicy(pebbleCfg.BloomFilterBitsPerKey)
		}
		opts.Levels[i].Compression = compression
	}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %v", path, err)
	}

	return db, nil
}

// ensure io.Closer is used (prevent unused import)
var _ io.Closer = (*pebble.DB)(nil)
