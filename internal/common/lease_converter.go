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
	"fmt"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	raftpb "metaStore/internal/proto"
	"time"

	"google.golang.org/protobuf/proto"
)

// 功能开关：启用 Protobuf Lease 序列化优化
// TODO: 未来移到配置文件中 (configs/config.yaml)
func EnableLeaseProtobuf() bool { return config.GetEnableLeaseProtobuf() }

// SerializeLease 序列化 Lease
// 优先使用 Protobuf（2-4x 性能提升），回退到 GOB（向后兼容）
func SerializeLease(lease *kvstore.Lease) ([]byte, error) {
	if lease == nil {
		return nil, fmt.Errorf("lease is nil")
	}

	if EnableLeaseProtobuf() {
		// 使用 Protobuf 序列化
		pbLease := LeaseToProto(lease)

		data, err := proto.Marshal(pbLease)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal lease failed: %w", err)
		}

		// 添加 Protobuf 标记前缀（用于反序列化时识别）
		return append([]byte("LEASE-PB:"), data...), nil
	}

	// 回退到 GOB（向后兼容）
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		return nil, fmt.Errorf("gob encode lease failed: %w", err)
	}
	return buf.Bytes(), nil
}

// DeserializeLease 反序列化 Lease
// 自动检测 Protobuf 或 GOB 格式
func DeserializeLease(data []byte) (*kvstore.Lease, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty lease data")
	}

	// 检查是否为 Protobuf 格式（以 "LEASE-PB:" 前缀标识）
	const pbPrefix = "LEASE-PB:"
	if len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix {
		// Protobuf 格式
		pbLease := &raftpb.LeaseProto{}
		if err := proto.Unmarshal(data[len(pbPrefix):], pbLease); err != nil {
			return nil, fmt.Errorf("protobuf unmarshal lease failed: %w", err)
		}

		return ProtoToLease(pbLease), nil
	}

	// GOB 格式（向后兼容旧数据）
	var lease kvstore.Lease
	buf := bytes.NewBuffer(data)
	if err := gob.NewDecoder(buf).Decode(&lease); err != nil {
		return nil, fmt.Errorf("gob decode lease failed: %w", err)
	}

	return &lease, nil
}

// LeaseToProto 将 kvstore.Lease 转换为 Protobuf
func LeaseToProto(lease *kvstore.Lease) *raftpb.LeaseProto {
	if lease == nil {
		return nil
	}

	keys := make([]string, 0, len(lease.Keys))
	for k := range lease.Keys {
		keys = append(keys, k)
	}

	return &raftpb.LeaseProto{
		Id:                lease.ID,
		Ttl:               lease.TTL,
		GrantTimeUnixNano: lease.GrantTime.UnixNano(),
		Keys:              keys,
	}
}

// ProtoToLease 将 Protobuf 转换为 kvstore.Lease
func ProtoToLease(pbLease *raftpb.LeaseProto) *kvstore.Lease {
	if pbLease == nil {
		return nil
	}

	keys := make(map[string]bool, len(pbLease.Keys))
	for _, k := range pbLease.Keys {
		keys[k] = true
	}

	return &kvstore.Lease{
		ID:        pbLease.Id,
		TTL:       pbLease.Ttl,
		GrantTime: time.Unix(0, pbLease.GrantTimeUnixNano),
		Keys:      keys,
	}
}
