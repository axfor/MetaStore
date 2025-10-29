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

package syncmap

import "sync"

// Map is a generic wrapper around sync.Map providing type-safe operations
// It's optimized for high-concurrency scenarios with mostly read operations
// Performance characteristics:
// - Load: O(1) amortized, lock-free for keys in read map
// - Store: O(1) amortized, uses atomic operations
// - Delete: O(1) amortized
// - Range: O(n) where n is number of entries
//
// Best use cases:
// - Cache-like structures with many reads and few writes
// - Concurrent maps where keys are stable after initial population
// - Avoiding mutex contention in read-heavy workloads
type Map[K comparable, V any] struct {
	m sync.Map
}

// NewMap creates a new generic sync.Map
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{}
}

// Load returns the value stored in the map for a key, or nil if no value is present.
// The ok result indicates whether value was found in the map.
func (m *Map[K, V]) Load(key K) (value V, ok bool) {
	v, ok := m.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// Store sets the value for a key.
func (m *Map[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	v, loaded := m.m.LoadOrStore(key, value)
	return v.(V), loaded
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	v, loaded := m.m.LoadAndDelete(key)
	if !loaded {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// Delete deletes the value for a key.
func (m *Map[K, V]) Delete(key K) {
	m.m.Delete(key)
}

// Swap swaps the value for a key and returns the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) Swap(key K, value V) (previous V, loaded bool) {
	v, loaded := m.m.Swap(key, value)
	if !loaded {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// CompareAndSwap swaps the old and new values for key if the value stored in the map is equal to old.
// The old value must be of a comparable type.
func (m *Map[K, V]) CompareAndSwap(key K, old, new V) (swapped bool) {
	return m.m.CompareAndSwap(key, old, new)
}

// CompareAndDelete deletes the entry for key if its value is equal to old.
// The old value must be of a comparable type.
//
// If there is no current value for key in the map, CompareAndDelete returns false (even if old is the nil interface value).
func (m *Map[K, V]) CompareAndDelete(key K, old V) (deleted bool) {
	return m.m.CompareAndDelete(key, old)
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's contents:
// no key will be visited more than once, but if the value for any key is stored or deleted concurrently
// (including by f), Range may reflect any mapping for that key from any point during the Range call.
// Range does not block other methods on the receiver; even f itself may call any method on m.
//
// Range may be O(N) with the number of elements in the map even if f returns false after a constant number of calls.
func (m *Map[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(key, value interface{}) bool {
		return f(key.(K), value.(V))
	})
}

// Len returns the number of elements in the map.
// Note: This is an O(n) operation that iterates through all entries.
// Use sparingly in performance-critical code.
func (m *Map[K, V]) Len() int {
	count := 0
	m.m.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// Clear removes all entries from the map.
// Note: This is an O(n) operation.
func (m *Map[K, V]) Clear() {
	m.m.Range(func(key, value interface{}) bool {
		m.m.Delete(key)
		return true
	})
}

// Keys returns a slice of all keys in the map.
// Note: This is an O(n) operation.
// The order of keys is non-deterministic.
func (m *Map[K, V]) Keys() []K {
	keys := []K{}
	m.m.Range(func(key, value interface{}) bool {
		keys = append(keys, key.(K))
		return true
	})
	return keys
}

// Values returns a slice of all values in the map.
// Note: This is an O(n) operation.
// The order of values is non-deterministic.
func (m *Map[K, V]) Values() []V {
	values := []V{}
	m.m.Range(func(key, value interface{}) bool {
		values = append(values, value.(V))
		return true
	})
	return values
}

// Clone creates a shallow copy of the map.
// Note: This is an O(n) operation.
// The values themselves are not cloned.
func (m *Map[K, V]) Clone() *Map[K, V] {
	newMap := NewMap[K, V]()
	m.m.Range(func(key, value interface{}) bool {
		newMap.m.Store(key, value)
		return true
	})
	return newMap
}
