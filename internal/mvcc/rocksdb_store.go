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

//go:build cgo
// +build cgo

package mvcc

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"

	"github.com/linxGnu/grocksdb"
)

const (
	// Key prefixes for MVCC storage
	// Key format: kvMVCCPrefix + user_key + "/" + revision_bytes (16 bytes)
	kvMVCCPrefix = "mvcc:kv:"

	// Meta keys
	metaCurrentRevision   = "mvcc:meta:current_revision"
	metaCompactedRevision = "mvcc:meta:compacted_revision"
)

// RocksDBStore is a RocksDB-backed MVCC store implementation.
// It uses key encoding to store multiple versions of each key.
// Key format: mvcc:kv:<user_key>/<16-byte revision>
type RocksDBStore struct {
	mu sync.RWMutex

	db *grocksdb.DB
	wo *grocksdb.WriteOptions
	ro *grocksdb.ReadOptions

	// keyIndex tracks revisions for each key (in-memory cache)
	keyIndex *KeyIndex

	// Current and compacted revisions (cached)
	currentRev   Revision
	compactedRev Revision

	closed bool
}

// NewRocksDBStore creates a new RocksDB-backed MVCC store.
// The caller is responsible for opening and closing the RocksDB instance.
func NewRocksDBStore(db *grocksdb.DB) (*RocksDBStore, error) {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(false) // Use fsync for durability
	wo.DisableWAL(false)

	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(true)

	s := &RocksDBStore{
		db:       db,
		wo:       wo,
		ro:       ro,
		keyIndex: NewKeyIndex(),
	}

	// Load current and compacted revisions
	if err := s.loadMetadata(); err != nil {
		wo.Destroy()
		ro.Destroy()
		return nil, err
	}

	// Rebuild key index from stored data
	if err := s.rebuildKeyIndex(); err != nil {
		wo.Destroy()
		ro.Destroy()
		return nil, err
	}

	return s, nil
}

// loadMetadata loads revision metadata from RocksDB.
func (s *RocksDBStore) loadMetadata() error {
	// Load current revision
	data, err := s.db.Get(s.ro, []byte(metaCurrentRevision))
	if err != nil {
		return err
	}
	defer data.Free()

	if data.Size() >= 16 {
		s.currentRev = ParseRevision(data.Data())
	}

	// Load compacted revision
	data2, err := s.db.Get(s.ro, []byte(metaCompactedRevision))
	if err != nil {
		return err
	}
	defer data2.Free()

	if data2.Size() >= 16 {
		s.compactedRev = ParseRevision(data2.Data())
	}

	return nil
}

// rebuildKeyIndex scans all MVCC keys and rebuilds the in-memory index.
func (s *RocksDBStore) rebuildKeyIndex() error {
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	prefix := []byte(kvMVCCPrefix)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		keyData := it.Key().Data()

		// Parse key: kvMVCCPrefix + user_key + "/" + revision_bytes
		userKey, rev, ok := s.parseStorageKey(keyData)
		if !ok {
			continue
		}

		// Skip compacted revisions
		if rev.LessThan(s.compactedRev) {
			continue
		}

		// Decode value to check if it's a tombstone
		kv, err := DefaultCodec.Decode(it.Value().Data())
		if err != nil {
			continue
		}

		if kv.Version == 0 {
			// Tombstone - mark key as deleted
			s.keyIndex.Delete(userKey, rev)
		} else {
			// Regular put
			s.keyIndex.Put(userKey, rev)
		}
	}

	return it.Err()
}

// makeStorageKey creates a RocksDB key from user key and revision.
// Format: kvMVCCPrefix + user_key + "/" + revision_bytes
func (s *RocksDBStore) makeStorageKey(key []byte, rev Revision) []byte {
	result := make([]byte, len(kvMVCCPrefix)+len(key)+1+RevisionSize)
	copy(result, kvMVCCPrefix)
	copy(result[len(kvMVCCPrefix):], key)
	result[len(kvMVCCPrefix)+len(key)] = '/'
	rev.EncodeTo(result[len(kvMVCCPrefix)+len(key)+1:])
	return result
}

// parseStorageKey extracts user key and revision from a storage key.
func (s *RocksDBStore) parseStorageKey(storageKey []byte) (userKey []byte, rev Revision, ok bool) {
	if !bytes.HasPrefix(storageKey, []byte(kvMVCCPrefix)) {
		return nil, Zero, false
	}

	remainder := storageKey[len(kvMVCCPrefix):]
	if len(remainder) < RevisionSize+1 {
		return nil, Zero, false
	}

	// Find the separator "/" before revision bytes
	sepIdx := len(remainder) - RevisionSize - 1
	if sepIdx < 0 || remainder[sepIdx] != '/' {
		return nil, Zero, false
	}

	userKey = remainder[:sepIdx]
	rev = ParseRevision(remainder[sepIdx+1:])
	return userKey, rev, true
}

// saveCurrentRevision persists the current revision to RocksDB.
func (s *RocksDBStore) saveCurrentRevision(batch *grocksdb.WriteBatch) {
	batch.Put([]byte(metaCurrentRevision), s.currentRev.Bytes())
}

// saveCompactedRevision persists the compacted revision to RocksDB.
func (s *RocksDBStore) saveCompactedRevision() error {
	return s.db.Put(s.wo, []byte(metaCompactedRevision), s.compactedRev.Bytes())
}

// Put stores a key-value pair and returns the new revision.
func (s *RocksDBStore) Put(key, value []byte, lease int64) (int64, error) {
	if len(key) == 0 {
		return 0, ErrEmptyKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, ErrClosed
	}

	// Increment revision
	s.currentRev.Main++
	s.currentRev.Sub = 0
	rev := s.currentRev

	// Get previous version info
	var createRev int64
	var version int64 = 1

	prevKeyRev := s.keyIndex.GetRevision(key, rev)
	if !prevKeyRev.IsZero() {
		// Read previous KeyValue
		prevData, err := s.db.Get(s.ro, s.makeStorageKey(key, prevKeyRev))
		if err == nil && prevData.Size() > 0 {
			if prevKv, err := DefaultCodec.Decode(prevData.Data()); err == nil {
				createRev = prevKv.CreateRevision
				version = prevKv.Version + 1
			}
		}
		if prevData != nil {
			prevData.Free()
		}
	} else {
		createRev = rev.Main
	}

	// Create KeyValue
	kv := &KeyValue{
		Key:            append([]byte{}, key...),
		Value:          append([]byte{}, value...),
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        version,
		Lease:          lease,
	}

	// Encode and store
	encoded := DefaultCodec.Encode(kv)

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	batch.Put(s.makeStorageKey(key, rev), encoded)
	s.saveCurrentRevision(batch)

	if err := s.db.Write(s.wo, batch); err != nil {
		return 0, err
	}

	// Update key index
	s.keyIndex.Put(key, rev)

	return rev.Main, nil
}

// Get retrieves the value for a key at a specific revision.
func (s *RocksDBStore) Get(key []byte, rev int64) (*KeyValue, error) {
	if len(key) == 0 {
		return nil, ErrEmptyKey
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrClosed
	}

	atRev := Revision{Main: rev}
	if rev == 0 {
		atRev = s.currentRev
	}

	// Check bounds
	if atRev.LessThan(s.compactedRev) {
		return nil, ErrCompacted
	}
	if atRev.GreaterThan(s.currentRev) {
		return nil, ErrFutureRevision
	}

	// Find revision in index
	keyRev := s.keyIndex.GetRevision(key, atRev)
	if keyRev.IsZero() {
		return nil, ErrKeyNotFound
	}

	// Read from RocksDB
	data, err := s.db.Get(s.ro, s.makeStorageKey(key, keyRev))
	if err != nil {
		return nil, err
	}
	defer data.Free()

	if data.Size() == 0 {
		return nil, ErrKeyNotFound
	}

	kv, err := DefaultCodec.Decode(data.Data())
	if err != nil {
		return nil, err
	}

	// Check if tombstone
	if kv.Version == 0 {
		return nil, ErrKeyNotFound
	}

	return kv, nil
}

// Range retrieves key-value pairs in the range [start, end).
func (s *RocksDBStore) Range(start, end []byte, rev int64, limit int64) ([]*KeyValue, int64, error) {
	if len(start) == 0 {
		return nil, 0, ErrEmptyKey
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, 0, ErrClosed
	}

	atRev := Revision{Main: rev}
	if rev == 0 {
		atRev = s.currentRev
	}

	// Check bounds
	if atRev.LessThan(s.compactedRev) {
		return nil, 0, ErrCompacted
	}
	if atRev.GreaterThan(s.currentRev) {
		return nil, 0, ErrFutureRevision
	}

	var result []*KeyValue
	var count int64

	s.keyIndex.Range(start, end, atRev, func(key []byte, keyRev Revision) bool {
		if limit > 0 && count >= limit {
			return false
		}

		// Read from RocksDB
		data, err := s.db.Get(s.ro, s.makeStorageKey(key, keyRev))
		if err != nil || data.Size() == 0 {
			if data != nil {
				data.Free()
			}
			return true
		}

		kv, err := DefaultCodec.Decode(data.Data())
		data.Free()

		if err != nil || kv.Version == 0 {
			return true
		}

		result = append(result, kv)
		count++
		return true
	})

	return result, count, nil
}

// Delete deletes a key and returns the revision and number of deleted keys.
func (s *RocksDBStore) Delete(key []byte) (int64, int64, error) {
	if len(key) == 0 {
		return 0, 0, ErrEmptyKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, 0, ErrClosed
	}

	// Check if key exists
	ki := s.keyIndex.Get(key)
	if ki == nil || ki.IsDeleted() {
		return s.currentRev.Main, 0, nil
	}

	// Increment revision
	s.currentRev.Main++
	s.currentRev.Sub = 0
	rev := s.currentRev

	// Get previous create revision
	prevKeyRev := ki.CurrentGeneration().LastRevision()
	var createRev int64
	if !prevKeyRev.IsZero() {
		data, err := s.db.Get(s.ro, s.makeStorageKey(key, prevKeyRev))
		if err == nil && data.Size() > 0 {
			if prevKv, err := DefaultCodec.Decode(data.Data()); err == nil {
				createRev = prevKv.CreateRevision
			}
		}
		if data != nil {
			data.Free()
		}
	}

	// Create tombstone
	tombstone := &KeyValue{
		Key:            append([]byte{}, key...),
		Value:          nil,
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        0,
		Lease:          0,
	}

	encoded := DefaultCodec.Encode(tombstone)

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	batch.Put(s.makeStorageKey(key, rev), encoded)
	s.saveCurrentRevision(batch)

	if err := s.db.Write(s.wo, batch); err != nil {
		return 0, 0, err
	}

	// Update key index
	s.keyIndex.Delete(key, rev)

	return rev.Main, 1, nil
}

// DeleteRange deletes all keys in the range [start, end).
func (s *RocksDBStore) DeleteRange(start, end []byte) (int64, int64, error) {
	if len(start) == 0 {
		return 0, 0, ErrEmptyKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, 0, ErrClosed
	}

	// Collect keys to delete
	var keysToDelete [][]byte
	s.keyIndex.Range(start, end, Zero, func(key []byte, keyRev Revision) bool {
		keysToDelete = append(keysToDelete, append([]byte{}, key...))
		return true
	})

	if len(keysToDelete) == 0 {
		return s.currentRev.Main, 0, nil
	}

	// Increment revision
	s.currentRev.Main++
	s.currentRev.Sub = 0
	baseRev := s.currentRev

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	var deleted int64

	for i, key := range keysToDelete {
		ki := s.keyIndex.Get(key)
		if ki == nil || ki.IsDeleted() {
			continue
		}

		rev := Revision{Main: baseRev.Main, Sub: int64(i)}

		// Get previous create revision
		prevKeyRev := ki.CurrentGeneration().LastRevision()
		var createRev int64
		if !prevKeyRev.IsZero() {
			data, err := s.db.Get(s.ro, s.makeStorageKey(key, prevKeyRev))
			if err == nil && data.Size() > 0 {
				if prevKv, err := DefaultCodec.Decode(data.Data()); err == nil {
					createRev = prevKv.CreateRevision
				}
			}
			if data != nil {
				data.Free()
			}
		}

		// Create tombstone
		tombstone := &KeyValue{
			Key:            key,
			Value:          nil,
			CreateRevision: createRev,
			ModRevision:    baseRev.Main,
			Version:        0,
			Lease:          0,
		}

		encoded := DefaultCodec.Encode(tombstone)
		batch.Put(s.makeStorageKey(key, rev), encoded)

		// Update key index
		s.keyIndex.Delete(key, rev)

		deleted++
	}

	// Update current revision to last used
	if deleted > 0 {
		s.currentRev.Sub = int64(len(keysToDelete) - 1)
	}
	s.saveCurrentRevision(batch)

	if err := s.db.Write(s.wo, batch); err != nil {
		return 0, 0, err
	}

	return baseRev.Main, deleted, nil
}

// Txn executes a transaction.
func (s *RocksDBStore) Txn(ctx context.Context) Txn {
	return &rocksDBTxn{
		store: s,
		ctx:   ctx,
	}
}

// CurrentRevision returns the current revision.
func (s *RocksDBStore) CurrentRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentRev.Main
}

// CompactedRevision returns the compacted revision.
func (s *RocksDBStore) CompactedRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.compactedRev.Main
}

// Compact compacts all revisions before the given revision.
func (s *RocksDBStore) Compact(rev int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	targetRev := Revision{Main: rev}

	if targetRev.LessThanOrEqual(s.compactedRev) {
		return ErrCompacted
	}
	if targetRev.GreaterThan(s.currentRev) {
		return ErrFutureRevision
	}

	// Delete old revisions from RocksDB
	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	it := s.db.NewIterator(s.ro)
	defer it.Close()

	prefix := []byte(kvMVCCPrefix)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		_, keyRev, ok := s.parseStorageKey(it.Key().Data())
		if !ok {
			continue
		}

		if keyRev.LessThan(targetRev) {
			batch.Delete(it.Key().Data())
		}
	}

	// Update compacted revision
	s.compactedRev = targetRev
	batch.Put([]byte(metaCompactedRevision), s.compactedRev.Bytes())

	if err := s.db.Write(s.wo, batch); err != nil {
		return err
	}

	// Compact key index
	s.keyIndex.Compact(targetRev)

	// Trigger RocksDB physical compaction
	startKey := []byte(kvMVCCPrefix)
	endKey := append([]byte(kvMVCCPrefix), 0xFF)
	s.db.CompactRange(grocksdb.Range{Start: startKey, Limit: endKey})

	return nil
}

// Close closes the store.
func (s *RocksDBStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	s.closed = true

	if s.wo != nil {
		s.wo.Destroy()
	}
	if s.ro != nil {
		s.ro.Destroy()
	}

	return nil
}

// Sync forces a sync to disk.
func (s *RocksDBStore) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	// Write current revision with sync
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(true)
	defer wo.Destroy()

	return s.db.Put(wo, []byte(metaCurrentRevision), s.currentRev.Bytes())
}

// rocksDBTxn implements Txn for RocksDBStore.
type rocksDBTxn struct {
	store *RocksDBStore
	ctx   context.Context

	conditions []Condition
	thenOps    []Op
	elseOps    []Op
}

func (t *rocksDBTxn) If(conds ...Condition) Txn {
	t.conditions = append(t.conditions, conds...)
	return t
}

func (t *rocksDBTxn) Then(ops ...Op) Txn {
	t.thenOps = append(t.thenOps, ops...)
	return t
}

func (t *rocksDBTxn) Else(ops ...Op) Txn {
	t.elseOps = append(t.elseOps, ops...)
	return t
}

func (t *rocksDBTxn) Commit() (*TxnResponse, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	if t.store.closed {
		return nil, ErrClosed
	}

	// Evaluate conditions
	succeeded := true
	for _, cond := range t.conditions {
		if !t.evaluateCondition(cond) {
			succeeded = false
			break
		}
	}

	// Choose operations
	var ops []Op
	if succeeded {
		ops = t.thenOps
	} else {
		ops = t.elseOps
	}

	// Increment revision for transaction
	t.store.currentRev.Main++
	t.store.currentRev.Sub = 0
	txnRev := t.store.currentRev

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	responses := make([]OpResponse, len(ops))

	for i, op := range ops {
		opRev := Revision{Main: txnRev.Main, Sub: int64(i)}
		responses[i] = t.executeOp(op, opRev, batch)
	}

	// Update current revision
	if len(ops) > 0 {
		t.store.currentRev.Sub = int64(len(ops) - 1)
	}
	t.store.saveCurrentRevision(batch)

	if err := t.store.db.Write(t.store.wo, batch); err != nil {
		return nil, err
	}

	return &TxnResponse{
		Succeeded: succeeded,
		Revision:  txnRev.Main,
		Responses: responses,
	}, nil
}

func (t *rocksDBTxn) evaluateCondition(cond Condition) bool {
	ki := t.store.keyIndex.Get(cond.Key)

	var kv *KeyValue
	if ki != nil && !ki.IsDeleted() {
		lastRev := ki.CurrentGeneration().LastRevision()
		if !lastRev.IsZero() {
			data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(cond.Key, lastRev))
			if err == nil && data.Size() > 0 {
				kv, _ = DefaultCodec.Decode(data.Data())
			}
			if data != nil {
				data.Free()
			}
		}
	}

	var actual interface{}
	switch cond.Target {
	case ConditionTargetVersion:
		if kv != nil {
			actual = kv.Version
		} else {
			actual = int64(0)
		}
	case ConditionTargetCreateRevision:
		if kv != nil {
			actual = kv.CreateRevision
		} else {
			actual = int64(0)
		}
	case ConditionTargetModRevision:
		if kv != nil {
			actual = kv.ModRevision
		} else {
			actual = int64(0)
		}
	case ConditionTargetValue:
		if kv != nil {
			actual = kv.Value
		} else {
			actual = []byte(nil)
		}
	}

	return t.compare(actual, cond.Compare, cond.Value)
}

func (t *rocksDBTxn) compare(actual interface{}, cmp CompareType, expected interface{}) bool {
	switch a := actual.(type) {
	case int64:
		e := expected.(int64)
		switch cmp {
		case CompareEqual:
			return a == e
		case CompareNotEqual:
			return a != e
		case CompareLess:
			return a < e
		case CompareGreater:
			return a > e
		}
	case []byte:
		e := expected.([]byte)
		result := bytes.Compare(a, e)
		switch cmp {
		case CompareEqual:
			return result == 0
		case CompareNotEqual:
			return result != 0
		case CompareLess:
			return result < 0
		case CompareGreater:
			return result > 0
		}
	}
	return false
}

func (t *rocksDBTxn) executeOp(op Op, rev Revision, batch *grocksdb.WriteBatch) OpResponse {
	switch op.Type {
	case OpTypePut:
		return t.executePut(op, rev, batch)
	case OpTypeGet:
		return t.executeGet(op)
	case OpTypeDelete:
		return t.executeDelete(op, rev, batch)
	case OpTypeDeleteRange:
		return t.executeDeleteRange(op, rev, batch)
	}
	return OpResponse{Type: op.Type}
}

func (t *rocksDBTxn) executePut(op Op, rev Revision, batch *grocksdb.WriteBatch) OpResponse {
	key := op.Key

	var createRev int64
	var version int64 = 1

	ki := t.store.keyIndex.Get(key)
	if ki != nil && !ki.IsDeleted() {
		prevKeyRev := ki.CurrentGeneration().LastRevision()
		if !prevKeyRev.IsZero() {
			data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(key, prevKeyRev))
			if err == nil && data.Size() > 0 {
				if prevKv, err := DefaultCodec.Decode(data.Data()); err == nil {
					createRev = prevKv.CreateRevision
					version = prevKv.Version + 1
				}
			}
			if data != nil {
				data.Free()
			}
		}
	} else {
		createRev = rev.Main
	}

	kv := &KeyValue{
		Key:            append([]byte{}, key...),
		Value:          append([]byte{}, op.Value...),
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        version,
		Lease:          op.Lease,
	}

	encoded := DefaultCodec.Encode(kv)
	batch.Put(t.store.makeStorageKey(key, rev), encoded)
	t.store.keyIndex.Put(key, rev)

	return OpResponse{Type: OpTypePut}
}

func (t *rocksDBTxn) executeGet(op Op) OpResponse {
	resp := OpResponse{Type: OpTypeGet}

	if op.End == nil {
		// Single key get
		ki := t.store.keyIndex.Get(op.Key)
		if ki != nil && !ki.IsDeleted() {
			lastRev := ki.CurrentGeneration().LastRevision()
			if !lastRev.IsZero() {
				data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(op.Key, lastRev))
				if err == nil && data.Size() > 0 {
					if kv, err := DefaultCodec.Decode(data.Data()); err == nil && kv.Version > 0 {
						resp.Kvs = []*KeyValue{kv}
					}
				}
				if data != nil {
					data.Free()
				}
			}
		}
	} else {
		// Range get
		t.store.keyIndex.Range(op.Key, op.End, Zero, func(key []byte, keyRev Revision) bool {
			data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(key, keyRev))
			if err == nil && data.Size() > 0 {
				if kv, err := DefaultCodec.Decode(data.Data()); err == nil && kv.Version > 0 {
					resp.Kvs = append(resp.Kvs, kv)
				}
			}
			if data != nil {
				data.Free()
			}
			return true
		})
	}

	return resp
}

func (t *rocksDBTxn) executeDelete(op Op, rev Revision, batch *grocksdb.WriteBatch) OpResponse {
	resp := OpResponse{Type: OpTypeDelete}

	ki := t.store.keyIndex.Get(op.Key)
	if ki == nil || ki.IsDeleted() {
		return resp
	}

	prevKeyRev := ki.CurrentGeneration().LastRevision()
	var createRev int64
	if !prevKeyRev.IsZero() {
		data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(op.Key, prevKeyRev))
		if err == nil && data.Size() > 0 {
			if prevKv, err := DefaultCodec.Decode(data.Data()); err == nil {
				createRev = prevKv.CreateRevision
			}
		}
		if data != nil {
			data.Free()
		}
	}

	tombstone := &KeyValue{
		Key:            append([]byte{}, op.Key...),
		Value:          nil,
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        0,
		Lease:          0,
	}

	encoded := DefaultCodec.Encode(tombstone)
	batch.Put(t.store.makeStorageKey(op.Key, rev), encoded)
	t.store.keyIndex.Delete(op.Key, rev)

	resp.Deleted = 1
	return resp
}

func (t *rocksDBTxn) executeDeleteRange(op Op, baseRev Revision, batch *grocksdb.WriteBatch) OpResponse {
	resp := OpResponse{Type: OpTypeDeleteRange}

	var keysToDelete [][]byte
	t.store.keyIndex.Range(op.Key, op.End, Zero, func(key []byte, keyRev Revision) bool {
		keysToDelete = append(keysToDelete, append([]byte{}, key...))
		return true
	})

	for i, key := range keysToDelete {
		ki := t.store.keyIndex.Get(key)
		if ki == nil || ki.IsDeleted() {
			continue
		}

		deleteRev := Revision{Main: baseRev.Main, Sub: baseRev.Sub + int64(i)}

		prevKeyRev := ki.CurrentGeneration().LastRevision()
		var createRev int64
		if !prevKeyRev.IsZero() {
			data, err := t.store.db.Get(t.store.ro, t.store.makeStorageKey(key, prevKeyRev))
			if err == nil && data.Size() > 0 {
				if prevKv, err := DefaultCodec.Decode(data.Data()); err == nil {
					createRev = prevKv.CreateRevision
				}
			}
			if data != nil {
				data.Free()
			}
		}

		tombstone := &KeyValue{
			Key:            key,
			Value:          nil,
			CreateRevision: createRev,
			ModRevision:    baseRev.Main,
			Version:        0,
			Lease:          0,
		}

		encoded := DefaultCodec.Encode(tombstone)
		batch.Put(t.store.makeStorageKey(key, deleteRev), encoded)
		t.store.keyIndex.Delete(key, deleteRev)

		resp.Deleted++
	}

	return resp
}

// DBSize returns the approximate size of the database in bytes.
func (s *RocksDBStore) DBSize() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0
	}

	// Get approximate size of all data
	start := []byte{0x00}
	end := []byte{0xFF}

	sizes, _ := s.db.GetApproximateSizes([]grocksdb.Range{{Start: start, Limit: end}})
	if len(sizes) > 0 {
		return int64(sizes[0])
	}
	return 0
}

// Hash returns a hash of all keys in the database.
func (s *RocksDBStore) Hash() (uint32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0, ErrClosed
	}

	var hash uint32
	it := s.db.NewIterator(s.ro)
	defer it.Close()

	prefix := []byte(kvMVCCPrefix)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		// Simple FNV-like hash
		for _, b := range it.Key().Data() {
			hash = hash*31 + uint32(b)
		}
		for _, b := range it.Value().Data() {
			hash = hash*31 + uint32(b)
		}
	}

	return hash, it.Err()
}

// encodeUint64 encodes a uint64 in big-endian format.
func encodeUint64(v uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, v)
	return buf
}

// decodeUint64 decodes a big-endian uint64.
func decodeUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
