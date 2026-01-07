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

package etcd

import (
	"context"
	"metaStore/internal/kvstore"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
)

// KVServer implement etcd KV
type KVServer struct {
	pb.UnimplementedKVServer
	server *Server
}

// Range performs range query
func (s *KVServer) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	key := string(req.Key)
	rangeEnd := string(req.RangeEnd)

	// Build RangeOptions
	opts := kvstore.RangeOptions{
		Limit:             req.Limit,
		Revision:          req.Revision,
		MaxCreateRevision: req.MaxCreateRevision,
		MinCreateRevision: req.MinCreateRevision,
		MaxModRevision:    req.MaxModRevision,
		MinModRevision:    req.MinModRevision,
		CountOnly:         req.CountOnly,
		KeysOnly:          req.KeysOnly,
	}

	// Convert sort options
	switch req.SortOrder {
	case pb.RangeRequest_ASCEND:
		opts.SortOrder = kvstore.SortAscend
	case pb.RangeRequest_DESCEND:
		opts.SortOrder = kvstore.SortDescend
	default:
		opts.SortOrder = kvstore.SortNone
	}

	switch req.SortTarget {
	case pb.RangeRequest_KEY:
		opts.SortTarget = kvstore.SortByKey
	case pb.RangeRequest_VERSION:
		opts.SortTarget = kvstore.SortByVersion
	case pb.RangeRequest_CREATE:
		opts.SortTarget = kvstore.SortByCreate
	case pb.RangeRequest_MOD:
		opts.SortTarget = kvstore.SortByMod
	case pb.RangeRequest_VALUE:
		opts.SortTarget = kvstore.SortByValue
	default:
		opts.SortTarget = kvstore.SortByKey
	}

	// Query from store
	resp, err := s.server.store.RangeWithOptions(ctx, key, rangeEnd, opts)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// Convert to protobuf format
	kvs := make([]*mvccpb.KeyValue, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		kvs[i] = &mvccpb.KeyValue{
			Key:            kv.Key,
			Value:          kv.Value,
			CreateRevision: kv.CreateRevision,
			ModRevision:    kv.ModRevision,
			Version:        kv.Version,
			Lease:          kv.Lease,
		}
	}

	return &pb.RangeResponse{
		Header: s.server.getResponseHeader(),
		Kvs:    kvs,
		More:   resp.More,
		Count:  resp.Count,
	}, nil
}

// Put stores key-value pair
func (s *KVServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	key := string(req.Key)
	value := string(req.Value)
	leaseID := req.Lease

	// Call store to save
	revision, prevKv, err := s.server.store.PutWithLease(ctx, key, value, leaseID)
	if err != nil {
		return nil, toGRPCError(err)
	}

	resp := &pb.PutResponse{
		Header: s.server.getResponseHeader(),
	}

	// If request returns previous value
	if req.PrevKv && prevKv != nil {
		resp.PrevKv = &mvccpb.KeyValue{
			Key:            prevKv.Key,
			Value:          prevKv.Value,
			CreateRevision: prevKv.CreateRevision,
			ModRevision:    prevKv.ModRevision,
			Version:        prevKv.Version,
			Lease:          prevKv.Lease,
		}
	}

	// update header in revision
	resp.Header.Revision = revision

	return resp, nil
}

// DeleteRange deletes keys in range
func (s *KVServer) DeleteRange(ctx context.Context, req *pb.DeleteRangeRequest) (*pb.DeleteRangeResponse, error) {
	key := string(req.Key)
	rangeEnd := string(req.RangeEnd)

	// Call store to delete
	deleted, prevKvs, revision, err := s.server.store.DeleteRange(ctx, key, rangeEnd)
	if err != nil {
		return nil, toGRPCError(err)
	}

	resp := &pb.DeleteRangeResponse{
		Header:  s.server.getResponseHeader(),
		Deleted: deleted,
	}

	// If request returns deleted values
	if req.PrevKv && len(prevKvs) > 0 {
		resp.PrevKvs = make([]*mvccpb.KeyValue, len(prevKvs))
		for i, kv := range prevKvs {
			resp.PrevKvs[i] = &mvccpb.KeyValue{
				Key:            kv.Key,
				Value:          kv.Value,
				CreateRevision: kv.CreateRevision,
				ModRevision:    kv.ModRevision,
				Version:        kv.Version,
				Lease:          kv.Lease,
			}
		}
	}

	// update header in revision
	resp.Header.Revision = revision

	return resp, nil
}

// Txn executes transaction
func (s *KVServer) Txn(ctx context.Context, req *pb.TxnRequest) (*pb.TxnResponse, error) {
	// Convert compare conditions
	cmps := make([]kvstore.Compare, len(req.Compare))
	for i, cmp := range req.Compare {
		cmps[i] = convertCompare(cmp)
	}

	// Convert success operations
	thenOps := make([]kvstore.Op, len(req.Success))
	for i, reqOp := range req.Success {
		thenOps[i] = convertRequestOp(reqOp)
	}

	// Convert failure operations
	elseOps := make([]kvstore.Op, len(req.Failure))
	for i, reqOp := range req.Failure {
		elseOps[i] = convertRequestOp(reqOp)
	}

	// Execute transaction
	txnResp, err := s.server.store.Txn(ctx, cmps, thenOps, elseOps)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// Convert response
	resp := &pb.TxnResponse{
		Header:    s.server.getResponseHeader(),
		Succeeded: txnResp.Succeeded,
		Responses: make([]*pb.ResponseOp, len(txnResp.Responses)),
	}

	for i, opResp := range txnResp.Responses {
		resp.Responses[i] = convertOpResponse(opResp)
	}

	// Update revision in header
	resp.Header.Revision = txnResp.Revision

	return resp, nil
}

// Compact compacts historical data
func (s *KVServer) Compact(ctx context.Context, req *pb.CompactionRequest) (*pb.CompactionResponse, error) {
	err := s.server.store.Compact(ctx, req.Revision)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.CompactionResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// Helper function: convert Compare
func convertCompare(cmp *pb.Compare) kvstore.Compare {
	c := kvstore.Compare{
		Key: cmp.Key,
	}

	// Convert target
	switch cmp.Target {
	case pb.Compare_VERSION:
		c.Target = kvstore.CompareVersion
		c.TargetUnion.Version = cmp.GetVersion()
	case pb.Compare_CREATE:
		c.Target = kvstore.CompareCreate
		c.TargetUnion.CreateRevision = cmp.GetCreateRevision()
	case pb.Compare_MOD:
		c.Target = kvstore.CompareMod
		c.TargetUnion.ModRevision = cmp.GetModRevision()
	case pb.Compare_VALUE:
		c.Target = kvstore.CompareValue
		c.TargetUnion.Value = cmp.GetValue()
	case pb.Compare_LEASE:
		c.Target = kvstore.CompareLease
		c.TargetUnion.Lease = cmp.GetLease()
	}

	// Convert result
	switch cmp.Result {
	case pb.Compare_EQUAL:
		c.Result = kvstore.CompareEqual
	case pb.Compare_GREATER:
		c.Result = kvstore.CompareGreater
	case pb.Compare_LESS:
		c.Result = kvstore.CompareLess
	case pb.Compare_NOT_EQUAL:
		c.Result = kvstore.CompareNotEqual
	}

	return c
}

// Helper function: convert RequestOp
func convertRequestOp(reqOp *pb.RequestOp) kvstore.Op {
	op := kvstore.Op{}

	if r := reqOp.GetRequestRange(); r != nil {
		op.Type = kvstore.OpRange
		op.Key = r.Key
		op.RangeEnd = r.RangeEnd
		op.Limit = r.Limit
	} else if p := reqOp.GetRequestPut(); p != nil {
		op.Type = kvstore.OpPut
		op.Key = p.Key
		op.Value = p.Value
		op.LeaseID = p.Lease
	} else if d := reqOp.GetRequestDeleteRange(); d != nil {
		op.Type = kvstore.OpDelete
		op.Key = d.Key
		op.RangeEnd = d.RangeEnd
	}

	return op
}

// Helper function: convert OpResponse
func convertOpResponse(opResp kvstore.OpResponse) *pb.ResponseOp {
	resp := &pb.ResponseOp{}

	switch opResp.Type {
	case kvstore.OpRange:
		if opResp.RangeResp != nil {
			kvs := make([]*mvccpb.KeyValue, len(opResp.RangeResp.Kvs))
			for i, kv := range opResp.RangeResp.Kvs {
				kvs[i] = &mvccpb.KeyValue{
					Key:            kv.Key,
					Value:          kv.Value,
					CreateRevision: kv.CreateRevision,
					ModRevision:    kv.ModRevision,
					Version:        kv.Version,
					Lease:          kv.Lease,
				}
			}
			resp.Response = &pb.ResponseOp_ResponseRange{
				ResponseRange: &pb.RangeResponse{
					Kvs:   kvs,
					More:  opResp.RangeResp.More,
					Count: opResp.RangeResp.Count,
				},
			}
		}
	case kvstore.OpPut:
		if opResp.PutResp != nil {
			putResp := &pb.PutResponse{}
			if opResp.PutResp.PrevKv != nil {
				putResp.PrevKv = &mvccpb.KeyValue{
					Key:            opResp.PutResp.PrevKv.Key,
					Value:          opResp.PutResp.PrevKv.Value,
					CreateRevision: opResp.PutResp.PrevKv.CreateRevision,
					ModRevision:    opResp.PutResp.PrevKv.ModRevision,
					Version:        opResp.PutResp.PrevKv.Version,
					Lease:          opResp.PutResp.PrevKv.Lease,
				}
			}
			resp.Response = &pb.ResponseOp_ResponsePut{
				ResponsePut: putResp,
			}
		}
	case kvstore.OpDelete:
		if opResp.DeleteResp != nil {
			deleteResp := &pb.DeleteRangeResponse{
				Deleted: opResp.DeleteResp.Deleted,
			}
			if len(opResp.DeleteResp.PrevKvs) > 0 {
				deleteResp.PrevKvs = make([]*mvccpb.KeyValue, len(opResp.DeleteResp.PrevKvs))
				for i, kv := range opResp.DeleteResp.PrevKvs {
					deleteResp.PrevKvs[i] = &mvccpb.KeyValue{
						Key:            kv.Key,
						Value:          kv.Value,
						CreateRevision: kv.CreateRevision,
						ModRevision:    kv.ModRevision,
						Version:        kv.Version,
						Lease:          kv.Lease,
					}
				}
			}
			resp.Response = &pb.ResponseOp_ResponseDeleteRange{
				ResponseDeleteRange: deleteResp,
			}
		}
	}

	return resp
}
