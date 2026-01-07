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

// TestLeaseProtobufSerialization test Protobuf Lease serialize
func TestLeaseProtobufSerialization(t *testing.T) {
	// 准备testdata
	now := time.Now()
	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: now,
		Keys:      map[string]bool{"key1": true, "key2": true, "key3": true},
	}

	// serialize
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// verifyuse Protobuf format
	if !isProtobufLease(data) {
		t.Error("Expected Protobuf format, got GOB")
	}

	// deserialize
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// verifydatacorrect性
	if decoded.ID != lease.ID {
		t.Errorf("Expected ID %d, got %d", lease.ID, decoded.ID)
	}
	if decoded.TTL != lease.TTL {
		t.Errorf("Expected TTL %d, got %d", lease.TTL, decoded.TTL)
	}

	// verify GrantTime（纳秒precision）
	if decoded.GrantTime.UnixNano() != lease.GrantTime.UnixNano() {
		t.Errorf("Expected GrantTime %v, got %v", lease.GrantTime, decoded.GrantTime)
	}

	// verify Keys
	if len(decoded.Keys) != len(lease.Keys) {
		t.Errorf("Expected %d keys, got %d", len(lease.Keys), len(decoded.Keys))
	}
	for key := range lease.Keys {
		if !decoded.Keys[key] {
			t.Errorf("Missing key %s", key)
		}
	}
}

// TestLeaseGOBBackwardCompatibility test GOB 向后compatible性
func TestLeaseGOBBackwardCompatibility(t *testing.T) {
	// 准备testdata（useold GOB format）
	now := time.Now()
	lease := &kvstore.Lease{
		ID:        456,
		TTL:       120,
		GrantTime: now,
		Keys:      map[string]bool{"oldkey": true},
	}

	// use GOB serialize（模拟olddata）
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		t.Fatalf("GOB encode failed: %v", err)
	}
	gobData := buf.Bytes()

	// usenewdeserializefunction（should能handle GOB）
	decoded, err := DeserializeLease(gobData)
	if err != nil {
		t.Fatalf("DeserializeLease failed for GOB: %v", err)
	}

	// verifydatacorrect性
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

// TestLeaseEmptyKeys test无关联 key  Lease
func TestLeaseEmptyKeys(t *testing.T) {
	lease := &kvstore.Lease{
		ID:        789,
		TTL:       30,
		GrantTime: time.Now(),
		Keys:      map[string]bool{}, // empty Keys
	}

	// serialize
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// deserialize
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// verify
	if decoded.ID != lease.ID {
		t.Errorf("Expected ID %d, got %d", lease.ID, decoded.ID)
	}
	if len(decoded.Keys) != 0 {
		t.Errorf("Expected empty Keys, got %d keys", len(decoded.Keys))
	}
}

// TestLeaseNilLease test nil Lease handle
func TestLeaseNilLease(t *testing.T) {
	_, err := SerializeLease(nil)
	if err == nil {
		t.Error("Expected error when serializing nil lease")
	}
}

// TestLeaseManyKeys testlarge number of key  Lease
func TestLeaseManyKeys(t *testing.T) {
	// createpackage含 1000 个 key  Lease
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

	// serialize
	data, err := SerializeLease(lease)
	if err != nil {
		t.Fatalf("SerializeLease failed: %v", err)
	}

	// deserialize
	decoded, err := DeserializeLease(data)
	if err != nil {
		t.Fatalf("DeserializeLease failed: %v", err)
	}

	// verify
	if len(decoded.Keys) != 1000 {
		t.Errorf("Expected 1000 keys, got %d", len(decoded.Keys))
	}
}

// BenchmarkLeaseProtobuf 基准test: Protobuf serialize
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

// BenchmarkLeaseGOB 基准test: GOB serialize（对比）
func BenchmarkLeaseGOB(b *testing.B) {
	lease := &kvstore.Lease{
		ID:        123,
		TTL:       60,
		GrantTime: time.Now(),
		Keys:      map[string]bool{"key1": true, "key2": true, "key3": true},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// GOB serialize
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
			b.Fatalf("GOB encode failed: %v", err)
		}
		data := buf.Bytes()

		// GOB deserialize
		var decoded kvstore.Lease
		if err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(&decoded); err != nil {
			b.Fatalf("GOB decode failed: %v", err)
		}
	}
}

// BenchmarkLeaseManyKeysProtobuf 基准test: 多 key Protobuf
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

// BenchmarkLeaseManyKeysGOB 基准test: 多 key GOB
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

// isProtobufLease checkisnoas Protobuf format
func isProtobufLease(data []byte) bool {
	const pbPrefix = "LEASE-PB:"
	return len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix
}
