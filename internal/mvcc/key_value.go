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
	"encoding/binary"
)

// KeyValue represents a versioned key-value pair with MVCC metadata.
// This is compatible with etcd's KeyValue structure.
type KeyValue struct {
	// Key is the key in bytes. An empty key is not allowed.
	Key []byte

	// Value is the value held by the key, in bytes.
	Value []byte

	// CreateRevision is the revision of the last creation on this key.
	CreateRevision int64

	// ModRevision is the revision of the last modification on this key.
	ModRevision int64

	// Version is the version of the key. A deletion resets the version to zero.
	// Each modification increments the version.
	Version int64

	// Lease is the ID of the lease attached to this key.
	// 0 means no lease is attached.
	Lease int64
}

// Clone creates a deep copy of the KeyValue.
func (kv *KeyValue) Clone() *KeyValue {
	if kv == nil {
		return nil
	}
	clone := &KeyValue{
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		Lease:          kv.Lease,
	}
	if kv.Key != nil {
		clone.Key = make([]byte, len(kv.Key))
		copy(clone.Key, kv.Key)
	}
	if kv.Value != nil {
		clone.Value = make([]byte, len(kv.Value))
		copy(clone.Value, kv.Value)
	}
	return clone
}

// Size returns the approximate size of the KeyValue in bytes.
func (kv *KeyValue) Size() int {
	// Key + Value + 4 int64 fields (8 bytes each)
	return len(kv.Key) + len(kv.Value) + 32
}

// IsTombstone returns true if this is a tombstone (deleted key).
// A tombstone has a nil or empty value and Version == 0.
type Tombstone struct {
	Key         []byte
	ModRevision int64
}

// Event represents a change event in the MVCC store.
type Event struct {
	// Type is the kind of event. If Type is PUT, the key-value pair is created or updated.
	// If Type is DELETE, the key-value pair is deleted.
	Type EventType

	// Kv holds the KeyValue for the event.
	// A PUT event contains current kv pair.
	// A DELETE event contains the deleted kv pair with Version set to 0.
	Kv *KeyValue

	// PrevKv holds the previous key-value pair before the event.
	// Only filled if explicitly requested.
	PrevKv *KeyValue
}

// EventType is the type of an event.
type EventType int32

const (
	// EventTypePut indicates a key is created or updated.
	EventTypePut EventType = iota

	// EventTypeDelete indicates a key is deleted.
	EventTypeDelete
)

// String returns the string representation of the event type.
func (t EventType) String() string {
	switch t {
	case EventTypePut:
		return "PUT"
	case EventTypeDelete:
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// KeyValueCodec provides encoding/decoding for KeyValue.
type KeyValueCodec struct{}

// Encode serializes a KeyValue to bytes.
// Format: [keyLen:4][valueLen:4][createRev:8][modRev:8][version:8][lease:8][key:keyLen][value:valueLen]
func (c *KeyValueCodec) Encode(kv *KeyValue) []byte {
	keyLen := len(kv.Key)
	valueLen := len(kv.Value)
	size := 4 + 4 + 8 + 8 + 8 + 8 + keyLen + valueLen
	buf := make([]byte, size)

	offset := 0
	binary.BigEndian.PutUint32(buf[offset:], uint32(keyLen))
	offset += 4
	binary.BigEndian.PutUint32(buf[offset:], uint32(valueLen))
	offset += 4
	binary.BigEndian.PutUint64(buf[offset:], uint64(kv.CreateRevision))
	offset += 8
	binary.BigEndian.PutUint64(buf[offset:], uint64(kv.ModRevision))
	offset += 8
	binary.BigEndian.PutUint64(buf[offset:], uint64(kv.Version))
	offset += 8
	binary.BigEndian.PutUint64(buf[offset:], uint64(kv.Lease))
	offset += 8
	copy(buf[offset:], kv.Key)
	offset += keyLen
	copy(buf[offset:], kv.Value)

	return buf
}

// Decode deserializes bytes to a KeyValue.
func (c *KeyValueCodec) Decode(data []byte) (*KeyValue, error) {
	if len(data) < 40 { // minimum size: 4+4+8+8+8+8 = 40
		return nil, ErrInvalidData
	}

	offset := 0
	keyLen := int(binary.BigEndian.Uint32(data[offset:]))
	offset += 4
	valueLen := int(binary.BigEndian.Uint32(data[offset:]))
	offset += 4

	if len(data) < 40+keyLen+valueLen {
		return nil, ErrInvalidData
	}

	kv := &KeyValue{
		CreateRevision: int64(binary.BigEndian.Uint64(data[offset:])),
	}
	offset += 8
	kv.ModRevision = int64(binary.BigEndian.Uint64(data[offset:]))
	offset += 8
	kv.Version = int64(binary.BigEndian.Uint64(data[offset:]))
	offset += 8
	kv.Lease = int64(binary.BigEndian.Uint64(data[offset:]))
	offset += 8

	if keyLen > 0 {
		kv.Key = make([]byte, keyLen)
		copy(kv.Key, data[offset:offset+keyLen])
	}
	offset += keyLen

	if valueLen > 0 {
		kv.Value = make([]byte, valueLen)
		copy(kv.Value, data[offset:offset+valueLen])
	}

	return kv, nil
}

// DefaultCodec is the default KeyValue codec.
var DefaultCodec = &KeyValueCodec{}
