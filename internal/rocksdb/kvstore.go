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
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"metaStore/internal/kvstore"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

const (
	// Key prefixes for different data types
	revisionKey = "meta:revision"
	kvPrefix    = "kv:"
	leasePrefix = "lease:"
)

// RocksDB integrates Raft consensus with etcd-compatible RocksDB storage
type RocksDB struct {
	db          *grocksdb.DB
	proposeC    chan<- string
	snapshotter *snap.Snapshotter

	wo *grocksdb.WriteOptions
	ro *grocksdb.ReadOptions

	mu         sync.Mutex
	pendingMu  sync.RWMutex
	pendingOps map[string]chan struct{} // for sync wait
	seqNum     int64

	// Watch support
	watchMu sync.RWMutex
	watches map[int64]*watchSubscription
}

// watchSubscription represents a watch subscription
type watchSubscription struct {
	watchID  int64
	key      string
	rangeEnd string
	startRev int64
	eventCh  chan kvstore.WatchEvent
	cancel   chan struct{}
	closed   atomic.Bool  // 防止重复关闭
	closeOnce sync.Once   // 确保只关闭一次

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}

// RaftOperation represents an operation to be committed through Raft
type RaftOperation struct {
	Type     string `json:"type"` // "PUT", "DELETE", "LEASE_GRANT", "LEASE_REVOKE"
	Key      string `json:"key"`
	Value    string `json:"value"`
	LeaseID  int64  `json:"lease_id"`
	RangeEnd string `json:"range_end"`
	SeqNum   string `json:"seq_num"` // for sync wait

	// Lease operations
	TTL int64 `json:"ttl"`
}

// NewRocksDB creates a new RocksDB + Raft + etcd semantic storage
func NewRocksDB(
	db *grocksdb.DB,
	snapshotter *snap.Snapshotter,
	proposeC chan<- string,
	commitC <-chan *kvstore.Commit,
	errorC <-chan error,
) *RocksDB {
	wo := grocksdb.NewDefaultWriteOptions()
	wo.SetSync(true) // Ensure durability
	ro := grocksdb.NewDefaultReadOptions()

	r := &RocksDB{
		db:          db,
		proposeC:    proposeC,
		snapshotter: snapshotter,
		wo:          wo,
		ro:          ro,
		pendingOps:  make(map[string]chan struct{}),
		watches:     make(map[int64]*watchSubscription),
	}

	// Recover from snapshot if exists
	snapshot, err := r.loadSnapshot()
	if err != nil {
		log.Panic(err)
	}
	if snapshot != nil {
		log.Printf("Loading RocksDB snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
		if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Panic(err)
		}
	}

	// Start commit handler
	go r.readCommits(commitC, errorC)

	return r
}

// Close closes resources
func (r *RocksDB) Close() {
	if r.wo != nil {
		r.wo.Destroy()
	}
	if r.ro != nil {
		r.ro.Destroy()
	}
}

// readCommits reads from Raft commitC and applies operations
func (r *RocksDB) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			// Reload snapshot
			snapshot, err := r.loadSnapshot()
			if err != nil {
				log.Panic(err)
			}
			if snapshot != nil {
				log.Printf("Reloading RocksDB snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
				if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Panic(err)
				}
			}
			continue
		}

		for _, data := range commit.Data {
			// Try JSON format first (etcd operations)
			var op RaftOperation
			if err := json.Unmarshal([]byte(data), &op); err == nil {
				r.applyOperation(op)
			} else {
				// Fallback to legacy gob format (for backward compatibility)
				r.applyLegacyOp(data)
			}
		}
		close(commit.ApplyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal(err)
	}
}

// applyOperation applies an etcd operation
func (r *RocksDB) applyOperation(op RaftOperation) {
	switch op.Type {
	case "PUT":
		// Apply PUT
		if err := r.putUnlocked(op.Key, op.Value, op.LeaseID); err != nil {
			log.Printf("Failed to apply PUT: %v", err)
		}

	case "DELETE":
		// Apply DELETE
		if err := r.deleteUnlocked(op.Key, op.RangeEnd); err != nil {
			log.Printf("Failed to apply DELETE: %v", err)
		}

	case "LEASE_GRANT":
		// Apply Lease Grant
		if err := r.leaseGrantUnlocked(op.LeaseID, op.TTL); err != nil {
			log.Printf("Failed to apply LEASE_GRANT: %v", err)
		}

	case "LEASE_REVOKE":
		// Apply Lease Revoke
		if err := r.leaseRevokeUnlocked(op.LeaseID); err != nil {
			log.Printf("Failed to apply LEASE_REVOKE: %v", err)
		}

	default:
		log.Printf("Unknown operation type: %s", op.Type)
	}

	// Notify waiting client
	if op.SeqNum != "" {
		r.pendingMu.Lock()
		if ch, exists := r.pendingOps[op.SeqNum]; exists {
			close(ch)
			delete(r.pendingOps, op.SeqNum)
		}
		r.pendingMu.Unlock()
	}
}

// applyLegacyOp applies legacy gob-encoded operation (for backward compatibility)
func (r *RocksDB) applyLegacyOp(data string) {
	var dataKv kvstore.KV
	dec := gob.NewDecoder(bytes.NewBufferString(data))
	if err := dec.Decode(&dataKv); err != nil {
		log.Fatalf("Could not decode message: %v", err)
	}

	// Convert to etcd operation
	if err := r.putUnlocked(dataKv.Key, dataKv.Val, 0); err != nil {
		log.Printf("Failed to apply legacy PUT: %v", err)
	}
}

// CurrentRevision returns current revision
func (r *RocksDB) CurrentRevision() int64 {
	data, err := r.db.Get(r.ro, []byte(revisionKey))
	if err != nil {
		return 0
	}
	defer data.Free()

	if data.Size() == 0 {
		return 0
	}

	var rev int64
	buf := bytes.NewBuffer(data.Data())
	if err := gob.NewDecoder(buf).Decode(&rev); err != nil {
		return 0
	}
	return rev
}

// incrementRevision increments and returns new revision
func (r *RocksDB) incrementRevision() (int64, error) {
	rev := r.CurrentRevision() + 1

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(rev); err != nil {
		return 0, err
	}

	if err := r.db.Put(r.wo, []byte(revisionKey), buf.Bytes()); err != nil {
		return 0, err
	}

	return rev, nil
}

// Range performs range query
func (r *RocksDB) Range(key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	var kvs []*kvstore.KeyValue

	// Single key query
	if rangeEnd == "" {
		kv, err := r.getKeyValue(key)
		if err == nil && kv != nil {
			kvs = append(kvs, kv)
		}
	} else {
		// Range query
		it := r.db.NewIterator(r.ro)
		defer it.Close()

		startKey := []byte(kvPrefix + key)
		it.Seek(startKey)

		for it.ValidForPrefix([]byte(kvPrefix)) {
			k := string(it.Key().Data())
			k = k[len(kvPrefix):] // Remove prefix

			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				var kv kvstore.KeyValue
				if err := gob.NewDecoder(bytes.NewBuffer(it.Value().Data())).Decode(&kv); err == nil {
					kvs = append(kvs, &kv)
				}
			}

			if rangeEnd != "\x00" && k >= rangeEnd {
				break
			}

			it.Next()
		}

		// Sort by key
		sort.Slice(kvs, func(i, j int) bool {
			return string(kvs[i].Key) < string(kvs[j].Key)
		})
	}

	// Apply limit
	more := false
	count := int64(len(kvs))
	if limit > 0 && int64(len(kvs)) > limit {
		kvs = kvs[:limit]
		more = true
	}

	return &kvstore.RangeResponse{
		Kvs:      kvs,
		More:     more,
		Count:    count,
		Revision: r.CurrentRevision(),
	}, nil
}

// PutWithLease stores key-value with optional lease
func (r *RocksDB) PutWithLease(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// Check prevKv before submitting to Raft
	prevKv, _ := r.getKeyValue(key)

	// Generate sequence number
	r.mu.Lock()
	r.seqNum++
	seqNum := fmt.Sprintf("seq-%d", r.seqNum)
	r.mu.Unlock()

	// Create wait channel
	waitCh := make(chan struct{})
	r.pendingMu.Lock()
	r.pendingOps[seqNum] = waitCh
	r.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "PUT",
		Key:     key,
		Value:   value,
		LeaseID: leaseID,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
		return 0, nil, err
	}

	r.proposeC <- string(data)

	// Wait for Raft commit
	<-waitCh

	currentRevision := r.CurrentRevision()
	return currentRevision, prevKv, nil
}

// putUnlocked applies put operation (called after Raft commit)
func (r *RocksDB) putUnlocked(key, value string, leaseID int64) error {
	// Get previous KeyValue
	prevKv, _ := r.getKeyValue(key)

	// Increment revision
	newRevision, err := r.incrementRevision()
	if err != nil {
		return err
	}

	// Create or update KeyValue
	var version int64 = 1
	var createRevision int64 = newRevision
	if prevKv != nil {
		version = prevKv.Version + 1
		createRevision = prevKv.CreateRevision
	}

	kv := &kvstore.KeyValue{
		Key:            []byte(key),
		Value:          []byte(value),
		CreateRevision: createRevision,
		ModRevision:    newRevision,
		Version:        version,
		Lease:          leaseID,
	}

	// Serialize and store
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(kv); err != nil {
		return err
	}

	dbKey := []byte(kvPrefix + key)
	if err := r.db.Put(r.wo, dbKey, buf.Bytes()); err != nil {
		return err
	}

	// Update lease's key tracking if leaseID is specified
	if leaseID != 0 {
		lease, err := r.getLease(leaseID)
		if err != nil {
			return fmt.Errorf("failed to get lease %d: %v", leaseID, err)
		}
		if lease != nil {
			// Add key to lease's key set
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true

			// Save updated lease
			var leaseBuf bytes.Buffer
			if err := gob.NewEncoder(&leaseBuf).Encode(lease); err != nil {
				return fmt.Errorf("failed to encode lease: %v", err)
			}

			leaseKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
			if err := r.db.Put(r.wo, leaseKey, leaseBuf.Bytes()); err != nil {
				return fmt.Errorf("failed to update lease: %v", err)
			}
		}
	}

	// Trigger watch events
	r.notifyWatches(kvstore.WatchEvent{
		Type:     kvstore.EventTypePut,
		Kv:       kv,
		PrevKv:   prevKv,
		Revision: newRevision,
	})

	return nil
}

// DeleteRange deletes keys in range
func (r *RocksDB) DeleteRange(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	// Check what will be deleted (before Raft commit)
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		kv, err := r.getKeyValue(key)
		if err == nil && kv != nil {
			deleted = 1
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// Range delete - scan first
		it := r.db.NewIterator(r.ro)
		defer it.Close()

		startKey := []byte(kvPrefix + key)
		it.Seek(startKey)

		for it.ValidForPrefix([]byte(kvPrefix)) {
			k := string(it.Key().Data())
			k = k[len(kvPrefix):]

			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				var kv kvstore.KeyValue
				if err := gob.NewDecoder(bytes.NewBuffer(it.Value().Data())).Decode(&kv); err == nil {
					deleted++
					prevKvs = append(prevKvs, &kv)
				}
			}

			if rangeEnd != "\x00" && k >= rangeEnd {
				break
			}

			it.Next()
		}
	}

	if deleted == 0 {
		return 0, nil, r.CurrentRevision(), nil
	}

	// Generate sequence number
	r.mu.Lock()
	r.seqNum++
	seqNum := fmt.Sprintf("seq-%d", r.seqNum)
	r.mu.Unlock()

	// Create wait channel
	waitCh := make(chan struct{})
	r.pendingMu.Lock()
	r.pendingOps[seqNum] = waitCh
	r.pendingMu.Unlock()

	op := RaftOperation{
		Type:     "DELETE",
		Key:      key,
		RangeEnd: rangeEnd,
		SeqNum:   seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
		return 0, nil, 0, err
	}

	r.proposeC <- string(data)

	// Wait for Raft commit
	<-waitCh

	return deleted, prevKvs, r.CurrentRevision(), nil
}

// deleteUnlocked applies delete operation (called after Raft commit)
func (r *RocksDB) deleteUnlocked(key, rangeEnd string) error {
	// Get revision for watch events
	newRevision, err := r.incrementRevision()
	if err != nil {
		return err
	}

	if rangeEnd == "" {
		// Single key delete - get old value first for watch event
		prevKv, _ := r.getKeyValue(key)

		dbKey := []byte(kvPrefix + key)
		if err := r.db.Delete(r.wo, dbKey); err != nil {
			return err
		}

		// Trigger watch event if key existed
		if prevKv != nil {
			// For DELETE events, Kv contains the deleted key with ModRevision set to deletion revision
			deletedKv := &kvstore.KeyValue{
				Key:            prevKv.Key,
				Value:          nil, // Value is nil for deleted key
				CreateRevision: prevKv.CreateRevision,
				ModRevision:    newRevision, // Set to deletion revision
				Version:        0,           // Version is 0 for deleted key
				Lease:          0,
			}
			r.notifyWatches(kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   prevKv,
				Revision: newRevision,
			})
		}

		return nil
	}

	// Range delete - collect old values first
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	var deletedKeys []*kvstore.KeyValue

	startKey := []byte(kvPrefix + key)
	it.Seek(startKey)

	for it.ValidForPrefix([]byte(kvPrefix)) {
		k := string(it.Key().Data())
		k = k[len(kvPrefix):]

		if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
			// Get old value for watch event
			var kv kvstore.KeyValue
			if err := gob.NewDecoder(bytes.NewBuffer(it.Value().Data())).Decode(&kv); err == nil {
				deletedKeys = append(deletedKeys, &kv)
			}
			wb.Delete(it.Key().Data())
		}

		if rangeEnd != "\x00" && k >= rangeEnd {
			break
		}

		it.Next()
	}

	if err := r.db.Write(r.wo, wb); err != nil {
		return err
	}

	// Trigger watch events for all deleted keys
	for _, prevKv := range deletedKeys {
		// For DELETE events, Kv contains the deleted key with ModRevision set to deletion revision
		deletedKv := &kvstore.KeyValue{
			Key:            prevKv.Key,
			Value:          nil, // Value is nil for deleted key
			CreateRevision: prevKv.CreateRevision,
			ModRevision:    newRevision, // Set to deletion revision
			Version:        0,           // Version is 0 for deleted key
			Lease:          0,
		}
		r.notifyWatches(kvstore.WatchEvent{
			Type:     kvstore.EventTypeDelete,
			Kv:       deletedKv,
			PrevKv:   prevKv,
			Revision: newRevision,
		})
	}

	return nil
}

// LeaseGrant creates a lease
func (r *RocksDB) LeaseGrant(id int64, ttl int64) (*kvstore.Lease, error) {
	// Generate sequence number
	r.mu.Lock()
	r.seqNum++
	seqNum := fmt.Sprintf("seq-%d", r.seqNum)
	r.mu.Unlock()

	// Create wait channel
	waitCh := make(chan struct{})
	r.pendingMu.Lock()
	r.pendingOps[seqNum] = waitCh
	r.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "LEASE_GRANT",
		LeaseID: id,
		TTL:     ttl,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
		return nil, err
	}

	r.proposeC <- string(data)

	// Wait for Raft commit
	<-waitCh

	// Return lease info
	return r.getLease(id)
}

// leaseGrantUnlocked applies lease grant (called after Raft commit)
func (r *RocksDB) leaseGrantUnlocked(id int64, ttl int64) error {
	lease := &kvstore.Lease{
		ID:        id,
		TTL:       ttl,
		GrantTime: timeNow(),
		Keys:      make(map[string]bool),
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		return err
	}

	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	return r.db.Put(r.wo, dbKey, buf.Bytes())
}

// LeaseRevoke revokes a lease
func (r *RocksDB) LeaseRevoke(id int64) error {
	// Generate sequence number
	r.mu.Lock()
	r.seqNum++
	seqNum := fmt.Sprintf("seq-%d", r.seqNum)
	r.mu.Unlock()

	// Create wait channel
	waitCh := make(chan struct{})
	r.pendingMu.Lock()
	r.pendingOps[seqNum] = waitCh
	r.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "LEASE_REVOKE",
		LeaseID: id,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
		return err
	}

	r.proposeC <- string(data)

	// Wait for Raft commit
	<-waitCh

	return nil
}

// leaseRevokeUnlocked applies lease revoke (called after Raft commit)
func (r *RocksDB) leaseRevokeUnlocked(id int64) error {
	// Get lease to find associated keys
	lease, err := r.getLease(id)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil // Already deleted
	}

	// Delete all keys associated with this lease
	for key := range lease.Keys {
		if err := r.deleteUnlocked(key, ""); err != nil {
			log.Printf("Failed to delete key %s during lease revoke: %v", key, err)
		}
	}

	// Delete lease
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	return r.db.Delete(r.wo, dbKey)
}

// Watch creates a watch and returns an event channel
func (r *RocksDB) Watch(key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return r.WatchWithOptions(key, rangeEnd, startRevision, watchID, nil)
}

// WatchWithOptions creates a watch with options
func (r *RocksDB) WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()

	// Check if watchID already exists
	if _, exists := r.watches[watchID]; exists {
		return nil, fmt.Errorf("watch ID %d already exists", watchID)
	}

	// Create event channel (buffered to avoid blocking)
	eventCh := make(chan kvstore.WatchEvent, 100)

	// Parse options
	var prevKV, progressNotify, fragment bool
	var filters []kvstore.WatchFilterType
	if opts != nil {
		prevKV = opts.PrevKV
		progressNotify = opts.ProgressNotify
		filters = opts.Filters
		fragment = opts.Fragment
	}

	// Create subscription
	sub := &watchSubscription{
		watchID:        watchID,
		key:            key,
		rangeEnd:       rangeEnd,
		startRev:       startRevision,
		eventCh:        eventCh,
		cancel:         make(chan struct{}),
		prevKV:         prevKV,
		progressNotify: progressNotify,
		filters:        filters,
		fragment:       fragment,
	}

	r.watches[watchID] = sub

	// TODO: If startRevision > 0, send historical events
	// This requires maintaining a history of changes or using WAL replay

	return eventCh, nil
}

// CancelWatch cancels a watch
func (r *RocksDB) CancelWatch(watchID int64) error {
	r.watchMu.Lock()
	sub, ok := r.watches[watchID]
	if !ok {
		r.watchMu.Unlock()
		return fmt.Errorf("watch not found: %d", watchID)
	}

	// Check if already closed
	if !sub.closed.CompareAndSwap(false, true) {
		r.watchMu.Unlock()
		return nil // Already cancelled
	}

	// Remove from map
	delete(r.watches, watchID)
	r.watchMu.Unlock()

	// Close channels only once using sync.Once
	sub.closeOnce.Do(func() {
		close(sub.cancel)
		close(sub.eventCh)
	})

	return nil
}

// Compact compresses historical data before specified revision
func (r *RocksDB) Compact(revision int64) error {
	// TODO: Implement compaction
	// For now, this is a no-op
	return nil
}

// LeaseRenew renews a lease
func (r *RocksDB) LeaseRenew(id int64) (*kvstore.Lease, error) {
	// Get current lease
	lease, err := r.getLease(id)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// Update grant time
	lease.GrantTime = time.Now()

	// Save updated lease
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(lease); err != nil {
		return nil, err
	}

	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	if err := r.db.Put(r.wo, dbKey, buf.Bytes()); err != nil {
		return nil, err
	}

	return lease, nil
}

// LeaseTimeToLive gets remaining time of a lease
func (r *RocksDB) LeaseTimeToLive(id int64) (*kvstore.Lease, error) {
	return r.getLease(id)
}

// Leases returns all leases
func (r *RocksDB) Leases() ([]*kvstore.Lease, error) {
	var leases []*kvstore.Lease

	it := r.db.NewIterator(r.ro)
	defer it.Close()

	prefix := []byte(leasePrefix)
	it.Seek(prefix)

	for it.ValidForPrefix(prefix) {
		var lease kvstore.Lease
		if err := gob.NewDecoder(bytes.NewBuffer(it.Value().Data())).Decode(&lease); err == nil {
			leases = append(leases, &lease)
		}
		it.Next()
	}

	return leases, nil
}

// Propose proposes a value (for backward compatibility with old Store interface)
func (r *RocksDB) Propose(k string, v string) {
	// Convert to etcd Put operation
	r.PutWithLease(k, v, 0)
}

// Lookup looks up a key (for backward compatibility)
func (r *RocksDB) Lookup(key string) (string, bool) {
	kv, err := r.getKeyValue(key)
	if err != nil || kv == nil {
		return "", false
	}
	return string(kv.Value), true
}

// Txn executes a transaction (simplified implementation)
func (r *RocksDB) Txn(compare []kvstore.Compare, onSuccess []kvstore.Op, onFailure []kvstore.Op) (*kvstore.TxnResponse, error) {
	// TODO: Implement transaction support
	return &kvstore.TxnResponse{
		Succeeded: false,
		Responses: nil,
	}, fmt.Errorf("transaction not yet implemented for RocksDB")
}

// Helper functions

func (r *RocksDB) getKeyValue(key string) (*kvstore.KeyValue, error) {
	dbKey := []byte(kvPrefix + key)
	data, err := r.db.Get(r.ro, dbKey)
	if err != nil {
		return nil, err
	}
	defer data.Free()

	if data.Size() == 0 {
		return nil, nil
	}

	var kv kvstore.KeyValue
	if err := gob.NewDecoder(bytes.NewBuffer(data.Data())).Decode(&kv); err != nil {
		return nil, err
	}

	return &kv, nil
}

func (r *RocksDB) getLease(id int64) (*kvstore.Lease, error) {
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	data, err := r.db.Get(r.ro, dbKey)
	if err != nil {
		return nil, err
	}
	defer data.Free()

	if data.Size() == 0 {
		return nil, nil
	}

	var lease kvstore.Lease
	if err := gob.NewDecoder(bytes.NewBuffer(data.Data())).Decode(&lease); err != nil {
		return nil, err
	}

	return &lease, nil
}

// Snapshot support

func (r *RocksDB) GetSnapshot() ([]byte, error) {
	// Create snapshot of all data
	snapshot := make(map[string][]byte)

	it := r.db.NewIterator(r.ro)
	defer it.Close()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := make([]byte, len(it.Key().Data()))
		copy(key, it.Key().Data())

		value := make([]byte, len(it.Value().Data()))
		copy(value, it.Value().Data())

		snapshot[string(key)] = value
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snapshot); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (r *RocksDB) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := r.snapshotter.Load()
	if err == snap.ErrNoSnapshot {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *RocksDB) recoverFromSnapshot(snapshot []byte) error {
	var snapshotData map[string][]byte
	if err := gob.NewDecoder(bytes.NewBuffer(snapshot)).Decode(&snapshotData); err != nil {
		return err
	}

	// Clear existing data
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	wb := grocksdb.NewWriteBatch()
	defer wb.Destroy()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		wb.Delete(it.Key().Data())
	}

	// Restore from snapshot
	for k, v := range snapshotData {
		wb.Put([]byte(k), v)
	}

	return r.db.Write(r.wo, wb)
}

// timeNow returns current timestamp
func timeNow() time.Time {
	return time.Now()
}

// notifyWatches notifies all matching watches (high-performance lock-free version)
func (r *RocksDB) notifyWatches(event kvstore.WatchEvent) {
	key := ""
	if event.Kv != nil {
		key = string(event.Kv.Key)
	} else if event.PrevKv != nil {
		key = string(event.PrevKv.Key)
	}

	// Fast path: copy matching subscriptions (minimal lock time)
	r.watchMu.RLock()
	matchingSubs := make([]*watchSubscription, 0, len(r.watches))
	for _, sub := range r.watches {
		if sub.closed.Load() {
			continue // Skip closed watches
		}
		if r.matchWatch(key, sub.key, sub.rangeEnd) {
			matchingSubs = append(matchingSubs, sub)
		}
	}
	r.watchMu.RUnlock()

	// Send events outside of lock
	for _, sub := range matchingSubs {
		// Apply filters
		if r.shouldFilter(event.Type, sub.filters) {
			continue
		}

		// Prepare event based on prevKV option
		eventToSend := event
		if !sub.prevKV {
			eventToSend.PrevKv = nil
		}

		// Non-blocking send with slow client handling
		select {
		case sub.eventCh <- eventToSend:
			// Success
		case <-sub.cancel:
			// Watch已取消
		default:
			// Channel满了，异步发送（慢客户端）
			go r.slowSendEvent(sub, eventToSend)
		}
	}
}

// shouldFilter checks if event should be filtered out
func (r *RocksDB) shouldFilter(eventType kvstore.EventType, filters []kvstore.WatchFilterType) bool {
	for _, f := range filters {
		switch f {
		case kvstore.FilterNoPut:
			if eventType == kvstore.EventTypePut {
				return true
			}
		case kvstore.FilterNoDelete:
			if eventType == kvstore.EventTypeDelete {
				return true
			}
		}
	}
	return false
}

// slowSendEvent handles slow clients with timeout
func (r *RocksDB) slowSendEvent(sub *watchSubscription, event kvstore.WatchEvent) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case sub.eventCh <- event:
		// Successfully sent after retry
	case <-sub.cancel:
		// Watch cancelled
	case <-timer.C:
		// Timeout - force cancel this slow watch
		log.Printf("Watch %d is too slow, force cancelling", sub.watchID)
		r.CancelWatch(sub.watchID)
	}
}

// matchWatch checks if key matches watch range
func (r *RocksDB) matchWatch(key, watchKey, rangeEnd string) bool {
	if rangeEnd == "" {
		// Single key match
		return key == watchKey
	}
	// Range match
	return key >= watchKey && (rangeEnd == "\x00" || key < rangeEnd)
}

