// Copyright 2025 The axfor Authors
// Licensed under the Apache License, Version 2.0

package memory

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"

	"metaStore/internal/kvstore"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// Memory is a key-value store backed by raft with in-memory storage
type Memory struct {
	proposeC    chan<- string
	mu          sync.RWMutex
	kvStore     map[string]string
	snapshotter *snap.Snapshotter
}

// NewMemory creates a new memory-backed kvstore.KV store
func NewMemory(snapshotter *snap.Snapshotter, proposeC chan<- string, commitC <-chan *kvstore.Commit, errorC <-chan error) *Memory {
	s := &Memory{proposeC: proposeC, kvStore: make(map[string]string), snapshotter: snapshotter}
	snapshot, err := s.loadSnapshot()
	if err != nil {
		log.Panic(err)
	}
	if snapshot != nil {
		log.Printf("loading snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
		if err := s.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Panic(err)
		}
	}
	go s.readCommits(commitC, errorC)
	return s
}

func (s *Memory) Lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.kvStore[key]
	return v, ok
}

func (s *Memory) Propose(k string, v string) {
	var buf strings.Builder
	if err := gob.NewEncoder(&buf).Encode(kvstore.KV{k, v}); err != nil {
		log.Fatal(err)
	}
	s.proposeC <- buf.String()
}

func (s *Memory) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			snapshot, err := s.loadSnapshot()
			if err != nil {
				log.Panic(err)
			}
			if snapshot != nil {
				log.Printf("loading snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
				if err := s.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Panic(err)
				}
			}
			continue
		}

		for _, data := range commit.Data {
			var dataKv kvstore.KV
			dec := gob.NewDecoder(bytes.NewBufferString(data))
			if err := dec.Decode(&dataKv); err != nil {
				log.Fatalf("store: could not decode message (%v)", err)
			}
			s.mu.Lock()
			s.kvStore[dataKv.Key] = dataKv.Val
			s.mu.Unlock()
		}
		close(commit.ApplyDoneC)
	}
	if err, ok := <-errorC; ok {
		log.Fatal(err)
	}
}

func (s *Memory) GetSnapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.Marshal(s.kvStore)
}

func (s *Memory) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := s.snapshotter.Load()
	if errors.Is(err, snap.ErrNoSnapshot) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Memory) recoverFromSnapshot(snapshot []byte) error {
	var store map[string]string
	if err := json.Unmarshal(snapshot, &store); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kvStore = store
	return nil
}
