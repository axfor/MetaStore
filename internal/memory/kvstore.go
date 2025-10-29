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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"metaStore/internal/kvstore"
	"strings"
	"sync"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// RaftNode Raft 节点接口，用于获取 Raft 状态和控制
type RaftNode interface {
	Status() kvstore.RaftStatus
	TransferLeadership(targetID uint64) error
}

// Memory 集成了 Raft 共识的 etcd 兼容存储
type Memory struct {
	*MemoryEtcd // 嵌入 etcd 语义实现

	proposeC    chan<- string           // 发送 Raft 提案
	snapshotter *snap.Snapshotter
	mu          sync.Mutex              // 保护 pending 操作

	// 用于同步等待 Raft commit 的简单机制
	pendingMu    sync.RWMutex
	pendingOps   map[string]chan struct{}          // key -> wait channel
	pendingTxnResults map[string]*kvstore.TxnResponse // seqNum -> txn result
	seqNum       int64

	// Raft 节点引用（用于获取状态信息）
	raftNode RaftNode
	nodeID   uint64
}

// RaftOperation 表示通过 Raft 提交的操作
type RaftOperation struct {
	Type     string `json:"type"`      // "PUT", "DELETE", "LEASE_GRANT", "LEASE_REVOKE", "TXN"
	Key      string `json:"key"`
	Value    string `json:"value"`
	LeaseID  int64  `json:"lease_id"`
	RangeEnd string `json:"range_end"`
	SeqNum   string `json:"seq_num"`   // 用于同步等待的序列号

	// Lease 操作
	TTL int64 `json:"ttl"`

	// Transaction 操作
	Compares   []kvstore.Compare `json:"compares,omitempty"`
	ThenOps    []kvstore.Op      `json:"then_ops,omitempty"`
	ElseOps    []kvstore.Op      `json:"else_ops,omitempty"`
}

// NewMemory 创建集成 Raft 的 etcd 兼容存储
func NewMemory(snapshotter *snap.Snapshotter, proposeC chan<- string, commitC <-chan *kvstore.Commit, errorC <-chan error) *Memory {
	m := &Memory{
		MemoryEtcd:        NewMemoryEtcd(),
		proposeC:          proposeC,
		snapshotter:       snapshotter,
		pendingOps:        make(map[string]chan struct{}),
		pendingTxnResults: make(map[string]*kvstore.TxnResponse),
	}

	// 从快照恢复
	snapshot, err := m.loadSnapshot()
	if err != nil {
		log.Panic(err)
	}
	if snapshot != nil {
		log.Printf("Loading snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
		if err := m.recoverFromSnapshot(snapshot.Data); err != nil {
			log.Panic(err)
		}
	}

	// 启动 commit 处理
	go m.readCommits(commitC, errorC)

	return m
}

// readCommits 从 Raft commitC 读取并应用操作
func (m *Memory) readCommits(commitC <-chan *kvstore.Commit, errorC <-chan error) {
	for commit := range commitC {
		if commit == nil {
			// 重新加载快照
			snapshot, err := m.loadSnapshot()
			if err != nil {
				log.Panic(err)
			}
			if snapshot != nil {
				log.Printf("Loading snapshot at term %d and index %d", snapshot.Metadata.Term, snapshot.Metadata.Index)
				if err := m.recoverFromSnapshot(snapshot.Data); err != nil {
					log.Panic(err)
				}
			}
			continue
		}

		// 应用所有操作
		for _, data := range commit.Data {
			// 尝试解析为 RaftOperation
			var op RaftOperation
			if err := json.Unmarshal([]byte(data), &op); err != nil {
				// 向后兼容：旧格式（gob 编码的 KV）
				m.applyLegacyOp(data)
				continue
			}

			// 应用 etcd 操作
			m.applyOperation(op)
		}

		close(commit.ApplyDoneC)
	}

	if err, ok := <-errorC; ok {
		log.Fatal(err)
	}
}

// applyOperation 应用一个 etcd 操作
func (m *Memory) applyOperation(op RaftOperation) {
	m.MemoryEtcd.mu.Lock()
	defer m.MemoryEtcd.mu.Unlock()

	switch op.Type {
	case "PUT":
		// 直接应用 Put（已经通过 Raft 共识）
		_, _, err := m.MemoryEtcd.putUnlocked(op.Key, op.Value, op.LeaseID)
		if err != nil {
			log.Printf("Failed to apply PUT: %v", err)
		}

	case "DELETE":
		// 直接应用 Delete
		_, _, _, err := m.MemoryEtcd.deleteUnlocked(op.Key, op.RangeEnd)
		if err != nil {
			log.Printf("Failed to apply DELETE: %v", err)
		}

	case "LEASE_GRANT":
		// 应用 Lease Grant
		if m.MemoryEtcd.leases == nil {
			m.MemoryEtcd.leases = make(map[int64]*kvstore.Lease)
		}
		lease := &kvstore.Lease{
			ID:        op.LeaseID,
			TTL:       op.TTL,
			GrantTime: timeNow(),
			Keys:      make(map[string]bool),
		}
		m.MemoryEtcd.leases[op.LeaseID] = lease

	case "LEASE_REVOKE":
		// 应用 Lease Revoke（删除所有关联的键）
		lease, ok := m.MemoryEtcd.leases[op.LeaseID]
		if !ok {
			return
		}

		// 删除所有关联的键
		for key := range lease.Keys {
			m.MemoryEtcd.deleteUnlocked(key, "")
		}

		delete(m.MemoryEtcd.leases, op.LeaseID)

	case "TXN":
		// 应用 Transaction（使用未加锁版本，因为 MemoryEtcd.mu 已经在外部持有）
		txnResp, err := m.MemoryEtcd.txnUnlocked(op.Compares, op.ThenOps, op.ElseOps)
		if err != nil {
			log.Printf("Failed to apply TXN: %v", err)
		}
		// 保存事务结果供客户端读取
		if op.SeqNum != "" && txnResp != nil {
			m.pendingMu.Lock()
			m.pendingTxnResults[op.SeqNum] = txnResp
			m.pendingMu.Unlock()
		}

	default:
		log.Printf("Unknown operation type: %s", op.Type)
	}

	// 通知等待的客户端操作已完成
	if op.SeqNum != "" {
		m.pendingMu.Lock()
		if ch, exists := m.pendingOps[op.SeqNum]; exists {
			close(ch)
			delete(m.pendingOps, op.SeqNum)
		}
		m.pendingMu.Unlock()
	}
}

// applyLegacyOp 应用旧格式的操作（向后兼容）
func (m *Memory) applyLegacyOp(data string) {
	var dataKv kvstore.KV
	dec := gob.NewDecoder(bytes.NewBufferString(data))
	if err := dec.Decode(&dataKv); err != nil {
		log.Fatalf("Could not decode message: %v", err)
	}

	m.MemoryEtcd.mu.Lock()
	defer m.MemoryEtcd.mu.Unlock()

	// 转换为 etcd 操作
	m.MemoryEtcd.putUnlocked(dataKv.Key, dataKv.Val, 0)
}

// PutWithLease 存储键值对（通过 Raft）
func (m *Memory) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	// 生成唯一序列号
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// 创建等待通道
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

	// 序列化并 propose
	data, err := json.Marshal(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, err
	}

	m.proposeC <- string(data)

	// 等待 Raft 提交完成
	<-waitCh

	m.MemoryEtcd.mu.RLock()
	defer m.MemoryEtcd.mu.RUnlock()

	currentRevision := m.MemoryEtcd.revision.Load()
	prevKv := m.MemoryEtcd.kvData[key]

	return currentRevision, prevKv, nil
}

// DeleteRange 删除范围内的键（通过 Raft）
func (m *Memory) DeleteRange(ctx context.Context, key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	// 先检查有多少 key 会被删除（在提交到 Raft 之前）
	m.MemoryEtcd.mu.RLock()
	var deleted int64
	var prevKvs []*kvstore.KeyValue

	if rangeEnd == "" {
		if kv, ok := m.MemoryEtcd.kvData[key]; ok {
			deleted = 1
			prevKvs = append(prevKvs, kv)
		}
	} else {
		for k, v := range m.MemoryEtcd.kvData {
			if k >= key && (rangeEnd == "\x00" || k < rangeEnd) {
				deleted++
				prevKvs = append(prevKvs, v)
			}
		}
	}
	m.MemoryEtcd.mu.RUnlock()

	// 如果没有 key 需要删除，直接返回
	if deleted == 0 {
		return 0, nil, m.MemoryEtcd.revision.Load(), nil
	}

	// 生成唯一序列号
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// 创建等待通道
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

	data, err := json.Marshal(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return 0, nil, 0, err
	}

	m.proposeC <- string(data)

	// 等待 Raft 提交完成
	<-waitCh

	return deleted, prevKvs, m.MemoryEtcd.revision.Load(), nil
}

// LeaseGrant 创建租约（通过 Raft）
func (m *Memory) LeaseGrant(ctx context.Context, id int64, ttl int64) (*kvstore.Lease, error) {
	// 生成唯一序列号
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// 创建等待通道
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

	data, err := json.Marshal(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, err
	}

	m.proposeC <- string(data)

	// 等待 Raft 提交完成
	<-waitCh

	// 返回租约信息
	lease := &kvstore.Lease{
		ID:        id,
		TTL:       ttl,
		GrantTime: timeNow(),
		Keys:      make(map[string]bool),
	}

	return lease, nil
}

// LeaseRevoke 撤销租约（通过 Raft）
func (m *Memory) LeaseRevoke(ctx context.Context, id int64) error {
	// 生成唯一序列号
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// 创建等待通道
	waitCh := make(chan struct{})
	m.pendingMu.Lock()
	m.pendingOps[seqNum] = waitCh
	m.pendingMu.Unlock()

	op := RaftOperation{
		Type:    "LEASE_REVOKE",
		LeaseID: id,
		SeqNum:  seqNum,
	}

	data, err := json.Marshal(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return err
	}

	m.proposeC <- string(data)

	// 等待 Raft 提交完成
	<-waitCh

	return nil
}

// Txn 执行事务（通过 Raft）
func (m *Memory) Txn(ctx context.Context, cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	// 生成唯一序列号
	m.mu.Lock()
	m.seqNum++
	seqNum := fmt.Sprintf("seq-%d", m.seqNum)
	m.mu.Unlock()

	// 创建等待通道
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

	// 序列化并 propose
	data, err := json.Marshal(op)
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pendingOps, seqNum)
		m.pendingMu.Unlock()
		return nil, err
	}

	m.proposeC <- string(data)

	// 等待 Raft 提交完成
	<-waitCh

	// 读取事务结果
	m.pendingMu.Lock()
	txnResp := m.pendingTxnResults[seqNum]
	delete(m.pendingTxnResults, seqNum) // 清理结果
	m.pendingMu.Unlock()

	if txnResp == nil {
		return nil, fmt.Errorf("transaction result not found")
	}

	return txnResp, nil
}

// Propose 提交操作（向后兼容旧的 HTTP API）
func (m *Memory) Propose(k string, v string) {
	var buf strings.Builder
	if err := gob.NewEncoder(&buf).Encode(kvstore.KV{Key: k, Val: v}); err != nil {
		log.Fatal(err)
	}
	m.proposeC <- buf.String()
}

// GetSnapshot 获取快照
func (m *Memory) GetSnapshot() ([]byte, error) {
	m.MemoryEtcd.mu.RLock()
	defer m.MemoryEtcd.mu.RUnlock()

	// 序列化快照
	snapshot := struct {
		Revision int64
		KVData   map[string]*kvstore.KeyValue
		Leases   map[int64]*kvstore.Lease
	}{
		Revision: m.MemoryEtcd.revision.Load(),
		KVData:   m.MemoryEtcd.kvData,
		Leases:   m.MemoryEtcd.leases,
	}

	return json.Marshal(snapshot)
}

// loadSnapshot 加载快照
func (m *Memory) loadSnapshot() (*raftpb.Snapshot, error) {
	snapshot, err := m.snapshotter.Load()
	if errors.Is(err, snap.ErrNoSnapshot) {
		return nil, nil
	}
	return snapshot, err
}

// recoverFromSnapshot 从快照恢复
func (m *Memory) recoverFromSnapshot(snapshotData []byte) error {
	var snapshot struct {
		Revision int64
		KVData   map[string]*kvstore.KeyValue
		Leases   map[int64]*kvstore.Lease
	}

	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		return err
	}

	m.MemoryEtcd.mu.Lock()
	defer m.MemoryEtcd.mu.Unlock()

	m.MemoryEtcd.revision.Store(snapshot.Revision)
	m.MemoryEtcd.kvData = snapshot.KVData
	m.MemoryEtcd.leases = snapshot.Leases

	return nil
}

// SetRaftNode 设置 Raft 节点引用（用于依赖注入）
func (m *Memory) SetRaftNode(node RaftNode, nodeID uint64) {
	m.raftNode = node
	m.nodeID = nodeID
}

// GetRaftStatus 获取 Raft 状态信息
func (m *Memory) GetRaftStatus() kvstore.RaftStatus {
	if m.raftNode == nil {
		// 如果没有 Raft 节点，返回默认状态（单机模式）
		return kvstore.RaftStatus{
			NodeID:   m.nodeID,
			Term:     0,
			LeaderID: 0,
			State:    "standalone",
			Applied:  0,
			Commit:   0,
		}
	}

	// 从 Raft 节点获取真实状态
	return m.raftNode.Status()
}

// TransferLeadership 转移 leader 角色到指定节点
func (m *Memory) TransferLeadership(targetID uint64) error {
	if m.raftNode == nil {
		return fmt.Errorf("raft node not available")
	}

	// 检查当前节点是否是 leader
	status := m.raftNode.Status()
	if status.LeaderID != m.nodeID {
		return fmt.Errorf("not leader, current leader: %d", status.LeaderID)
	}

	// 调用 Raft 节点的 TransferLeadership
	return m.raftNode.TransferLeadership(targetID)
}
