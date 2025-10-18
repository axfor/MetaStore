//go:build rocksdb
// +build rocksdb

// Copyright 2015 The etcd Authors
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

package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

const (
	// Key prefix for KV data in RocksDB
	kvDataPrefix = "kv_data_"
)

// kvstoreRocks is a key-value store backed by raft and RocksDB
type kvstoreRocks struct {
	proposeC chan<- string // channel for proposing updates
	mu       sync.RWMutex
	db       *grocksdb.DB // RocksDB instance for persistent KV storage
	wo       *grocksdb.WriteOptions
	ro       *grocksdb.ReadOptions
	nodeID   string
	snapshotter *snap.Snapshotter
}

// newKVStoreRocks creates a new RocksDB-backed KV store
func newKVStoreRocks(db *grocksdb.DB, nodeID string, snapshotter *snap.Snapshotter, proposeC chan<- string, commitC <-chan *commit, errorC <-chan error) *kvstoreRocks {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(false) // For KV data, we can use async writes for better performance
	ro := grocksdb.NewDefaultReadOptions()

	s := &kvstoreRocks{
		proposeC:    proposeC,
		db:          db,
		wo:          wo,
		ro:          ro,
		nodeID:      nodeID,
		snapshotter: snapshotter,
	}

	// Load from snapshot if exists
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

	// Read commits from raft into RocksDB until error
	go s.readCommits(commitC, errorC)

	return s
}

// Close closes the RocksDB resources
func (s *kvstoreRocks) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wo != nil {
		s.wo.Destroy()
		s.wo = nil
	}
	if s.ro != nil {
		s.ro.Destroy()
		s.ro = nil
	}
}

// kvKey generates the key for storing KV data in RocksDB
func (s *kvstoreRocks) kvKey(key string) []byte {
	return []byte(fmt.Sprintf("%s_%s_%s", s.nodeID, kvDataPrefix, key))
}

// Lookup retrieves a value for a given key
func (s *kvstoreRocks) Lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dbKey := s.kvKey(key)
	data, err := s.db.Get(s.ro, dbKey)
	if err != nil {
		log.Printf("Failed to get key %s: %v", key, err)
		return "", false
	}
	defer data.Free()

	if data.Size() == 0 {
		return "", false
	}

	return string(data.Data()), true
}

// Propose proposes a key-value update to raft
func (s *kvstoreRocks) Propose(k string, v string) {
	var buf strings.Builder
	if err := gob.NewEncoder(&buf).Encode(kv{k, v}); err != nil {
		log.Fatal(err)
	}
	s.proposeC <- buf.String()
}

// readCommits reads committed entries from raft and applies them to RocksDB
func (s *kvstoreRocks) readCommits(commitC <-chan *commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			// Signaled to load snapshot
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

		// Apply all committed KV pairs to RocksDB
		wb := grocksdb.NewWriteBatch()
		for _, data := range commit.data {
			var dataKv kv
			dec := gob.NewDecoder(bytes.NewBufferString(data))
			if err := dec.Decode(&dataKv); err != nil {
				log.Fatalf("kvstore: could not decode message (%v)", err)
			}

			dbKey := s.kvKey(dataKv.Key)
			wb.Put(dbKey, []byte(dataKv.Val))
		}

		// Write batch to RocksDB
		s.mu.Lock()
		if err := s.db.Write(s.wo, wb); err != nil {
			s.mu.Unlock()
			wb.Destroy()
			log.Fatalf("kvstore: failed to write to RocksDB (%v)", err)
		}
		s.mu.Unlock()
		wb.Destroy()

		close(commit.applyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal(err)
	}
}

// getSnapshot creates a snapshot of all KV pairs in RocksDB
func (s *kvstoreRocks) getSnapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Create a map to hold all KV pairs
	kvStore := make(map[string]string)

	// Iterate through all KV entries in RocksDB
	prefix := []byte(fmt.Sprintf("%s_%s", s.nodeID, kvDataPrefix))

	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(false)
	defer ro.Destroy()

	it := s.db.NewIterator(ro)
	defer it.Close()

	// Seek to the start of our KV data
	it.Seek(prefix)

	for ; it.Valid() && bytes.HasPrefix(it.Key().Data(), prefix); it.Next() {
		// Extract the actual key (remove prefix)
		fullKey := string(it.Key().Data())
		keyPrefix := fmt.Sprintf("%s_%s_", s.nodeID, kvDataPrefix)
		actualKey := strings.TrimPrefix(fullKey, keyPrefix)

		// Get the value
		value := string(it.Value().Data())

		kvStore[actualKey] = value
	}

	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("iterator error: %v", err)
	}

	// Marshal to JSON
	return json.Marshal(kvStore)
}

// loadSnapshot loads the latest snapshot from disk
func (s *kvstoreRocks) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := s.snapshotter.Load()
	if errors.Is(err, snap.ErrNoSnapshot) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// recoverFromSnapshot restores the KV store from snapshot data
func (s *kvstoreRocks) recoverFromSnapshot(snapshot []byte) error {
	var store map[string]string
	if err := json.Unmarshal(snapshot, &store); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing KV data for this node
	if err := s.clearKVData(); err != nil {
		return fmt.Errorf("failed to clear existing KV data: %v", err)
	}

	// Restore all KV pairs from snapshot
	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for k, v := range store {
		dbKey := s.kvKey(k)
		wb.Put(dbKey, []byte(v))
	}

	if err := s.db.Write(s.wo, wb); err != nil {
		return fmt.Errorf("failed to write snapshot data: %v", err)
	}

	log.Printf("Restored %d KV pairs from snapshot", len(store))

	return nil
}

// clearKVData removes all KV data for this node (used during snapshot recovery)
func (s *kvstoreRocks) clearKVData() error {
	prefix := []byte(fmt.Sprintf("%s_%s", s.nodeID, kvDataPrefix))

	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()

	it := s.db.NewIterator(ro)
	defer it.Close()

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	it.Seek(prefix)
	for ; it.Valid() && bytes.HasPrefix(it.Key().Data(), prefix); it.Next() {
		wb.Delete(it.Key().Data())
	}

	if err := it.Err(); err != nil {
		return err
	}

	return s.db.Write(s.wo, wb)
}
