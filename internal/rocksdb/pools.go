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

package rocksdb

import (
	"bytes"
	"encoding/binary"
	"sync"

	"metaStore/internal/kvstore"
)

// Object pools for performance optimization
var (
	// bufferPool reuses byte buffers for encoding/decoding
	bufferPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	// kvSlicePool reuses KeyValue slices for range queries
	kvSlicePool = sync.Pool{
		New: func() interface{} {
			// Pre-allocate with reasonable capacity
			slice := make([]*kvstore.KeyValue, 0, 100)
			return &slice
		},
	}
)

// getBuffer gets a buffer from the pool
func getBuffer() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// putBuffer returns a buffer to the pool
func putBuffer(buf *bytes.Buffer) {
	if buf.Cap() > 64*1024 { // Don't pool very large buffers
		return
	}
	bufferPool.Put(buf)
}

// getKVSlice gets a KeyValue slice from the pool
func getKVSlice() *[]*kvstore.KeyValue {
	slice := kvSlicePool.Get().(*[]*kvstore.KeyValue)
	*slice = (*slice)[:0] // Reset length but keep capacity
	return slice
}

// putKVSlice returns a KeyValue slice to the pool
func putKVSlice(slice *[]*kvstore.KeyValue) {
	if cap(*slice) > 1000 { // Don't pool very large slices
		return
	}
	// Clear references to avoid memory leaks
	for i := range *slice {
		(*slice)[i] = nil
	}
	kvSlicePool.Put(slice)
}

// Binary encoding for KeyValue (faster than gob)
// Format: [keyLen(4)][key][valueLen(4)][value][createRev(8)][modRev(8)][version(8)][lease(8)]

// encodeKeyValue encodes a KeyValue to binary format
func encodeKeyValue(kv *kvstore.KeyValue) ([]byte, error) {
	// Calculate total size
	size := 4 + len(kv.Key) + 4 + len(kv.Value) + 8*4

	buf := getBuffer()
	defer putBuffer(buf)
	buf.Grow(size)

	// Write key length and key
	binary.Write(buf, binary.LittleEndian, uint32(len(kv.Key)))
	buf.Write(kv.Key)

	// Write value length and value
	binary.Write(buf, binary.LittleEndian, uint32(len(kv.Value)))
	buf.Write(kv.Value)

	// Write fixed-size fields
	binary.Write(buf, binary.LittleEndian, kv.CreateRevision)
	binary.Write(buf, binary.LittleEndian, kv.ModRevision)
	binary.Write(buf, binary.LittleEndian, kv.Version)
	binary.Write(buf, binary.LittleEndian, kv.Lease)

	// Return a copy since we're reusing the buffer
	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

// decodeKeyValue decodes a KeyValue from binary format
func decodeKeyValue(data []byte) (*kvstore.KeyValue, error) {
	if len(data) < 8 { // Minimum size check
		return nil, nil
	}

	kv := &kvstore.KeyValue{}
	offset := 0

	// Read key length and key
	keyLen := binary.LittleEndian.Uint32(data[offset:])
	offset += 4
	kv.Key = make([]byte, keyLen)
	copy(kv.Key, data[offset:offset+int(keyLen)])
	offset += int(keyLen)

	// Read value length and value
	valueLen := binary.LittleEndian.Uint32(data[offset:])
	offset += 4
	kv.Value = make([]byte, valueLen)
	copy(kv.Value, data[offset:offset+int(valueLen)])
	offset += int(valueLen)

	// Read fixed-size fields
	kv.CreateRevision = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	kv.ModRevision = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	kv.Version = int64(binary.LittleEndian.Uint64(data[offset:]))
	offset += 8
	kv.Lease = int64(binary.LittleEndian.Uint64(data[offset:]))

	return kv, nil
}
