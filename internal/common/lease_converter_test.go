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

package common

import (
	"bytes"
	"encoding/gob"
	"metaStore/internal/kvstore"
	"testing"
	"time"
)

// TestLeaseProtobufSerialization 测试 Protobuf Lease 序列化
func TestLeaseProtobufSerialization(t *testing.T) {
	// 准备测试数据
	now := time.Now()
	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: now,
		Keys:      map[string]bool{"key1": true, "key2": true, "key3": true},
	}

	// 序列化
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// 验证使用了 Protobuf 格式
	if !isProtobufLease(data) {
		t.Error("Expected Protobuf format, got GOB")
	}

	// 反序列化
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// 验证数据正确性
	if decoded.ID != lease.ID {
		t.Errorf("Expected ID %d, got %d", lease.ID, decoded.ID)
	}
	if decoded.TTL != lease.TTL {
		t.Errorf("Expected TTL %d, got %d", lease.TTL, decoded.TTL)
	}

	// 验证 GrantTime（纳秒精度）
	if decoded.GrantTime.UnixNano() != lease.GrantTime.UnixNano() {
		t.Errorf("Expected GrantTime %v, got %v", lease.GrantTime, decoded.GrantTime)
	}

	// 验证 Keys
	if len(decoded.Keys) != len(lease.Keys) {
		t.Errorf("Expected %d keys, got %d", len(lease.Keys), len(decoded.Keys))
	}
	for key := range lease.Keys {
		if !decoded.Keys[key] {
			t.Errorf("Missing key %s", key)
		}
	}
}

// TestLeaseGOBBackwardCompatibility 测试 GOB 向后兼容性
func TestLeaseGOBBackwardCompatibility(t *testing.T) {
	// 准备测试数据（使用旧的 GOB 格式）
	now := time.Now()
	lease := &kvstore.Lease{
		ID:        456,
		TTL:       120,
		GrantTime: now,
		Keys:      map[string]bool{"oldkey": true},
	}

	// 使用 GOB 序列化（模拟旧数据）
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		t.Fatalf("GOB encode failed: %v", err)
	}
	gobData := buf.Bytes()

	// 使用新的反序列化函数（应该能处理 GOB）
	decoded, err := DeserializeLease(gobData)
	if err != nil {
		t.Fatalf("DeserializeLease failed for GOB: %v", err)
	}

	// 验证数据正确性
	if decoded.ID != lease.ID {
		t.Errorf("Expected ID %d, got %d", lease.ID, decoded.ID)
	}
	if decoded.TTL != lease.TTL {
		t.Errorf("Expected TTL %d, got %d", lease.TTL, decoded.TTL)
	}
	if len(decoded.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(decoded.Keys))
	}
	if !decoded.Keys["oldkey"] {
		t.Error("Missing key 'oldkey'")
	}
}

// TestLeaseEmptyKeys 测试无关联 key 的 Lease
func TestLeaseEmptyKeys(t *testing.T) {
	lease := &kvstore.Lease{
		ID:        789,
		TTL:       30,
		GrantTime: time.Now(),
		Keys:      map[string]bool{}, // 空 Keys
	}

	// 序列化
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// 反序列化
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// 验证
	if decoded.ID != lease.ID {
		t.Errorf("Expected ID %d, got %d", lease.ID, decoded.ID)
	}
	if len(decoded.Keys) != 0 {
		t.Errorf("Expected empty Keys, got %d keys", len(decoded.Keys))
	}
}

// TestLeaseNilLease 测试 nil Lease 处理
func TestLeaseNilLease(t *testing.T) {
	_, err := SerializeLease(nil)
	if err == nil {
		t.Error("Expected error when serializing nil lease")
	}
}

// TestLeaseManyKeys 测试大量 key 的 Lease
func TestLeaseManyKeys(t *testing.T) {
	// 创建包含 1000 个 key 的 Lease
	keys := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		keys[string(rune('k'))+string(rune(i))] = true
	}

	lease := &kvstore.Lease{
		ID:        999,
		TTL:       300,
		GrantTime: time.Now(),
		Keys:      keys,
	}

	// 序列化
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// 反序列化
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// 验证
	if len(decoded.Keys) != 1000 {
		t.Errorf("Expected 1000 keys, got %d", len(decoded.Keys))
	}
}

// BenchmarkLeaseProtobuf 基准测试: Protobuf 序列化
func BenchmarkLeaseProtobuf(b *testing.B) {
	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: time.Now(),
		Keys:      map[string]bool{"key1": true, "key2": true, "key3": true},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := SerializeLease(lease)
		if err != nil {
			b.Fatalf("SerializeLease failed: %v", err)
		}

		_, err = DeserializeLease(data)
		if err != nil {
			b.Fatalf("DeserializeLease failed: %v", err)
		}
	}
}

// BenchmarkLeaseGOB 基准测试: GOB 序列化（对比）
func BenchmarkLeaseGOB(b *testing.B) {
	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: time.Now(),
		Keys:      map[string]bool{"key1": true, "key2": true, "key3": true},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// GOB 序列化
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
			b.Fatalf("GOB encode failed: %v", err)
		}
		data := buf.Bytes()

		// GOB 反序列化
		var decoded kvstore.Lease
		if err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(&decoded); err != nil {
			b.Fatalf("GOB decode failed: %v", err)
		}
	}
}

// BenchmarkLeaseManyKeysProtobuf 基准测试: 多 key Protobuf
func BenchmarkLeaseManyKeysProtobuf(b *testing.B) {
	keys := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		keys[string(rune('k'))+string(rune(i))] = true
	}

	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: time.Now(),
		Keys:      keys,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := SerializeLease(lease)
		DeserializeLease(data)
	}
}

// BenchmarkLeaseManyKeysGOB 基准测试: 多 key GOB
func BenchmarkLeaseManyKeysGOB(b *testing.B) {
	keys := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		keys[string(rune('k'))+string(rune(i))] = true
	}

	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: time.Now(),
		Keys:      keys,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		gob.NewEncoder(&buf).Encode(lease)
		data := buf.Bytes()

		var decoded kvstore.Lease
		gob.NewDecoder(bytes.NewBuffer(data)).Decode(&decoded)
	}
}

// isProtobufLease 检查是否为 Protobuf 格式
func isProtobufLease(data []byte) bool {
	const pbPrefix = "LEASE-PB:"
	return len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix
}
