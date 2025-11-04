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

package memory

import (
	"hash/fnv"
	"sort"
	"sync"

	"metaStore/internal/kvstore"
)

const (
	// numShards defines the number of shards for the map
	// Power of 2 for efficient modulo operation using bitwise AND
	numShards = 512
	shardMask = numShards - 1
)

// ShardedMap is a thread-safe sharded map for better concurrency
// Each shard has its own lock, allowing parallel access to different shards
type ShardedMap struct {
	shards [numShards]shard
}

// shard represents a single shard with independent locking
type shard struct {
	mu   sync.RWMutex
	data map[string]*kvstore.KeyValue
}

// NewShardedMap creates a new sharded map
func NewShardedMap() *ShardedMap {
	sm := &ShardedMap{}
	for i := 0; i < numShards; i++ {
		sm.shards[i].data = make(map[string]*kvstore.KeyValue)
	}
	return sm
}

// getShard returns the shard index for a given key
// Uses FNV-1a hash for good distribution
func (sm *ShardedMap) getShard(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32() & shardMask
}

// Get retrieves a value from the map
func (sm *ShardedMap) Get(key string) (*kvstore.KeyValue, bool) {
	shardIdx := sm.getShard(key)
	shard := &sm.shards[shardIdx]

	shard.mu.RLock()
	defer shard.mu.RUnlock()

	kv, ok := shard.data[key]
	return kv, ok
}

// Set stores a value in the map
func (sm *ShardedMap) Set(key string, kv *kvstore.KeyValue) {
	shardIdx := sm.getShard(key)
	shard := &sm.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	shard.data[key] = kv
}

// Delete removes a key from the map
func (sm *ShardedMap) Delete(key string) {
	shardIdx := sm.getShard(key)
	shard := &sm.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	delete(shard.data, key)
}

// Range iterates over keys in the specified range
// For range queries, we need to scan all shards and combine results
func (sm *ShardedMap) Range(startKey, endKey string, limit int64) []*kvstore.KeyValue {
	// Collect from all shards
	var allKvs []*kvstore.KeyValue

	// We need to lock all shards for range query
	// Lock them in order to prevent deadlock
	for i := 0; i < numShards; i++ {
		shard := &sm.shards[i]
		shard.mu.RLock()
	}

	// Collect matching keys from all shards
	for i := 0; i < numShards; i++ {
		shard := &sm.shards[i]
		for k, v := range shard.data {
			if k >= startKey && (endKey == "\x00" || k < endKey) {
				allKvs = append(allKvs, v)
			}
		}
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		shard := &sm.shards[i]
		shard.mu.RUnlock()
	}

	// Sort by key
	sort.Slice(allKvs, func(i, j int) bool {
		return string(allKvs[i].Key) < string(allKvs[j].Key)
	})

	// Apply limit
	if limit > 0 && int64(len(allKvs)) > limit {
		allKvs = allKvs[:limit]
	}

	return allKvs
}

// RangeFunc iterates over keys in the specified range with a callback function
// This is more efficient for operations that don't need to collect all results
func (sm *ShardedMap) RangeFunc(startKey, endKey string, limit int64, fn func(*kvstore.KeyValue) bool) {
	// Collect from all shards first
	var allKvs []*kvstore.KeyValue

	// Lock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RLock()
	}

	// Collect matching keys
	for i := 0; i < numShards; i++ {
		for k, v := range sm.shards[i].data {
			if k >= startKey && (endKey == "\x00" || k < endKey) {
				allKvs = append(allKvs, v)
			}
		}
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RUnlock()
	}

	// Sort by key
	sort.Slice(allKvs, func(i, j int) bool {
		return string(allKvs[i].Key) < string(allKvs[j].Key)
	})

	// Apply limit and call function
	count := int64(0)
	for _, kv := range allKvs {
		if limit > 0 && count >= limit {
			break
		}
		if !fn(kv) {
			break
		}
		count++
	}
}

// Len returns the total number of entries across all shards
func (sm *ShardedMap) Len() int {
	total := 0

	// Lock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RLock()
	}

	// Count entries
	for i := 0; i < numShards; i++ {
		total += len(sm.shards[i].data)
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RUnlock()
	}

	return total
}

// Clear removes all entries from all shards
func (sm *ShardedMap) Clear() {
	// Lock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.Lock()
	}

	// Clear data
	for i := 0; i < numShards; i++ {
		sm.shards[i].data = make(map[string]*kvstore.KeyValue)
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.Unlock()
	}
}

// GetAll returns all key-value pairs (used for snapshots)
func (sm *ShardedMap) GetAll() map[string]*kvstore.KeyValue {
	result := make(map[string]*kvstore.KeyValue)

	// Lock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RLock()
	}

	// Copy all data
	for i := 0; i < numShards; i++ {
		for k, v := range sm.shards[i].data {
			result[k] = v
		}
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.RUnlock()
	}

	return result
}

// SetAll replaces all data (used for snapshot recovery)
func (sm *ShardedMap) SetAll(data map[string]*kvstore.KeyValue) {
	// Lock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.Lock()
	}

	// Clear existing data
	for i := 0; i < numShards; i++ {
		sm.shards[i].data = make(map[string]*kvstore.KeyValue)
	}

	// Distribute new data to shards
	for k, v := range data {
		shardIdx := sm.getShard(k)
		sm.shards[shardIdx].data[k] = v
	}

	// Unlock all shards
	for i := 0; i < numShards; i++ {
		sm.shards[i].mu.Unlock()
	}
}
