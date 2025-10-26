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

package etcdapi

import (
	"log"
	"metaStore/internal/kvstore"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
)

// WatchServer 实现 etcd Watch 服务
type WatchServer struct {
	pb.UnimplementedWatchServer
	server *Server
}

// Watch 创建 watch 流
func (s *WatchServer) Watch(stream pb.Watch_WatchServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		// 处理创建 watch 请求
		if createReq := req.GetCreateRequest(); createReq != nil {
			if err := s.handleCreateWatch(stream, createReq); err != nil {
				return err
			}
		}

		// 处理取消 watch 请求
		if cancelReq := req.GetCancelRequest(); cancelReq != nil {
			if err := s.handleCancelWatch(stream, cancelReq); err != nil {
				return err
			}
		}
	}
}

// handleCreateWatch 处理创建 watch 请求
func (s *WatchServer) handleCreateWatch(stream pb.Watch_WatchServer, req *pb.WatchCreateRequest) error {
	key := string(req.Key)
	rangeEnd := string(req.RangeEnd)
	startRevision := req.StartRevision

	// Parse watch options
	opts := &kvstore.WatchOptions{
		PrevKV:         req.PrevKv,
		ProgressNotify: req.ProgressNotify,
		Filters:        convertFilters(req.Filters),
		Fragment:       req.Fragment,
	}

	// 创建 watch - 支持客户端指定 WatchId
	var watchID int64
	if req.WatchId != 0 {
		// Client specified watchID
		watchID = s.server.watchMgr.CreateWithID(req.WatchId, key, rangeEnd, startRevision, opts)
	} else {
		// Server generates watchID
		watchID = s.server.watchMgr.Create(key, rangeEnd, startRevision, opts)
	}

	if watchID < 0 {
		// 创建失败，发送错误响应
		return stream.Send(&pb.WatchResponse{
			Header:  s.server.getResponseHeader(),
			WatchId: -1,
			Created: false,
			Canceled: true,
			CancelReason: "failed to create watch",
		})
	}

	// 发送创建成功响应
	if err := stream.Send(&pb.WatchResponse{
		Header:  s.server.getResponseHeader(),
		WatchId: watchID,
		Created: true,
	}); err != nil {
		return err
	}

	// 启动 goroutine 发送事件
	go s.sendEvents(stream, watchID)

	return nil
}

// convertFilters converts etcd filters to internal types
func convertFilters(etcdFilters []pb.WatchCreateRequest_FilterType) []kvstore.WatchFilterType {
	if len(etcdFilters) == 0 {
		return nil
	}

	filters := make([]kvstore.WatchFilterType, 0, len(etcdFilters))
	for _, f := range etcdFilters {
		switch f {
		case pb.WatchCreateRequest_NOPUT:
			filters = append(filters, kvstore.FilterNoPut)
		case pb.WatchCreateRequest_NODELETE:
			filters = append(filters, kvstore.FilterNoDelete)
		}
	}
	return filters
}

// handleCancelWatch 处理取消 watch 请求
func (s *WatchServer) handleCancelWatch(stream pb.Watch_WatchServer, req *pb.WatchCancelRequest) error {
	watchID := req.WatchId

	// 取消 watch
	if err := s.server.watchMgr.Cancel(watchID); err != nil {
		log.Printf("Failed to cancel watch %d: %v", watchID, err)
	}

	// 发送取消响应
	return stream.Send(&pb.WatchResponse{
		Header:   s.server.getResponseHeader(),
		WatchId:  watchID,
		Canceled: true,
	})
}

// sendEvents 发送 watch 事件
func (s *WatchServer) sendEvents(stream pb.Watch_WatchServer, watchID int64) {
	eventCh, ok := s.server.watchMgr.GetEventChan(watchID)
	if !ok {
		return
	}

	for event := range eventCh {
		// 转换事件类型
		var eventType mvccpb.Event_EventType
		switch event.Type {
		case kvstore.EventTypePut:
			eventType = mvccpb.PUT
		case kvstore.EventTypeDelete:
			eventType = mvccpb.DELETE
		}

		// 构造 watch 事件
		watchEvent := &mvccpb.Event{
			Type: eventType,
		}

		// 添加当前键值对
		// For both PUT and DELETE events, Kv is properly populated
		if event.Kv != nil {
			watchEvent.Kv = &mvccpb.KeyValue{
				Key:            event.Kv.Key,
				Value:          event.Kv.Value,
				CreateRevision: event.Kv.CreateRevision,
				ModRevision:    event.Kv.ModRevision,
				Version:        event.Kv.Version,
				Lease:          event.Kv.Lease,
			}
		}

		// 添加前一个键值对（如果有）
		// Note: event.PrevKv may be nil if prevKV option was false
		if event.PrevKv != nil {
			watchEvent.PrevKv = &mvccpb.KeyValue{
				Key:            event.PrevKv.Key,
				Value:          event.PrevKv.Value,
				CreateRevision: event.PrevKv.CreateRevision,
				ModRevision:    event.PrevKv.ModRevision,
				Version:        event.PrevKv.Version,
				Lease:          event.PrevKv.Lease,
			}
		}

		// 发送事件
		resp := &pb.WatchResponse{
			Header:  s.server.getResponseHeader(),
			WatchId: watchID,
			Events:  []*mvccpb.Event{watchEvent},
		}

		// 更新 header 中的 revision
		resp.Header.Revision = event.Revision

		if err := stream.Send(resp); err != nil {
			log.Printf("Failed to send watch event for watch %d: %v", watchID, err)
			s.server.watchMgr.Cancel(watchID)
			return
		}
	}
}
