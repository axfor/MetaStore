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
	"fmt"
	"metaStore/internal/common"
	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	raftpb "metaStore/internal/proto"

	"google.golang.org/protobuf/proto"
)

// 功能开关：启用 Protobuf 快照序列化优化
// TODO: 未来移到配置文件中 (configs/config.yaml)
func enableSnapshotProtobuf() bool { return config.GetEnableSnapshotProtobuf() }

// SnapshotData 快照数据结构（用于 JSON 向后兼容）
type SnapshotData struct {
	Revision int64
	KVData   map[string]*kvstore.KeyValue
	Leases   map[int64]*kvstore.Lease
}

// serializeSnapshot 序列化快照
// 优先使用 Protobuf（2-3x 性能提升），回退到 JSON（向后兼容）
func serializeSnapshot(revision int64, kvData map[string]*kvstore.KeyValue, leases map[int64]*kvstore.Lease) ([]byte, error) {
	if enableSnapshotProtobuf() {
		// 使用 Protobuf 序列化
		pbSnapshot := &raftpb.StoreSnapshot{
			Revision: revision,
			KvData:   make(map[string]*raftpb.KeyValueProto),
			Leases:   make(map[int64]*raftpb.LeaseProto),
		}

		// 转换 KV 数据
		for k, v := range kvData {
			pbSnapshot.KvData[k] = keyValueToProto(v)
		}

		// 转换 Lease 数据
		for id, lease := range leases {
			pbSnapshot.Leases[id] = leaseToProto(lease)
		}

		// Marshal to Protobuf
		data, err := proto.Marshal(pbSnapshot)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal snapshot failed: %w", err)
		}

		// 添加 Protobuf 标记前缀（用于反序列化时识别）
		return append([]byte("SNAP-PB:"), data...), nil
	}

	// 回退到 JSON（向后兼容）
	snapshot := SnapshotData{
		Revision: revision,
		KVData:   kvData,
		Leases:   leases,
	}
	return json.Marshal(snapshot)
}

// deserializeSnapshot 反序列化快照
// 自动检测 Protobuf 或 JSON 格式
func deserializeSnapshot(data []byte) (*SnapshotData, error) {
	// 检查是否为 Protobuf 格式（以 "SNAP-PB:" 前缀标识）
	const pbPrefix = "SNAP-PB:"
	if len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix {
		// Protobuf 格式（包括空快照的情况）
		pbSnapshot := &raftpb.StoreSnapshot{}
		if err := proto.Unmarshal(data[len(pbPrefix):], pbSnapshot); err != nil {
			return nil, fmt.Errorf("protobuf unmarshal snapshot failed: %w", err)
		}

		// 转换回 Go 结构
		snapshot := &SnapshotData{
			Revision: pbSnapshot.Revision,
			KVData:   make(map[string]*kvstore.KeyValue),
			Leases:   make(map[int64]*kvstore.Lease),
		}

		// 转换 KV 数据
		for k, v := range pbSnapshot.KvData {
			snapshot.KVData[k] = protoToKeyValue(v)
		}

		// 转换 Lease 数据
		for id, lease := range pbSnapshot.Leases {
			snapshot.Leases[id] = protoToLease(lease)
		}

		return snapshot, nil
	}

	// JSON 格式（向后兼容）
	var snapshot SnapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("json unmarshal snapshot failed: %w", err)
	}

	return &snapshot, nil
}

// keyValueToProto 将 kvstore.KeyValue 转换为 Protobuf
func keyValueToProto(kv *kvstore.KeyValue) *raftpb.KeyValueProto {
	if kv == nil {
		return nil
	}
	return &raftpb.KeyValueProto{
		Key:            kv.Key,
		Value:          kv.Value,
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		Lease:          kv.Lease,
	}
}

// protoToKeyValue 将 Protobuf 转换为 kvstore.KeyValue
func protoToKeyValue(pbKv *raftpb.KeyValueProto) *kvstore.KeyValue {
	if pbKv == nil {
		return nil
	}
	return &kvstore.KeyValue{
		Key:            pbKv.Key,
		Value:          pbKv.Value,
		CreateRevision: pbKv.CreateRevision,
		ModRevision:    pbKv.ModRevision,
		Version:        pbKv.Version,
		Lease:          pbKv.Lease,
	}
}

// leaseToProto 将 kvstore.Lease 转换为 Protobuf
// 复用 common 包的实现
func leaseToProto(lease *kvstore.Lease) *raftpb.LeaseProto {
	return common.LeaseToProto(lease)
}

// protoToLease 将 Protobuf 转换为 kvstore.Lease
// 复用 common 包的实现
func protoToLease(pbLease *raftpb.LeaseProto) *kvstore.Lease {
	return common.ProtoToLease(pbLease)
}
