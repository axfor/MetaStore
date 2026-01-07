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

package http

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"metaStore/internal/kvstore"
	"metaStore/pkg/log"

	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

// Server HTTP API server
type Server struct {
	store       kvstore.Store
	confChangeC chan<- raftpb.ConfChange
	httpServer  *http.Server
}

// Config HTTP API configuration
type Config struct {
	Store       kvstore.Store
	Port        int
	ConfChangeC chan<- raftpb.ConfChange
}

// NewServer creates a new HTTP API server
func NewServer(cfg Config) *Server {
	s := &Server{
		store:       cfg.Store,
		confChangeC: cfg.ConfChangeC,
	}

	mux := http.NewServeMux()
	mux.Handle("/", s)

	s.httpServer = &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.Port),
		Handler: mux,
	}

	return s
}

// Start starts the HTTP server
func (s *Server) Start() error {
	log.Info("Starting HTTP API server", zap.String("address", s.httpServer.Addr), zap.String("component", "http"))
	return s.httpServer.ListenAndServe()
}

// Stop stops the HTTP server
func (s *Server) Stop() error {
	log.Info("Stopping HTTP API server", zap.String("component", "http"))
	return s.httpServer.Close()
}

// ServeHTTP handles HTTP requests
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Info("HTTP request received",
		zap.String("method", r.Method),
		zap.String("uri", r.RequestURI),
		zap.String("component", "http"))

	// Remove leading slash to make key consistent with etcd API
	key := strings.TrimPrefix(r.RequestURI, "/")
	defer r.Body.Close()

	// Check if it's a cluster management operation (starts with numeric ID)
	// Cluster operations: POST /{nodeID} add node, DELETE /{nodeID} remove node
	isClusterOp := false
	if r.Method == http.MethodPost || r.Method == http.MethodDelete {
		// Try to parse as nodeID, if successful treat as cluster operation
		_, err := strconv.ParseUint(key, 0, 64)
		isClusterOp = (err == nil)
	}

	switch r.Method {
	case http.MethodPut:
		s.handlePut(w, r, key)
	case http.MethodGet:
		s.handleGet(w, r, key)
	case http.MethodPost:
		if isClusterOp {
			s.handleClusterAdd(w, r, key)
		} else {
			http.Error(w, "POST requires numeric node ID", http.StatusBadRequest)
		}
	case http.MethodDelete:
		if isClusterOp {
			s.handleClusterDelete(w, r, key)
		} else {
			s.handleKeyDelete(w, r, key)
		}
	default:
		w.Header().Set("Allow", http.MethodPut)
		w.Header().Add("Allow", http.MethodGet)
		w.Header().Add("Allow", http.MethodPost)
		w.Header().Add("Allow", http.MethodDelete)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePut handles PUT requests (store key-value pairs)
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	v, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("Failed to read body on PUT", zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on PUT", http.StatusBadRequest)
		return
	}

	log.Info("HTTP PUT request",
		zap.String("key", key),
		zap.String("value", string(v)),
		zap.String("component", "http"))

	// Use synchronous PutWithLease instead of asynchronous Propose to ensure immediate readability after write
	ctx := context.Background()
	_, _, err = s.store.PutWithLease(ctx, key, string(v), 0)
	if err != nil {
		log.Error("Failed to put key-value", zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on PUT", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleGet handles GET requests (query key-value)
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, key string) {
	log.Info("HTTP GET request",
		zap.String("key", key),
		zap.String("component", "http"))

	if v, ok := s.store.Lookup(key); ok {
		log.Info("HTTP GET found value",
			zap.String("key", key),
			zap.String("value", v),
			zap.String("component", "http"))
		w.Write([]byte(v))
	} else {
		log.Info("HTTP GET key not found",
			zap.String("key", key),
			zap.String("component", "http"))
		http.Error(w, "Failed to GET", http.StatusNotFound)
	}
}

// handleClusterAdd handles POST requests (add Raft node)
func (s *Server) handleClusterAdd(w http.ResponseWriter, r *http.Request, key string) {
	url, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("Failed to read body on POST", zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on POST", http.StatusBadRequest)
		return
	}

	// key already has leading slash removed, parse directly
	nodeID, err := strconv.ParseUint(key, 0, 64)
	if err != nil {
		log.Error("Failed to convert ID for conf change", zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on POST", http.StatusBadRequest)
		return
	}

	cc := raftpb.ConfChange{
		Type:    raftpb.ConfChangeAddNode,
		NodeID:  nodeID,
		Context: url,
	}
	s.confChangeC <- cc

	// As above, optimistic that raft will apply the conf change
	w.WriteHeader(http.StatusNoContent)
}

// handleClusterDelete handles DELETE requests (remove Raft node)
func (s *Server) handleClusterDelete(w http.ResponseWriter, r *http.Request, key string) {
	// key already has leading slash removed, parse directly
	nodeID, err := strconv.ParseUint(key, 0, 64)
	if err != nil {
		log.Error("Failed to convert ID for conf change", zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on DELETE", http.StatusBadRequest)
		return
	}

	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeRemoveNode,
		NodeID: nodeID,
	}
	s.confChangeC <- cc

	// As above, optimistic that raft will apply the conf change
	w.WriteHeader(http.StatusNoContent)
}

// handleKeyDelete handles DELETE requests (delete key-value pairs)
func (s *Server) handleKeyDelete(w http.ResponseWriter, r *http.Request, key string) {
	// Use DeleteRange to delete single key (empty rangeEnd means single key deletion)
	_, _, _, err := s.store.DeleteRange(context.Background(), key, "")
	if err != nil {
		log.Error("Failed to delete key", zap.String("key", key), zap.Error(err), zap.String("component", "http"))
		http.Error(w, "Failed on DELETE", http.StatusInternalServerError)
		return
	}

	// Optimistic-- no waiting for ack from raft
	w.WriteHeader(http.StatusNoContent)
}

// ServeHTTPKVAPI starts HTTP KV API (maintains backward compatibility)
func ServeHTTPKVAPI(kv kvstore.Store, port int, confChangeC chan<- raftpb.ConfChange, errorC <-chan error) {
	srv := NewServer(Config{
		Store:       kv,
		Port:        port,
		ConfChangeC: confChangeC,
	})

	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatal("HTTP server failed", zap.Error(err), zap.String("component", "http"))
		}
	}()

	// exit when raft goes down
	if err, ok := <-errorC; ok {
		log.Fatal("Raft error", zap.Error(err), zap.String("component", "http"))
	}
}
