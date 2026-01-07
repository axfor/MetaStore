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

package reliability

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"sync/atomic"
)

var (
	// ValidationErrorCounter verifyincorrectcount器
	ValidationErrorCounter int64
)

// DataValidator dataverify器
type DataValidator struct {
	enableCRC bool
}

// NewDataValidator createdataverify器
func NewDataValidator(enableCRC bool) *DataValidator {
	return &DataValidator{
		enableCRC: enableCRC,
	}
}

// ValidateData verifydata完整性（use CRC32）
func (dv *DataValidator) ValidateData(data []byte, expectedCRC uint32) error {
	if !dv.enableCRC {
		return nil
	}

	actualCRC := crc32.ChecksumIEEE(data)
	if actualCRC != expectedCRC {
		atomic.AddInt64(&ValidationErrorCounter, 1)
		return fmt.Errorf("CRC mismatch: expected %x, got %x", expectedCRC, actualCRC)
	}

	return nil
}

// ComputeCRC calculatedata CRC32
func (dv *DataValidator) ComputeCRC(data []byte) uint32 {
	if !dv.enableCRC {
		return 0
	}
	return crc32.ChecksumIEEE(data)
}

// AppendCRC will CRC 附加todata末尾
func (dv *DataValidator) AppendCRC(data []byte) []byte {
	if !dv.enableCRC {
		return data
	}

	crc := crc32.ChecksumIEEE(data)
	result := make([]byte, len(data)+4)
	copy(result, data)
	binary.LittleEndian.PutUint32(result[len(data):], crc)

	return result
}

// ValidateAndStripCRC verify并移除data末尾 CRC
func (dv *DataValidator) ValidateAndStripCRC(data []byte) ([]byte, error) {
	if !dv.enableCRC {
		return data, nil
	}

	if len(data) < 4 {
		atomic.AddInt64(&ValidationErrorCounter, 1)
		return nil, fmt.Errorf("data too short for CRC validation")
	}

	// 提取 CRC
	dataLen := len(data) - 4
	expectedCRC := binary.LittleEndian.Uint32(data[dataLen:])

	// verify CRC
	actualCRC := crc32.ChecksumIEEE(data[:dataLen])
	if actualCRC != expectedCRC {
		atomic.AddInt64(&ValidationErrorCounter, 1)
		return nil, fmt.Errorf("CRC mismatch: expected %x, got %x", expectedCRC, actualCRC)
	}

	return data[:dataLen], nil
}

// ValidateKeyValue verifykey-value pair
func (dv *DataValidator) ValidateKeyValue(key, value []byte) error {
	// keynot能asempty
	if len(key) == 0 {
		return fmt.Errorf("key cannot be empty")
	}

	// keylengthlimit（etcd limitas 1.5 KB）
	if len(key) > 1536 {
		return fmt.Errorf("key too large: %d bytes (max 1536 bytes)", len(key))
	}

	// valuelengthlimit（etcd limitas 1 MB）
	if len(value) > 1024*1024 {
		return fmt.Errorf("value too large: %d bytes (max 1 MB)", len(value))
	}

	return nil
}

// ValidateRevision verifyversion号
func (dv *DataValidator) ValidateRevision(rev int64) error {
	if rev < 0 {
		return fmt.Errorf("revision cannot be negative: %d", rev)
	}
	return nil
}

// ValidateLease verifylease ID
func (dv *DataValidator) ValidateLease(leaseID int64) error {
	if leaseID < 0 {
		return fmt.Errorf("lease ID cannot be negative: %d", leaseID)
	}
	return nil
}

// GetValidationErrorCount getverifyincorrectcount
func GetValidationErrorCount() int64 {
	return atomic.LoadInt64(&ValidationErrorCounter)
}

// ResetValidationErrorCount resetverifyincorrectcount
func ResetValidationErrorCount() {
	atomic.StoreInt64(&ValidationErrorCounter, 0)
}

// SnapshotValidator snapshotverify器
type SnapshotValidator struct {
	validator *DataValidator
}

// NewSnapshotValidator createsnapshotverify器
func NewSnapshotValidator(enableCRC bool) *SnapshotValidator {
	return &SnapshotValidator{
		validator: NewDataValidator(enableCRC),
	}
}

// ValidateSnapshot verifysnapshot完整性
func (sv *SnapshotValidator) ValidateSnapshot(snapshot []byte) error {
	if len(snapshot) == 0 {
		return nil // emptysnapshotvalid
	}

	// verifysnapshotformatand CRC
	return sv.validator.ValidateData(snapshot[:len(snapshot)-4], binary.LittleEndian.Uint32(snapshot[len(snapshot)-4:]))
}

// CreateSnapshot create带 CRC snapshot
func (sv *SnapshotValidator) CreateSnapshot(data []byte) []byte {
	return sv.validator.AppendCRC(data)
}
