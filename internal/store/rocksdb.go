// Copyright 2025 The axfor Authors
// Licensed under the Apache License, Version 2.0

package store

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
	kvDataPrefix = "kv_data_"
)

// RocksDB is a key-value store backed by raft and RocksDB
type RocksDB struct {
	proposeC    chan<- string
	mu          sync.RWMutex
	db          *grocksdb.DB
	wo          *grocksdb.WriteOptions
	ro          *grocksdb.ReadOptions
	nodeID      string
	snapshotter *snap.Snapshotter
}

// NewRocksDB creates a new RocksDB-backed KV store
func NewRocksDB(db *grocksdb.DB, nodeID string, snapshotter *snap.Snapshotter, proposeC chan<- string, commitC <-chan *Commit, errorC <-chan error) *RocksDB {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(false)
	ro := grocksdb.NewDefaultReadOptions()

	s := &RocksDB{
		proposeC:    proposeC,
		db:          db,
		wo:          wo,
		ro:          ro,
		nodeID:      nodeID,
		snapshotter: snapshotter,
	}

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

// Close closes the RocksDB resources
func (s *RocksDB) Close() {
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

func (s *RocksDB) kvKey(key string) []byte {
	return []byte(fmt.Sprintf("%s_%s_%s", s.nodeID, kvDataPrefix, key))
}

func (s *RocksDB) Lookup(key string) (string, bool) {
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

func (s *RocksDB) Propose(k string, v string) {
	var buf strings.Builder
	if err := gob.NewEncoder(&buf).Encode(KV{k, v}); err != nil {
		log.Fatal(err)
	}
	s.proposeC <- buf.String()
}

func (s *RocksDB) readCommits(commitC <-chan *Commit, errorC <-chan error) {
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

		wb := grocksdb.NewWriteBatch()
		for _, data := range commit.Data {
			var dataKv KV
			dec := gob.NewDecoder(bytes.NewBufferString(data))
			if err := dec.Decode(&dataKv); err != nil {
				log.Fatalf("kvstore: could not decode message (%v)", err)
			}

			dbKey := s.kvKey(dataKv.Key)
			wb.Put(dbKey, []byte(dataKv.Val))
		}

		s.mu.Lock()
		if err := s.db.Write(s.wo, wb); err != nil {
			s.mu.Unlock()
			wb.Destroy()
			log.Fatalf("kvstore: failed to write to RocksDB (%v)", err)
		}
		s.mu.Unlock()
		wb.Destroy()

		close(commit.ApplyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal(err)
	}
}

func (s *RocksDB) GetSnapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	kvStore := make(map[string]string)
	prefix := []byte(fmt.Sprintf("%s_%s", s.nodeID, kvDataPrefix))

	ro := grocksdb.NewDefaultReadOptions()
	ro.SetFillCache(false)
	defer ro.Destroy()

	it := s.db.NewIterator(ro)
	defer it.Close()

	it.Seek(prefix)

	for ; it.Valid() && bytes.HasPrefix(it.Key().Data(), prefix); it.Next() {
		fullKey := string(it.Key().Data())
		keyPrefix := fmt.Sprintf("%s_%s_", s.nodeID, kvDataPrefix)
		actualKey := strings.TrimPrefix(fullKey, keyPrefix)
		value := string(it.Value().Data())
		kvStore[actualKey] = value
	}

	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("iterator error: %v", err)
	}

	return json.Marshal(kvStore)
}

func (s *RocksDB) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := s.snapshotter.Load()
	if errors.Is(err, snap.ErrNoSnapshot) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *RocksDB) recoverFromSnapshot(snapshot []byte) error {
	var store map[string]string
	if err := json.Unmarshal(snapshot, &store); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.clearKVData(); err != nil {
		return fmt.Errorf("failed to clear existing KV data: %v", err)
	}

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

func (s *RocksDB) clearKVData() error {
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
