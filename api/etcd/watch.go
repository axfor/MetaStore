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
	"io"
	"metaStore/internal/kvstore"
	"metaStore/pkg/log"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	"go.uber.org/zap"
)

// WatchServer implements etcd Watch service
type WatchServer struct {
	pb.UnimplementedWatchServer
	server *Server
}

// Watch creates a watch stream
func (s *WatchServer) Watch(stream pb.Watch_WatchServer) error {
	// Track all watchIDs created by this stream for cleanup
	streamWatches := make(map[int64]struct{})

	// Ensure all watches are cleaned up when function returns to prevent goroutine leaks
	defer func() {
		for watchID := range streamWatches {
			if err := s.server.watchMgr.Cancel(watchID); err != nil {
				log.Warn("Failed to cancel watch during cleanup", zap.Int64("watch_id", watchID), zap.Error(err), zap.String("component", "etcdapi-watch"))
			}
		}
	}()

	for {
		req, err := stream.Recv()
		if err != nil {
			// grpc-gateway (and some HTTP clients) may send a single create_request and
			// then close the request body, which manifests as io.EOF on the server side.
			// etcd keeps the watch alive until the transport is closed; mirror that.
			if err == io.EOF {
				<-stream.Context().Done()
				return stream.Context().Err()
			}
			return err
		}

		// Handle create watch request
		if createReq := req.GetCreateRequest(); createReq != nil {
			watchID, err := s.handleCreateWatch(stream, createReq)
			if err != nil {
				return err
			}
			// Record that this watchID belongs to current stream
			if watchID > 0 {
				streamWatches[watchID] = struct{}{}
			}
		}

		// Handle cancel watch request
		if cancelReq := req.GetCancelRequest(); cancelReq != nil {
			if err := s.handleCancelWatch(stream, cancelReq); err != nil {
				return err
			}
			// Remove from tracking
			delete(streamWatches, cancelReq.WatchId)
		}
	}
}

// handleCreateWatch handles create watch request, returns watchID and error
func (s *WatchServer) handleCreateWatch(stream pb.Watch_WatchServer, req *pb.WatchCreateRequest) (int64, error) {
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

	// Create watch - supports client specified WatchId
	var watchID int64
	if req.WatchId != 0 {
		// Client specified watchID
		watchID = s.server.watchMgr.CreateWithID(req.WatchId, key, rangeEnd, startRevision, opts)
	} else {
		// Server generates watchID
		watchID = s.server.watchMgr.Create(key, rangeEnd, startRevision, opts)
	}

	if watchID < 0 {
		// Creation failed, send error response
		err := stream.Send(&pb.WatchResponse{
			Header:       s.server.getResponseHeader(),
			WatchId:      -1,
			Created:      false,
			Canceled:     true,
			CancelReason: "failed to create watch",
		})
		return -1, err
	}

	// Send success response
	if err := stream.Send(&pb.WatchResponse{
		Header:  s.server.getResponseHeader(),
		WatchId: watchID,
		Created: true,
	}); err != nil {
		return watchID, err
	}

	// Start goroutine to send events
	go s.sendEvents(stream, watchID)

	return watchID, nil
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

// handleCancelWatch handles cancel watch request
func (s *WatchServer) handleCancelWatch(stream pb.Watch_WatchServer, req *pb.WatchCancelRequest) error {
	watchID := req.WatchId

	// Cancel watch
	if err := s.server.watchMgr.Cancel(watchID); err != nil {
		log.Warn("Failed to cancel watch", zap.Int64("watch_id", watchID), zap.Error(err), zap.String("component", "etcdapi-watch"))
	}

	// Send cancel response
	return stream.Send(&pb.WatchResponse{
		Header:   s.server.getResponseHeader(),
		WatchId:  watchID,
		Canceled: true,
	})
}

// sendEvents sends watch events
func (s *WatchServer) sendEvents(stream pb.Watch_WatchServer, watchID int64) {
	eventCh, ok := s.server.watchMgr.GetEventChan(watchID)
	if !ok {
		return
	}

	// Ensure watch is cancelled when this goroutine exits
	defer func() {
		s.server.watchMgr.Cancel(watchID)
	}()

	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed, watch cancelled
				return
			}

			// Convert event type
			var eventType mvccpb.Event_EventType
			switch event.Type {
			case kvstore.EventTypePut:
				eventType = mvccpb.PUT
			case kvstore.EventTypeDelete:
				eventType = mvccpb.DELETE
			}

			// Build watch event
			watchEvent := &mvccpb.Event{
				Type: eventType,
			}

			// Add current key-value pair
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

			// Add previous key-value pair (if any)
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

			// Send event
			resp := &pb.WatchResponse{
				Header:  s.server.getResponseHeader(),
				WatchId: watchID,
				Events:  []*mvccpb.Event{watchEvent},
			}

			// Update revision in header
			resp.Header.Revision = event.Revision

			if err := stream.Send(resp); err != nil {
				log.Warn("Failed to send watch event", zap.Int64("watch_id", watchID), zap.Error(err), zap.String("component", "etcdapi-watch"))
				return
			}

		case <-stream.Context().Done():
			// Stream context cancelled, clean up
			return
		}
	}
}
