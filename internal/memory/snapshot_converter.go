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

// 功能switch：enabled Protobuf snapshotserializeoptimize
// TODO: 未来移toconfigfile中 (configs/config.yaml)
func enableSnapshotProtobuf() bool { return config.GetEnableSnapshotProtobuf() }

// SnapshotData snapshotdatastructure（用at JSON 向后compatible）
type SnapshotData struct {
	Revision int64
	KVData   map[string]*kvstore.KeyValue
	Leases   map[int64]*kvstore.Lease
}

// serializeSnapshot serializesnapshot
// 优先use Protobuf（2-3x 性能提升），回退to JSON（向后compatible）
func serializeSnapshot(revision int64, kvData map[string]*kvstore.KeyValue, leases map[int64]*kvstore.Lease) ([]byte, error) {
	if enableSnapshotProtobuf() {
		// use Protobuf serialize
		pbSnapshot := &raftpb.StoreSnapshot{
			Revision: revision,
			KvData:   make(map[string]*raftpb.KeyValueProto),
			Leases:   make(map[int64]*raftpb.LeaseProto),
		}

		// convert KV data
		for k, v := range kvData {
			pbSnapshot.KvData[k] = keyValueToProto(v)
		}

		// convert Lease data
		for id, lease := range leases {
			pbSnapshot.Leases[id] = leaseToProto(lease)
		}

		// Marshal to Protobuf
		data, err := proto.Marshal(pbSnapshot)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal snapshot failed: %w", err)
		}

		// add Protobuf markerprefix（用atdeserialize时识别）
		return append([]byte("SNAP-PB:"), data...), nil
	}

	// 回退to JSON（向后compatible）
	snapshot := SnapshotData{
		Revision: revision,
		KVData:   kvData,
		Leases:   leases,
	}
	return json.Marshal(snapshot)
}

// deserializeSnapshot deserializesnapshot
// 自动检测 Protobuf or JSON format
func deserializeSnapshot(data []byte) (*SnapshotData, error) {
	// checkisnoas Protobuf format（以 "SNAP-PB:" prefix标识）
	const pbPrefix = "SNAP-PB:"
	if len(data) >= len(pbPrefix) && string(data[:len(pbPrefix)]) == pbPrefix {
		// Protobuf format（package括emptysnapshot情况）
		pbSnapshot := &raftpb.StoreSnapshot{}
		if err := proto.Unmarshal(data[len(pbPrefix):], pbSnapshot); err != nil {
			return nil, fmt.Errorf("protobuf unmarshal snapshot failed: %w", err)
		}

		// convert回 Go structure
		snapshot := &SnapshotData{
			Revision: pbSnapshot.Revision,
			KVData:   make(map[string]*kvstore.KeyValue),
			Leases:   make(map[int64]*kvstore.Lease),
		}

		// convert KV data
		for k, v := range pbSnapshot.KvData {
			snapshot.KVData[k] = protoToKeyValue(v)
		}

		// convert Lease data
		for id, lease := range pbSnapshot.Leases {
			snapshot.Leases[id] = protoToLease(lease)
		}

		return snapshot, nil
	}

	// JSON format（向后compatible）
	var snapshot SnapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("json unmarshal snapshot failed: %w", err)
	}

	return &snapshot, nil
}

// keyValueToProto will kvstore.KeyValue convertas Protobuf
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

// protoToKeyValue will Protobuf convertas kvstore.KeyValue
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

// leaseToProto will kvstore.Lease convertas Protobuf
// 复用 common packageimplement
func leaseToProto(lease *kvstore.Lease) *raftpb.LeaseProto {
	return common.LeaseToProto(lease)
}

// protoToLease will Protobuf convertas kvstore.Lease
// 复用 common packageimplement
func protoToLease(pbLease *raftpb.LeaseProto) *kvstore.Lease {
	return common.ProtoToLease(pbLease)
}
