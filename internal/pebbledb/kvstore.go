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

package pebbledb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"metaStore/internal/common"
	"metaStore/internal/kvstore"
	"metaStore/internal/lease"
	"metaStore/pkg/log"

	"github.com/cockroachdb/pebble"
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

// RaftNode Raft nodeinterface，used to get Raft status
type RaftNode interface {
	Status() kvstore.RaftStatus
	TransferLeadership(targetID uint64) error
	LeaseManager() *lease.LeaseManager
	ReadIndexManager() *lease.ReadIndexManager
	LeaderChangeC() <-chan kvstore.RaftStatus
}

// PebbleDB integrates Raft consensus with etcd-compatible PebbleDB storage
type PebbleDB struct {
	db          *pebble.DB
	proposeC    chan<- string
	snapshotter *snap.Snapshotter

	wo *pebble.WriteOptions

	mu                sync.Mutex
	pendingMu         sync.RWMutex
	pendingOps        map[string]chan struct{}        // for sync wait
	pendingTxnResults map[string]*kvstore.TxnResponse // seqNum -> txn result
	seqNum            atomic.Int64                    // Atomic counter for sequence numbers

	// Watch support
	watchMu sync.RWMutex
	watches map[int64]*watchSubscription

	// Performance optimization: cached revision (atomic for lock-free access)
	cachedRevision atomic.Int64

	// Raft nodereference(used to getstatus info)
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
	closed    atomic.Bool // duplicateclose
	closeOnce sync.Once   // close first time

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
	TTL       int64 `json:"ttl"`
	GrantTime int64 `json:"grant_time,omitempty"` // unix nano, for WAL replay to preserve original GrantTime

	// Transaction operations
	Compares []kvstore.Compare `json:"compares,omitempty"`
	ThenOps  []kvstore.Op      `json:"then_ops,omitempty"`
	ElseOps  []kvstore.Op      `json:"else_ops,omitempty"`
}

// NewPebbleDB creates a new PebbleDB + Raft + etcd semantic storage
func NewPebbleDB(
	db *pebble.DB,
	snapshotter *snap.Snapshotter,
	proposeC chan<- string,
	commitC <-chan *kvstore.Commit,
	errorC <-chan error,
) *PebbleDB {
	// Apply Tier 6 optimizations (WAL + Block Cache + future Column Families)
	config := DefaultOptimizationConfig()

	wo := config.WriteOptions()

	r := &PebbleDB{
		db:                db,
		proposeC:          proposeC,
		snapshotter:       snapshotter,
		wo:                wo,
		pendingOps:        make(map[string]chan struct{}),
		pendingTxnResults: make(map[string]*kvstore.TxnResponse),
		watches:           make(map[int64]*watchSubscription),
	}

	// Recover from snapshot if exists
	snapshot, err := r.loadSnapshot()
	if err != nil {
		log.Fatal("Failed to load snapshot", zap.Error(err), zap.String("component", "storage-pebble"))
	}
	if snapshot != nil {
		log.Info("Loading PebbleDB snapshot",
			zap.Uint64("term", snapshot.Metadata.Term),
			zap.Uint64("index", snapshot.Metadata.Index),
			zap.String("component", "storage-pebble"))
		if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Fatal("Failed to recover from snapshot", zap.Error(err), zap.String("component", "storage-pebble"))
		}
	}

	// Initialize cached revision from DB
	r.cachedRevision.Store(r.loadCurrentRevision())

	// Start commit handler
	go r.readCommits(commitC, errorC)

	return r
}

func (r *PebbleDB) Close() {
	// Pebble WriteOptions are simple values, no Destroy needed
}

func (r *PebbleDB) propose(ctx context.Context, data []byte) error {

	// aftercompatible：usestart proposeC
	select {
	case r.proposeC <- string(data):
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timeout proposing operation")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readCommits reads from Raft commitC and applies operations
func (r *PebbleDB) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			// Reload snapshot
			snapshot, err := r.loadSnapshot()
			if err != nil {
				log.Fatal("Failed to reload snapshot", zap.Error(err), zap.String("component", "storage-pebble"))
			}
			if snapshot != nil {
				log.Info("Reloading PebbleDB snapshot",
					zap.Uint64("term", snapshot.Metadata.Term),
					zap.Uint64("index", snapshot.Metadata.Index),
					zap.String("component", "storage-pebble"))
				if err := r.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Fatal("Failed to recover from reloaded snapshot", zap.Error(err), zap.String("component", "storage-pebble"))
				}
			}
			continue
		}

		// Collect all operations from this commit for batch processing
		var batchOps []*RaftOperation

		for _, data := range commit.Data {
			if ops, err := unmarshalRaftMessage([]byte(data)); err == nil && ops != nil {
				// Try RaftMessage format (supports both single and batch operations)
				// supportedoldformat(aftercompatible)
				batchOps = append(batchOps, ops...)
			} else if op, err := unmarshalRaftOperation([]byte(data)); err == nil && op != nil {
				// Fallback to single operation format (backward compatibility)
				batchOps = append(batchOps, op)
			} else {
				// Fallback to legacy gob format (for backward compatibility)
				r.applyLegacyOp(data)
			}
		}

		// Apply all operations in a single WriteBatch for maximum performance
		if len(batchOps) > 0 {
			r.applyOperationsBatch(batchOps)
		}
		close(commit.ApplyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal("Raft commit error", zap.Error(err), zap.String("component", "storage-pebble"))
	}
}

// applyOperation applies an etcd operation
func (r *PebbleDB) applyOperation(op RaftOperation) {
	switch op.Type {
	case "PUT":
		// Apply PUT
		if err := r.putUnlocked(op.Key, op.Value, op.LeaseID); err != nil {
			log.Error("Failed to apply PUT operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("component", "storage-pebble"))
		}

	case "DELETE":
		// Apply DELETE
		if err := r.deleteUnlocked(op.Key, op.RangeEnd); err != nil {
			log.Error("Failed to apply DELETE operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("rangeEnd", op.RangeEnd),
				zap.String("component", "storage-pebble"))
		}

	case "LEASE_GRANT":
		// Apply Lease Grant (preserve original GrantTime for WAL replay correctness)
		if err := r.leaseGrantUnlockedWithTime(op.LeaseID, op.TTL, op.GrantTime); err != nil {
			log.Error("Failed to apply LEASE_GRANT operation",
				zap.Error(err),
				zap.Int64("leaseID", op.LeaseID),
				zap.Int64("ttl", op.TTL),
				zap.String("component", "storage-pebble"))
		}

	case "LEASE_REVOKE":
		// Apply Lease Revoke
		if err := r.leaseRevokeUnlocked(op.LeaseID); err != nil {
			log.Error("Failed to apply LEASE_REVOKE operation",
				zap.Error(err),
				zap.Int64("leaseID", op.LeaseID),
				zap.String("component", "storage-pebble"))
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
				zap.String("component", "storage-pebble"))
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
			zap.String("component", "storage-pebble"))
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

// applyOperationsBatch applies multiple operations using a single WriteBatch
// This significantly reduces the number of fsync calls and improves throughput
func (r *PebbleDB) applyOperationsBatch(ops []*RaftOperation) {
	if len(ops) == 0 {
		return
	}

	// Create a single WriteBatch for all operations
	batch := r.db.NewBatch()
	defer batch.Close()

	// Track watch events to emit after batch write completes
	var watchEvents []kvstore.WatchEvent

	// Process each operation and add to batch
	for _, op := range ops {
		switch op.Type {
		case "PUT":
			events, err := r.preparePutBatch(batch, op.Key, op.Value, op.LeaseID)
			if err != nil {
				log.Error("Failed to prepare PUT in batch",
					zap.Error(err),
					zap.String("key", op.Key),
					zap.String("component", "storage-pebble"))
				continue
			}
			watchEvents = append(watchEvents, events...)

		case "DELETE":
			events, err := r.prepareDeleteBatch(batch, op.Key, op.RangeEnd)
			if err != nil {
				log.Error("Failed to prepare DELETE in batch",
					zap.Error(err),
					zap.String("key", op.Key),
					zap.String("component", "storage-pebble"))
				continue
			}
			watchEvents = append(watchEvents, events...)

		case "LEASE_GRANT":
			if err := r.prepareLeaseGrantBatchWithTime(batch, op.LeaseID, op.TTL, op.GrantTime); err != nil {
				log.Error("Failed to prepare LEASE_GRANT in batch",
					zap.Error(err),
					zap.Int64("leaseID", op.LeaseID),
					zap.String("component", "storage-pebble"))
			}

		case "LEASE_REVOKE":
			events, err := r.prepareLeaseRevokeBatch(batch, op.LeaseID)
			if err != nil {
				log.Error("Failed to prepare LEASE_REVOKE in batch",
					zap.Error(err),
					zap.Int64("leaseID", op.LeaseID),
					zap.String("component", "storage-pebble"))
				continue
			}
			watchEvents = append(watchEvents, events...)

		case "TXN":
			// Transactions need special handling - apply individually for now
			// TODO: Optimize transaction batching in future
			txnResp, err := r.txnUnlocked(op.Compares, op.ThenOps, op.ElseOps)
			if err != nil {
				log.Error("Failed to apply TXN in batch",
					zap.Error(err),
					zap.String("component", "storage-pebble"))
			}
			if op.SeqNum != "" && txnResp != nil {
				r.pendingMu.Lock()
				r.pendingTxnResults[op.SeqNum] = txnResp
				r.pendingMu.Unlock()
			}
		}
	}

	// Atomic write of all operations in one fsync
	if err := batch.Commit(r.wo); err != nil {
		log.Error("Failed to write batch",
			zap.Error(err),
			zap.Int("batch_size", len(ops)),
			zap.String("component", "storage-pebble"))
		return
	}

	// Notify waiting clients AFTER successful batch write
	// This ensures data is committed before clients read it
	for _, op := range ops {
		if op.SeqNum != "" {
			r.pendingMu.Lock()
			if ch, exists := r.pendingOps[op.SeqNum]; exists {
				close(ch)
				delete(r.pendingOps, op.SeqNum)
			}
			r.pendingMu.Unlock()
		}
	}

	// Emit all watch events after successful write
	for _, event := range watchEvents {
		r.notifyWatches(event)
	}

	log.Debug("Applied operations batch",
		zap.Int("batch_size", len(ops)),
		zap.String("component", "storage-pebble"))
}

// applyLegacyOp applies legacy gob-encoded operation (for backward compatibility)
func (r *PebbleDB) applyLegacyOp(data string) {
	var dataKv kvstore.KV
	dec := gob.NewDecoder(bytes.NewBufferString(data))
	if err := dec.Decode(&dataKv); err != nil {
		log.Fatal("Failed to decode legacy message",
			zap.Error(err),
			zap.String("component", "storage-pebble"))
	}

	// Convert to etcd operation
	if err := r.putUnlocked(dataKv.Key, dataKv.Val, 0); err != nil {
		log.Error("Failed to apply legacy PUT operation",
			zap.Error(err),
			zap.String("key", dataKv.Key),
			zap.String("component", "storage-pebble"))
	}
}

// loadCurrentRevision loads the current revision from DB (used during initialization)
func (r *PebbleDB) loadCurrentRevision() int64 {
	val, closer, err := r.db.Get([]byte(revisionKey))
	if err != nil {
		if err == pebble.ErrNotFound {
			return 0
		}
		return 0
	}
	data := make([]byte, len(val))
	copy(data, val)
	closer.Close()

	if len(data) == 0 {
		return 0
	}

	var rev int64
	buf := bytes.NewBuffer(data)
	if err := gob.NewDecoder(buf).Decode(&rev); err != nil {
		return 0
	}
	return rev
}

// CurrentRevision returns current revision (lock-free cached version)
func (r *PebbleDB) CurrentRevision() int64 {
	return r.cachedRevision.Load()
}

// incrementRevision increments and returns new revision
func (r *PebbleDB) incrementRevision() (int64, error) {
	// Atomically increment cached revision
	rev := r.cachedRevision.Add(1)

	// Persist to DB using binary encoding for efficiency
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(rev))

	if err := r.db.Set([]byte(revisionKey), buf, r.wo); err != nil {
		// Rollback cache on error
		r.cachedRevision.Add(-1)
		return 0, err
	}

	return rev, nil
}

// Range performs range query
func (r *PebbleDB) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	// convertas RangeWithOptions call
	return r.RangeWithOptions(ctx, key, rangeEnd, kvstore.RangeOptions{
		Limit:    limit,
		Revision: revision,
	})
}

// RangeWithOptions performs range query with full options support
func (r *PebbleDB) RangeWithOptions(ctx context.Context, key, rangeEnd string, opts kvstore.RangeOptions) (*kvstore.RangeResponse, error) {
	// Lease Read optimize: checkisnocanusefastpath
	if r.raftNode != nil {
		leaseManager := r.raftNode.LeaseManager()
		readIndexManager := r.raftNode.ReadIndexManager()

		if leaseManager != nil && readIndexManager != nil {
			// Fast Path: Leader havevalidlease
			if leaseManager.IsLeader() && leaseManager.HasValidLease() {
				// recordfastpathread
				readIndexManager.RecordFastPathRead()
				// continueexecutenextread(already leasecertifyfirst)
			}
			// Slow Path:  Leader orlease
			// TODO: implement ReadIndex protocolorto Leader
			// currenttransformimplement：read(incompleteimplementbeforeholdaftercompatible)
		}
	}

	// Pre-allocate slice with estimated capacity
	estimatedCap := 100
	if opts.Limit > 0 && opts.Limit < 100 {
		estimatedCap = int(opts.Limit)
	}
	kvs := make([]*kvstore.KeyValue, 0, estimatedCap)

	// Single key query
	if rangeEnd == "" {
		kv, err := r.getKeyValue(key)
		if err == nil && kv != nil {
			kvs = append(kvs, kv)
		}
	} else {
		// Range query
		prefix := []byte(kvPrefix)
		it, err := r.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: prefixUpperBound(prefix),
		})
		if err != nil {
			return nil, err
		}
		defer it.Close()

		startKey := []byte(kvPrefix + key)
		it.SeekGE(startKey)

		for ; it.Valid(); it.Next() {
			keyBytes := make([]byte, len(it.Key()))
			copy(keyBytes, it.Key())
			k := string(keyBytes)
			k = k[len(kvPrefix):] // Remove prefix

			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				// Use optimized binary decoding instead of gob
				valBytes := make([]byte, len(it.Value()))
				copy(valBytes, it.Value())
				kv, err := decodeKeyValue(valBytes)
				if err == nil && kv != nil {
					kvs = append(kvs, kv)
				}
			}

			if rangeEnd != "\x00" && k >= rangeEnd {
				break
			}
		}
	}

	// Apply CreateRevision filter
	if opts.MaxCreateRevision > 0 || opts.MinCreateRevision > 0 {
		filtered := make([]*kvstore.KeyValue, 0, len(kvs))
		for _, kv := range kvs {
			if opts.MaxCreateRevision > 0 && kv.CreateRevision > opts.MaxCreateRevision {
				continue
			}
			if opts.MinCreateRevision > 0 && kv.CreateRevision < opts.MinCreateRevision {
				continue
			}
			filtered = append(filtered, kv)
		}
		kvs = filtered
	}

	// Apply ModRevision filter
	if opts.MaxModRevision > 0 || opts.MinModRevision > 0 {
		filtered := make([]*kvstore.KeyValue, 0, len(kvs))
		for _, kv := range kvs {
			if opts.MaxModRevision > 0 && kv.ModRevision > opts.MaxModRevision {
				continue
			}
			if opts.MinModRevision > 0 && kv.ModRevision < opts.MinModRevision {
				continue
			}
			filtered = append(filtered, kv)
		}
		kvs = filtered
	}

	// Apply sorting
	if opts.SortOrder != kvstore.SortNone && len(kvs) > 1 {
		r.sortKvs(kvs, opts.SortTarget, opts.SortOrder)
	} else if len(kvs) > 1 {
		// Default sort by key
		sort.Slice(kvs, func(i, j int) bool {
			return string(kvs[i].Key) < string(kvs[j].Key)
		})
	}

	// Calculate count before applying limit
	count := int64(len(kvs))

	// CountOnly: only return count
	if opts.CountOnly {
		return &kvstore.RangeResponse{
			Kvs:      nil,
			More:     false,
			Count:    count,
			Revision: r.CurrentRevision(),
		}, nil
	}

	// Apply limit
	more := false
	if opts.Limit > 0 && int64(len(kvs)) > opts.Limit {
		kvs = kvs[:opts.Limit]
		more = true
	}

	// KeysOnly: clear values
	if opts.KeysOnly {
		for _, kv := range kvs {
			kv.Value = nil
		}
	}

	return &kvstore.RangeResponse{
		Kvs:      kvs,
		More:     more,
		Count:    count,
		Revision: r.CurrentRevision(),
	}, nil
}

// sortKvs sorts key-value pairs according to target and order
func (r *PebbleDB) sortKvs(kvs []*kvstore.KeyValue, target kvstore.SortTarget, order kvstore.SortOrder) {
	less := func(i, j int) bool {
		var cmp int
		switch target {
		case kvstore.SortByKey:
			cmp = bytes.Compare(kvs[i].Key, kvs[j].Key)
		case kvstore.SortByCreate:
			cmp = int(kvs[i].CreateRevision - kvs[j].CreateRevision)
		case kvstore.SortByMod:
			cmp = int(kvs[i].ModRevision - kvs[j].ModRevision)
		case kvstore.SortByVersion:
			cmp = int(kvs[i].Version - kvs[j].Version)
		case kvstore.SortByValue:
			cmp = bytes.Compare(kvs[i].Value, kvs[j].Value)
		default:
			cmp = bytes.Compare(kvs[i].Key, kvs[j].Key)
		}
		if order == kvstore.SortDescend {
			return cmp > 0
		}
		return cmp < 0
	}

	sort.Slice(kvs, less)
}

// PutWithLease stores key-value with optional lease
func (r *PebbleDB) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// Check prevKv before submitting to Raft
	prevKv, _ := r.getKeyValue(key)

	// Generate sequence number (lock-free atomic operation)
	seq := r.seqNum.Add(1)
	seqNum := fmt.Sprintf("seq-%d", seq)

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

	data, err := marshalRaftOperation(&op)
	if err != nil {
		cleanup()
		return 0, nil, err
	}

	// Use BatchProposer for improved throughput (firstuse propose auxiliarymethod)
	if err := r.propose(ctx, data); err != nil {
		cleanup()
		return 0, nil, err
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

// preparePutBatch prepares a PUT operation to be added to a WriteBatch
// Returns watch events to be emitted after batch write succeeds
func (r *PebbleDB) preparePutBatch(batch *pebble.Batch, key, value string, leaseID int64) ([]kvstore.WatchEvent, error) {
	// Get previous KeyValue
	prevKv, _ := r.getKeyValue(key)

	// Increment revision
	newRevision, err := r.incrementRevision()
	if err != nil {
		return nil, err
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

	// Serialize using optimized binary encoding
	encodedKV, err := encodeKeyValue(kv)
	if err != nil {
		return nil, err
	}

	// Add to batch
	dbKey := []byte(kvPrefix + key)
	batch.Set(dbKey, encodedKV, nil)

	// Update lease's key tracking if leaseID is specified
	if leaseID != 0 {
		lease, err := r.getLease(leaseID)
		if err != nil {
			return nil, fmt.Errorf("failed to get lease %d: %v", leaseID, err)
		}
		if lease != nil {
			// Add key to lease's key set
			if lease.Keys == nil {
				lease.Keys = make(map[string]bool)
			}
			lease.Keys[key] = true

			// Save updated lease to batch - use Protobuf(20x performance)
			leaseData, err := common.SerializeLease(lease)
			if err != nil {
				return nil, fmt.Errorf("failed to encode lease: %v", err)
			}

			leaseKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
			batch.Set(leaseKey, leaseData, nil)
		}
	}

	// Prepare watch event (to be emitted after successful write)
	event := kvstore.WatchEvent{
		Type:     kvstore.EventTypePut,
		Kv:       kv,
		PrevKv:   prevKv,
		Revision: newRevision,
	}

	return []kvstore.WatchEvent{event}, nil
}

// prepareDeleteBatch prepares a DELETE operation to be added to a WriteBatch
// Returns watch events to be emitted after batch write succeeds
func (r *PebbleDB) prepareDeleteBatch(batch *pebble.Batch, key, rangeEnd string) ([]kvstore.WatchEvent, error) {
	// Get revision for watch events
	newRevision, err := r.incrementRevision()
	if err != nil {
		return nil, err
	}

	var events []kvstore.WatchEvent

	if rangeEnd == "" {
		// Single key delete - get old value first for watch event
		prevKv, _ := r.getKeyValue(key)

		dbKey := []byte(kvPrefix + key)
		batch.Delete(dbKey, nil)

		// Prepare watch event if key existed
		if prevKv != nil {
			deletedKv := &kvstore.KeyValue{
				Key:            prevKv.Key,
				Value:          nil,
				CreateRevision: prevKv.CreateRevision,
				ModRevision:    newRevision,
				Version:        0,
				Lease:          0,
			}
			events = append(events, kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   prevKv,
				Revision: newRevision,
			})
		}

		return events, nil
	}

	// Range delete - collect old values first
	startKey := []byte(kvPrefix + key)
	endKey := []byte(kvPrefix + rangeEnd)

	it, err := r.db.NewIter(&pebble.IterOptions{
		LowerBound: startKey,
		UpperBound: endKey,
	})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var toDelete []string
	for it.First(); it.Valid(); it.Next() {
		k := make([]byte, len(it.Key()))
		copy(k, it.Key())

		// Extract actual key (remove prefix)
		actualKey := string(k[len(kvPrefix):])
		toDelete = append(toDelete, actualKey)
	}

	// Delete all keys in range
	for _, actualKey := range toDelete {
		prevKv, _ := r.getKeyValue(actualKey)

		dbKey := []byte(kvPrefix + actualKey)
		batch.Delete(dbKey, nil)

		// Prepare watch event
		if prevKv != nil {
			deletedKv := &kvstore.KeyValue{
				Key:            prevKv.Key,
				Value:          nil,
				CreateRevision: prevKv.CreateRevision,
				ModRevision:    newRevision,
				Version:        0,
				Lease:          0,
			}
			events = append(events, kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   prevKv,
				Revision: newRevision,
			})
		}
	}

	return events, nil
}

// prepareLeaseGrantBatch prepares a LEASE_GRANT operation to be added to a WriteBatch
func (r *PebbleDB) prepareLeaseGrantBatch(batch *pebble.Batch, leaseID, ttl int64) error {
	return r.prepareLeaseGrantBatchWithTime(batch, leaseID, ttl, 0)
}

// prepareLeaseGrantBatchWithTime prepares a LEASE_GRANT with an explicit GrantTime.
func (r *PebbleDB) prepareLeaseGrantBatchWithTime(batch *pebble.Batch, leaseID, ttl int64, grantTimeNano int64) error {
	grantTime := timeNow()
	if grantTimeNano > 0 {
		grantTime = time.Unix(0, grantTimeNano)
	}

	lease := &kvstore.Lease{
		ID:        leaseID,
		TTL:       ttl,
		GrantTime: grantTime,
		Keys:      make(map[string]bool),
	}

	// use Protobuf serialize(20x performance)
	data, err := common.SerializeLease(lease)
	if err != nil {
		return fmt.Errorf("failed to encode lease: %v", err)
	}

	leaseKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
	batch.Set(leaseKey, data, nil)

	return nil
}

// prepareLeaseRevokeBatch prepares a LEASE_REVOKE operation to be added to a WriteBatch
// Returns watch events to be emitted after batch write succeeds
func (r *PebbleDB) prepareLeaseRevokeBatch(batch *pebble.Batch, leaseID int64) ([]kvstore.WatchEvent, error) {
	// Get the lease to find associated keys
	lease, err := r.getLease(leaseID)
	if err != nil {
		return nil, fmt.Errorf("failed to get lease %d: %v", leaseID, err)
	}

	if lease == nil {
		// Lease doesn't exist, nothing to revoked
		return nil, nil
	}

	var events []kvstore.WatchEvent

	// Delete all keys associated with this lease and prepare watch events
	for key := range lease.Keys {
		// Get old value first for watch event
		prevKv, _ := r.getKeyValue(key)

		dbKey := []byte(kvPrefix + key)
		batch.Delete(dbKey, nil)

		// Prepare watch event if key existed
		if prevKv != nil {
			// Get revision for watch event
			newRevision, err := r.incrementRevision()
			if err != nil {
				return nil, err
			}

			deletedKv := &kvstore.KeyValue{
				Key:            prevKv.Key,
				Value:          nil,
				CreateRevision: prevKv.CreateRevision,
				ModRevision:    newRevision,
				Version:        0,
				Lease:          0,
			}
			events = append(events, kvstore.WatchEvent{
				Type:     kvstore.EventTypeDelete,
				Kv:       deletedKv,
				PrevKv:   prevKv,
				Revision: newRevision,
			})
		}
	}

	// Delete the lease itself
	leaseKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
	batch.Delete(leaseKey, nil)

	return events, nil
}

// putUnlocked applies put operation (called after Raft commit)
func (r *PebbleDB) putUnlocked(key, value string, leaseID int64) error {
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

	// Serialize using optimized binary encoding
	encodedKV, err := encodeKeyValue(kv)
	if err != nil {
		return err
	}

	// Use WriteBatch for atomic multi-key operations
	batch := r.db.NewBatch()
	defer batch.Close()

	dbKey := []byte(kvPrefix + key)
	batch.Set(dbKey, encodedKV, nil)

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

			// Save updated lease - use Protobuf(20x performance)
			leaseData, err := common.SerializeLease(lease)
			if err != nil {
				return fmt.Errorf("failed to encode lease: %v", err)
			}

			leaseKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
			batch.Set(leaseKey, leaseData, nil)
		}
	}

	// Atomic commit of all operations
	if err := batch.Commit(r.wo); err != nil {
		return err
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
func (r *PebbleDB) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
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
		prefix := []byte(kvPrefix)
		it, err := r.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: prefixUpperBound(prefix),
		})
		if err != nil {
			return 0, nil, 0, err
		}
		defer it.Close()

		startKey := []byte(kvPrefix + key)
		it.SeekGE(startKey)

		for ; it.Valid(); it.Next() {
			keyBytes := make([]byte, len(it.Key()))
			copy(keyBytes, it.Key())
			k := string(keyBytes)
			k = k[len(kvPrefix):]

			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				valBytes := make([]byte, len(it.Value()))
				copy(valBytes, it.Value())
				var kv kvstore.KeyValue
				if err := gob.NewDecoder(bytes.NewBuffer(valBytes)).Decode(&kv); err == nil {
					deleted++
					prevKvs = append(prevKvs, &kv)
				}
			}

			if rangeEnd != "\x00" && k >= rangeEnd {
				break
			}
		}
	}

	if deleted == 0 {
		return 0, nil, r.CurrentRevision(), nil
	}

	// Generate sequence number (lock-free atomic operation)
	seq := r.seqNum.Add(1)
	seqNum := fmt.Sprintf("seq-%d", seq)

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

	data, err := marshalRaftOperation(&op)
	if err != nil {
		cleanup()
		return 0, nil, 0, err
	}

	// Use BatchProposer for improved throughput (firstuse propose auxiliarymethod)
	if err := r.propose(ctx, data); err != nil {
		cleanup()
		return 0, nil, 0, err
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
func (r *PebbleDB) deleteUnlocked(key, rangeEnd string) error {
	// Get revision for watch events
	newRevision, err := r.incrementRevision()
	if err != nil {
		return err
	}

	if rangeEnd == "" {
		// Single key delete - get old value first for watch event
		prevKv, _ := r.getKeyValue(key)

		dbKey := []byte(kvPrefix + key)
		if err := r.db.Delete(dbKey, r.wo); err != nil {
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
	prefix := []byte(kvPrefix)
	it, err := r.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return err
	}
	defer it.Close()

	wb := r.db.NewBatch()
	defer wb.Close()

	var deletedKeys []*kvstore.KeyValue

	startKey := []byte(kvPrefix + key)
	it.SeekGE(startKey)

	for ; it.Valid(); it.Next() {
		keyBytes := make([]byte, len(it.Key()))
		copy(keyBytes, it.Key())
		k := string(keyBytes)
		k = k[len(kvPrefix):]

		if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
			// Get old value for watch event
			valBytes := make([]byte, len(it.Value()))
			copy(valBytes, it.Value())
			var kv kvstore.KeyValue
			if err := gob.NewDecoder(bytes.NewBuffer(valBytes)).Decode(&kv); err == nil {
				deletedKeys = append(deletedKeys, &kv)
			}
			wb.Delete(keyBytes, nil)
		}

		if rangeEnd != "\x00" && k >= rangeEnd {
			break
		}
	}

	if err := wb.Commit(r.wo); err != nil {
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
func (r *PebbleDB) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	// Generate sequence number (lock-free atomic operation)
	seq := r.seqNum.Add(1)
	seqNum := fmt.Sprintf("seq-%d", seq)

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
		Type:      "LEASE_GRANT",
		LeaseID:   id,
		TTL:       ttl,
		GrantTime: timeNow().UnixNano(),
		SeqNum:    seqNum,
	}

	data, err := marshalRaftOperation(&op)
	if err != nil {
		cleanup()
		return nil, err
	}

	// Use BatchProposer for improved throughput (firstuse propose auxiliarymethod)
	if err := r.propose(ctx, data); err != nil {
		cleanup()
		return nil, err
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
func (r *PebbleDB) leaseGrantUnlocked(id int64, ttl int64) error {
	return r.leaseGrantUnlockedWithTime(id, ttl, 0)
}

// leaseGrantUnlockedWithTime applies lease grant with an explicit GrantTime.
// grantTimeNano is the unix nano timestamp from the Raft log entry.
// Using the original GrantTime ensures WAL replay does not reset the lease TTL.
func (r *PebbleDB) leaseGrantUnlockedWithTime(id int64, ttl int64, grantTimeNano int64) error {
	grantTime := timeNow()
	if grantTimeNano > 0 {
		grantTime = time.Unix(0, grantTimeNano)
	}

	lease := &kvstore.Lease{
		ID:        id,
		TTL:       ttl,
		GrantTime: grantTime,
		Keys:      make(map[string]bool),
	}

	// use Protobuf serialize(20x performance)
	data, err := common.SerializeLease(lease)
	if err != nil {
		return err
	}

	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	return r.db.Set(dbKey, data, r.wo)
}

// LeaseRevoke revokeds a lease
func (r *PebbleDB) LeaseRevoke(ctx context.Context, id int64) error {
	// Generate sequence number (lock-free atomic operation)
	seq := r.seqNum.Add(1)
	seqNum := fmt.Sprintf("seq-%d", seq)

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

	data, err := marshalRaftOperation(&op)
	if err != nil {
		cleanup()
		return err
	}

	// Use BatchProposer for improved throughput (firstuse propose auxiliarymethod)
	if err := r.propose(ctx, data); err != nil {
		log.Errorf("failed to propose revoke lease: %v", err)
		cleanup()
		return err
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

// leaseRevokeUnlocked applies lease revoked (called after Raft commit)
func (r *PebbleDB) leaseRevokeUnlocked(id int64) error {
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
			log.Error("Failed to delete key during lease revoked",
				zap.Error(err),
				zap.String("key", key),
				zap.Int64("leaseID", id),
				zap.String("component", "storage-pebble"))
		}
	}

	// Delete lease
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	return r.db.Delete(dbKey, r.wo)
}

// Watch creates a watch and returns an event channel
func (r *PebbleDB) Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return r.WatchWithOptions(key, rangeEnd, startRevision, watchID, nil)
}

// WatchWithOptions creates a watch with options
func (r *PebbleDB) WatchWithOptions(key, rangeEnd string, startRevision int64, watchID int64, opts *kvstore.WatchOptions) (<-chan kvstore.WatchEvent, error) {
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

	// if startRevision > 0，sendevent
	// note：currentimplementnotcomplete，canfromcurrentdatabecomeinitialsnapshot
	if startRevision > 0 && startRevision < r.CurrentRevision() {
		// asynchronoussendcurrentallmatchkeyas PUT event
		go r.sendHistoricalEvents(sub, key, rangeEnd)
	}

	return eventCh, nil
}

// sendHistoricalEvents sendevent(fromcurrentdatasnapshot)
func (r *PebbleDB) sendHistoricalEvents(sub *watchSubscription, key, rangeEnd string) {
	// use Range querygetallmatchkey
	resp, err := r.Range(context.Background(), key, rangeEnd, 0, 0)
	if err != nil {
		log.Error("Failed to get historical events for watch",
			zap.Error(err),
			zap.Int64("watchID", sub.watchID),
			zap.String("key", key),
			zap.String("rangeEnd", rangeEnd),
			zap.String("component", "storage-pebble"))
		return
	}

	// sendallkeyas PUT event
	for _, kv := range resp.Kvs {
		event := kvstore.WatchEvent{
			Type:     kvstore.EventTypePut,
			Kv:       kv,
			PrevKv:   nil, // eventnotreturn prevKv
			Revision: kv.ModRevision,
		}

		// non-blockingsend
		select {
		case sub.eventCh <- event:
			// successsend
		case <-sub.cancel:
			// Watch already cancel
			return
		default:
			// Channel full，skipevent
			log.Warn("Watch channel full, skipping historical event",
				zap.Int64("watchID", sub.watchID),
				zap.String("key", string(kv.Key)),
				zap.String("component", "storage-pebble"))
		}
	}
}

// CancelWatch cancels a watch
func (r *PebbleDB) CancelWatch(watchID int64) error {
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
// 2. Triggers Pebble physical compaction (SST file merging)
// 3. Cleans up expired lease metadata
func (r *PebbleDB) Compact(ctx context.Context, revision int64) error {
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
		zap.String("component", "storage-pebble"))

	startTime := time.Now()

	// 1. Record compacted revision
	if err := r.setCompactedRevisionUnlocked(revision); err != nil {
		return fmt.Errorf("failed to record compacted revision: %w", err)
	}

	// 2. Trigger Pebble physical compaction (SST file merging)
	// This reclaims space from deleted keys and reduces read amplification
	startKey := []byte(kvPrefix)
	endKey := []byte(kvPrefix + "\xff")

	// Compact is asynchronous but we can wait for it
	r.db.Compact(startKey, endKey, true)

	// 3. Optional: Clean up expired leases (best effort)
	// This doesn't affect correctness but helps reclaim space
	cleanedLeases := r.cleanupExpiredLeasesUnlocked()

	duration := time.Since(startTime)
	log.Info("Compact operation completed",
		zap.Int64("revision", revision),
		zap.Duration("duration", duration),
		zap.Int("cleanedLeases", cleanedLeases),
		zap.String("component", "storage-pebble"))

	return nil
}

// getCompactedRevisionUnlocked reads the compacted revision from DB (caller must hold lock)
func (r *PebbleDB) getCompactedRevisionUnlocked() int64 {
	key := []byte("meta:compacted_revision")
	val, closer, err := r.db.Get(key)
	if err != nil {
		return 0
	}
	data := make([]byte, len(val))
	copy(data, val)
	closer.Close()

	if len(data) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(data))
}

// setCompactedRevisionUnlocked writes the compacted revision to DB (caller must hold lock)
func (r *PebbleDB) setCompactedRevisionUnlocked(revision int64) error {
	key := []byte("meta:compacted_revision")
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(revision))

	return r.db.Set(key, value, r.wo)
}

// cleanupExpiredLeasesUnlocked removes expired leases (caller must hold lock)
// Returns number of cleaned leases
func (r *PebbleDB) cleanupExpiredLeasesUnlocked() int {
	cleaned := 0
	now := time.Now()

	// Iterate all leases
	prefix := []byte(leasePrefix)
	it, err := r.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return 0
	}
	defer it.Close()

	// Collect keys to delete (cannot delete while iterating)
	var toDelete [][]byte

	for it.First(); it.Valid(); it.Next() {
		// Decode lease - use Protobuf(testformat，aftercompatible)
		valBytes := make([]byte, len(it.Value()))
		copy(valBytes, it.Value())
		lease, err := common.DeserializeLease(valBytes)
		if err != nil {
			log.Warn("Failed to decode lease during cleanup",
				zap.Error(err),
				zap.String("component", "storage-pebble"))
			continue
		}

		// Check if expired
		elapsed := now.Sub(lease.GrantTime)
		if elapsed > time.Duration(lease.TTL)*time.Second {
			keyBytes := make([]byte, len(it.Key()))
			copy(keyBytes, it.Key())
			toDelete = append(toDelete, keyBytes)
		}
	}

	// Delete expired leases outside the iterator
	for _, key := range toDelete {
		if err := r.db.Delete(key, r.wo); err != nil {
			log.Warn("Failed to delete expired lease",
				zap.Error(err),
				zap.String("component", "storage-pebble"))
		} else {
			cleaned++
		}
	}

	return cleaned
}

// LeaseRenew renews a lease
func (r *PebbleDB) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
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

	// Save updated lease - use Protobuf serialize(20x performance)
	data, err := common.SerializeLease(lease)
	if err != nil {
		return nil, err
	}

	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	if err := r.db.Set(dbKey, data, r.wo); err != nil {
		return nil, err
	}

	return lease, nil
}

// LeaseTimeToLive gets remaining time of a lease
func (r *PebbleDB) LeaseTimeToLive(ctx context.Context, id int64) (*kvstore.Lease, error) {
	return r.getLease(id)
}

// Leases returns all leases
func (r *PebbleDB) Leases(ctx context.Context) ([]*kvstore.Lease, error) {
	var leases []*kvstore.Lease

	prefix := []byte(leasePrefix)
	it, err := r.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	for it.First(); it.Valid(); it.Next() {
		// use Protobuf deserialize(testformat，aftercompatible)
		valBytes := make([]byte, len(it.Value()))
		copy(valBytes, it.Value())
		lease, err := common.DeserializeLease(valBytes)
		if err == nil && lease != nil {
			leases = append(leases, lease)
		}
	}

	return leases, nil
}

// Propose proposes a value (for backward compatibility with old Store interface)
func (r *PebbleDB) Propose(k string, v string) {
	// Convert to etcd Put operation
	r.PutWithLease(context.Background(), k, v, 0)
}

// Lookup looks up a key (for backward compatibility)
func (r *PebbleDB) Lookup(key string) (string, bool) {
	kv, err := r.getKeyValue(key)
	if err != nil || kv == nil {
		return "", false
	}
	return string(kv.Value), true
}

// Txn executes a transaction (through Raft)
func (r *PebbleDB) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// Generate sequence number (lock-free atomic operation)
	seq := r.seqNum.Add(1)
	seqNum := fmt.Sprintf("seq-%d", seq)

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
	data, err := marshalRaftOperation(&op)
	if err != nil {
		cleanup()
		return nil, err
	}

	// Use BatchProposer for improved throughput (firstuse propose auxiliarymethod)
	if err := r.propose(ctx, data); err != nil {
		cleanup()
		return nil, err
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

func (r *PebbleDB) getKeyValue(key string) (*kvstore.KeyValue, error) {
	dbKey := []byte(kvPrefix + key)
	val, closer, err := r.db.Get(dbKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	data := make([]byte, len(val))
	copy(data, val)
	closer.Close()

	if len(data) == 0 {
		return nil, nil
	}

	// Use optimized binary decoding
	kv, err := decodeKeyValue(data)
	if err != nil {
		return nil, err
	}

	return kv, nil
}

func (r *PebbleDB) getLease(id int64) (*kvstore.Lease, error) {
	dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
	val, closer, err := r.db.Get(dbKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("lease not found: %d", id)
		}
		return nil, err
	}
	data := make([]byte, len(val))
	copy(data, val)
	closer.Close()

	if len(data) == 0 {
		return nil, fmt.Errorf("lease not found: %d", id)
	}

	// use Protobuf deserialize
	lease, err := common.DeserializeLease(data)
	if err != nil {
		return nil, err
	}

	return lease, nil
}

// Snapshot support

func (r *PebbleDB) GetSnapshot() ([]byte, error) {
	// Create snapshot of all data
	snapshot := make(map[string][]byte)

	it, err := r.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	for it.First(); it.Valid(); it.Next() {
		key := make([]byte, len(it.Key()))
		copy(key, it.Key())

		value := make([]byte, len(it.Value()))
		copy(value, it.Value())

		snapshot[string(key)] = value
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snapshot); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (r *PebbleDB) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := r.snapshotter.Load()
	if err == snap.ErrNoSnapshot {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *PebbleDB) recoverFromSnapshot(snapshot []byte) error {
	var snapshotData map[string][]byte
	if err := gob.NewDecoder(bytes.NewBuffer(snapshot)).Decode(&snapshotData); err != nil {
		return err
	}

	// Clear existing data
	it, err := r.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return err
	}

	wb := r.db.NewBatch()
	defer wb.Close()

	for it.First(); it.Valid(); it.Next() {
		keyBytes := make([]byte, len(it.Key()))
		copy(keyBytes, it.Key())
		wb.Delete(keyBytes, nil)
	}
	it.Close()

	// Restore from snapshot
	for k, v := range snapshotData {
		wb.Set([]byte(k), v, nil)
	}

	return wb.Commit(r.wo)
}

// timeNow returns current timestamp
func timeNow() time.Time {
	return time.Now()
}

// notifyWatches notifies all matching watches (high-performance lock-free version)
func (r *PebbleDB) notifyWatches(event kvstore.WatchEvent) {
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
			// Watchalready cancel
		default:
			// Channelfull，asynchronoussend(slowclient)
			go r.slowSendEvent(sub, eventToSend)
		}
	}
}

// shouldFilter checks if event should be filtered out
func (r *PebbleDB) shouldFilter(eventType kvstore.EventType, filters []kvstore.WatchFilterType) bool {
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
func (r *PebbleDB) slowSendEvent(sub *watchSubscription, event kvstore.WatchEvent) {
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
			zap.String("component", "storage-pebble"))
		r.CancelWatch(sub.watchID)
	}
}

// matchWatch checks if key matches watch range
func (r *PebbleDB) matchWatch(key, watchKey, rangeEnd string) bool {
	if rangeEnd == "" {
		// Single key match
		return key == watchKey
	}
	// Range match
	return key >= watchKey && (rangeEnd == "\x00" || key < rangeEnd)
}

// txnUnlocked executes a transaction (called after Raft commit, must be called without external locks)
func (r *PebbleDB) txnUnlocked(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
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
				prefix := []byte(kvPrefix)
				it, err := r.db.NewIter(&pebble.IterOptions{
					LowerBound: prefix,
					UpperBound: prefixUpperBound(prefix),
				})
				if err != nil {
					return nil, err
				}
				defer it.Close()

				startKey := []byte(kvPrefix + key)
				it.SeekGE(startKey)

				for ; it.Valid(); it.Next() {
					keyBytes := make([]byte, len(it.Key()))
					copy(keyBytes, it.Key())
					k := string(keyBytes)
					k = k[len(kvPrefix):]

					if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
						valBytes := make([]byte, len(it.Value()))
						copy(valBytes, it.Value())
						var kv kvstore.KeyValue
						if err := gob.NewDecoder(bytes.NewBuffer(valBytes)).Decode(&kv); err == nil {
							deleted++
							prevKvs = append(prevKvs, &kv)
						}
					}

					if rangeEnd != "\x00" && k >= rangeEnd {
						break
					}
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
func (r *PebbleDB) evaluateCompare(cmp kvstore.Compare) bool {
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
func (r *PebbleDB) compareInt(a, b int64, result kvstore.CompareResult) bool {
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
func (r *PebbleDB) compareBytes(a, b []byte, result kvstore.CompareResult) bool {
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

// SetRaftNode set Raft nodereference(for )
func (r *PebbleDB) SetRaftNode(node RaftNode, nodeID uint64) {
	r.raftNode = node
	r.nodeID = nodeID
}

// GetRaftStatus get Raft status info
func (r *PebbleDB) GetRaftStatus() kvstore.RaftStatus {
	if r.raftNode == nil {
		// ifnone Raft node，returndefaultstatus(singleschema)
		return kvstore.RaftStatus{
			NodeID:   r.nodeID,
			Term:     0,
			LeaderID: 0,
			State:    "standalone",
			Applied:  0,
			Commit:   0,
		}
	}

	// from Raft nodegettruestatus
	return r.raftNode.Status()
}

// TransferLeadership  leader roletospecifiednode
func (r *PebbleDB) TransferLeadership(targetID uint64) error {
	if r.raftNode == nil {
		return fmt.Errorf("raft node not available")
	}

	// checkcurrentnodeisnois leader
	status := r.raftNode.Status()
	if status.LeaderID != r.nodeID {
		return fmt.Errorf("not leader, current leader: %d", status.LeaderID)
	}

	// call Raft node TransferLeadership
	return r.raftNode.TransferLeadership(targetID)
}
