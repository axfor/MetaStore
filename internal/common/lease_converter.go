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

// 功能switch：enabled Protobuf Lease serializeoptimize
// TODO: 未来移toconfigfile中 (configs/config.yaml)
func EnableLeaseProtobuf() bool { return config.GetEnableLeaseProtobuf() }

// SerializeLease serialize Lease
// 优先use Protobuf（2-4x 性能提升），回退to GOB（向后compatible）
func SerializeLease(lease *kvstore.Lease) ([]byte, error) {
	if lease == nil {
		return nil, fmt.Errorf("lease is nil")
	}

	if EnableLeaseProtobuf() {
		// use Protobuf serialize
		pbLease := LeaseToProto(lease)

		data, err := proto.Marshal(pbLease)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal lease failed: %w", err)
		}

		// add Protobuf markerprefix（用atdeserialize时识别）
		return append([]byte("LEASE-PB:"), data...), nil
	}

	// 回退to GOB（向后compatible）
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		return nil, fmt.Errorf("gob encode lease failed: %w", err)
	}
	return buf.Bytes(), nil
}

// DeserializeLease deserialize Lease
// 自动检测 Protobuf or GOB format
func DeserializeLease(data []byte) (*kvstore.Lease, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty lease data")
	}

	// checkisnoas Protobuf format（以 "LEASE-PB:" prefix标识）
	const pbPrefix = "LEASE-PB:"
	if len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix {
		// Protobuf format
		pbLease := &raftpb.LeaseProto{}
		if err := proto.Unmarshal(data[len(pbPrefix):], pbLease); err != nil {
			return nil, fmt.Errorf("protobuf unmarshal lease failed: %w", err)
		}

		return ProtoToLease(pbLease), nil
	}

	// GOB format（向后compatibleolddata）
	var lease kvstore.Lease
	buf := bytes.NewBuffer(data)
	if err := gob.NewDecoder(buf).Decode(&lease); err != nil {
		return nil, fmt.Errorf("gob decode lease failed: %w", err)
	}

	return &lease, nil
}

// LeaseToProto will kvstore.Lease convertas Protobuf
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

// ProtoToLease will Protobuf convertas kvstore.Lease
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
