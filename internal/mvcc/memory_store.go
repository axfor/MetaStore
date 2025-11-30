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
	"sync"

	"github.com/google/btree"
)

// MemoryStore is an in-memory MVCC store implementation.
// It uses a B-tree for storing versioned key-value pairs and
// a KeyIndex for tracking revision history.
type MemoryStore struct {
	mu sync.RWMutex

	// keyIndex tracks all revisions for each key
	keyIndex *KeyIndex

	// revisionStore maps revision -> KeyValue
	// Uses B-tree for ordered iteration and efficient range queries
	revisionStore *btree.BTree

	// revisionGen generates new revisions
	revisionGen *RevisionGenerator

	// compactedRev is the revision that has been compacted
	compactedRev Revision

	// closed indicates if the store is closed
	closed bool
}

// revisionItem wraps a KeyValue with its revision for B-tree storage.
type revisionItem struct {
	rev Revision
	kv  *KeyValue
}

// Less implements btree.Item.
func (ri *revisionItem) Less(other btree.Item) bool {
	return ri.rev.LessThan(other.(*revisionItem).rev)
}

// NewMemoryStore creates a new in-memory MVCC store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		keyIndex:      NewKeyIndex(),
		revisionStore: btree.New(32),
		revisionGen:   NewRevisionGenerator(Revision{0, 0}),
		compactedRev:  Zero,
	}
}

// Put stores a key-value pair and returns the new revision.
func (s *MemoryStore) Put(key, value []byte, lease int64) (int64, error) {
	if len(key) == 0 {
		return 0, ErrEmptyKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, ErrClosed
	}

	// Generate new revision
	rev := s.revisionGen.Next()

	// Get previous version info
	var createRev int64
	var version int64 = 1

	if ki := s.keyIndex.Get(key); ki != nil && !ki.IsDeleted() {
		// Key exists, increment version
		prevRev := ki.CurrentGeneration().LastRevision()
		if !prevRev.IsZero() {
			if item := s.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
				prevKv := item.(*revisionItem).kv
				createRev = prevKv.CreateRevision
				version = prevKv.Version + 1
			}
		}
	} else {
		// New key
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

	// Store in revision store
	s.revisionStore.ReplaceOrInsert(&revisionItem{rev: rev, kv: kv})

	// Update key index
	s.keyIndex.Put(key, rev)

	return rev.Main, nil
}

// Get retrieves the value for a key at a specific revision.
func (s *MemoryStore) Get(key []byte, rev int64) (*KeyValue, error) {
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
		atRev = s.revisionGen.Current()
	}

	// Check if revision is compacted
	if atRev.LessThan(s.compactedRev) {
		return nil, ErrCompacted
	}

	// Check if revision is in the future
	if atRev.GreaterThan(s.revisionGen.Current()) {
		return nil, ErrFutureRevision
	}

	// Find the revision for this key
	keyRev := s.keyIndex.GetRevision(key, atRev)
	if keyRev.IsZero() {
		return nil, ErrKeyNotFound
	}

	// Get the KeyValue from revision store
	item := s.revisionStore.Get(&revisionItem{rev: keyRev})
	if item == nil {
		return nil, ErrKeyNotFound
	}

	kv := item.(*revisionItem).kv

	// Check if this is a delete marker (Version == 0)
	if kv.Version == 0 {
		return nil, ErrKeyNotFound
	}

	return kv.Clone(), nil
}

// Range retrieves key-value pairs in the range [start, end).
func (s *MemoryStore) Range(start, end []byte, rev int64, limit int64) ([]*KeyValue, int64, error) {
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
		atRev = s.revisionGen.Current()
	}

	// Check if revision is compacted
	if atRev.LessThan(s.compactedRev) {
		return nil, 0, ErrCompacted
	}

	// Check if revision is in the future
	if atRev.GreaterThan(s.revisionGen.Current()) {
		return nil, 0, ErrFutureRevision
	}

	var result []*KeyValue
	var count int64

	s.keyIndex.Range(start, end, atRev, func(key []byte, keyRev Revision) bool {
		// Check limit
		if limit > 0 && count >= limit {
			return false
		}

		// Get the KeyValue
		item := s.revisionStore.Get(&revisionItem{rev: keyRev})
		if item == nil {
			return true
		}

		kv := item.(*revisionItem).kv

		// Skip delete markers
		if kv.Version == 0 {
			return true
		}

		result = append(result, kv.Clone())
		count++

		return true
	})

	return result, count, nil
}

// Delete deletes a key and returns the revision and number of deleted keys.
func (s *MemoryStore) Delete(key []byte) (int64, int64, error) {
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
		// Key doesn't exist, return success with 0 deleted
		return s.revisionGen.Current().Main, 0, nil
	}

	// Generate new revision
	rev := s.revisionGen.Next()

	// Get previous KeyValue for the tombstone
	prevRev := ki.CurrentGeneration().LastRevision()
	var createRev int64
	if !prevRev.IsZero() {
		if item := s.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
			createRev = item.(*revisionItem).kv.CreateRevision
		}
	}

	// Create tombstone (Version = 0)
	tombstone := &KeyValue{
		Key:            append([]byte{}, key...),
		Value:          nil,
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        0, // Tombstone marker
		Lease:          0,
	}

	// Store tombstone
	s.revisionStore.ReplaceOrInsert(&revisionItem{rev: rev, kv: tombstone})

	// Update key index
	s.keyIndex.Delete(key, rev)

	return rev.Main, 1, nil
}

// DeleteRange deletes all keys in the range [start, end).
func (s *MemoryStore) DeleteRange(start, end []byte) (int64, int64, error) {
	if len(start) == 0 {
		return 0, 0, ErrEmptyKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, 0, ErrClosed
	}

	// Collect keys to delete (use Zero to get currently live keys)
	var keysToDelete [][]byte
	s.keyIndex.Range(start, end, Zero, func(key []byte, keyRev Revision) bool {
		keysToDelete = append(keysToDelete, append([]byte{}, key...))
		return true
	})

	if len(keysToDelete) == 0 {
		return s.revisionGen.Current().Main, 0, nil
	}

	// Generate revision for this batch delete
	rev := s.revisionGen.Next()
	var deleted int64
	var lastSubRev int64

	for i, key := range keysToDelete {
		ki := s.keyIndex.Get(key)
		if ki == nil || ki.IsDeleted() {
			continue
		}

		// For batch deletes, use sub-revisions
		deleteRev := Revision{Main: rev.Main, Sub: int64(i)}
		lastSubRev = int64(i)

		// Get previous KeyValue
		prevRev := ki.CurrentGeneration().LastRevision()
		var createRev int64
		if !prevRev.IsZero() {
			if item := s.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
				createRev = item.(*revisionItem).kv.CreateRevision
			}
		}

		// Create tombstone
		tombstone := &KeyValue{
			Key:            key,
			Value:          nil,
			CreateRevision: createRev,
			ModRevision:    rev.Main,
			Version:        0,
			Lease:          0,
		}

		// Store tombstone
		s.revisionStore.ReplaceOrInsert(&revisionItem{rev: deleteRev, kv: tombstone})

		// Update key index
		s.keyIndex.Delete(key, deleteRev)

		deleted++
	}

	// Update the revision generator to reflect the highest sub-revision used
	// This ensures that subsequent Range queries with current revision see all deletes
	if deleted > 0 && lastSubRev > 0 {
		s.revisionGen.current.Sub = lastSubRev
	}

	return rev.Main, deleted, nil
}

// Txn executes a transaction.
func (s *MemoryStore) Txn(ctx context.Context) Txn {
	return &memoryTxn{
		store: s,
		ctx:   ctx,
	}
}

// CurrentRevision returns the current revision.
func (s *MemoryStore) CurrentRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revisionGen.Current().Main
}

// CompactedRevision returns the revision that has been compacted.
func (s *MemoryStore) CompactedRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.compactedRev.Main
}

// Compact compacts all revisions before the given revision.
func (s *MemoryStore) Compact(rev int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	targetRev := Revision{Main: rev}

	// Check if already compacted
	if targetRev.LessThanOrEqual(s.compactedRev) {
		return ErrCompacted
	}

	// Check if future revision
	if targetRev.GreaterThan(s.revisionGen.Current()) {
		return ErrFutureRevision
	}

	// Compact key index
	s.keyIndex.Compact(targetRev)

	// Remove old revisions from revision store
	var toDelete []*revisionItem
	s.revisionStore.Ascend(func(item btree.Item) bool {
		ri := item.(*revisionItem)
		if ri.rev.LessThan(targetRev) {
			toDelete = append(toDelete, ri)
		}
		return true
	})

	for _, ri := range toDelete {
		s.revisionStore.Delete(ri)
	}

	s.compactedRev = targetRev

	return nil
}

// Close closes the store.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	s.closed = true
	return nil
}

// memoryTxn implements Txn for MemoryStore.
type memoryTxn struct {
	store *MemoryStore
	ctx   context.Context

	conditions []Condition
	thenOps    []Op
	elseOps    []Op
}

func (t *memoryTxn) If(conds ...Condition) Txn {
	t.conditions = append(t.conditions, conds...)
	return t
}

func (t *memoryTxn) Then(ops ...Op) Txn {
	t.thenOps = append(t.thenOps, ops...)
	return t
}

func (t *memoryTxn) Else(ops ...Op) Txn {
	t.elseOps = append(t.elseOps, ops...)
	return t
}

func (t *memoryTxn) Commit() (*TxnResponse, error) {
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

	// Execute appropriate operations
	var ops []Op
	if succeeded {
		ops = t.thenOps
	} else {
		ops = t.elseOps
	}

	// Generate revision for this transaction
	rev := t.store.revisionGen.Next()

	responses := make([]OpResponse, len(ops))
	for i, op := range ops {
		responses[i] = t.executeOp(op, rev, i)
	}

	return &TxnResponse{
		Succeeded: succeeded,
		Revision:  rev.Main,
		Responses: responses,
	}, nil
}

func (t *memoryTxn) evaluateCondition(cond Condition) bool {
	ki := t.store.keyIndex.Get(cond.Key)

	var kv *KeyValue
	if ki != nil && !ki.IsDeleted() {
		lastRev := ki.CurrentGeneration().LastRevision()
		if !lastRev.IsZero() {
			if item := t.store.revisionStore.Get(&revisionItem{rev: lastRev}); item != nil {
				kv = item.(*revisionItem).kv
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

func (t *memoryTxn) compare(actual interface{}, cmp CompareType, expected interface{}) bool {
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

func (t *memoryTxn) executeOp(op Op, txnRev Revision, subIndex int) OpResponse {
	opRev := Revision{Main: txnRev.Main, Sub: int64(subIndex)}

	switch op.Type {
	case OpTypePut:
		return t.executePut(op, opRev)
	case OpTypeGet:
		return t.executeGet(op)
	case OpTypeDelete:
		return t.executeDelete(op, opRev)
	case OpTypeDeleteRange:
		return t.executeDeleteRange(op, opRev)
	}
	return OpResponse{Type: op.Type}
}

func (t *memoryTxn) executePut(op Op, rev Revision) OpResponse {
	key := op.Key

	// Get previous version info
	var createRev int64
	var version int64 = 1

	ki := t.store.keyIndex.Get(key)
	if ki != nil && !ki.IsDeleted() {
		prevRev := ki.CurrentGeneration().LastRevision()
		if !prevRev.IsZero() {
			if item := t.store.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
				prevKv := item.(*revisionItem).kv
				createRev = prevKv.CreateRevision
				version = prevKv.Version + 1
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

	t.store.revisionStore.ReplaceOrInsert(&revisionItem{rev: rev, kv: kv})
	t.store.keyIndex.Put(key, rev)

	return OpResponse{Type: OpTypePut}
}

func (t *memoryTxn) executeGet(op Op) OpResponse {
	resp := OpResponse{Type: OpTypeGet}

	if op.End == nil {
		// Single key get
		ki := t.store.keyIndex.Get(op.Key)
		if ki != nil && !ki.IsDeleted() {
			lastRev := ki.CurrentGeneration().LastRevision()
			if !lastRev.IsZero() {
				if item := t.store.revisionStore.Get(&revisionItem{rev: lastRev}); item != nil {
					resp.Kvs = []*KeyValue{item.(*revisionItem).kv.Clone()}
				}
			}
		}
	} else {
		// Range get
		t.store.keyIndex.Range(op.Key, op.End, Zero, func(key []byte, keyRev Revision) bool {
			if item := t.store.revisionStore.Get(&revisionItem{rev: keyRev}); item != nil {
				kv := item.(*revisionItem).kv
				if kv.Version > 0 { // Skip tombstones
					resp.Kvs = append(resp.Kvs, kv.Clone())
				}
			}
			return true
		})
	}

	return resp
}

func (t *memoryTxn) executeDelete(op Op, rev Revision) OpResponse {
	resp := OpResponse{Type: OpTypeDelete}

	ki := t.store.keyIndex.Get(op.Key)
	if ki == nil || ki.IsDeleted() {
		return resp
	}

	// Get previous KeyValue
	prevRev := ki.CurrentGeneration().LastRevision()
	var createRev int64
	if !prevRev.IsZero() {
		if item := t.store.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
			createRev = item.(*revisionItem).kv.CreateRevision
		}
	}

	// Create tombstone
	tombstone := &KeyValue{
		Key:            append([]byte{}, op.Key...),
		Value:          nil,
		CreateRevision: createRev,
		ModRevision:    rev.Main,
		Version:        0,
		Lease:          0,
	}

	t.store.revisionStore.ReplaceOrInsert(&revisionItem{rev: rev, kv: tombstone})
	t.store.keyIndex.Delete(op.Key, rev)

	resp.Deleted = 1
	return resp
}

func (t *memoryTxn) executeDeleteRange(op Op, baseRev Revision) OpResponse {
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

		prevRev := ki.CurrentGeneration().LastRevision()
		var createRev int64
		if !prevRev.IsZero() {
			if item := t.store.revisionStore.Get(&revisionItem{rev: prevRev}); item != nil {
				createRev = item.(*revisionItem).kv.CreateRevision
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

		t.store.revisionStore.ReplaceOrInsert(&revisionItem{rev: deleteRev, kv: tombstone})
		t.store.keyIndex.Delete(key, deleteRev)

		resp.Deleted++
	}

	return resp
}
