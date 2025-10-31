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
	"context"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/pkg/log"

	"github.com/linxGnu/grocksdb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

const (
	// Key prefixes for different data types
	revisionKey = "meta:revision"
	kvPrefix    = "kv:"
	leasePrefix = "lease:"
)

// RaftNode Raft 节点接口，用于获取 Raft 状态
type RaftNode interface {
	Status() kvstore.RaftStatus
	TransferLeadership(targetID uint64) error
}

// RocksDB integrates Raft consensus with etcd-compatible RocksDB storage
type RocksDB struct {
	db          *grocksdb.DB
	proposeC    chan<- string
	snapshotter *snap.Snapshotter

	wo *grocksdb.WriteOptions
	ro *grocksdb.ReadOptions

	mu                sync.Mutex
	pendingMu         sync.RWMutex
	pendingOps        map[string]chan struct{}        // for sync wait
	pendingTxnResults map[string]*kvstore.TxnResponse // seqNum -> txn result
	seqNum            int64

	// Watch support
	watchMu sync.RWMutex
	watches map[int64]*watchSubscription

	// Raft 节点引用（用于获取状态信息）
	raftNode RaftNode
	nodeID   uint64
}

// watchSubscription represents a watch subscription
type watchSubscription struct {
	watchID   int64
	key       string
	rangeEnd  string
	startRev  int64
	eventCh   chan kvstore.WatchEvent
	cancel    chan struct{}
	closed    atomic.Bool // 防止重复关闭
	closeOnce sync.Once   // 确保只关闭一次

	// Options
	prevKV         bool
	progressNotify bool
	filters        []kvstore.WatchFilterType
	fragment       bool
}

// RaftOperation represents an operation to be committed through Raft
type RaftOperation struct {
	Type     string `json:"type"` // "PUT", "DELETE", "LEASE_GRANT", "LEASE_REVOKE", "TXN"
	Key      string `json:"key"`
	Value    string `json:"value"`
	LeaseID  int64  `json:"lease_id"`
	RangeEnd string `json:"range_end"`
	SeqNum   string `json:"seq_num"` // for sync wait

	// Lease operations
	TTL int64 `json:"ttl"`

	// Transaction operations
	Compares []kvstore.Compare `json:"compares,omitempty"`
	ThenOps  []kvstore.Op      `json:"then_ops,omitempty"`
	ElseOps  []kvstore.Op      `json:"else_ops,omitempty"`
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
	// Using default Sync=false for better write performance
	// Raft consensus provides durability through replication
	ro := grocksdb.NewDefaultReadOptions()

	r := &RocksDB{
		db:                db,
		proposeC:          proposeC,
		snapshotter:       snapshotter,
		wo:                wo,
		ro:                ro,
		pendingOps:        make(map[string]chan struct{}),
		pendingTxnResults: make(map[string]*kvstore.TxnResponse),
		watches:           make(map[int64]*watchSubscription),
	}

	// Recover from snapshot if exists
	snapshot, err := r.loadSnapshot()
	if err != nil {
		log.Fatal("Failed to load snapshot", zap.Error(err), zap.String("component", "storage-rocksdb"))
	}
	if snapshot != nil {
		log.Info("Loading RocksDB snapshot",
			zap.Uint64("term", snapshot.Metadata.Term),
			zap.Uint64("index", snapshot.Metadata.Index),
			zap.String("component", "storage-rocksdb"))
		if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Fatal("Failed to recover from snapshot", zap.Error(err), zap.String("component", "storage-rocksdb"))
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
				log.Fatal("Failed to reload snapshot", zap.Error(err), zap.String("component", "storage-rocksdb"))
			}
			if snapshot != nil {
				log.Info("Reloading RocksDB snapshot",
					zap.Uint64("term", snapshot.Metadata.Term),
					zap.Uint64("index", snapshot.Metadata.Index),
					zap.String("component", "storage-rocksdb"))
				if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Fatal("Failed to recover from reloaded snapshot", zap.Error(err), zap.String("component", "storage-rocksdb"))
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
		log.Fatal("Raft commit error", zap.Error(err), zap.String("component", "storage-rocksdb"))
	}
}

// applyOperation applies an etcd operation
func (r *RocksDB) applyOperation(op RaftOperation) {
	switch op.Type {
	case "PUT":
		// Apply PUT
		if err := r.putUnlocked(op.Key, op.Value, op.LeaseID); err != nil {
			log.Error("Failed to apply PUT operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("component", "storage-rocksdb"))
		}

	case "DELETE":
		// Apply DELETE
		if err := r.deleteUnlocked(op.Key, op.RangeEnd); err != nil {
			log.Error("Failed to apply DELETE operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("rangeEnd", op.RangeEnd),
				zap.String("component", "storage-rocksdb"))
		}

	case "LEASE_GRANT":
		// Apply Lease Grant
		if err := r.leaseGrantUnlocked(op.LeaseID, op.TTL); err != nil {
			log.Error("Failed to apply LEASE_GRANT operation",
				zap.Error(err),
				zap.Int64("leaseID", op.LeaseID),
				zap.Int64("ttl", op.TTL),
				zap.String("component", "storage-rocksdb"))
		}

	case "LEASE_REVOKE":
		// Apply Lease Revoke
		if err := r.leaseRevokeUnlocked(op.LeaseID); err != nil {
			log.Error("Failed to apply LEASE_REVOKE operation",
				zap.Error(err),
				zap.Int64("leaseID", op.LeaseID),
				zap.String("component", "storage-rocksdb"))
		}

	case "TXN":
		// Apply Transaction
		txnResp, err := r.txnUnlocked(op.Compares, op.ThenOps, op.ElseOps)
		if err != nil {
			log.Error("Failed to apply TXN operation",
				zap.Error(err),
				zap.Int("compareCount", len(op.Compares)),
				zap.Int("thenOpsCount", len(op.ThenOps)),
				zap.Int("elseOpsCount", len(op.ElseOps)),
				zap.String("component", "storage-rocksdb"))
		}
		// Save transaction result for client to read
		if op.SeqNum != "" && txnResp != nil {
			r.pendingMu.Lock()
			r.pendingTxnResults[op.SeqNum] = txnResp
			r.pendingMu.Unlock()
		}

	default:
		log.Warn("Unknown operation type",
			zap.String("type", op.Type),
			zap.String("component", "storage-rocksdb"))
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
		log.Fatal("Failed to decode legacy message",
			zap.Error(err),
			zap.String("component", "storage-rocksdb"))
	}

	// Convert to etcd operation
	if err := r.putUnlocked(dataKv.Key, dataKv.Val, 0); err != nil {
		log.Error("Failed to apply legacy PUT operation",
			zap.Error(err),
			zap.String("key", dataKv.Key),
			zap.String("component", "storage-rocksdb"))
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
func (r *RocksDB) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
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
func (r *RocksDB) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
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

	// Cleanup function to remove pending operation on error/timeout
	cleanup := func() {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
	}

	op := RaftOperation{
		Type:    "PUT",
		Key:     key,
		Value:   value,
		LeaseID: leaseID,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		cleanup()
		return 0, nil, err
	}

	// Use select to handle proposeC with timeout
	select {
	case r.proposeC <- string(data):
		// Successfully sent to Raft
	case <-ctx.Done():
		cleanup()
		return 0, nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return 0, nil, fmt.Errorf("timeout proposing operation to Raft")
	}

	// Wait for Raft commit with timeout
	select {
	case <-waitCh:
		// Raft commit completed
		currentRevision := r.CurrentRevision()
		return currentRevision, prevKv, nil
	case <-ctx.Done():
		cleanup()
		return 0, nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return 0, nil, fmt.Errorf("timeout waiting for Raft commit")
	}
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
func (r *RocksDB) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
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

	// Cleanup function to remove pending operation on error/timeout
	cleanup := func() {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
	}

	op := RaftOperation{
		Type:     "DELETE",
		Key:      key,
		RangeEnd: rangeEnd,
		SeqNum:   seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		cleanup()
		return 0, nil, 0, err
	}

	// Use select to handle proposeC with timeout
	select {
	case r.proposeC <- string(data):
		// Successfully sent to Raft
	case <-ctx.Done():
		cleanup()
		return 0, nil, 0, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return 0, nil, 0, fmt.Errorf("timeout proposing operation to Raft")
	}

	// Wait for Raft commit with timeout
	select {
	case <-waitCh:
		// Raft commit completed
		return deleted, prevKvs, r.CurrentRevision(), nil
	case <-ctx.Done():
		cleanup()
		return 0, nil, 0, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return 0, nil, 0, fmt.Errorf("timeout waiting for Raft commit")
	}
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
func (r *RocksDB) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
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

	// Cleanup function to remove pending operation on error/timeout
	cleanup := func() {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
	}

	op := RaftOperation{
		Type:    "LEASE_GRANT",
		LeaseID: id,
		TTL:     ttl,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		cleanup()
		return nil, err
	}

	// Use select to handle proposeC with timeout
	select {
	case r.proposeC <- string(data):
		// Successfully sent to Raft
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout proposing operation to Raft")
	}

	// Wait for Raft commit with timeout
	select {
	case <-waitCh:
		// Raft commit completed
		// Return lease info
		return r.getLease(id)
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout waiting for Raft commit")
	}
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
func (r *RocksDB) LeaseRevoke(ctx context.Context, id int64) error {
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

	// Cleanup function to remove pending operation on error/timeout
	cleanup := func() {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		r.pendingMu.Unlock()
	}

	op := RaftOperation{
		Type:    "LEASE_REVOKE",
		LeaseID: id,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		cleanup()
		return err
	}

	// Use select to handle proposeC with timeout
	select {
	case r.proposeC <- string(data):
		// Successfully sent to Raft
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return fmt.Errorf("timeout proposing operation to Raft")
	}

	// Wait for Raft commit with timeout
	select {
	case <-waitCh:
		// Raft commit completed
		return nil
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return fmt.Errorf("timeout waiting for Raft commit")
	}
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
			log.Error("Failed to delete key during lease revoke",
				zap.Error(err),
				zap.String("key", key),
				zap.Int64("leaseID", id),
				zap.String("component", "storage-rocksdb"))
		}
	}

	// Delete lease
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	return r.db.Delete(r.wo, dbKey)
}

// Watch creates a watch and returns an event channel
func (r *RocksDB) Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
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

	// 如果 startRevision > 0，发送历史事件
	// 注意：当前实现不保留完整历史，只能从当前数据生成初始快照
	if startRevision > 0 && startRevision < r.CurrentRevision() {
		// 异步发送当前所有匹配的键作为 PUT 事件
		go r.sendHistoricalEvents(sub, key, rangeEnd)
	}

	return eventCh, nil
}

// sendHistoricalEvents 发送历史事件（从当前数据快照）
func (r *RocksDB) sendHistoricalEvents(sub *watchSubscription, key, rangeEnd string) {
	// 使用 Range 查询获取所有匹配的键
	resp, err := r.Range(context.Background(), key, rangeEnd, 0, 0)
	if err != nil {
		log.Error("Failed to get historical events for watch",
			zap.Error(err),
			zap.Int64("watchID", sub.watchID),
			zap.String("key", key),
			zap.String("rangeEnd", rangeEnd),
			zap.String("component", "storage-rocksdb"))
		return
	}

	// 发送所有键作为 PUT 事件
	for _, kv := range resp.Kvs {
		event := kvstore.WatchEvent{
			Type:     kvstore.EventTypePut,
			Kv:       kv,
			PrevKv:   nil, // 历史事件不返回 prevKv
			Revision: kv.ModRevision,
		}

		// 非阻塞发送
		select {
		case sub.eventCh <- event:
			// 成功发送
		case <-sub.cancel:
			// Watch 已取消
			return
		default:
			// Channel 满了，跳过此事件
			log.Warn("Watch channel full, skipping historical event",
				zap.Int64("watchID", sub.watchID),
				zap.String("key", string(kv.Key)),
				zap.String("component", "storage-rocksdb"))
		}
	}
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
// Lightweight implementation that:
// 1. Records compacted revision for client query validation
// 2. Triggers RocksDB physical compaction (SST file merging)
// 3. Cleans up expired lease metadata
func (r *RocksDB) Compact(ctx context.Context, revision int64) error {
	currentRev := r.CurrentRevision()

	// Validation: cannot compact future revisions
	if revision > currentRev {
		return fmt.Errorf("cannot compact to future revision %d (current: %d)", revision, currentRev)
	}

	// Validation: cannot compact to revision 0 or negative
	if revision <= 0 {
		return fmt.Errorf("invalid compact revision: %d", revision)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Get current compacted revision
	compactedRev := r.getCompactedRevisionUnlocked()
	if revision <= compactedRev {
		return fmt.Errorf("already compacted to revision %d (requested: %d)", compactedRev, revision)
	}

	log.Info("Starting compact operation",
		zap.Int64("targetRevision", revision),
		zap.Int64("currentRevision", currentRev),
		zap.Int64("lastCompacted", compactedRev),
		zap.String("component", "storage-rocksdb"))

	startTime := time.Now()

	// 1. Record compacted revision
	if err := r.setCompactedRevisionUnlocked(revision); err != nil {
		return fmt.Errorf("failed to record compacted revision: %w", err)
	}

	// 2. Trigger RocksDB physical compaction (SST file merging)
	// This reclaims space from deleted keys and reduces read amplification
	startKey := []byte(kvPrefix)
	endKey := []byte(kvPrefix + "\xff")

	// CompactRange is asynchronous but we can wait for it
	r.db.CompactRange(grocksdb.Range{Start: startKey, Limit: endKey})

	// 3. Optional: Clean up expired leases (best effort)
	// This doesn't affect correctness but helps reclaim space
	cleanedLeases := r.cleanupExpiredLeasesUnlocked()

	duration := time.Since(startTime)
	log.Info("Compact operation completed",
		zap.Int64("revision", revision),
		zap.Duration("duration", duration),
		zap.Int("cleanedLeases", cleanedLeases),
		zap.String("component", "storage-rocksdb"))

	return nil
}

// getCompactedRevisionUnlocked reads the compacted revision from DB (caller must hold lock)
func (r *RocksDB) getCompactedRevisionUnlocked() int64 {
	key := []byte("meta:compacted_revision")
	value, err := r.db.Get(r.ro, key)
	if err != nil || value.Size() == 0 {
		return 0
	}
	defer value.Free()

	data := value.Data()
	if len(data) != 8 {
		return 0
	}

	return int64(binary.BigEndian.Uint64(data))
}

// setCompactedRevisionUnlocked writes the compacted revision to DB (caller must hold lock)
func (r *RocksDB) setCompactedRevisionUnlocked(revision int64) error {
	key := []byte("meta:compacted_revision")
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(revision))

	return r.db.Put(r.wo, key, value)
}

// cleanupExpiredLeasesUnlocked removes expired leases (caller must hold lock)
// Returns number of cleaned leases
func (r *RocksDB) cleanupExpiredLeasesUnlocked() int {
	cleaned := 0
	now := time.Now()

	// Iterate all leases
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	prefix := []byte(leasePrefix)
	for it.Seek(prefix); it.Valid() && bytes.HasPrefix(it.Key().Data(), prefix); it.Next() {
		// Decode lease
		var lease kvstore.Lease
		if err := gob.NewDecoder(bytes.NewReader(it.Value().Data())).Decode(&lease); err != nil {
			log.Warn("Failed to decode lease during cleanup",
				zap.Error(err),
				zap.String("component", "storage-rocksdb"))
			continue
		}

		// Check if expired
		elapsed := now.Sub(lease.GrantTime)
		if elapsed > time.Duration(lease.TTL)*time.Second {
			// Delete expired lease metadata
			// Note: Associated keys are already deleted by LeaseManager
			if err := r.db.Delete(r.wo, it.Key().Data()); err != nil {
				log.Warn("Failed to delete expired lease",
					zap.Error(err),
					zap.Int64("leaseID", lease.ID),
					zap.String("component", "storage-rocksdb"))
			} else {
				cleaned++
			}
		}
	}

	return cleaned
}

// LeaseRenew renews a lease
func (r *RocksDB) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
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
func (r *RocksDB) LeaseTimeToLive(ctx context.Context, id int64) (*kvstore.Lease, error) {
	return r.getLease(id)
}

// Leases returns all leases
func (r *RocksDB) Leases(ctx context.Context) ([]*kvstore.Lease, error) {
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
	r.PutWithLease(context.Background(), k, v, 0)
}

// Lookup looks up a key (for backward compatibility)
func (r *RocksDB) Lookup(key string) (string, bool) {
	kv, err := r.getKeyValue(key)
	if err != nil || kv == nil {
		return "", false
	}
	return string(kv.Value), true
}

// Txn executes a transaction (through Raft)
func (r *RocksDB) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
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

	// Cleanup function to remove pending operation on error/timeout
	cleanup := func() {
		r.pendingMu.Lock()
		delete(r.pendingOps, seqNum)
		delete(r.pendingTxnResults, seqNum)
		r.pendingMu.Unlock()
	}

	op := RaftOperation{
		Type:     "TXN",
		Compares: cmps,
		ThenOps:  thenOps,
		ElseOps:  elseOps,
		SeqNum:   seqNum,
	}

	// Serialize and propose
	data, err := json.Marshal(op)
	if err != nil {
		cleanup()
		return nil, err
	}

	// Use select to handle proposeC with timeout
	select {
	case r.proposeC <- string(data):
		// Successfully sent to Raft
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout proposing operation to Raft")
	}

	// Wait for Raft commit with timeout
	select {
	case <-waitCh:
		// Raft commit completed
		// Read transaction result
		r.pendingMu.Lock()
		txnResp := r.pendingTxnResults[seqNum]
		delete(r.pendingTxnResults, seqNum) // Clean up result
		r.pendingMu.Unlock()

		if txnResp == nil {
			return nil, fmt.Errorf("transaction result not found")
		}

		return txnResp, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout waiting for Raft commit")
	}
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
		log.Warn("Watch is too slow, force cancelling",
			zap.Int64("watchID", sub.watchID),
			zap.String("component", "storage-rocksdb"))
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

// txnUnlocked executes a transaction (called after Raft commit, must be called without external locks)
func (r *RocksDB) txnUnlocked(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// Evaluate all compare conditions
	succeeded := true
	for _, cmp := range cmps {
		if !r.evaluateCompare(cmp) {
			succeeded = false
			break
		}
	}

	// Choose operations to execute
	var ops []kvstore.Op
	if succeeded {
		ops = thenOps
	} else {
		ops = elseOps
	}

	// Execute operations
	responses := make([]kvstore.OpResponse, len(ops))
	for i, op := range ops {
		switch op.Type {
		case kvstore.OpRange:
			resp, err := r.Range(context.Background(), string(op.Key), string(op.RangeEnd), op.Limit, 0)
			if err != nil {
				return nil, err
			}
			responses[i] = kvstore.OpResponse{
				Type:      kvstore.OpRange,
				RangeResp: resp,
			}
		case kvstore.OpPut:
			// For txn operations, we need to call the unlocked version
			// Get previous value first
			prevKv, _ := r.getKeyValue(string(op.Key))

			// Apply put
			if err := r.putUnlocked(string(op.Key), string(op.Value), op.LeaseID); err != nil {
				return nil, err
			}

			responses[i] = kvstore.OpResponse{
				Type: kvstore.OpPut,
				PutResp: &kvstore.PutResponse{
					PrevKv:   prevKv,
					Revision: r.CurrentRevision(),
				},
			}
		case kvstore.OpDelete:
			// Get previous values first
			var deleted int64
			var prevKvs []*kvstore.KeyValue

			key := string(op.Key)
			rangeEnd := string(op.RangeEnd)

			if rangeEnd == "" {
				if kv, err := r.getKeyValue(key); err == nil && kv != nil {
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

			// Apply delete
			if err := r.deleteUnlocked(key, rangeEnd); err != nil {
				return nil, err
			}

			responses[i] = kvstore.OpResponse{
				Type: kvstore.OpDelete,
				DeleteResp: &kvstore.DeleteResponse{
					Deleted:  deleted,
					PrevKvs:  prevKvs,
					Revision: r.CurrentRevision(),
				},
			}
		}
	}

	return &kvstore.TxnResponse{
		Succeeded: succeeded,
		Responses: responses,
		Revision:  r.CurrentRevision(),
	}, nil
}

// evaluateCompare evaluates a compare condition
func (r *RocksDB) evaluateCompare(cmp kvstore.Compare) bool {
	kv, _ := r.getKeyValue(string(cmp.Key))
	exists := (kv != nil)

	switch cmp.Target {
	case kvstore.CompareVersion:
		v := int64(0)
		if exists {
			v = kv.Version
		}
		return r.compareInt(v, cmp.TargetUnion.Version, cmp.Result)
	case kvstore.CompareCreate:
		v := int64(0)
		if exists {
			v = kv.CreateRevision
		}
		return r.compareInt(v, cmp.TargetUnion.CreateRevision, cmp.Result)
	case kvstore.CompareMod:
		v := int64(0)
		if exists {
			v = kv.ModRevision
		}
		return r.compareInt(v, cmp.TargetUnion.ModRevision, cmp.Result)
	case kvstore.CompareValue:
		v := []byte{}
		if exists {
			v = kv.Value
		}
		return r.compareBytes(v, cmp.TargetUnion.Value, cmp.Result)
	case kvstore.CompareLease:
		v := int64(0)
		if exists {
			v = kv.Lease
		}
		return r.compareInt(v, cmp.TargetUnion.Lease, cmp.Result)
	}
	return false
}

// compareInt compares integers
func (r *RocksDB) compareInt(a, b int64, result kvstore.CompareResult) bool {
	switch result {
	case kvstore.CompareEqual:
		return a == b
	case kvstore.CompareGreater:
		return a > b
	case kvstore.CompareLess:
		return a < b
	case kvstore.CompareNotEqual:
		return a != b
	}
	return false
}

// compareBytes compares byte arrays
func (r *RocksDB) compareBytes(a, b []byte, result kvstore.CompareResult) bool {
	cmp := bytes.Compare(a, b)
	switch result {
	case kvstore.CompareEqual:
		return cmp == 0
	case kvstore.CompareGreater:
		return cmp > 0
	case kvstore.CompareLess:
		return cmp < 0
	case kvstore.CompareNotEqual:
		return cmp != 0
	}
	return false
}

// SetRaftNode 设置 Raft 节点引用（用于依赖注入）
func (r *RocksDB) SetRaftNode(node RaftNode, nodeID uint64) {
	r.raftNode = node
	r.nodeID = nodeID
}

// GetRaftStatus 获取 Raft 状态信息
func (r *RocksDB) GetRaftStatus() kvstore.RaftStatus {
	if r.raftNode == nil {
		// 如果没有 Raft 节点，返回默认状态（单机模式）
		return kvstore.RaftStatus{
			NodeID:   r.nodeID,
			Term:     0,
			LeaderID: 0,
			State:    "standalone",
			Applied:  0,
			Commit:   0,
		}
	}

	// 从 Raft 节点获取真实状态
	return r.raftNode.Status()
}

// TransferLeadership 转移 leader 角色到指定节点
func (r *RocksDB) TransferLeadership(targetID uint64) error {
	if r.raftNode == nil {
		return fmt.Errorf("raft node not available")
	}

	// 检查当前节点是否是 leader
	status := r.raftNode.Status()
	if status.LeaderID != r.nodeID {
		return fmt.Errorf("not leader, current leader: %d", status.LeaderID)
	}

	// 调用 Raft 节点的 TransferLeadership
	return r.raftNode.TransferLeadership(targetID)
}
