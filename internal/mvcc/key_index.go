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
	"sync"

	"github.com/google/btree"
)

// Generation represents a lifetime of a key.
// A new generation is created when a key is created after being deleted.
type Generation struct {
	// Created is the revision when this generation was created.
	Created Revision

	// Revisions is the list of all revisions in this generation.
	// Sorted in ascending order.
	Revisions []Revision
}

// IsEmpty returns true if this generation has no revisions.
func (g *Generation) IsEmpty() bool {
	return len(g.Revisions) == 0
}

// LastRevision returns the last revision in this generation.
func (g *Generation) LastRevision() Revision {
	if len(g.Revisions) == 0 {
		return Zero
	}
	return g.Revisions[len(g.Revisions)-1]
}

// KeyItem represents a key's revision history.
// It implements btree.Item for use in a B-tree index.
type KeyItem struct {
	// Key is the user key.
	Key []byte

	// Generations holds all generations for this key.
	// The last generation is the current one.
	Generations []Generation

	// Modified is the revision of the last modification.
	Modified Revision
}

// Less implements btree.Item.
func (ki *KeyItem) Less(other btree.Item) bool {
	return bytes.Compare(ki.Key, other.(*KeyItem).Key) < 0
}

// CurrentGeneration returns the current (last) generation.
// Returns nil if there are no generations.
func (ki *KeyItem) CurrentGeneration() *Generation {
	if len(ki.Generations) == 0 {
		return nil
	}
	return &ki.Generations[len(ki.Generations)-1]
}

// IsDeleted returns true if the key is currently deleted.
// A key is deleted if it has no generations or the current generation is empty.
func (ki *KeyItem) IsDeleted() bool {
	gen := ki.CurrentGeneration()
	return gen == nil || gen.IsEmpty()
}

// FindRevision finds the revision at or before the given revision.
// Returns Zero if not found or the key was deleted at that point.
func (ki *KeyItem) FindRevision(atRev Revision) Revision {
	// Search generations in reverse order
	for i := len(ki.Generations) - 1; i >= 0; i-- {
		gen := &ki.Generations[i]

		// Skip if generation was created after the target revision
		if gen.Created.GreaterThan(atRev) {
			continue
		}

		// Binary search for the revision at or before atRev
		revs := gen.Revisions
		idx := binarySearchRevision(revs, atRev)
		if idx >= 0 {
			return revs[idx]
		}
	}
	return Zero
}

// binarySearchRevision finds the largest revision <= target.
// Returns -1 if no such revision exists.
func binarySearchRevision(revs []Revision, target Revision) int {
	left, right := 0, len(revs)-1
	result := -1

	for left <= right {
		mid := (left + right) / 2
		if revs[mid].LessThanOrEqual(target) {
			result = mid
			left = mid + 1
		} else {
			right = mid - 1
		}
	}
	return result
}

// KeyIndex is an in-memory B-tree index for tracking key revisions.
// It maps keys to their revision history.
type KeyIndex struct {
	mu   sync.RWMutex
	tree *btree.BTree
}

// NewKeyIndex creates a new KeyIndex.
func NewKeyIndex() *KeyIndex {
	return &KeyIndex{
		tree: btree.New(32), // degree 32 for good balance
	}
}

// Get retrieves the KeyItem for the given key.
// Returns nil if the key doesn't exist.
func (idx *KeyIndex) Get(key []byte) *KeyItem {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	item := idx.tree.Get(&KeyItem{Key: key})
	if item == nil {
		return nil
	}
	return item.(*KeyItem)
}

// GetRevision finds the revision for the key at the specified revision.
// Returns Zero if the key doesn't exist or was deleted at that revision.
func (idx *KeyIndex) GetRevision(key []byte, atRev Revision) Revision {
	ki := idx.Get(key)
	if ki == nil {
		return Zero
	}
	return ki.FindRevision(atRev)
}

// Put adds or updates a key with the given revision.
// If the key doesn't exist or was deleted, a new generation is created.
func (idx *KeyIndex) Put(key []byte, rev Revision) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	item := idx.tree.Get(&KeyItem{Key: key})
	var ki *KeyItem

	if item == nil {
		// New key
		ki = &KeyItem{
			Key: append([]byte{}, key...), // copy key
			Generations: []Generation{
				{Created: rev, Revisions: []Revision{rev}},
			},
			Modified: rev,
		}
		idx.tree.ReplaceOrInsert(ki)
		return
	}

	ki = item.(*KeyItem)
	gen := ki.CurrentGeneration()

	if gen == nil || gen.IsEmpty() {
		// Key was deleted, create new generation
		ki.Generations = append(ki.Generations, Generation{
			Created:   rev,
			Revisions: []Revision{rev},
		})
	} else {
		// Append to current generation
		gen.Revisions = append(gen.Revisions, rev)
	}
	ki.Modified = rev
}

// Delete marks a key as deleted at the given revision.
// The current generation ends (becomes empty for future lookups).
func (idx *KeyIndex) Delete(key []byte, rev Revision) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	item := idx.tree.Get(&KeyItem{Key: key})
	if item == nil {
		return false
	}

	ki := item.(*KeyItem)
	gen := ki.CurrentGeneration()
	if gen == nil || gen.IsEmpty() {
		// Already deleted
		return false
	}

	// Add the delete revision to mark the end
	gen.Revisions = append(gen.Revisions, rev)
	ki.Modified = rev

	// Create empty generation to mark deletion
	ki.Generations = append(ki.Generations, Generation{Created: rev})

	return true
}

// Range iterates over keys in the range [start, end).
// If end is nil, iterates from start to the end of the index.
// The callback receives the key and its latest revision at the specified atRev.
// If atRev is Zero, returns the current latest revision.
func (idx *KeyIndex) Range(start, end []byte, atRev Revision, fn func(key []byte, rev Revision) bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	startItem := &KeyItem{Key: start}

	idx.tree.AscendGreaterOrEqual(startItem, func(item btree.Item) bool {
		ki := item.(*KeyItem)

		// Check end boundary
		if end != nil && bytes.Compare(ki.Key, end) >= 0 {
			return false
		}

		var rev Revision
		if atRev.IsZero() {
			// Get current revision
			gen := ki.CurrentGeneration()
			if gen != nil && !gen.IsEmpty() {
				rev = gen.LastRevision()
			}
		} else {
			rev = ki.FindRevision(atRev)
		}

		if !rev.IsZero() {
			return fn(ki.Key, rev)
		}
		return true
	})
}

// Compact removes all revisions before the given revision.
// Returns the number of revisions removed.
func (idx *KeyIndex) Compact(atRev Revision) int64 {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var removed int64
	var keysToDelete []*KeyItem

	idx.tree.Ascend(func(item btree.Item) bool {
		ki := item.(*KeyItem)

		// Compact each generation
		newGens := make([]Generation, 0, len(ki.Generations))
		for i := range ki.Generations {
			gen := &ki.Generations[i]

			// Find first revision >= atRev
			keepFrom := 0
			for j, r := range gen.Revisions {
				if r.GreaterThanOrEqual(atRev) {
					keepFrom = j
					break
				}
				removed++
				keepFrom = j + 1
			}

			if keepFrom < len(gen.Revisions) {
				// Keep remaining revisions
				newGen := Generation{
					Created:   gen.Created,
					Revisions: make([]Revision, len(gen.Revisions)-keepFrom),
				}
				copy(newGen.Revisions, gen.Revisions[keepFrom:])
				newGens = append(newGens, newGen)
			}
		}

		if len(newGens) == 0 {
			// All generations removed, mark key for deletion
			keysToDelete = append(keysToDelete, ki)
		} else {
			ki.Generations = newGens
		}

		return true
	})

	// Remove fully compacted keys
	for _, ki := range keysToDelete {
		idx.tree.Delete(ki)
	}

	return removed
}

// Len returns the number of keys in the index.
func (idx *KeyIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Len()
}

// RevisionCount returns the total number of revisions across all keys.
func (idx *KeyIndex) RevisionCount() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var count int64
	idx.tree.Ascend(func(item btree.Item) bool {
		ki := item.(*KeyItem)
		for _, gen := range ki.Generations {
			count += int64(len(gen.Revisions))
		}
		return true
	})
	return count
}
