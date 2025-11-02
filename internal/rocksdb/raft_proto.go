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
	"metaStore/internal/kvstore"
	pb "metaStore/internal/proto"
	"google.golang.org/protobuf/proto"
)

// toProto converts RaftOperation to protobuf format
func toProto(op *RaftOperation) *pb.RaftOperation {
	pbOp := &pb.RaftOperation{
		Type:     op.Type,
		Key:      op.Key,
		Value:    op.Value,
		LeaseId:  op.LeaseID,
		RangeEnd: op.RangeEnd,
		SeqNum:   op.SeqNum,
		Ttl:      op.TTL,
	}

	// Convert Compares
	if len(op.Compares) > 0 {
		pbOp.Compares = make([]*pb.Compare, len(op.Compares))
		for i, cmp := range op.Compares {
			pbOp.Compares[i] = compareToProto(&cmp)
		}
	}

	// Convert ThenOps
	if len(op.ThenOps) > 0 {
		pbOp.ThenOps = make([]*pb.Op, len(op.ThenOps))
		for i, thenOp := range op.ThenOps {
			pbOp.ThenOps[i] = opToProto(&thenOp)
		}
	}

	// Convert ElseOps
	if len(op.ElseOps) > 0 {
		pbOp.ElseOps = make([]*pb.Op, len(op.ElseOps))
		for i, elseOp := range op.ElseOps {
			pbOp.ElseOps[i] = opToProto(&elseOp)
		}
	}

	return pbOp
}

// fromProto converts protobuf format to RaftOperation
func fromProto(pbOp *pb.RaftOperation) *RaftOperation {
	op := &RaftOperation{
		Type:     pbOp.Type,
		Key:      pbOp.Key,
		Value:    pbOp.Value,
		LeaseID:  pbOp.LeaseId,
		RangeEnd: pbOp.RangeEnd,
		SeqNum:   pbOp.SeqNum,
		TTL:      pbOp.Ttl,
	}

	// Convert Compares
	if len(pbOp.Compares) > 0 {
		op.Compares = make([]kvstore.Compare, len(pbOp.Compares))
		for i, pbCmp := range pbOp.Compares {
			op.Compares[i] = compareFromProto(pbCmp)
		}
	}

	// Convert ThenOps
	if len(pbOp.ThenOps) > 0 {
		op.ThenOps = make([]kvstore.Op, len(pbOp.ThenOps))
		for i, pbThenOp := range pbOp.ThenOps {
			op.ThenOps[i] = opFromProto(pbThenOp)
		}
	}

	// Convert ElseOps
	if len(pbOp.ElseOps) > 0 {
		op.ElseOps = make([]kvstore.Op, len(pbOp.ElseOps))
		for i, pbElseOp := range pbOp.ElseOps {
			op.ElseOps[i] = opFromProto(pbElseOp)
		}
	}

	return op
}

// compareToProto converts kvstore.Compare to protobuf format
func compareToProto(cmp *kvstore.Compare) *pb.Compare {
	pbCmp := &pb.Compare{
		Key:    string(cmp.Key),
		Result: pb.Compare_CompareResult(cmp.Result),
		Target: pb.Compare_CompareTarget(cmp.Target),
	}

	// Set target value based on target type
	switch cmp.Target {
	case kvstore.CompareVersion:
		pbCmp.TargetUnion = &pb.Compare_Version{Version: cmp.TargetUnion.Version}
	case kvstore.CompareCreate:
		pbCmp.TargetUnion = &pb.Compare_CreateRevision{CreateRevision: cmp.TargetUnion.CreateRevision}
	case kvstore.CompareMod:
		pbCmp.TargetUnion = &pb.Compare_ModRevision{ModRevision: cmp.TargetUnion.ModRevision}
	case kvstore.CompareValue:
		pbCmp.TargetUnion = &pb.Compare_Value{Value: cmp.TargetUnion.Value}
	case kvstore.CompareLease:
		pbCmp.TargetUnion = &pb.Compare_Lease{Lease: cmp.TargetUnion.Lease}
	}

	return pbCmp
}

// compareFromProto converts protobuf format to kvstore.Compare
func compareFromProto(pbCmp *pb.Compare) kvstore.Compare {
	cmp := kvstore.Compare{
		Key:    []byte(pbCmp.Key),
		Result: kvstore.CompareResult(pbCmp.Result),
		Target: kvstore.CompareTarget(pbCmp.Target),
	}

	// Extract target value from union type
	switch v := pbCmp.TargetUnion.(type) {
	case *pb.Compare_Version:
		cmp.TargetUnion.Version = v.Version
	case *pb.Compare_CreateRevision:
		cmp.TargetUnion.CreateRevision = v.CreateRevision
	case *pb.Compare_ModRevision:
		cmp.TargetUnion.ModRevision = v.ModRevision
	case *pb.Compare_Value:
		cmp.TargetUnion.Value = v.Value
	case *pb.Compare_Lease:
		cmp.TargetUnion.Lease = v.Lease
	}

	return cmp
}

// opToProto converts kvstore.Op to protobuf format
func opToProto(op *kvstore.Op) *pb.Op {
	pbOp := &pb.Op{
		Type:     pb.Op_OpType(op.Type),
		Key:      string(op.Key),
		Value:    op.Value,
		RangeEnd: string(op.RangeEnd),
		Lease:    op.LeaseID,
	}
	return pbOp
}

// opFromProto converts protobuf format to kvstore.Op
func opFromProto(pbOp *pb.Op) kvstore.Op {
	op := kvstore.Op{
		Type:     kvstore.OpType(pbOp.Type),
		Key:      []byte(pbOp.Key),
		Value:    pbOp.Value,
		RangeEnd: []byte(pbOp.RangeEnd),
		LeaseID:  pbOp.Lease,
	}
	return op
}

// marshalRaftOperation marshals RaftOperation using protobuf
func marshalRaftOperation(op *RaftOperation) ([]byte, error) {
	pbOp := toProto(op)
	return proto.Marshal(pbOp)
}

// unmarshalRaftOperation unmarshals RaftOperation from protobuf
func unmarshalRaftOperation(data []byte) (*RaftOperation, error) {
	pbOp := &pb.RaftOperation{}
	if err := proto.Unmarshal(data, pbOp); err != nil {
		return nil, err
	}
	return fromProto(pbOp), nil
}

// marshalBatchOperations marshals multiple RaftOperations into a single batch
func marshalBatchOperations(ops []*RaftOperation) ([]byte, error) {
	// Convert all operations to protobuf
	pbOps := make([]*pb.RaftOperation, len(ops))
	for i, op := range ops {
		pbOps[i] = toProto(op)
	}

	// Create batch message
	batchMsg := &pb.RaftMessage{
		Payload: &pb.RaftMessage_Batch{
			Batch: &pb.BatchOperation{
				Operations: pbOps,
			},
		},
	}

	return proto.Marshal(batchMsg)
}

// unmarshalRaftMessage unmarshals a RaftMessage (single or batch)
func unmarshalRaftMessage(data []byte) ([]*RaftOperation, error) {
	msg := &pb.RaftMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}

	switch payload := msg.Payload.(type) {
	case *pb.RaftMessage_Single:
		// Single operation
		return []*RaftOperation{fromProto(payload.Single)}, nil

	case *pb.RaftMessage_Batch:
		// Batch of operations
		ops := make([]*RaftOperation, len(payload.Batch.Operations))
		for i, pbOp := range payload.Batch.Operations {
			ops[i] = fromProto(pbOp)
		}
		return ops, nil

	default:
		return nil, nil
	}
}
