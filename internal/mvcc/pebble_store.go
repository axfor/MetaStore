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

package mvcc

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"

	"github.com/cockroachdb/pebble"
)

const (
	// Key prefixes for MVCC storage
	// Key format: kvMVCCPrefix + user_key + "/" + revision_bytes (16 bytes)
	kvMVCCPrefix = "mvcc:kv:"

	// Meta keys
	metaCurrentRevision   = "mvcc:meta:current_revision"
	metaCompactedRevision = "mvcc:meta:compacted_revision"
)

// PebbleStore is a Pebble-backed MVCC store implementation.
// It uses key encoding to store multiple versions of each key.
// Key format: mvcc:kv:<user_key>/<16-byte revision>
type PebbleStore struct {
	mu sync.RWMutex

	db *pebble.DB
	wo *pebble.WriteOptions

	// keyIndex tracks revisions for each key (in-memory cache)
	keyIndex *KeyIndex

	// Current and compacted revisions (cached)
	currentRev   Revision
	compactedRev Revision

	closed bool
}

// NewPebbleStore creates a new Pebble-backed MVCC store.
// The caller is responsible for opening and closing the Pebble instance.
func NewPebbleStore(db *pebble.DB) (*PebbleStore, error) {
	wo := pebble.NoSync

	s := &PebbleStore{
		db:       db,
		wo:       wo,
		keyIndex: NewKeyIndex(),
	}

	// Load current and compacted revisions
	if err := s.loadMetadata(); err != nil {
		return nil, err
	}

	// Rebuild key index from stored data
	if err := s.rebuildKeyIndex(); err != nil {
		return nil, err
	}

	return s, nil
}

// pebbleGet retrieves a value from pebble. Returns nil, nil if not found.
func (s *PebbleStore) pebbleGet(key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	data := make([]byte, len(val))
	copy(data, val)
	closer.Close()
	return data, nil
}

// loadMetadata loads revision metadata from Pebble.
func (s *PebbleStore) loadMetadata() error {
	// Load current revision
	data, err := s.pebbleGet([]byte(metaCurrentRevision))
	if err != nil {
		return err
	}

	if len(data) >= 16 {
		s.currentRev = ParseRevision(data)
	}

	// Load compacted revision
	data2, err := s.pebbleGet([]byte(metaCompactedRevision))
	if err != nil {
		return err
	}

	if len(data2) >= 16 {
		s.compactedRev = ParseRevision(data2)
	}

	return nil
}

// prefixUpperBound returns the upper bound for prefix iteration.
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil
}

// rebuildKeyIndex scans all MVCC keys and rebuilds the in-memory index.
func (s *PebbleStore) rebuildKeyIndex() error {
	prefix := []byte(kvMVCCPrefix)
	it, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return err
	}
	defer it.Close()

	for it.SeekGE(prefix); it.Valid(); it.Next() {
		keyData := make([]byte, len(it.Key()))
		copy(keyData, it.Key())

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
		valData := make([]byte, len(it.Value()))
		copy(valData, it.Value())

		kv, err := DefaultCodec.Decode(valData)
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

	return it.Error()
}

// makeStorageKey creates a storage key from user key and revision.
// Format: kvMVCCPrefix + user_key + "/" + revision_bytes
func (s *PebbleStore) makeStorageKey(key []byte, rev Revision) []byte {
	result := make([]byte, len(kvMVCCPrefix)+len(key)+1+RevisionSize)
	copy(result, kvMVCCPrefix)
	copy(result[len(kvMVCCPrefix):], key)
	result[len(kvMVCCPrefix)+len(key)] = '/'
	rev.EncodeTo(result[len(kvMVCCPrefix)+len(key)+1:])
	return result
}

// parseStorageKey extracts user key and revision from a storage key.
func (s *PebbleStore) parseStorageKey(storageKey []byte) (userKey []byte, rev Revision, ok bool) {
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

// saveCurrentRevision persists the current revision to the batch.
func (s *PebbleStore) saveCurrentRevision(batch *pebble.Batch) {
	batch.Set([]byte(metaCurrentRevision), s.currentRev.Bytes(), nil)
}

// saveCompactedRevision persists the compacted revision to Pebble.
func (s *PebbleStore) saveCompactedRevision() error {
	return s.db.Set([]byte(metaCompactedRevision), s.compactedRev.Bytes(), s.wo)
}

// Put stores a key-value pair and returns the new revision.
func (s *PebbleStore) Put(key, value []byte, lease int64) (int64, error) {
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
		prevData, err := s.pebbleGet(s.makeStorageKey(key, prevKeyRev))
		if err == nil && len(prevData) > 0 {
			if prevKv, err := DefaultCodec.Decode(prevData); err == nil {
				createRev = prevKv.CreateRevision
				version = prevKv.Version + 1
			}
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

	batch := s.db.NewBatch()
	defer batch.Close()

	batch.Set(s.makeStorageKey(key, rev), encoded, nil)
	s.saveCurrentRevision(batch)

	if err := batch.Commit(s.wo); err != nil {
		return 0, err
	}

	// Update key index
	s.keyIndex.Put(key, rev)

	return rev.Main, nil
}

// Get retrieves the value for a key at a specific revision.
func (s *PebbleStore) Get(key []byte, rev int64) (*KeyValue, error) {
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

	// Read from Pebble
	data, err := s.pebbleGet(s.makeStorageKey(key, keyRev))
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, ErrKeyNotFound
	}

	kv, err := DefaultCodec.Decode(data)
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
func (s *PebbleStore) Range(start, end []byte, rev int64, limit int64) ([]*KeyValue, int64, error) {
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

		// Read from Pebble
		data, err := s.pebbleGet(s.makeStorageKey(key, keyRev))
		if err != nil || len(data) == 0 {
			return true
		}

		kv, err := DefaultCodec.Decode(data)
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
func (s *PebbleStore) Delete(key []byte) (int64, int64, error) {
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
		data, err := s.pebbleGet(s.makeStorageKey(key, prevKeyRev))
		if err == nil && len(data) > 0 {
			if prevKv, err := DefaultCodec.Decode(data); err == nil {
				createRev = prevKv.CreateRevision
			}
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

	batch := s.db.NewBatch()
	defer batch.Close()

	batch.Set(s.makeStorageKey(key, rev), encoded, nil)
	s.saveCurrentRevision(batch)

	if err := batch.Commit(s.wo); err != nil {
		return 0, 0, err
	}

	// Update key index
	s.keyIndex.Delete(key, rev)

	return rev.Main, 1, nil
}

// DeleteRange deletes all keys in the range [start, end).
func (s *PebbleStore) DeleteRange(start, end []byte) (int64, int64, error) {
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

	batch := s.db.NewBatch()
	defer batch.Close()

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
			data, err := s.pebbleGet(s.makeStorageKey(key, prevKeyRev))
			if err == nil && len(data) > 0 {
				if prevKv, err := DefaultCodec.Decode(data); err == nil {
					createRev = prevKv.CreateRevision
				}
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
		batch.Set(s.makeStorageKey(key, rev), encoded, nil)

		// Update key index
		s.keyIndex.Delete(key, rev)

		deleted++
	}

	// Update current revision to last used
	if deleted > 0 {
		s.currentRev.Sub = int64(len(keysToDelete) - 1)
	}
	s.saveCurrentRevision(batch)

	if err := batch.Commit(s.wo); err != nil {
		return 0, 0, err
	}

	return baseRev.Main, deleted, nil
}

// Txn executes a transaction.
func (s *PebbleStore) Txn(ctx context.Context) Txn {
	return &pebbleDBTxn{
		store: s,
		ctx:   ctx,
	}
}

// CurrentRevision returns the current revision.
func (s *PebbleStore) CurrentRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentRev.Main
}

// CompactedRevision returns the compacted revision.
func (s *PebbleStore) CompactedRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.compactedRev.Main
}

// Compact compacts all revisions before the given revision.
func (s *PebbleStore) Compact(rev int64) error {
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

	// Delete old revisions from Pebble
	batch := s.db.NewBatch()
	defer batch.Close()

	prefix := []byte(kvMVCCPrefix)
	it, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return err
	}
	defer it.Close()

	for it.SeekGE(prefix); it.Valid(); it.Next() {
		keyData := make([]byte, len(it.Key()))
		copy(keyData, it.Key())

		_, keyRev, ok := s.parseStorageKey(keyData)
		if !ok {
			continue
		}

		if keyRev.LessThan(targetRev) {
			batch.Delete(keyData, nil)
		}
	}

	// Update compacted revision
	s.compactedRev = targetRev
	batch.Set([]byte(metaCompactedRevision), s.compactedRev.Bytes(), nil)

	if err := batch.Commit(s.wo); err != nil {
		return err
	}

	// Compact key index
	s.keyIndex.Compact(targetRev)

	// Trigger Pebble physical compaction
	startKey := []byte(kvMVCCPrefix)
	endKey := append([]byte(kvMVCCPrefix), 0xFF)
	s.db.Compact(startKey, endKey, true)

	return nil
}

// Close closes the store.
func (s *PebbleStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	s.closed = true

	return nil
}

// Sync forces a sync to disk.
func (s *PebbleStore) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	// Write current revision with sync
	return s.db.Set([]byte(metaCurrentRevision), s.currentRev.Bytes(), pebble.Sync)
}

// pebbleDBTxn implements Txn for PebbleStore.
type pebbleDBTxn struct {
	store *PebbleStore
	ctx   context.Context

	conditions []Condition
	thenOps    []Op
	elseOps    []Op
}

func (t *pebbleDBTxn) If(conds ...Condition) Txn {
	t.conditions = append(t.conditions, conds...)
	return t
}

func (t *pebbleDBTxn) Then(ops ...Op) Txn {
	t.thenOps = append(t.thenOps, ops...)
	return t
}

func (t *pebbleDBTxn) Else(ops ...Op) Txn {
	t.elseOps = append(t.elseOps, ops...)
	return t
}

func (t *pebbleDBTxn) Commit() (*TxnResponse, error) {
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

	batch := t.store.db.NewBatch()
	defer batch.Close()

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

	if err := batch.Commit(t.store.wo); err != nil {
		return nil, err
	}

	return &TxnResponse{
		Succeeded: succeeded,
		Revision:  txnRev.Main,
		Responses: responses,
	}, nil
}

func (t *pebbleDBTxn) evaluateCondition(cond Condition) bool {
	ki := t.store.keyIndex.Get(cond.Key)

	var kv *KeyValue
	if ki != nil && !ki.IsDeleted() {
		lastRev := ki.CurrentGeneration().LastRevision()
		if !lastRev.IsZero() {
			data, err := t.store.pebbleGet(t.store.makeStorageKey(cond.Key, lastRev))
			if err == nil && len(data) > 0 {
				kv, _ = DefaultCodec.Decode(data)
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

func (t *pebbleDBTxn) compare(actual interface{}, cmp CompareType, expected interface{}) bool {
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

func (t *pebbleDBTxn) executeOp(op Op, rev Revision, batch *pebble.Batch) OpResponse {
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

func (t *pebbleDBTxn) executePut(op Op, rev Revision, batch *pebble.Batch) OpResponse {
	key := op.Key

	var createRev int64
	var version int64 = 1

	ki := t.store.keyIndex.Get(key)
	if ki != nil && !ki.IsDeleted() {
		prevKeyRev := ki.CurrentGeneration().LastRevision()
		if !prevKeyRev.IsZero() {
			data, err := t.store.pebbleGet(t.store.makeStorageKey(key, prevKeyRev))
			if err == nil && len(data) > 0 {
				if prevKv, err := DefaultCodec.Decode(data); err == nil {
					createRev = prevKv.CreateRevision
					version = prevKv.Version + 1
				}
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
	batch.Set(t.store.makeStorageKey(key, rev), encoded, nil)
	t.store.keyIndex.Put(key, rev)

	return OpResponse{Type: OpTypePut}
}

func (t *pebbleDBTxn) executeGet(op Op) OpResponse {
	resp := OpResponse{Type: OpTypeGet}

	if op.End == nil {
		// Single key get
		ki := t.store.keyIndex.Get(op.Key)
		if ki != nil && !ki.IsDeleted() {
			lastRev := ki.CurrentGeneration().LastRevision()
			if !lastRev.IsZero() {
				data, err := t.store.pebbleGet(t.store.makeStorageKey(op.Key, lastRev))
				if err == nil && len(data) > 0 {
					if kv, err := DefaultCodec.Decode(data); err == nil && kv.Version > 0 {
						resp.Kvs = []*KeyValue{kv}
					}
				}
			}
		}
	} else {
		// Range get
		t.store.keyIndex.Range(op.Key, op.End, Zero, func(key []byte, keyRev Revision) bool {
			data, err := t.store.pebbleGet(t.store.makeStorageKey(key, keyRev))
			if err == nil && len(data) > 0 {
				if kv, err := DefaultCodec.Decode(data); err == nil && kv.Version > 0 {
					resp.Kvs = append(resp.Kvs, kv)
				}
			}
			return true
		})
	}

	return resp
}

func (t *pebbleDBTxn) executeDelete(op Op, rev Revision, batch *pebble.Batch) OpResponse {
	resp := OpResponse{Type: OpTypeDelete}

	ki := t.store.keyIndex.Get(op.Key)
	if ki == nil || ki.IsDeleted() {
		return resp
	}

	prevKeyRev := ki.CurrentGeneration().LastRevision()
	var createRev int64
	if !prevKeyRev.IsZero() {
		data, err := t.store.pebbleGet(t.store.makeStorageKey(op.Key, prevKeyRev))
		if err == nil && len(data) > 0 {
			if prevKv, err := DefaultCodec.Decode(data); err == nil {
				createRev = prevKv.CreateRevision
			}
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
	batch.Set(t.store.makeStorageKey(op.Key, rev), encoded, nil)
	t.store.keyIndex.Delete(op.Key, rev)

	resp.Deleted = 1
	return resp
}

func (t *pebbleDBTxn) executeDeleteRange(op Op, baseRev Revision, batch *pebble.Batch) OpResponse {
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
			data, err := t.store.pebbleGet(t.store.makeStorageKey(key, prevKeyRev))
			if err == nil && len(data) > 0 {
				if prevKv, err := DefaultCodec.Decode(data); err == nil {
					createRev = prevKv.CreateRevision
				}
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
		batch.Set(t.store.makeStorageKey(key, deleteRev), encoded, nil)
		t.store.keyIndex.Delete(key, deleteRev)

		resp.Deleted++
	}

	return resp
}

// DBSize returns the approximate size of the database in bytes.
func (s *PebbleStore) DBSize() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0
	}

	// Get approximate size of all data
	start := []byte{0x00}
	end := []byte{0xFF}

	size, _ := s.db.EstimateDiskUsage(start, end)
	return int64(size)
}

// Hash returns a hash of all keys in the database.
func (s *PebbleStore) Hash() (uint32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0, ErrClosed
	}

	var hash uint32
	prefix := []byte(kvMVCCPrefix)
	it, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return 0, err
	}
	defer it.Close()

	for it.SeekGE(prefix); it.Valid(); it.Next() {
		// Simple FNV-like hash
		for _, b := range it.Key() {
			hash = hash*31 + uint32(b)
		}
		for _, b := range it.Value() {
			hash = hash*31 + uint32(b)
		}
	}

	return hash, it.Error()
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
