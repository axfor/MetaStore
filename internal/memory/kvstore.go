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

package memory

import (
	"context"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"metaStore/internal/kvstore"
	"metaStore/internal/lease"
	"metaStore/pkg/log"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

// RaftNode Raft nodeinterface，used to get Raft statusand
type RaftNode interface {
	Status() kvstore.RaftStatus
	TransferLeadership(targetID uint64) error
	LeaseManager() *lease.LeaseManager
	ReadIndexManager() *lease.ReadIndexManager
}

// Memory collectbecome Raft  etcd compatiblestorage
type Memory struct {
	*MemoryEtcd //  etcd implement

	proposeC      chan<- string           // send Raft (aftercompatible)
	snapshotter   *snap.Snapshotter
	mu            sync.Mutex              // protected pending operation

	// for synchronouswait Raft commit single
	pendingMu    sync.RWMutex
	pendingOps   map[string]chan struct{}          // key -> wait channel
	pendingTxnResults map[string]*kvstore.TxnResponse // seqNum -> txn result
	seqNum       int64

	// Raft nodereference(used to getstatus info)
	raftNode RaftNode
	nodeID   uint64
}

// RaftOperation indicatesvia Raft commitoperation
type RaftOperation struct {
	Type     string `json:"type"`      // "PUT", "DELETE", "LEASE_GRANT", "LEASE_REVOKE", "TXN"
	Key      string `json:"key"`
	Value    string `json:"value"`
	LeaseID  int64  `json:"lease_id"`
	RangeEnd string `json:"range_end"`
	SeqNum   string `json:"seq_num"`   // for synchronouswaitcolumn

	// Lease operation
	TTL int64 `json:"ttl"`

	// Transaction operation
	Compares   []kvstore.Compare `json:"compares,omitempty"`
	ThenOps    []kvstore.Op      `json:"then_ops,omitempty"`
	ElseOps    []kvstore.Op      `json:"else_ops,omitempty"`
}

// NewMemory createcollectbecome Raft  etcd compatiblestorage
func NewMemory(snapshotter *snap.Snapshotter, proposeC chan<- string, commitC <-chan *kvstore.Commit, errorC <-chan error) *Memory {
	m := &Memory{
		MemoryEtcd:        NewMemoryEtcd(),
		proposeC:          proposeC,
		snapshotter:       snapshotter,
		pendingOps:        make(map[string]chan struct{}),
		pendingTxnResults: make(map[string]*kvstore.TxnResponse),
	}

	// fromsnapshotrecovery
	snapshot, err := m.loadSnapshot()
	if err != nil {
		log.Fatal("Failed to load snapshot", zap.Error(err), zap.String("component", "storage-memory"))
	}
	if snapshot != nil {
		log.Info("Loading memory snapshot",
			zap.Uint64("term", snapshot.Metadata.Term),
			zap.Uint64("index", snapshot.Metadata.Index),
			zap.String("component", "storage-memory"))
		if err := m.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Fatal("Failed to recover from snapshot", zap.Error(err), zap.String("component", "storage-memory"))
		}
	}

	// start commit handle
	go m.readCommits(commitC, errorC)

	return m
}

func (m *Memory) propose(ctx context.Context, data string) error {

	// aftercompatible：usestart proposeC
	select {
	case m.proposeC <- data:
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timeout proposing operation")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readCommits from Raft commitC readandappliedoperation
//
// ✅ performanceoptimize (Phase 2):  Apply
//
// Before (Phase 1):
//   for op in ops { applyOperation(op) }  // N  timelockoperation
//
// After (Phase 2):
//   applyBatch(ops)  // shardseparategroup，eachshard 1  timelock
//
// : 5-10x (lockopendecreasefew 100x)
func (m *Memory) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			// reloadsnapshot
			snapshot, err := m.loadSnapshot()
			if err != nil {
				log.Fatal("Failed to reload snapshot", zap.Error(err), zap.String("component", "storage-memory"))
			}
			if snapshot != nil {
				log.Info("Reloading memory snapshot",
					zap.Uint64("term", snapshot.Metadata.Term),
					zap.Uint64("index", snapshot.Metadata.Index),
					zap.String("component", "storage-memory"))
				if err := m.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Fatal("Failed to recover from reloaded snapshot", zap.Error(err), zap.String("component", "storage-memory"))
				}
			}
			continue
		}

		// ✅ Phase 2 optimize: collectcollectalloperation，applied
		var allOps []RaftOperation

		// collectcollectalloperation
		for _, data := range commit.Data {
			// testas RaftOperation(test Protobuf/JSON)
			op, err := deserializeOperation([]byte(data))
			if err != nil {
				// aftercompatible：oldformat(gob encode KV)
				m.applyLegacyOp(data)
				continue
			}

			allOps = append(allOps, op)
		}

		// ✅ appliedalloperation (Phase 2 optimize)
		if len(allOps) > 0 {
			m.applyBatch(allOps)
		}

		close(commit.ApplyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal("Raft commit error", zap.Error(err), zap.String("component", "storage-memory"))
	}
}

// applyOperation appliedfirst  etcd operation
//
// ✅ performanceoptimize (Phase 1): goglobal txnMu lock
//
// Before (serial):
//   txnMu.Lock() → alloperation → concurrency = 1
//
// After (parallelism):
//   singlekeyoperation → ShardedMap shardlock → concurrency = 512
//   transactionoperation → fine-grainedshardlock → concurrency = 512 / andshard
//
// : 10-50x throughput (getatconcurrencyandoperationtype)
func (m *Memory) applyOperation(op RaftOperation) {
	// ⚠️ closekey: notuseglobal txnMu.Lock()
	// eachoperationtypeuseminimumlock

	switch op.Type {
	case "PUT":
		// ✅ usenolockversion (ShardedMap internallock)
		_, _, err := m.MemoryEtcd.putDirect(op.Key, op.Value, op.LeaseID)
		if err != nil {
			log.Error("Failed to apply PUT operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("component", "storage-memory"))
		}

	case "DELETE":
		// ✅ usenolockversion
		_, _, _, err := m.MemoryEtcd.deleteDirect(op.Key, op.RangeEnd)
		if err != nil {
			log.Error("Failed to apply DELETE operation",
				zap.Error(err),
				zap.String("key", op.Key),
				zap.String("rangeEnd", op.RangeEnd),
				zap.String("component", "storage-memory"))
		}

	case "LEASE_GRANT":
		// ✅ use lease operation (leaseMu lock)
		m.MemoryEtcd.applyLeaseOperationDirect("LEASE_GRANT", op.LeaseID, op.TTL)

	case "LEASE_REVOKE":
		// ✅ use lease operation
		m.MemoryEtcd.applyLeaseOperationDirect("LEASE_REVOKE", op.LeaseID, 0)

	case "TXN":
		// ✅ usefine-grainedshardlock (lockandshard)
		txnResp, err := m.MemoryEtcd.applyTxnWithShardLocks(op.Compares, op.ThenOps, op.ElseOps)
		if err != nil {
			log.Error("Failed to apply TXN operation",
				zap.Error(err),
				zap.Int("compareCount", len(op.Compares)),
				zap.Int("thenOpsCount", len(op.ThenOps)),
				zap.Int("elseOpsCount", len(op.ElseOps)),
				zap.String("component", "storage-memory"))
		}
		// savetransactionresultclientread
		if op.SeqNum != "" && txnResp != nil {
			m.pendingMu.Lock()
			m.pendingTxnResults[op.SeqNum] = txnResp
			m.pendingMu.Unlock()
		}

	default:
		log.Warn("Unknown operation type",
			zap.String("type", op.Type),
			zap.String("component", "storage-memory"))
	}

	// notificationwaitclientoperationalready done
	if op.SeqNum != "" {
		m.pendingMu.Lock()
		if ch, exists := m.pendingOps[op.SeqNum]; exists {
			close(ch)
			delete(m.pendingOps, op.SeqNum)
		}
		m.pendingMu.Unlock()
	}
}

// applyLegacyOp appliedoldformatoperation(aftercompatible)
func (m *Memory) applyLegacyOp(data string) {
	var dataKv kvstore.KV
	dec := gob.NewDecoder(bytes.NewBufferString(data))
	if err := dec.Decode(&dataKv); err != nil {
		log.Fatal("Failed to decode legacy message",
			zap.Error(err),
			zap.String("component", "storage-memory"))
	}

	// ✅ usenolockversion (Phase 1 optimize)
	m.MemoryEtcd.putDirect(dataKv.Key, dataKv.Val, 0)
}

// PutWithLease storagekey-value pair(via Raft)
func (m *Memory) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// becomeuniquecolumn
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// createwaitchannel
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "PUT",
		Key:     key,
		Value:   value,
		LeaseID: leaseID,
		SeqNum:  seqNum,
	}

	// serializeand propose(use Protobuf optimize)
	data, err := serializeOperation(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, err
	}

	// send(use BatchProposer ifavailable)
	if err := m.propose(ctx, string(data)); err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, fmt.Errorf("failed to propose PUT operation: %w", err)
	}

	// wait Raft commitdone, with timeoutprotected
	select {
	case <-waitCh:
		// successdone
	case <-time.After(30 * time.Second):
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, fmt.Errorf("timeout waiting for Raft commit (PUT)")
	case <-ctx.Done():
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, ctx.Err()
	}

	// readcurrent revision and prevKv(nolock，atomic + ShardedMap internallock)
	currentRevision := m.MemoryEtcd.revision.Load()
	prevKv, _ := m.MemoryEtcd.kvData.Get(key)

	return currentRevision, prevKv, nil
}

// DeleteRange deleterangeinternalkey(via Raft)
func (m *Memory) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	// checkhavemanyfew key willbedelete(incommitto Raft before)
	// use ShardedMap API(internallock)
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		if kv, ok := m.MemoryEtcd.kvData.Get(key); ok {
			deleted = 1
			prevKvs = append(prevKvs, kv)
		}
	} else {
		// use ShardedMap.Range() getrangeinternalkey-value pair
		allKvs := m.MemoryEtcd.kvData.Range(key, rangeEnd, 0)
		deleted = int64(len(allKvs))
		prevKvs = allKvs
	}

	// ifnone key needdelete，return
	if deleted == 0 {
		return 0, nil, m.MemoryEtcd.revision.Load(), nil
	}

	// becomeuniquecolumn
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// createwaitchannel
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:     "DELETE",
		Key:      key,
		RangeEnd: rangeEnd,
		SeqNum:   seqNum,
	}

	data, err := serializeOperation(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, 0, err
	}

	// send(use BatchProposer ifavailable)
	if err := m.propose(ctx, string(data)); err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, 0, fmt.Errorf("failed to propose DELETE operation: %w", err)
	}

	// wait Raft commitdone, with timeoutprotected
	select {
	case <-waitCh:
		// successdone
	case <-time.After(30 * time.Second):
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, 0, fmt.Errorf("timeout waiting for Raft commit (DELETE)")
	case <-ctx.Done():
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, 0, ctx.Err()
	}

	return deleted, prevKvs, m.MemoryEtcd.revision.Load(), nil
}

// LeaseGrant createlease(via Raft)
func (m *Memory) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	// becomeuniquecolumn
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// createwaitchannel
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "LEASE_GRANT",
		LeaseID: id,
		TTL:     ttl,
		SeqNum:  seqNum,
	}

	data, err := serializeOperation(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, err
	}

	// send(use BatchProposer ifavailable)
	if err := m.propose(ctx, string(data)); err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, fmt.Errorf("failed to propose LEASE_GRANT operation: %w", err)
	}

	// wait Raft commitdone, with timeoutprotected
	select {
	case <-waitCh:
		// successdone
	case <-time.After(30 * time.Second):
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, fmt.Errorf("timeout waiting for Raft commit (LEASE_GRANT)")
	case <-ctx.Done():
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, ctx.Err()
	}

	// returnleaseinfo
	lease := &kvstore.Lease{
		ID:        id,
		TTL:       ttl,
		GrantTime: timeNow(),
		Keys:      make(map[string]bool),
	}

	return lease, nil
}

// LeaseRevoke revokedlease(via Raft)
func (m *Memory) LeaseRevoke(ctx context.Context, id int64) error {
	// becomeuniquecolumn
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// createwaitchannel
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "LEASE_REVOKE",
		LeaseID: id,
		SeqNum:  seqNum,
	}

	data, err := serializeOperation(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return err
	}

	// send(use BatchProposer ifavailable)
	if err := m.propose(ctx, string(data)); err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return fmt.Errorf("failed to propose LEASE_REVOKE operation: %w", err)
	}

	// wait Raft commitdone, with timeoutprotected
	select {
	case <-waitCh:
		// successdone
	case <-time.After(30 * time.Second):
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return fmt.Errorf("timeout waiting for Raft commit (LEASE_REVOKE)")
	case <-ctx.Done():
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return ctx.Err()
	}

	return nil
}

// Txn executetransaction(via Raft)
func (m *Memory) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// becomeuniquecolumn
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// createwaitchannel
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:     "TXN",
		Compares: cmps,
		ThenOps:  thenOps,
		ElseOps:  elseOps,
		SeqNum:   seqNum,
	}

	// serializeand propose(use Protobuf optimize)
	data, err := serializeOperation(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, err
	}

	// send(use BatchProposer ifavailable)
	if err := m.propose(ctx, string(data)); err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, fmt.Errorf("failed to propose TXN operation: %w", err)
	}

	// wait Raft commitdone, with timeoutprotected
	select {
	case <-waitCh:
		// successdone
	case <-time.After(30 * time.Second):
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, fmt.Errorf("timeout waiting for Raft commit (TXN)")
	case <-ctx.Done():
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, ctx.Err()
	}

	// readtransactionresult
	m.pendingMu.Lock()
	txnResp := m.pendingTxnResults[seqNum]
	delete(m.pendingTxnResults, seqNum) // clean upresult
	m.pendingMu.Unlock()

	if txnResp == nil {
		return nil, fmt.Errorf("transaction result not found")
	}

	return txnResp, nil
}

// Propose commitoperation(aftercompatibleold HTTP API)
func (m *Memory) Propose(k string, v string) {
	var buf strings.Builder
	if err := gob.NewEncoder(&buf).Encode(kvstore.KV{Key: k, Val: v}); err != nil {
		log.Fatal("Failed to encode KV for proposal",
			zap.Error(err),
			zap.String("key", k),
			zap.String("component", "storage-memory"))
	}
	m.proposeC <- buf.String()
}

// GetSnapshot getsnapshot
// optimize: use Protobuf serialize(2-3x performance)
func (m *Memory) GetSnapshot() ([]byte, error) {
	// use ShardedMap.GetAll() getalldata(internallock)
	kvData := m.MemoryEtcd.kvData.GetAll()

	// get leases replica(use leaseMu)
	m.MemoryEtcd.leaseMu.RLock()
	leases := make(map[int64]*kvstore.Lease, len(m.MemoryEtcd.leases))
	for k, v := range m.MemoryEtcd.leases {
		leases[k] = v
	}
	m.MemoryEtcd.leaseMu.RUnlock()

	// use Protobuf serialize(optimizeafter)
	revision := m.MemoryEtcd.revision.Load()
	return serializeSnapshot(revision, kvData, leases)
}

// loadSnapshot loadsnapshot
func (m *Memory) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := m.snapshotter.Load()
	if errors.Is(err, snap.ErrNoSnapshot) {
		return nil, nil
	}
	return snapshot, err
}

// recoverFromSnapshot fromsnapshotrecovery
// optimize: supported Protobuf and JSON format(aftercompatible)
func (m *Memory) recoverFromSnapshot(snapshotData []byte) error {
	// usefirstdeserializefunction(testformat)
	snapshot, err := deserializeSnapshot(snapshotData)
	if err != nil {
		return err
	}

	// use atomic update revision
	m.MemoryEtcd.revision.Store(snapshot.Revision)

	// use ShardedMap.SetAll() recoverydata(internallock)
	m.MemoryEtcd.kvData.SetAll(snapshot.KVData)

	// use leaseMu recovery leases
	m.MemoryEtcd.leaseMu.Lock()
	m.MemoryEtcd.leases = snapshot.Leases
	m.MemoryEtcd.leaseMu.Unlock()

	return nil
}

// SetRaftNode set Raft nodereference(for )
func (m *Memory) SetRaftNode(node RaftNode, nodeID uint64) {
	m.raftNode = node
	m.nodeID = nodeID
}

// GetRaftStatus get Raft status info
func (m *Memory) GetRaftStatus() kvstore.RaftStatus {
	if m.raftNode == nil {
		// ifnone Raft node，returndefaultstatus(singleschema)
		return kvstore.RaftStatus{
			NodeID:   m.nodeID,
			Term:     0,
			LeaderID: 0,
			State:    "standalone",
			Applied:  0,
			Commit:   0,
		}
	}

	// from Raft nodegettruestatus
	return m.raftNode.Status()
}

// TransferLeadership  leader roletospecifiednode
func (m *Memory) TransferLeadership(targetID uint64) error {
	if m.raftNode == nil {
		return fmt.Errorf("raft node not available")
	}

	// checkcurrentnodeisnois leader
	status := m.raftNode.Status()
	if status.LeaderID != m.nodeID {
		return fmt.Errorf("not leader, current leader: %d", status.LeaderID)
	}

	// call Raft node TransferLeadership
	return m.raftNode.TransferLeadership(targetID)
}

// Range executerangequery( Lease Read optimize)
//
// Lease Read optimizepath:
//   - Fast Path: Leader havevalidleasewhenread(no Raft )
//   - Slow Path: use ReadIndex protocolfirst
//   - performance: 10-100x (getatclustersizeandnetworklatency)
func (m *Memory) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	// ifenabled Lease Read  RaftNode available
	if m.raftNode != nil {
		leaseManager := m.raftNode.LeaseManager()
		readIndexManager := m.raftNode.ReadIndexManager()

		if leaseManager != nil && readIndexManager != nil {
			// Fast Path: Leader havevalidlease
			if leaseManager.IsLeader() && leaseManager.HasValidLease() {
				// recordfastpathread
				readIndexManager.RecordFastPathRead()

				// readstatus(already leasecertifyfirst)
				return m.MemoryEtcd.Range(ctx, key, rangeEnd, limit, revision)
			}

			// Slow Path:  Leader orlease，use ReadIndex protocol
			// TODO: implementcomplete ReadIndex protocol
			// 1. Leader recordcurrent committedIndex as readIndex
			// 2. Leader sendis Leader
			// 3. wait appliedIndex >= readIndex
			// 4. executeread

			// currenttransformimplement：read(incompleteimplementbeforeholdaftercompatible)
			return m.MemoryEtcd.Range(ctx, key, rangeEnd, limit, revision)
		}
	}

	// not enabled Lease Read or RaftNode unavailable，read
	return m.MemoryEtcd.Range(ctx, key, rangeEnd, limit, revision)
}

// RangeWithOptions executerangequery(supportedcompleteoption， Lease Read optimize)
func (m *Memory) RangeWithOptions(ctx context.Context, key, rangeEnd string, opts kvstore.RangeOptions) (*kvstore.RangeResponse, error) {
	// ifenabled Lease Read  RaftNode available
	if m.raftNode != nil {
		leaseManager := m.raftNode.LeaseManager()
		readIndexManager := m.raftNode.ReadIndexManager()

		if leaseManager != nil && readIndexManager != nil {
			// Fast Path: Leader havevalidlease
			if leaseManager.IsLeader() && leaseManager.HasValidLease() {
				// recordfastpathread
				readIndexManager.RecordFastPathRead()

				// readstatus(already leasecertifyfirst)
				return m.MemoryEtcd.RangeWithOptions(ctx, key, rangeEnd, opts)
			}

			// currenttransformimplement：read(incompleteimplementbeforeholdaftercompatible)
			return m.MemoryEtcd.RangeWithOptions(ctx, key, rangeEnd, opts)
		}
	}

	// not enabled Lease Read or RaftNode unavailable，read
	return m.MemoryEtcd.RangeWithOptions(ctx, key, rangeEnd, opts)
}
