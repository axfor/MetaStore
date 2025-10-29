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
	// ValidationErrorCounter 验证错误计数器
	ValidationErrorCounter int64
)

// DataValidator 数据验证器
type DataValidator struct {
	enableCRC bool
}

// NewDataValidator 创建数据验证器
func NewDataValidator(enableCRC bool) *DataValidator {
	return &DataValidator{
		enableCRC: enableCRC,
	}
}

// ValidateData 验证数据完整性（使用 CRC32）
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

// ComputeCRC 计算数据的 CRC32
func (dv *DataValidator) ComputeCRC(data []byte) uint32 {
	if !dv.enableCRC {
		return 0
	}
	return crc32.ChecksumIEEE(data)
}

// AppendCRC 将 CRC 附加到数据末尾
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

// ValidateAndStripCRC 验证并移除数据末尾的 CRC
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

	// 验证 CRC
	actualCRC := crc32.ChecksumIEEE(data[:dataLen])
	if actualCRC != expectedCRC {
		atomic.AddInt64(&ValidationErrorCounter, 1)
		return nil, fmt.Errorf("CRC mismatch: expected %x, got %x", expectedCRC, actualCRC)
	}

	return data[:dataLen], nil
}

// ValidateKeyValue 验证键值对
func (dv *DataValidator) ValidateKeyValue(key, value []byte) error {
	// 键不能为空
	if len(key) == 0 {
		return fmt.Errorf("key cannot be empty")
	}

	// 键长度限制（etcd 限制为 1.5 KB）
	if len(key) > 1536 {
		return fmt.Errorf("key too large: %d bytes (max 1536 bytes)", len(key))
	}

	// 值长度限制（etcd 限制为 1 MB）
	if len(value) > 1024*1024 {
		return fmt.Errorf("value too large: %d bytes (max 1 MB)", len(value))
	}

	return nil
}

// ValidateRevision 验证版本号
func (dv *DataValidator) ValidateRevision(rev int64) error {
	if rev < 0 {
		return fmt.Errorf("revision cannot be negative: %d", rev)
	}
	return nil
}

// ValidateLease 验证租约 ID
func (dv *DataValidator) ValidateLease(leaseID int64) error {
	if leaseID < 0 {
		return fmt.Errorf("lease ID cannot be negative: %d", leaseID)
	}
	return nil
}

// GetValidationErrorCount 获取验证错误计数
func GetValidationErrorCount() int64 {
	return atomic.LoadInt64(&ValidationErrorCounter)
}

// ResetValidationErrorCount 重置验证错误计数
func ResetValidationErrorCount() {
	atomic.StoreInt64(&ValidationErrorCounter, 0)
}

// SnapshotValidator 快照验证器
type SnapshotValidator struct {
	validator *DataValidator
}

// NewSnapshotValidator 创建快照验证器
func NewSnapshotValidator(enableCRC bool) *SnapshotValidator {
	return &SnapshotValidator{
		validator: NewDataValidator(enableCRC),
	}
}

// ValidateSnapshot 验证快照完整性
func (sv *SnapshotValidator) ValidateSnapshot(snapshot []byte) error {
	if len(snapshot) == 0 {
		return nil // 空快照有效
	}

	// 验证快照格式和 CRC
	return sv.validator.ValidateData(snapshot[:len(snapshot)-4], binary.LittleEndian.Uint32(snapshot[len(snapshot)-4:]))
}

// CreateSnapshot 创建带 CRC 的快照
func (sv *SnapshotValidator) CreateSnapshot(data []byte) []byte {
	return sv.validator.AppendCRC(data)
}
