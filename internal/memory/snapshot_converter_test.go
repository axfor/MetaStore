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
	"encoding/json"
	"metaStore/internal/kvstore"
	"testing"
	"time"
)

// TestSnapshotProtobufSerialization 测试 Protobuf 快照序列化
func TestSnapshotProtobufSerialization(t *testing.T) {
	// 准备测试数据
	revision := int64(100)
	kvData := map[string]*kvstore.KeyValue{
		"key1": {
			Key:            []byte("key1"),
			Value:          []byte("value1"),
			CreateRevision: 1,
			ModRevision:    10,
			Version:        5,
			Lease:          123,
		},
		"key2": {
			Key:            []byte("key2"),
			Value:          []byte("value2"),
			CreateRevision: 2,
			ModRevision:    20,
			Version:        3,
			Lease:          0,
		},
	}

	now := time.Now()
	leases := map[int64]*kvstore.Lease{
		123: {
			ID:        123,
			TTL:       60,
			GrantTime: now,
			Keys:      map[string]bool{"key1": true},
		},
		456: {
			ID:        456,
			TTL:       120,
			GrantTime: now.Add(-30 * time.Second),
			Keys:      map[string]bool{"key3": true, "key4": true},
		},
	}

	// 序列化
	data, err := serializeSnapshot(revision, kvData, leases)
	if err != nil {
		t.Fatalf("serializeSnapshot failed: %v", err)
	}

	// 验证使用了 Protobuf 格式
	if !isProtobufSnapshot(data) {
		t.Error("Expected Protobuf format, got JSON")
	}

	// 反序列化
	snapshot, err := deserializeSnapshot(data)
	if err != nil {
		t.Fatalf("deserializeSnapshot failed: %v", err)
	}

	// 验证 Revision
	if snapshot.Revision != revision {
		t.Errorf("Expected revision %d, got %d", revision, snapshot.Revision)
	}

	// 验证 KV 数据
	if len(snapshot.KVData) != len(kvData) {
		t.Errorf("Expected %d KV entries, got %d", len(kvData), len(snapshot.KVData))
	}

	for k, expectedKV := range kvData {
		actualKV, exists := snapshot.KVData[k]
		if !exists {
			t.Errorf("Key %s not found in deserialized snapshot", k)
			continue
		}

		if string(actualKV.Key) != string(expectedKV.Key) {
			t.Errorf("Key mismatch: expected %s, got %s", expectedKV.Key, actualKV.Key)
		}
		if string(actualKV.Value) != string(expectedKV.Value) {
			t.Errorf("Value mismatch for %s: expected %s, got %s", k, expectedKV.Value, actualKV.Value)
		}
		if actualKV.CreateRevision != expectedKV.CreateRevision {
			t.Errorf("CreateRevision mismatch for %s: expected %d, got %d", k, expectedKV.CreateRevision, actualKV.CreateRevision)
		}
		if actualKV.ModRevision != expectedKV.ModRevision {
			t.Errorf("ModRevision mismatch for %s: expected %d, got %d", k, expectedKV.ModRevision, actualKV.ModRevision)
		}
		if actualKV.Version != expectedKV.Version {
			t.Errorf("Version mismatch for %s: expected %d, got %d", k, expectedKV.Version, actualKV.Version)
		}
		if actualKV.Lease != expectedKV.Lease {
			t.Errorf("Lease mismatch for %s: expected %d, got %d", k, expectedKV.Lease, actualKV.Lease)
		}
	}

	// 验证 Lease 数据
	if len(snapshot.Leases) != len(leases) {
		t.Errorf("Expected %d leases, got %d", len(leases), len(snapshot.Leases))
	}

	for id, expectedLease := range leases {
		actualLease, exists := snapshot.Leases[id]
		if !exists {
			t.Errorf("Lease %d not found in deserialized snapshot", id)
			continue
		}

		if actualLease.ID != expectedLease.ID {
			t.Errorf("Lease ID mismatch: expected %d, got %d", expectedLease.ID, actualLease.ID)
		}
		if actualLease.TTL != expectedLease.TTL {
			t.Errorf("Lease TTL mismatch for %d: expected %d, got %d", id, expectedLease.TTL, actualLease.TTL)
		}

		// 验证 GrantTime（允许纳秒级误差）
		if actualLease.GrantTime.UnixNano() != expectedLease.GrantTime.UnixNano() {
			t.Errorf("Lease GrantTime mismatch for %d: expected %v, got %v", id, expectedLease.GrantTime, actualLease.GrantTime)
		}

		// 验证 Keys
		if len(actualLease.Keys) != len(expectedLease.Keys) {
			t.Errorf("Lease Keys count mismatch for %d: expected %d, got %d", id, len(expectedLease.Keys), len(actualLease.Keys))
		}
		for key := range expectedLease.Keys {
			if !actualLease.Keys[key] {
				t.Errorf("Lease %d missing key %s", id, key)
			}
		}
	}
}

// TestSnapshotJSONBackwardCompatibility 测试 JSON 向后兼容性
func TestSnapshotJSONBackwardCompatibility(t *testing.T) {
	// 准备测试数据（使用旧的 JSON 格式）
	revision := int64(50)
	kvData := map[string]*kvstore.KeyValue{
		"oldkey": {
			Key:            []byte("oldkey"),
			Value:          []byte("oldvalue"),
			CreateRevision: 1,
			ModRevision:    5,
			Version:        2,
			Lease:          0,
		},
	}

	now := time.Now()
	leases := map[int64]*kvstore.Lease{
		789: {
			ID:        789,
			TTL:       30,
			GrantTime: now,
			Keys:      map[string]bool{"oldkey": true},
		},
	}

	// 使用 JSON 序列化（模拟旧快照）
	oldSnapshot := SnapshotData{
		Revision: revision,
		KVData:   kvData,
		Leases:   leases,
	}
	jsonData, err := json.Marshal(oldSnapshot)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// 使用新的反序列化函数（应该能处理 JSON）
	snapshot, err := deserializeSnapshot(jsonData)
	if err != nil {
		t.Fatalf("deserializeSnapshot failed for JSON: %v", err)
	}

	// 验证数据正确性
	if snapshot.Revision != revision {
		t.Errorf("Expected revision %d, got %d", revision, snapshot.Revision)
	}

	if len(snapshot.KVData) != 1 {
		t.Errorf("Expected 1 KV entry, got %d", len(snapshot.KVData))
	}

	kv := snapshot.KVData["oldkey"]
	if kv == nil {
		t.Fatal("oldkey not found")
	}
	if string(kv.Value) != "oldvalue" {
		t.Errorf("Expected value 'oldvalue', got '%s'", string(kv.Value))
	}

	if len(snapshot.Leases) != 1 {
		t.Errorf("Expected 1 lease, got %d", len(snapshot.Leases))
	}
}

// TestSnapshotEmptyData 测试空快照
func TestSnapshotEmptyData(t *testing.T) {
	revision := int64(0)
	kvData := map[string]*kvstore.KeyValue{}
	leases := map[int64]*kvstore.Lease{}

	// 序列化空快照
	data, err := serializeSnapshot(revision, kvData, leases)
	if err != nil {
		t.Fatalf("serializeSnapshot failed: %v", err)
	}

	// 调试输出
	t.Logf("Serialized empty snapshot: len=%d, data=%q", len(data), string(data[:min(len(data), 50)]))

	// 反序列化
	snapshot, err := deserializeSnapshot(data)
	if err != nil {
		t.Fatalf("deserializeSnapshot failed: %v (data len=%d, prefix=%q)", err, len(data), string(data[:min(len(data), 10)]))
	}

	// 验证
	if snapshot.Revision != 0 {
		t.Errorf("Expected revision 0, got %d", snapshot.Revision)
	}
	if len(snapshot.KVData) != 0 {
		t.Errorf("Expected empty KVData, got %d entries", len(snapshot.KVData))
	}
	if len(snapshot.Leases) != 0 {
		t.Errorf("Expected empty Leases, got %d entries", len(snapshot.Leases))
	}
}

// BenchmarkSnapshotProtobuf 基准测试: Protobuf 序列化
func BenchmarkSnapshotProtobuf(b *testing.B) {
	// 准备大量测试数据（模拟真实场景）
	kvData := make(map[string]*kvstore.KeyValue, 1000)
	for i := 0; i < 1000; i++ {
		key := string(rune('k')) + string(rune(i))
		kvData[key] = &kvstore.KeyValue{
			Key:            []byte(key),
			Value:          []byte("value" + string(rune(i))),
			CreateRevision: int64(i),
			ModRevision:    int64(i * 2),
			Version:        int64(i % 10),
			Lease:          0,
		}
	}

	leases := make(map[int64]*kvstore.Lease, 100)
	for i := 0; i < 100; i++ {
		leases[int64(i)] = &kvstore.Lease{
			ID:        int64(i),
			TTL:       60,
			GrantTime: time.Now(),
			Keys:      map[string]bool{"key": true},
		}
	}

	revision := int64(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := serializeSnapshot(revision, kvData, leases)
		if err != nil {
			b.Fatalf("serializeSnapshot failed: %v", err)
		}

		_, err = deserializeSnapshot(data)
		if err != nil {
			b.Fatalf("deserializeSnapshot failed: %v", err)
		}
	}
}

// BenchmarkSnapshotJSON 基准测试: JSON 序列化（对比）
func BenchmarkSnapshotJSON(b *testing.B) {
	// 准备相同的测试数据
	kvData := make(map[string]*kvstore.KeyValue, 1000)
	for i := 0; i < 1000; i++ {
		key := string(rune('k')) + string(rune(i))
		kvData[key] = &kvstore.KeyValue{
			Key:            []byte(key),
			Value:          []byte("value" + string(rune(i))),
			CreateRevision: int64(i),
			ModRevision:    int64(i * 2),
			Version:        int64(i % 10),
			Lease:          0,
		}
	}

	leases := make(map[int64]*kvstore.Lease, 100)
	for i := 0; i < 100; i++ {
		leases[int64(i)] = &kvstore.Lease{
			ID:        int64(i),
			TTL:       60,
			GrantTime: time.Now(),
			Keys:      map[string]bool{"key": true},
		}
	}

	snapshot := SnapshotData{
		Revision: 1000,
		KVData:   kvData,
		Leases:   leases,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(snapshot)
		if err != nil {
			b.Fatalf("JSON marshal failed: %v", err)
		}

		var decoded SnapshotData
		err = json.Unmarshal(data, &decoded)
		if err != nil {
			b.Fatalf("JSON unmarshal failed: %v", err)
		}
	}
}

// isProtobufSnapshot 检查是否为 Protobuf 格式
func isProtobufSnapshot(data []byte) bool {
	const pbPrefix = "SNAP-PB:"
	return len(data) > len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix
}
