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

package pool

import (
	"runtime"
	"testing"

	"metaStore/internal/kvstore"
)

// TestKVPool_GetPut tests basic pool get/put operations
func TestKVPool_GetPut(t *testing.T) {
	pool := NewKVPool()

	// Get KV from pool
	kv := pool.GetKV()
	if kv == nil {
		t.Fatal("GetKV returned nil")
	}

	// Verify zero values
	if kv.Key != nil || kv.Value != nil {
		t.Error("GetKV did not return zeroed KeyValue")
	}
	if kv.CreateRevision != 0 || kv.ModRevision != 0 || kv.Version != 0 || kv.Lease != 0 {
		t.Error("GetKV did not return zeroed KeyValue fields")
	}

	// Set values
	kv.Key = []byte("test-key")
	kv.Value = []byte("test-value")
	kv.CreateRevision = 100
	kv.ModRevision = 200
	kv.Version = 5
	kv.Lease = 12345

	// Return to pool
	pool.PutKV(kv)

	// Get again - should be zeroed
	kv2 := pool.GetKV()
	if kv2.Key != nil || kv2.Value != nil {
		t.Error("GetKV after PutKV did not return zeroed KeyValue")
	}

	pool.PutKV(kv2)
}

// TestKVPool_GetPutSlice tests slice pool operations
func TestKVPool_GetPutSlice(t *testing.T) {
	pool := NewKVPool()

	// Get slice from pool
	slice := pool.GetKVSlice()
	if slice == nil {
		t.Fatal("GetKVSlice returned nil")
	}

	// Should have zero length but non-zero capacity
	if len(slice) != 0 {
		t.Errorf("GetKVSlice returned slice with length %d, expected 0", len(slice))
	}
	if cap(slice) == 0 {
		t.Error("GetKVSlice returned slice with zero capacity")
	}

	// Add some elements
	for i := 0; i < 10; i++ {
		kv := pool.GetKV()
		kv.Key = []byte("key")
		slice = append(slice, kv)
	}

	if len(slice) != 10 {
		t.Errorf("Slice length = %d, expected 10", len(slice))
	}

	// Return to pool
	pool.PutKVSlice(slice)

	// Get again - should be empty
	slice2 := pool.GetKVSlice()
	if len(slice2) != 0 {
		t.Errorf("GetKVSlice after PutKVSlice returned length %d, expected 0", len(slice2))
	}

	pool.PutKVSlice(slice2)
}

// TestKVPool_ConvertKV tests single KeyValue conversion
func TestKVPool_ConvertKV(t *testing.T) {
	pool := NewKVPool()

	internal := &kvstore.KeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 100,
		ModRevision:    200,
		Version:        5,
		Lease:          12345,
	}

	// Convert
	proto := pool.ConvertKV(internal)

	// Verify conversion
	if string(proto.Key) != string(internal.Key) {
		t.Error("Key not converted correctly")
	}
	if string(proto.Value) != string(internal.Value) {
		t.Error("Value not converted correctly")
	}
	if proto.CreateRevision != internal.CreateRevision {
		t.Error("CreateRevision not converted correctly")
	}
	if proto.ModRevision != internal.ModRevision {
		t.Error("ModRevision not converted correctly")
	}
	if proto.Version != internal.Version {
		t.Error("Version not converted correctly")
	}
	if proto.Lease != internal.Lease {
		t.Error("Lease not converted correctly")
	}

	// Return to pool
	pool.PutKV(proto)
}

// TestKVPool_ConvertKVSlice tests batch KeyValue conversion
func TestKVPool_ConvertKVSlice(t *testing.T) {
	pool := NewKVPool()

	// Create internal KeyValues
	internals := []*kvstore.KeyValue{
		{Key: []byte("key1"), Value: []byte("val1"), CreateRevision: 1, ModRevision: 10, Version: 1, Lease: 100},
		{Key: []byte("key2"), Value: []byte("val2"), CreateRevision: 2, ModRevision: 20, Version: 2, Lease: 200},
		{Key: []byte("key3"), Value: []byte("val3"), CreateRevision: 3, ModRevision: 30, Version: 3, Lease: 300},
	}

	// Convert
	protos := pool.ConvertKVSlice(internals)

	// Verify
	if len(protos) != len(internals) {
		t.Fatalf("ConvertKVSlice returned %d KeyValues, expected %d", len(protos), len(internals))
	}

	for i, proto := range protos {
		internal := internals[i]
		if string(proto.Key) != string(internal.Key) {
			t.Errorf("KeyValue[%d] Key mismatch", i)
		}
		if string(proto.Value) != string(internal.Value) {
			t.Errorf("KeyValue[%d] Value mismatch", i)
		}
		if proto.CreateRevision != internal.CreateRevision {
			t.Errorf("KeyValue[%d] CreateRevision mismatch", i)
		}
	}

	// Return everything to pool
	pool.PutKVSliceWithKVs(protos)
}

// TestKVPool_ConvertKVNil tests nil handling
func TestKVPool_ConvertKVNil(t *testing.T) {
	pool := NewKVPool()

	// Nil KeyValue
	proto := pool.ConvertKV(nil)
	if proto != nil {
		t.Error("ConvertKV(nil) should return nil")
	}

	// Empty slice
	protos := pool.ConvertKVSlice(nil)
	if protos != nil {
		t.Error("ConvertKVSlice(nil) should return nil")
	}

	protos = pool.ConvertKVSlice([]*kvstore.KeyValue{})
	if protos != nil {
		t.Error("ConvertKVSlice([]) should return nil")
	}
}

// TestKVPool_GlobalHelpers tests global helper functions
func TestKVPool_GlobalHelpers(t *testing.T) {
	// Test global GetKV/PutKV
	kv := GetKV()
	if kv == nil {
		t.Fatal("Global GetKV returned nil")
	}
	PutKV(kv)

	// Test global slice helpers
	slice := GetKVSlice()
	if slice == nil {
		t.Fatal("Global GetKVSlice returned nil")
	}
	PutKVSlice(slice)

	// Test global conversion
	internal := &kvstore.KeyValue{
		Key:   []byte("key"),
		Value: []byte("value"),
	}
	proto := ConvertKV(internal)
	if proto == nil {
		t.Fatal("Global ConvertKV returned nil")
	}
	PutKV(proto)
}

// TestKVPool_Concurrent tests thread safety
func TestKVPool_Concurrent(t *testing.T) {
	pool := NewKVPool()

	// Run concurrent get/put operations
	concurrency := 100
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			// Each goroutine does 1000 operations
			for j := 0; j < 1000; j++ {
				kv := pool.GetKV()
				kv.Key = []byte("key")
				kv.Value = []byte("value")
				pool.PutKV(kv)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < concurrency; i++ {
		<-done
	}
}

// Benchmarks

// BenchmarkKVPool_GetPut benchmarks pool get/put operations
func BenchmarkKVPool_GetPut(b *testing.B) {
	pool := NewKVPool()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kv := pool.GetKV()
		kv.Key = []byte("benchmark-key")
		kv.Value = []byte("benchmark-value")
		pool.PutKV(kv)
	}
}

// BenchmarkKVPool_DirectAlloc benchmarks direct allocation (no pool)
func BenchmarkKVPool_DirectAlloc(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kv := &struct {
			Key            []byte
			Value          []byte
			CreateRevision int64
			ModRevision    int64
			Version        int64
			Lease          int64
		}{}
		kv.Key = []byte("benchmark-key")
		kv.Value = []byte("benchmark-value")
		_ = kv // Prevent optimization
	}
}

// BenchmarkKVPool_ConvertKV benchmarks single KeyValue conversion
func BenchmarkKVPool_ConvertKV(b *testing.B) {
	pool := NewKVPool()
	internal := &kvstore.KeyValue{
		Key:            []byte("benchmark-key"),
		Value:          []byte("benchmark-value"),
		CreateRevision: 100,
		ModRevision:    200,
		Version:        5,
		Lease:          12345,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proto := pool.ConvertKV(internal)
		pool.PutKV(proto)
	}
}

// BenchmarkKVPool_ConvertKVSlice benchmarks batch conversion
func BenchmarkKVPool_ConvertKVSlice(b *testing.B) {
	pool := NewKVPool()

	// Create 100 KeyValues (typical Range query result)
	internals := make([]*kvstore.KeyValue, 100)
	for i := 0; i < 100; i++ {
		internals[i] = &kvstore.KeyValue{
			Key:            []byte("key"),
			Value:          []byte("value"),
			CreateRevision: int64(i),
			ModRevision:    int64(i * 2),
			Version:        1,
			Lease:          0,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		protos := pool.ConvertKVSlice(internals)
		pool.PutKVSliceWithKVs(protos)
	}
}

// BenchmarkKVPool_ConvertKVSlice_NoPool benchmarks batch conversion without pool
func BenchmarkKVPool_ConvertKVSlice_NoPool(b *testing.B) {
	// Create 100 KeyValues (typical Range query result)
	internals := make([]*kvstore.KeyValue, 100)
	for i := 0; i < 100; i++ {
		internals[i] = &kvstore.KeyValue{
			Key:            []byte("key"),
			Value:          []byte("value"),
			CreateRevision: int64(i),
			ModRevision:    int64(i * 2),
			Version:        1,
			Lease:          0,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		protos := make([]*kvstore.KeyValue, len(internals))
		for j, internal := range internals {
			protos[j] = &kvstore.KeyValue{
				Key:            internal.Key,
				Value:          internal.Value,
				CreateRevision: internal.CreateRevision,
				ModRevision:    internal.ModRevision,
				Version:        internal.Version,
				Lease:          internal.Lease,
			}
		}
		_ = protos // Prevent optimization
	}
}

// BenchmarkKVPool_Parallel benchmarks parallel pool usage
func BenchmarkKVPool_Parallel(b *testing.B) {
	pool := NewKVPool()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			kv := pool.GetKV()
			kv.Key = []byte("key")
			kv.Value = []byte("value")
			pool.PutKV(kv)
		}
	})
}

// BenchmarkKVPool_GCPressure measures GC impact with pool
func BenchmarkKVPool_GCPressure(b *testing.B) {
	pool := NewKVPool()

	var ms1, ms2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kv := pool.GetKV()
		kv.Key = make([]byte, 100)
		kv.Value = make([]byte, 100)
		pool.PutKV(kv)
	}

	runtime.GC()
	runtime.ReadMemStats(&ms2)

	b.ReportMetric(float64(ms2.TotalAlloc-ms1.TotalAlloc)/float64(b.N), "allocs/op")
}
