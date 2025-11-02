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
	"metaStore/internal/kvstore"
	"metaStore/internal/proto"
	"metaStore/pkg/config"

	"google.golang.org/protobuf/proto"
)

// 功能开关：启用 Protobuf 序列化优化
func enableProtobuf() bool { return config.GetEnableProtobuf() }

// serializeOperation 序列化 RaftOperation
// 优先使用 Protobuf（3-5x 性能提升），回退到 JSON（向后兼容）
func serializeOperation(op RaftOperation) ([]byte, error) {
	if enableProtobuf() {
		// 使用 Protobuf 序列化
		pbOp := raftOperationToProto(op)
		data, err := proto.Marshal(pbOp)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal failed: %w", err)
		}
		// 添加 Protobuf 标记前缀（用于反序列化时识别）
		return append([]byte("PB:"), data...), nil
	}

	// 回退到 JSON（向后兼容）
	return json.Marshal(op)
}

// deserializeOperation 反序列化 RaftOperation
// 自动检测 Protobuf 或 JSON 格式
func deserializeOperation(data []byte) (RaftOperation, error) {
	// 检查是否为 Protobuf 格式（以 "PB:" 前缀标识）
	if len(data) > 3 && data[0] == 'P' && data[1] == 'B' && data[2] == ':' {
		// Protobuf 格式
		pbOp := &raftpb.RaftOperation{}
		if err := proto.Unmarshal(data[3:], pbOp); err != nil {
			return RaftOperation{}, fmt.Errorf("protobuf unmarshal failed: %w", err)
		}
		return protoToRaftOperation(pbOp), nil
	}

	// JSON 格式（向后兼容）
	var op RaftOperation
	if err := json.Unmarshal(data, &op); err != nil {
		return RaftOperation{}, fmt.Errorf("json unmarshal failed: %w", err)
	}
	return op, nil
}

// raftOperationToProto 将 RaftOperation 转换为 Protobuf 格式
func raftOperationToProto(op RaftOperation) *raftpb.RaftOperation {
	pbOp := &raftpb.RaftOperation{
		Type:     op.Type,
		Key:      op.Key,
		Value:    op.Value,
		RangeEnd: op.RangeEnd,
		LeaseId:  op.LeaseID,
		Ttl:      op.TTL,
		SeqNum:   op.SeqNum,
	}

	// 转换 Compares
	if len(op.Compares) > 0 {
		pbOp.Compares = make([]*raftpb.Compare, len(op.Compares))
		for i, cmp := range op.Compares {
			pbOp.Compares[i] = compareToProto(cmp)
		}
	}

	// 转换 ThenOps
	if len(op.ThenOps) > 0 {
		pbOp.ThenOps = make([]*raftpb.Op, len(op.ThenOps))
		for i, txnOp := range op.ThenOps {
			pbOp.ThenOps[i] = opToProto(txnOp)
		}
	}

	// 转换 ElseOps
	if len(op.ElseOps) > 0 {
		pbOp.ElseOps = make([]*raftpb.Op, len(op.ElseOps))
		for i, txnOp := range op.ElseOps {
			pbOp.ElseOps[i] = opToProto(txnOp)
		}
	}

	return pbOp
}

// protoToRaftOperation 将 Protobuf 格式转换为 RaftOperation
func protoToRaftOperation(pbOp *raftpb.RaftOperation) RaftOperation {
	op := RaftOperation{
		Type:     pbOp.Type,
		Key:      pbOp.Key,
		Value:    pbOp.Value,
		RangeEnd: pbOp.RangeEnd,
		LeaseID:  pbOp.LeaseId,
		TTL:      pbOp.Ttl,
		SeqNum:   pbOp.SeqNum,
	}

	// 转换 Compares
	if len(pbOp.Compares) > 0 {
		op.Compares = make([]kvstore.Compare, len(pbOp.Compares))
		for i, pbCmp := range pbOp.Compares {
			op.Compares[i] = protoToCompare(pbCmp)
		}
	}

	// 转换 ThenOps
	if len(pbOp.ThenOps) > 0 {
		op.ThenOps = make([]kvstore.Op, len(pbOp.ThenOps))
		for i, pbTxnOp := range pbOp.ThenOps {
			op.ThenOps[i] = protoToOp(pbTxnOp)
		}
	}

	// 转换 ElseOps
	if len(pbOp.ElseOps) > 0 {
		op.ElseOps = make([]kvstore.Op, len(pbOp.ElseOps))
		for i, pbTxnOp := range pbOp.ElseOps {
			op.ElseOps[i] = protoToOp(pbTxnOp)
		}
	}

	return op
}

// compareToProto 将 kvstore.Compare 转换为 Protobuf 格式
func compareToProto(cmp kvstore.Compare) *raftpb.Compare {
	pbCmp := &raftpb.Compare{
		Key:    string(cmp.Key),
		Result: raftpb.Compare_CompareResult(cmp.Result),
		Target: raftpb.Compare_CompareTarget(cmp.Target),
	}

	// 转换 TargetUnion（使用 oneof）
	switch cmp.Target {
	case kvstore.CompareVersion:
		pbCmp.TargetUnion = &raftpb.Compare_Version{Version: cmp.TargetUnion.Version}
	case kvstore.CompareCreate:
		pbCmp.TargetUnion = &raftpb.Compare_CreateRevision{CreateRevision: cmp.TargetUnion.CreateRevision}
	case kvstore.CompareMod:
		pbCmp.TargetUnion = &raftpb.Compare_ModRevision{ModRevision: cmp.TargetUnion.ModRevision}
	case kvstore.CompareValue:
		pbCmp.TargetUnion = &raftpb.Compare_Value{Value: cmp.TargetUnion.Value}
	case kvstore.CompareLease:
		pbCmp.TargetUnion = &raftpb.Compare_Lease{Lease: cmp.TargetUnion.Lease}
	}

	return pbCmp
}

// protoToCompare 将 Protobuf 格式转换为 kvstore.Compare
func protoToCompare(pbCmp *raftpb.Compare) kvstore.Compare {
	cmp := kvstore.Compare{
		Target: kvstore.CompareTarget(pbCmp.Target),
		Result: kvstore.CompareResult(pbCmp.Result),
		Key:    []byte(pbCmp.Key),
	}

	// 转换 TargetUnion（从 oneof）
	switch v := pbCmp.TargetUnion.(type) {
	case *raftpb.Compare_Version:
		cmp.TargetUnion.Version = v.Version
	case *raftpb.Compare_CreateRevision:
		cmp.TargetUnion.CreateRevision = v.CreateRevision
	case *raftpb.Compare_ModRevision:
		cmp.TargetUnion.ModRevision = v.ModRevision
	case *raftpb.Compare_Value:
		cmp.TargetUnion.Value = v.Value
	case *raftpb.Compare_Lease:
		cmp.TargetUnion.Lease = v.Lease
	}

	return cmp
}

// opToProto 将 kvstore.Op 转换为 Protobuf 格式
func opToProto(op kvstore.Op) *raftpb.Op {
	return &raftpb.Op{
		Type:     raftpb.Op_OpType(op.Type),
		Key:      string(op.Key),
		Value:    op.Value,
		RangeEnd: string(op.RangeEnd),
		Lease:    op.LeaseID,
	}
}

// protoToOp 将 Protobuf 格式转换为 kvstore.Op
func protoToOp(pbOp *raftpb.Op) kvstore.Op {
	return kvstore.Op{
		Type:     kvstore.OpType(pbOp.Type),
		Key:      []byte(pbOp.Key),
		RangeEnd: []byte(pbOp.RangeEnd),
		Value:    pbOp.Value,
		LeaseID:  pbOp.Lease,
	}
}
