// Package rocksdbstorage implements the raft.Storage interface using RocksDB
// for high-performance, large-scale storage. It stores log entries individually
// with keys like "entry_{index}" for efficient append and retrieval.
// This implementation mimics the API of raft.MemoryStorage where applicable,
// e.g., using Append instead of StoreEntries.
package main

import (
	"encoding/binary"
	"sync"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
)

const (
	hardStateKey = "hard_state"
	snapshotKey  = "snapshot"
	// Prefix for log entry keys: "entry_" followed by 8-byte big-endian index
	entryKeyPrefix = "entry_"
	entryKeyLength = len(entryKeyPrefix) + 8
)

// makeEntryKey creates a key for a log entry based on its index.
func makeEntryKey(index uint64) []byte {
	key := make([]byte, entryKeyLength)
	copy(key, entryKeyPrefix)
	binary.BigEndian.PutUint64(key[len(entryKeyPrefix):], index)
	return key
}

// parseEntryIndex extracts the index from an entry key.
// It assumes the key is valid (correct prefix and length).
func parseEntryIndex(key []byte) uint64 {
	return binary.BigEndian.Uint64(key[len(entryKeyPrefix):])
}

// RocksDBStorage implements the raft.Storage interface backed by RocksDB,
// optimized for performance and large-scale log storage.
// It aims to provide an API similar to raft.MemoryStorage for mutating operations.
type RocksDBStorage struct {
	db *grocksdb.DB
	wo *grocksdb.WriteOptions // Default options, WAL enabled
	ro *grocksdb.ReadOptions
	// sync.RWMutex protects cached metadata (hardState, snapshot)
	// that might be accessed concurrently.
	mu sync.RWMutex

	// Cached metadata to avoid frequent RocksDB reads for critical paths.
	cachedHardState raftpb.HardState
	cachedSnapshot  raftpb.Snapshot
}

// NewRocksDBStorage creates a new high-performance Storage implementation using RocksDB.
// It requires an already opened, writable grocksdb.DB instance.
// It's recommended to configure RocksDB for your performance/scalability needs
// (e.g., block cache, compaction styles, WAL settings) before passing the DB instance.
func NewRocksDBStorage(db *grocksdb.DB) *RocksDBStorage {
	wo := grocksdb.NewDefaultWriteOptions()
	ro := grocksdb.NewDefaultReadOptions()

	storage := &RocksDBStorage{
		db: db,
		wo: wo,
		ro: ro,
		// Initial cached state is zero-valued.
	}

	// Pre-populate cache from existing RocksDB state if available.
	_ = storage.loadMetadata()

	return storage
}

// loadMetadata loads HardState and Snapshot from RocksDB into the cache.
func (rs *RocksDBStorage) loadMetadata() error {
	rs.mu.Lock() // Lock for updating cache
	defer rs.mu.Unlock()

	// Load HardState
	hsData, err := rs.db.Get(rs.ro, []byte(hardStateKey))
	if err != nil {
		return err
	}
	if hsData.Size() > 0 {
		var hs raftpb.HardState
		if err := hs.Unmarshal(hsData.Data()); err != nil {
			hsData.Free()
			return err
		}
		rs.cachedHardState = hs
	}
	hsData.Free()

	// Load Snapshot
	snapData, err := rs.db.Get(rs.ro, []byte(snapshotKey))
	if err != nil {
		return err
	}
	if snapData.Size() > 0 {
		var snap raftpb.Snapshot
		if err := snap.Unmarshal(snapData.Data()); err != nil {
			snapData.Free()
			return err
		}
		rs.cachedSnapshot = snap
	}
	snapData.Free()

	return nil
}

// InitialState implements the raft.Storage interface.
func (rs *RocksDBStorage) InitialState() (raftpb.HardState, raftpb.ConfState, error) {
	// Read lock is sufficient for accessing cached metadata.
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.cachedHardState, rs.cachedSnapshot.Metadata.ConfState, nil
}

// FirstIndex returns the first index available in the log.
func (rs *RocksDBStorage) FirstIndex() (uint64, error) {
	rs.mu.RLock()
	snapIndex := rs.cachedSnapshot.Metadata.Index
	rs.mu.RUnlock()

	// Create iterator to find the first entry key
	iter := rs.db.NewIterator(rs.ro)
	defer iter.Close()

	iter.Seek([]byte(entryKeyPrefix))
	if iter.Valid() {
		key := iter.Key().Data()
		if len(key) == entryKeyLength && string(key[:len(entryKeyPrefix)]) == entryKeyPrefix {
			firstEntryIndex := parseEntryIndex(key)
			// The actual first index is the maximum of the snapshot index + 1
			// and the first entry's index.
			if firstEntryIndex > snapIndex+1 {
				return firstEntryIndex, nil
			}
		}
	}
	// If no entries or first entry is before/eq snapshot, first available is after snapshot
	return snapIndex + 1, nil
}

// LastIndex returns the last index available in the log.
func (rs *RocksDBStorage) LastIndex() (uint64, error) {
	rs.mu.RLock()
	snapIndex := rs.cachedSnapshot.Metadata.Index
	rs.mu.RUnlock()

	// Create iterator to find the last entry key
	iter := rs.db.NewIterator(rs.ro)
	defer iter.Close()

	// Seek to the start of entry keys and iterate to the end
	iter.Seek([]byte(entryKeyPrefix))
	if !iter.Valid() {
		// No entries found, last index is the snapshot index
		return snapIndex, nil
	}

	// Iterate to the last key
	var lastEntryIndex uint64
	for iter.Valid() {
		key := iter.Key().Data()
		// Check if the key is an entry key
		if len(key) == entryKeyLength && string(key[:len(entryKeyPrefix)]) == entryKeyPrefix {
			lastEntryIndex = parseEntryIndex(key)
		} else {
			// Moved past entry keys
			break
		}
		iter.Next()
	}

	return lastEntryIndex, nil
}

// Entries implements the raft.Storage interface.
// It retrieves log entries from RocksDB efficiently using an iterator.
func (rs *RocksDBStorage) Entries(lo, hi, maxSize uint64) ([]raftpb.Entry, error) {
	firstIndex, err := rs.FirstIndex()
	if err != nil {
		return nil, err
	}
	if lo < firstIndex {
		return nil, raft.ErrCompacted
	}

	lastIndex, err := rs.LastIndex()
	if err != nil {
		return nil, err
	}
	if hi > lastIndex+1 {
		return nil, raft.ErrUnavailable
	}

	// Use iterator to fetch entries
	iter := rs.db.NewIterator(rs.ro)
	defer iter.Close()

	var entries []raftpb.Entry
	sizeSoFar := uint64(0)

	// Start iteration from the 'lo' index
	iter.Seek(makeEntryKey(lo))
	for iter.Valid() && len(entries) < int(hi-lo) {
		key := iter.Key().Data()
		// Ensure we are still within the entry key space and within the requested range
		if len(key) != entryKeyLength || string(key[:len(entryKeyPrefix)]) != entryKeyPrefix {
			break // Moved past entry keys
		}
		currentIndex := parseEntryIndex(key)
		if currentIndex >= hi {
			break // Reached the end of the requested range
		}

		value := iter.Value()
		var entry raftpb.Entry
		if err := entry.Unmarshal(value.Data()); err != nil {
			return nil, err
		}

		entrySize := uint64(entry.Size())
		// Check maxSize limit
		if maxSize > 0 && sizeSoFar+entrySize > maxSize {
			// If we haven't added any entries yet and the first one is too big, it's an error
			if len(entries) == 0 {
				return nil, raft.ErrUnavailable
			}
			// Otherwise, we stop adding entries
			break
		}

		entries = append(entries, entry)
		sizeSoFar += entrySize
		iter.Next()
	}

	if err := iter.Err(); err != nil {
		return nil, err
	}

	// If we couldn't get any entries, it might be unavailable
	if len(entries) == 0 {
		// Check if the range was valid but just empty
		if lo < hi {
			return nil, raft.ErrUnavailable
		}
	}

	return entries, nil
}

// Term implements the raft.Storage interface.
// It retrieves the term for a specific log index from RocksDB.
func (rs *RocksDBStorage) Term(i uint64) (uint64, error) {
	firstIndex, err := rs.FirstIndex()
	if err != nil {
		return 0, err
	}
	if i < firstIndex {
		return 0, raft.ErrCompacted
	}

	lastIndex, err := rs.LastIndex()
	if err != nil {
		return 0, err
	}
	if i > lastIndex {
		return 0, raft.ErrUnavailable
	}

	// Direct key lookup for the specific entry
	key := makeEntryKey(i)
	value, err := rs.db.Get(rs.ro, key)
	if err != nil {
		return 0, err
	}
	defer value.Free()

	if value.Size() == 0 {
		// Key not found, even though index checks passed - unexpected
		return 0, raft.ErrUnavailable
	}

	var entry raftpb.Entry
	if err := entry.Unmarshal(value.Data()); err != nil {
		return 0, err
	}

	return entry.Term, nil
}

// Snapshot implements the raft.Storage interface.
func (rs *RocksDBStorage) Snapshot() (raftpb.Snapshot, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.cachedSnapshot, nil
}

// --- Methods for modifying the storage (aligning API with raft.MemoryStorage) ---

// Append appends log entries to the storage.
// It replaces existing entries in the range [first_index_of_entries, last_index_of_entries]
// and deletes any entries following them.
// This method aligns with raft.MemoryStorage.Append.
func (rs *RocksDBStorage) Append(entries []raftpb.Entry) error {
	if len(entries) == 0 {
		return nil
	}

	// Determine the range of entries to be added/updated
	firstToAppend := entries[0].Index
	lastToAppend := entries[len(entries)-1].Index

	// Get the current last index to know if we need to truncate
	currentLastIndex, err := rs.LastIndex()
	if err != nil {
		return err
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	// Add/Update the new entries
	for _, entry := range entries {
		key := makeEntryKey(entry.Index)
		data, err := entry.Marshal()
		if err != nil {
			return err // Consider partial rollback strategy if needed
		}
		wb.Put(key, data)
	}

	// Truncate any existing entries that come after the new entries
	// This mimics the behavior of MemoryStorage.Append
	if lastToAppend < currentLastIndex {
		iter := rs.db.NewIterator(rs.ro)
		defer iter.Close()

		// Start deleting from the index right after the last appended entry
		iter.Seek(makeEntryKey(lastToAppend + 1))
		for iter.Valid() {
			key := iter.Key().Data()
			// Check if the key is an entry key
			if len(key) == entryKeyLength && string(key[:len(entryKeyPrefix)]) == entryKeyPrefix {
				// Delete the key
				wb.Delete(key)
			} else {
				// Moved past entry keys
				break
			}
			iter.Next()
		}
		if err := iter.Err(); err != nil {
			return err
		}
	}

	// Atomically write all entries and deletions
	if err := rs.db.Write(rs.wo, wb); err != nil {
		return err
	}

	// Note: We don't update cached metadata here as Append doesn't directly
	// affect HardState or Snapshot. FirstIndex/LastIndex will query RocksDB.

	return nil
}

// SetHardState saves the current HardState to RocksDB and updates the cache.
// This method aligns with raft.MemoryStorage.SetHardState.
func (rs *RocksDBStorage) SetHardState(st raftpb.HardState) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Marshal and store the HardState
	data, err := st.Marshal()
	if err != nil {
		return err
	}

	if err := rs.db.Put(rs.wo, []byte(hardStateKey), data); err != nil {
		return err
	}

	// Update cache
	rs.cachedHardState = st
	return nil
}

// ApplySnapshot overwrites the snapshot in RocksDB and updates the cache.
// It also cleans up log entries that are now covered by the snapshot.
// This method aligns with raft.MemoryStorage.ApplySnapshot.
func (rs *RocksDBStorage) ApplySnapshot(snap raftpb.Snapshot) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Validate against cached snapshot
	if snap.Metadata.Index <= rs.cachedSnapshot.Metadata.Index {
		return raft.ErrSnapOutOfDate
	}

	// Marshal and store the new snapshot
	snapData, err := snap.Marshal()
	if err != nil {
		return err
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()
	wb.Put([]byte(snapshotKey), snapData)

	// Delete log entries covered by the new snapshot
	// Delete entries from old snapshot index + 1 up to new snapshot index
	firstIndexToDelete := rs.cachedSnapshot.Metadata.Index + 1
	lastIndexToDelete := snap.Metadata.Index

	if lastIndexToDelete >= firstIndexToDelete {
		iter := rs.db.NewIterator(rs.ro)
		defer iter.Close()

		iter.Seek(makeEntryKey(firstIndexToDelete))
		for iter.Valid() {
			key := iter.Key().Data()
			if len(key) != entryKeyLength || string(key[:len(entryKeyPrefix)]) != entryKeyPrefix {
				break // Moved past entry keys
			}
			index := parseEntryIndex(key)
			if index > lastIndexToDelete {
				break // Gone past the range to delete
			}
			wb.Delete(key)
			iter.Next()
		}
		if err := iter.Err(); err != nil {
			return err
		}
	}

	// Atomically apply snapshot and delete old entries
	if err := rs.db.Write(rs.wo, wb); err != nil {
		return err
	}

	// Update cache
	rs.cachedSnapshot = snap
	return nil
}

// Compact discards all log entries prior to compactIndex.
// It deletes the corresponding keys from RocksDB.
// This method aligns with raft.MemoryStorage.Compact.
func (rs *RocksDBStorage) Compact(compactIndex uint64) error {
	rs.mu.RLock()
	snapIndex := rs.cachedSnapshot.Metadata.Index
	rs.mu.RUnlock()

	if compactIndex <= snapIndex {
		return raft.ErrCompacted
	}

	// Determine the actual last index
	lastIndex, err := rs.LastIndex()
	if err != nil {
		return err
	}
	if compactIndex > lastIndex {
		// No-op if compacting beyond the log
		return nil
	}

	// Delete entries from firstIndex up to compactIndex - 1
	firstIndex, err := rs.FirstIndex()
	if err != nil {
		return err
	}
	endIndexToDelete := compactIndex - 1
	if endIndexToDelete < firstIndex {
		// Nothing to delete
		return nil
	}

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	// Use iterator to delete the range
	iter := rs.db.NewIterator(rs.ro)
	defer iter.Close()

	iter.Seek(makeEntryKey(firstIndex))
	for iter.Valid() {
		key := iter.Key().Data()
		if len(key) != entryKeyLength || string(key[:len(entryKeyPrefix)]) != entryKeyPrefix {
			break // Moved past entry keys
		}
		index := parseEntryIndex(key)
		if index >= compactIndex {
			break // Reached the index we want to keep
		}
		wb.Delete(key)
		iter.Next()
	}
	if err := iter.Err(); err != nil {
		return err
	}

	return rs.db.Write(rs.wo, wb)
}

// CreateSnapshot creates a snapshot in memory. The application is responsible
// for persisting it using ApplySnapshot if needed.
// This method aligns with raft.MemoryStorage.CreateSnapshot.
func (rs *RocksDBStorage) CreateSnapshot(i uint64, cs *raftpb.ConfState, data []byte) (raftpb.Snapshot, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if i <= rs.cachedSnapshot.Metadata.Index {
		return raftpb.Snapshot{}, raft.ErrSnapOutOfDate
	}

	// Get term for index i
	term, err := rs.Term(i)
	if err != nil {
		return raftpb.Snapshot{}, err
	}

	// Create snapshot in memory
	snap := raftpb.Snapshot{
		Data: data,
		Metadata: raftpb.SnapshotMetadata{
			Index: i,
			Term:  term,
		},
	}
	if cs != nil {
		snap.Metadata.ConfState = *cs
	}
	// Note: This only creates it in memory. ApplySnapshot persists it.
	return snap, nil
}

// Helper function to calculate the total size of entries (kept for potential internal use).
func entsSize(ents []raftpb.Entry) uint64 {
	var size uint64
	for _, ent := range ents {
		size += uint64(ent.Size())
	}
	return size
}
