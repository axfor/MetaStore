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

// featureswitch：enabled Protobuf serializeoptimize
func enableProtobuf() bool { return config.GetEnableProtobuf() }

// serializeOperation serialize RaftOperation
// use Protobuf(3-5x performance)，to JSON(aftercompatible)
func serializeOperation(op RaftOperation) ([]byte, error) {
	if enableProtobuf() {
		// use Protobuf serialize
		pbOp := raftOperationToProto(op)
		data, err := proto.Marshal(pbOp)
		if err != nil {
			return nil, fmt.Errorf("protobuf marshal failed: %w", err)
		}
		// add Protobuf markerprefix(for deserializewhen)
		return append([]byte("PB:"), data...), nil
	}

	// to JSON(aftercompatible)
	return json.Marshal(op)
}

// deserializeOperation deserialize RaftOperation
// test Protobuf or JSON format
func deserializeOperation(data []byte) (RaftOperation, error) {
	// checkisnoas Protobuf format( "PB:" prefix)
	if len(data) > 3 && data[0] == 'P' && data[1] == 'B' && data[2] == ':' {
		// Protobuf format
		pbOp := &raftpb.RaftOperation{}
		if err := proto.Unmarshal(data[3:], pbOp); err != nil {
			return RaftOperation{}, fmt.Errorf("protobuf unmarshal failed: %w", err)
		}
		return protoToRaftOperation(pbOp), nil
	}

	// JSON format(aftercompatible)
	var op RaftOperation
	if err := json.Unmarshal(data, &op); err != nil {
		return RaftOperation{}, fmt.Errorf("json unmarshal failed: %w", err)
	}
	return op, nil
}

// raftOperationToProto will RaftOperation convertas Protobuf format
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

	// convert Compares
	if len(op.Compares) > 0 {
		pbOp.Compares = make([]*raftpb.Compare, len(op.Compares))
		for i, cmp := range op.Compares {
			pbOp.Compares[i] = compareToProto(cmp)
		}
	}

	// convert ThenOps
	if len(op.ThenOps) > 0 {
		pbOp.ThenOps = make([]*raftpb.Op, len(op.ThenOps))
		for i, txnOp := range op.ThenOps {
			pbOp.ThenOps[i] = opToProto(txnOp)
		}
	}

	// convert ElseOps
	if len(op.ElseOps) > 0 {
		pbOp.ElseOps = make([]*raftpb.Op, len(op.ElseOps))
		for i, txnOp := range op.ElseOps {
			pbOp.ElseOps[i] = opToProto(txnOp)
		}
	}

	return pbOp
}

// protoToRaftOperation will Protobuf formatconvertas RaftOperation
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

	// convert Compares
	if len(pbOp.Compares) > 0 {
		op.Compares = make([]kvstore.Compare, len(pbOp.Compares))
		for i, pbCmp := range pbOp.Compares {
			op.Compares[i] = protoToCompare(pbCmp)
		}
	}

	// convert ThenOps
	if len(pbOp.ThenOps) > 0 {
		op.ThenOps = make([]kvstore.Op, len(pbOp.ThenOps))
		for i, pbTxnOp := range pbOp.ThenOps {
			op.ThenOps[i] = protoToOp(pbTxnOp)
		}
	}

	// convert ElseOps
	if len(pbOp.ElseOps) > 0 {
		op.ElseOps = make([]kvstore.Op, len(pbOp.ElseOps))
		for i, pbTxnOp := range pbOp.ElseOps {
			op.ElseOps[i] = protoToOp(pbTxnOp)
		}
	}

	return op
}

// compareToProto will kvstore.Compare convertas Protobuf format
func compareToProto(cmp kvstore.Compare) *raftpb.Compare {
	pbCmp := &raftpb.Compare{
		Key:    string(cmp.Key),
		Result: raftpb.Compare_CompareResult(cmp.Result),
		Target: raftpb.Compare_CompareTarget(cmp.Target),
	}

	// convert TargetUnion(use oneof)
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

// protoToCompare will Protobuf formatconvertas kvstore.Compare
func protoToCompare(pbCmp *raftpb.Compare) kvstore.Compare {
	cmp := kvstore.Compare{
		Target: kvstore.CompareTarget(pbCmp.Target),
		Result: kvstore.CompareResult(pbCmp.Result),
		Key:    []byte(pbCmp.Key),
	}

	// convert TargetUnion(from oneof)
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

// opToProto will kvstore.Op convertas Protobuf format
func opToProto(op kvstore.Op) *raftpb.Op {
	return &raftpb.Op{
		Type:     raftpb.Op_OpType(op.Type),
		Key:      string(op.Key),
		Value:    op.Value,
		RangeEnd: string(op.RangeEnd),
		Lease:    op.LeaseID,
	}
}

// protoToOp will Protobuf formatconvertas kvstore.Op
func protoToOp(pbOp *raftpb.Op) kvstore.Op {
	return kvstore.Op{
		Type:     kvstore.OpType(pbOp.Type),
		Key:      []byte(pbOp.Key),
		RangeEnd: []byte(pbOp.RangeEnd),
		Value:    pbOp.Value,
		LeaseID:  pbOp.Lease,
	}
}
