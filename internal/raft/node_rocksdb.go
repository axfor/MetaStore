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

package raft

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"metaStore/internal/batch"
	"metaStore/internal/kvstore"
	"metaStore/internal/lease"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/config"

	"github.com/linxGnu/grocksdb"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	stats "go.etcd.io/etcd/server/v3/etcdserver/api/v2stats"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"go.uber.org/zap"
)

// raftNodeRocks is a raft node backed by RocksDB for persistent storage
type raftNodeRocks struct {
	proposeC    <-chan string            // proposed messages (k,v)
	confChangeC <-chan raftpb.ConfChange // proposed cluster config changes
	commitC     chan<- *kvstore.Commit   // entries committed to log (k,v)
	errorC      chan<- error             // errors from raft session

	id          int      // client ID for raft session
	peers       []string // raft peer URLs
	join        bool     // node is joining an existing cluster
	dbdir       string   // path to RocksDB directory
	snapdir     string   // path to snapshot directory
	getSnapshot func() ([]byte, error)

	confState     raftpb.ConfState
	snapshotIndex uint64
	appliedIndex  uint64

	// raft backing for the commit/error channel
	node        raft.Node
	raftStorage *rocksdb.RocksDBStorage
	rocksDB     *grocksdb.DB

	snapshotter      *snap.Snapshotter
	snapshotterReady chan *snap.Snapshotter // signals when snapshotter is ready

	snapCount uint64
	transport *rafthttp.Transport
	stopc     chan struct{} // signals proposal channel closed
	httpstopc chan struct{} // signals http server to shutdown
	httpdonec chan struct{} // signals http server shutdown complete

	// 批量提案系统（可选）
	batcher         *batch.ProposalBatcher // 批量提案器（如果启用）
	batchedProposeC <-chan []byte          // 批量提案通道（如果启用批量，从 batcher 获取）

	// Lease Read 系统（可选）
	smartLeaseConfig *lease.SmartLeaseConfig // 智能配置管理器（支持动态扩缩容）
	leaseManager     *lease.LeaseManager     // 租约管理器（如果启用）
	readIndexManager *lease.ReadIndexManager // ReadIndex 管理器（如果启用）

	logger *zap.Logger
	cfg    *config.Config // Raft configuration
}

// newRaftNodeRocks initiates a raft instance backed by RocksDB
func NewNodeRocksDB(id int, peers []string, join bool, getSnapshot func() ([]byte, error),
	proposeC <-chan string, confChangeC <-chan raftpb.ConfChange, rocksDB *grocksdb.DB, dataDir string, cfg *config.Config,
) (<-chan *kvstore.Commit, <-chan error, <-chan *snap.Snapshotter, *raftNodeRocks) {
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)

	rc := &raftNodeRocks{
		proposeC:    proposeC,
		confChangeC: confChangeC,
		commitC:     commitC,
		errorC:      errorC,
		id:          id,
		peers:       peers,
		join:        join,
		dbdir:       dataDir,
		snapdir:     fmt.Sprintf("%s/snap", dataDir),
		getSnapshot: getSnapshot,
		snapCount:   defaultSnapshotCount,
		stopc:       make(chan struct{}),
		httpstopc:   make(chan struct{}),
		httpdonec:   make(chan struct{}),
		rocksDB:     rocksDB,

		logger: newLogger(),
		cfg:    cfg, // Store config reference

		snapshotterReady: make(chan *snap.Snapshotter, 1),
	}
	go rc.startRaft()
	return commitC, errorC, rc.snapshotterReady, rc
}

func (rc *raftNodeRocks) saveSnap(snap raftpb.Snapshot) error {
	// Save snapshot to file system using snapshotter
	if err := rc.snapshotter.SaveSnap(snap); err != nil {
		return err
	}
	rc.logger.Info("saved snapshot", zap.Uint64("index", snap.Metadata.Index), zap.String("component", "raft-rocks"))
	return nil
}

// isWitness returns true if this node is configured as a witness node
// Witness nodes participate in Raft voting but don't store data
func (rc *raftNodeRocks) isWitness() bool {
	return rc.cfg != nil && rc.cfg.Server.Raft.IsWitness()
}

func (rc *raftNodeRocks) entriesToApply(ents []raftpb.Entry) (nents []raftpb.Entry) {
	if len(ents) == 0 {
		return ents
	}
	firstIdx := ents[0].Index
	if firstIdx > rc.appliedIndex+1 {
		log.Fatalf("first index of committed entry[%d] should <= progress.appliedIndex[%d]+1", firstIdx, rc.appliedIndex)
	}
	if rc.appliedIndex-firstIdx+1 < uint64(len(ents)) {
		nents = ents[rc.appliedIndex-firstIdx+1:]
	}
	return nents
}

// publishEntries writes committed log entries to commit channel
func (rc *raftNodeRocks) publishEntries(ents []raftpb.Entry) (<-chan struct{}, bool) {
	if len(ents) == 0 {
		return nil, true
	}

	// Witness nodes only process ConfChange entries, skip data application
	if rc.isWitness() {
		return rc.publishEntriesAsWitness(ents)
	}

	data := make([]string, 0, len(ents))
	for i := range ents {
		switch ents[i].Type {
		case raftpb.EntryNormal:
			if len(ents[i].Data) == 0 {
				// ignore empty messages
				break
			}

			// 如果启用了批量提案，需要解码批量提案
			if rc.cfg.Server.Raft.Batch.Enable {
				proposals, err := batch.DecodeBatch(ents[i].Data)
				if err != nil {
					rc.logger.Error("failed to decode batch proposal",
						zap.Error(err),
						zap.Uint64("index", ents[i].Index),
						zap.String("component", "raft-rocks"))
					continue
				}
				data = append(data, proposals...)
			} else {
				// 不启用批量提案，直接使用字符串
				s := string(ents[i].Data)
				data = append(data, s)
			}
		case raftpb.EntryConfChange:
			var cc raftpb.ConfChange
			cc.Unmarshal(ents[i].Data)
			rc.confState = *rc.node.ApplyConfChange(cc)
			switch cc.Type {
			case raftpb.ConfChangeAddNode:
				if len(cc.Context) > 0 {
					rc.transport.AddPeer(types.ID(cc.NodeID), []string{string(cc.Context)})
				}
			case raftpb.ConfChangeRemoveNode:
				if cc.NodeID == uint64(rc.id) {
					log.Println("I've been removed from the cluster! Shutting down.")
					return nil, false
				}
				rc.transport.RemovePeer(types.ID(cc.NodeID))
			}
		}
	}

	var applyDoneC chan struct{}

	if len(data) > 0 {
		applyDoneC = make(chan struct{}, 1)
		select {
		case rc.commitC <- &kvstore.Commit{Data: data, ApplyDoneC: applyDoneC}:
		case <-rc.stopc:
			return nil, false
		}
	}

	// after commit, update appliedIndex
	rc.appliedIndex = ents[len(ents)-1].Index

	// Lease Read: 通知 ReadIndexManager 应用进度
	if rc.cfg.Server.Raft.LeaseRead.Enable && rc.readIndexManager != nil {
		rc.readIndexManager.NotifyApplied(rc.appliedIndex)
	}

	return applyDoneC, true
}

// publishEntriesAsWitness handles entries for witness nodes
// Witness nodes only process ConfChange entries (cluster membership changes)
// They skip all data entries since they don't store data
func (rc *raftNodeRocks) publishEntriesAsWitness(ents []raftpb.Entry) (<-chan struct{}, bool) {
	for i := range ents {
		switch ents[i].Type {
		case raftpb.EntryNormal:
			// Witness nodes skip normal data entries
			// They participate in Raft consensus but don't apply data
			continue

		case raftpb.EntryConfChange:
			// Process cluster configuration changes
			var cc raftpb.ConfChange
			cc.Unmarshal(ents[i].Data)
			rc.confState = *rc.node.ApplyConfChange(cc)

			switch cc.Type {
			case raftpb.ConfChangeAddNode:
				if len(cc.Context) > 0 {
					rc.transport.AddPeer(types.ID(cc.NodeID), []string{string(cc.Context)})
				}
				rc.logger.Info("witness: added peer",
					zap.Uint64("node_id", cc.NodeID),
					zap.String("component", "raft-rocks-witness"))

			case raftpb.ConfChangeRemoveNode:
				if cc.NodeID == uint64(rc.id) {
					rc.logger.Warn("witness: I've been removed from the cluster! Shutting down.",
						zap.String("component", "raft-rocks-witness"))
					return nil, false
				}
				rc.transport.RemovePeer(types.ID(cc.NodeID))
				rc.logger.Info("witness: removed peer",
					zap.Uint64("node_id", cc.NodeID),
					zap.String("component", "raft-rocks-witness"))
			}
		}
	}

	// Update appliedIndex even for witness nodes (for Raft protocol correctness)
	rc.appliedIndex = ents[len(ents)-1].Index

	return nil, true
}

func (rc *raftNodeRocks) loadSnapshot() *raftpb.Snapshot {
	snapshot, err := rc.snapshotter.Load()
	if err != nil && !errors.Is(err, snap.ErrNoSnapshot) {
		log.Fatalf("store: error loading snapshot (%v)", err)
	}
	if snapshot != nil {
		return snapshot
	}
	return &raftpb.Snapshot{}
}

// initRocksDBStorage initializes RocksDB storage and recovers state
func (rc *raftNodeRocks) initRocksDBStorage() error {
	nodeID := fmt.Sprintf("node_%d", rc.id)
	rocksdbStorage, err := rocksdb.NewRocksDBStorage(rc.rocksDB, nodeID)
	if err != nil {
		return fmt.Errorf("failed to create RocksDB storage: %v", err)
	}
	rc.raftStorage = rocksdbStorage

	// Load snapshot and apply to RocksDB storage
	snapshot := rc.loadSnapshot()
	if snapshot != nil && !raft.IsEmptySnap(*snapshot) {
		rc.logger.Info("applying snapshot to RocksDB storage",
			zap.Uint64("term", snapshot.Metadata.Term),
			zap.Uint64("index", snapshot.Metadata.Index),
			zap.String("component", "raft-rocks"))
		if err := rc.raftStorage.ApplySnapshot(*snapshot); err != nil {
			return fmt.Errorf("failed to apply snapshot: %v", err)
		}
	}

	return nil
}

func (rc *raftNodeRocks) writeError(err error) {
	rc.stopHTTP()
	close(rc.commitC)
	rc.errorC <- err
	close(rc.errorC)
	rc.node.Stop()
}

func (rc *raftNodeRocks) startRaft() {
	if !fileutil.Exist(rc.snapdir) {
		if err := os.Mkdir(rc.snapdir, 0o750); err != nil {
			log.Fatalf("store: cannot create dir for snapshot (%v)", err)
		}
	}
	rc.snapshotter = snap.New(newLogger(), rc.snapdir)

	// Initialize RocksDB storage
	if err := rc.initRocksDBStorage(); err != nil {
		log.Fatalf("store: failed to initialize RocksDB storage (%v)", err)
	}

	// Check if we're restarting an existing node
	hardState, confState, err := rc.raftStorage.InitialState()
	if err != nil {
		log.Fatalf("store: failed to get initial state (%v)", err)
	}

	// Update conf state
	if len(confState.Voters) > 0 {
		rc.confState = confState
	}

	oldNode := !raft.IsEmptyHardState(hardState)

	// signal initialization finished
	rc.snapshotterReady <- rc.snapshotter

	rpeers := make([]raft.Peer, len(rc.peers))
	for i := range rpeers {
		rpeers[i] = raft.Peer{ID: uint64(i + 1)}
	}
	// Raft 配置 - 从配置文件读取（基于业界最佳实践：etcd、TiKV、CockroachDB）
	c := &raft.Config{
		ID:            uint64(rc.id),
		ElectionTick:  rc.cfg.Server.Raft.ElectionTick,  // 从配置读取
		HeartbeatTick: rc.cfg.Server.Raft.HeartbeatTick, // 从配置读取

		Storage: rc.raftStorage,

		// 性能优化参数（从配置读取）
		MaxSizePerMsg:             rc.cfg.Server.Raft.MaxSizePerMsg,
		MaxInflightMsgs:           rc.cfg.Server.Raft.MaxInflightMsgs,
		MaxUncommittedEntriesSize: rc.cfg.Server.Raft.MaxUncommittedEntriesSize,

		// 稳定性优化（从配置读取）
		PreVote:     rc.cfg.Server.Raft.PreVote,
		CheckQuorum: rc.cfg.Server.Raft.CheckQuorum,

		// 避免在网络分区时立即降级 leader
		// DisableProposalForwarding: false, // 允许 follower 转发提案（默认行为）
	}

	if oldNode || rc.join {
		rc.node = raft.RestartNode(c)
	} else {
		rc.node = raft.StartNode(c, rpeers)
	}

	rc.transport = &rafthttp.Transport{
		Logger:      rc.logger,
		ID:          types.ID(rc.id),
		ClusterID:   0x1000,
		Raft:        rc,
		ServerStats: stats.NewServerStats("", ""),
		LeaderStats: stats.NewLeaderStats(newLogger(), strconv.Itoa(rc.id)),
		ErrorC:      make(chan error),
	}

	rc.transport.Start()
	for i := range rc.peers {
		if i+1 != rc.id {
			rc.transport.AddPeer(types.ID(i+1), []string{rc.peers[i]})
		}
	}

	// 初始化批量提案系统（如果启用）
	// Witness nodes don't propose data, so batch system is not needed
	if rc.cfg.Server.Raft.Batch.Enable && !rc.isWitness() {
		batchConfig := batch.BatchConfig{
			MinBatchSize:  rc.cfg.Server.Raft.Batch.MinBatchSize,
			MaxBatchSize:  rc.cfg.Server.Raft.Batch.MaxBatchSize,
			MinTimeout:    rc.cfg.Server.Raft.Batch.MinTimeout,
			MaxTimeout:    rc.cfg.Server.Raft.Batch.MaxTimeout,
			LoadThreshold: rc.cfg.Server.Raft.Batch.LoadThreshold,
		}
		// batcher 拥有并管理输出通道，通过 ProposeC() 获取
		rc.batcher = batch.NewProposalBatcher(batchConfig, rc.proposeC, rc.logger)
		rc.batcher.Start(context.Background())
		rc.batchedProposeC = rc.batcher.ProposeC() // 获取 batcher 的输出通道
		rc.logger.Info("batch proposal system enabled",
			zap.Int("min_batch_size", batchConfig.MinBatchSize),
			zap.Int("max_batch_size", batchConfig.MaxBatchSize),
			zap.Duration("min_timeout", batchConfig.MinTimeout),
			zap.Duration("max_timeout", batchConfig.MaxTimeout),
			zap.Float64("load_threshold", batchConfig.LoadThreshold),
			zap.String("component", "raft-rocks"))
	} else if rc.isWitness() {
		rc.logger.Info("batch proposal system skipped (witness node)",
			zap.String("component", "raft-rocks-witness"))
	} else {
		rc.logger.Info("batch proposal system disabled", zap.String("component", "raft-rocks"))
	}

	// 初始化 Lease Read 系统（如果启用）
	if rc.cfg.Server.Raft.LeaseRead.Enable {
		// 计算选举超时和心跳间隔
		electionTimeout := time.Duration(rc.cfg.Server.Raft.ElectionTick) * rc.cfg.Server.Raft.TickInterval
		heartbeatInterval := time.Duration(rc.cfg.Server.Raft.HeartbeatTick) * rc.cfg.Server.Raft.TickInterval

		// 1. 创建智能配置管理器（支持动态扩缩容）
		rc.smartLeaseConfig = lease.NewSmartLeaseConfig(true, rc.logger)

		// 2. 检测初始集群规模
		initialClusterSize := lease.DetectClusterSizeFromPeers(rc.peers)
		rc.smartLeaseConfig.UpdateClusterSize(initialClusterSize)

		// 3. ✅ 总是创建组件（即使单节点）- 支持动态扩缩容
		leaseConfig := lease.LeaseConfig{
			ElectionTimeout: electionTimeout,
			HeartbeatTick:   heartbeatInterval,
			ClockDrift:      rc.cfg.Server.Raft.LeaseRead.ClockDrift,
		}
		rc.leaseManager = lease.NewLeaseManager(leaseConfig, rc.smartLeaseConfig, rc.logger)
		rc.readIndexManager = lease.NewReadIndexManager(rc.smartLeaseConfig, rc.logger)

		// 4. 启动自动检测集群规模变化（每60秒检测一次）
		go rc.smartLeaseConfig.StartAutoDetection(
			func() int {
				// 从 Raft 节点状态获取当前集群规模
				status := rc.node.Status()
				clusterSize := len(status.Progress)

				// 容错：如果 Raft 状态还未就绪（Progress 为空），使用 peers 作为后备
				if clusterSize == 0 {
					clusterSize = len(rc.peers)
				}

				return clusterSize
			},
			60*time.Second, // 检测间隔
			rc.stopc,       // 停止信号
		)

		rc.logger.Info("lease read system enabled with smart scaling",
			zap.Duration("election_timeout", electionTimeout),
			zap.Duration("heartbeat_interval", heartbeatInterval),
			zap.Duration("clock_drift", rc.cfg.Server.Raft.LeaseRead.ClockDrift),
			zap.Int("initial_cluster_size", initialClusterSize),
			zap.Bool("currently_enabled", rc.smartLeaseConfig.IsEnabled()),
			zap.String("component", "raft-rocks"))
	} else {
		rc.logger.Info("lease read system disabled", zap.String("component", "raft-rocks"))
	}

	// Create an initial snapshot if none exists (for new clusters)
	// This prevents "need non-empty snapshot" panic when leader tries to sync followers
	// Witness nodes skip snapshot creation as they don't store data
	if !oldNode && !rc.join && !rc.isWitness() {
		go func() {
			// Wait a bit for the node to be ready
			time.Sleep(100 * time.Millisecond)

			// Check if we already have a snapshot
			snap, err := rc.raftStorage.Snapshot()
			if err == nil && raft.IsEmptySnap(snap) {
				rc.logger.Info("creating initial snapshot for new cluster", zap.String("component", "raft-rocks"))
				data, err := rc.getSnapshot()
				if err != nil {
					rc.logger.Error("failed to get initial snapshot data", zap.Error(err), zap.String("component", "raft-rocks"))
					return
				}

				// Create initial snapshot at index 0
				_, err = rc.raftStorage.CreateSnapshot(0, &rc.confState, data)
				if err != nil {
					rc.logger.Error("failed to create initial snapshot", zap.Error(err), zap.String("component", "raft-rocks"))
				}
			}
		}()
	}

	// Log witness node startup
	if rc.isWitness() {
		rc.logger.Info("witness node started",
			zap.Int("id", rc.id),
			zap.Int("peer_count", len(rc.peers)),
			zap.Bool("persist_vote", rc.cfg.Server.Raft.Witness.PersistVote),
			zap.String("role", "witness"),
			zap.String("component", "raft-rocks-witness"))
	}

	go rc.serveRaft()
	go rc.serveChannels()
}

// stop closes http, closes all channels, and stops raft
func (rc *raftNodeRocks) stop() {
	rc.stopHTTP()

	// 停止批量提案器（如果启用）
	if rc.batcher != nil {
		rc.batcher.Stop()
	}

	close(rc.commitC)
	close(rc.errorC)
	rc.node.Stop()

	// Close RocksDB storage resources
	if rc.raftStorage != nil {
		rc.raftStorage.Close()
	}
}

func (rc *raftNodeRocks) stopHTTP() {
	rc.transport.Stop()
	close(rc.httpstopc)
	<-rc.httpdonec
}

func (rc *raftNodeRocks) publishSnapshot(snapshotToSave raftpb.Snapshot) {
	if raft.IsEmptySnap(snapshotToSave) {
		return
	}

	rc.logger.Info("publishing snapshot", zap.Uint64("index", rc.snapshotIndex), zap.String("component", "raft-rocks"))
	defer rc.logger.Info("finished publishing snapshot", zap.Uint64("index", rc.snapshotIndex), zap.String("component", "raft-rocks"))

	if snapshotToSave.Metadata.Index <= rc.appliedIndex {
		log.Fatalf("snapshot index [%d] should > progress.appliedIndex [%d]", snapshotToSave.Metadata.Index, rc.appliedIndex)
	}
	rc.commitC <- nil // trigger kvstore to load snapshot

	rc.confState = snapshotToSave.Metadata.ConfState
	rc.snapshotIndex = snapshotToSave.Metadata.Index
	rc.appliedIndex = snapshotToSave.Metadata.Index
}

func (rc *raftNodeRocks) maybeTriggerSnapshot(applyDoneC <-chan struct{}) {
	if rc.appliedIndex-rc.snapshotIndex <= rc.snapCount {
		return
	}

	// wait until all committed entries are applied (or server is closed)
	if applyDoneC != nil {
		select {
		case <-applyDoneC:
		case <-rc.stopc:
			return
		}
	}

	rc.logger.Info("start snapshot",
		zap.Uint64("applied_index", rc.appliedIndex),
		zap.Uint64("last_snapshot_index", rc.snapshotIndex),
		zap.String("component", "raft-rocks"))
	data, err := rc.getSnapshot()
	if err != nil {
		log.Panic(err)
	}

	// Create snapshot using RocksDB storage
	snap, err := rc.raftStorage.CreateSnapshot(rc.appliedIndex, &rc.confState, data)
	if err != nil {
		panic(err)
	}

	// Save snapshot to file system
	if err := rc.saveSnap(snap); err != nil {
		panic(err)
	}

	// Compact RocksDB storage
	compactIndex := uint64(1)
	if rc.appliedIndex > snapshotCatchUpEntriesN {
		compactIndex = rc.appliedIndex - snapshotCatchUpEntriesN
	}
	if err := rc.raftStorage.Compact(compactIndex); err != nil {
		if !errors.Is(err, raft.ErrCompacted) {
			panic(err)
		}
	} else {
		rc.logger.Info("compacted log", zap.Uint64("index", compactIndex), zap.String("component", "raft-rocks"))
	}

	rc.snapshotIndex = rc.appliedIndex
}

func (rc *raftNodeRocks) serveChannels() {
	snap, err := rc.raftStorage.Snapshot()
	if err != nil {
		panic(err)
	}
	rc.confState = snap.Metadata.ConfState
	rc.snapshotIndex = snap.Metadata.Index
	rc.appliedIndex = snap.Metadata.Index

	// 使用配置文件中的 tick 间隔
	ticker := time.NewTicker(rc.cfg.Server.Raft.TickInterval)
	defer ticker.Stop()

	// send proposals over raft
	go func() {
		confChangeCount := uint64(0)

		// 如果启用了批量提案，从 batchedProposeC 读取
		if rc.cfg.Server.Raft.Batch.Enable {
			for rc.batchedProposeC != nil && rc.confChangeC != nil {
				select {
				case batchedProp, ok := <-rc.batchedProposeC:
					if !ok {
						rc.batchedProposeC = nil
					} else {
						// 批量提案已经编码为 []byte，直接提交
						rc.node.Propose(context.TODO(), batchedProp)
					}

				case cc, ok := <-rc.confChangeC:
					if !ok {
						rc.confChangeC = nil
					} else {
						confChangeCount++
						cc.ID = confChangeCount
						rc.node.ProposeConfChange(context.TODO(), cc)
					}
				}
			}
		} else {
			// 不启用批量提案，使用原始逻辑
			for rc.proposeC != nil && rc.confChangeC != nil {
				select {
				case prop, ok := <-rc.proposeC:
					if !ok {
						rc.proposeC = nil
					} else {
						// blocks until accepted by raft state machine
						rc.node.Propose(context.TODO(), []byte(prop))
					}

				case cc, ok := <-rc.confChangeC:
					if !ok {
						rc.confChangeC = nil
					} else {
						confChangeCount++
						cc.ID = confChangeCount
						rc.node.ProposeConfChange(context.TODO(), cc)
					}
				}
			}
		}
		// client closed channel; shutdown raft if not already
		close(rc.stopc)
	}()

	// 单节点租约续期定时器（方案3：单节点特殊处理）
	// 用于单节点场景下定期续约租约,因为单节点没有心跳消息触发Ready事件
	heartbeatInterval := time.Duration(rc.cfg.Server.Raft.HeartbeatTick) * rc.cfg.Server.Raft.TickInterval
	leaseRenewTicker := time.NewTicker(heartbeatInterval / 2)
	defer leaseRenewTicker.Stop()

	// event loop on raft state machine updates
	for {
		select {
		case <-ticker.C:
			rc.node.Tick()

		// 单节点租约续期定时器触发
		case <-leaseRenewTicker.C:
			// 仅在单节点场景下执行租约续期
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil && rc.leaseManager.IsLeader() {
				status := rc.node.Status()
				totalNodes := len(status.Progress)

				// 仅对单节点执行定时续约
				if totalNodes == 1 {
					rc.tryRenewLease()
				}
			}

		// store raft entries to RocksDB, then publish over commit channel
		case rd := <-rc.node.Ready():
			// Lease Read: 处理角色变更
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil {
				if rd.SoftState != nil {
					// 检查角色变更
					if rd.SoftState.RaftState == raft.StateLeader {
						rc.leaseManager.OnBecomeLeader()
					} else {
						rc.leaseManager.OnBecomeFollower()
					}
				}
			}

			// Save hard state to RocksDB
			if !raft.IsEmptyHardState(rd.HardState) {
				if err := rc.raftStorage.SetHardState(rd.HardState); err != nil {
					log.Fatalf("failed to save hard state: %v", err)
				}
			}

			// Handle snapshot
			if !raft.IsEmptySnap(rd.Snapshot) {
				if err := rc.raftStorage.ApplySnapshot(rd.Snapshot); err != nil {
					log.Fatalf("failed to apply snapshot: %v", err)
				}
				if err := rc.saveSnap(rd.Snapshot); err != nil {
					log.Fatalf("failed to save snapshot: %v", err)
				}
				rc.publishSnapshot(rd.Snapshot)
			}

			// Append entries to RocksDB
			if len(rd.Entries) > 0 {
				if err := rc.raftStorage.Append(rd.Entries); err != nil {
					log.Fatalf("failed to append entries: %v", err)
				}
			}

			// Send messages to peers
			rc.transport.Send(rc.processMessages(rd.Messages))

			// Lease Read: 处理心跳响应以续约租约(多节点场景)
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil && rc.leaseManager.IsLeader() {
				rc.tryRenewLease()
			}

			// Apply committed entries
			applyDoneC, ok := rc.publishEntries(rc.entriesToApply(rd.CommittedEntries))
			if !ok {
				rc.stop()
				return
			}

			// Trigger snapshot if needed
			rc.maybeTriggerSnapshot(applyDoneC)

			rc.node.Advance()

		case err := <-rc.transport.ErrorC:
			rc.writeError(err)
			return

		case <-rc.stopc:
			rc.stop()
			return
		}
	}
}

// processMessages updates conf state in snapshot messages
func (rc *raftNodeRocks) processMessages(ms []raftpb.Message) []raftpb.Message {
	for i := 0; i < len(ms); i++ {
		if ms[i].Type == raftpb.MsgSnap {
			ms[i].Snapshot.Metadata.ConfState = rc.confState
		}
	}
	return ms
}

func (rc *raftNodeRocks) serveRaft() {
	url, err := url.Parse(rc.peers[rc.id-1])
	if err != nil {
		log.Fatalf("store: Failed parsing URL (%v)", err)
	}

	ln, err := NewStoppableListener(url.Host, rc.httpstopc)
	if err != nil {
		log.Fatalf("store: Failed to listen rafthttp (%v)", err)
	}

	err = (&http.Server{Handler: rc.transport.Handler()}).Serve(ln)
	select {
	case <-rc.httpstopc:
	default:
		log.Fatalf("store: Failed to serve rafthttp (%v)", err)
	}
	close(rc.httpdonec)
}

func (rc *raftNodeRocks) Process(ctx context.Context, m raftpb.Message) error {
	return rc.node.Step(ctx, m)
}

func (rc *raftNodeRocks) IsIDRemoved(_ uint64) bool { return false }

func (rc *raftNodeRocks) ReportUnreachable(id uint64) { rc.node.ReportUnreachable(id) }

func (rc *raftNodeRocks) ReportSnapshot(id uint64, status raft.SnapshotStatus) {
	rc.node.ReportSnapshot(id, status)
}

// Status 返回 Raft 状态信息
func (rc *raftNodeRocks) Status() kvstore.RaftStatus {
	status := rc.node.Status()
	return kvstore.RaftStatus{
		NodeID:   status.ID,
		Term:     status.Term,
		LeaderID: status.Lead,
		State:    status.RaftState.String(),
		Applied:  status.Applied,
		Commit:   status.Commit,
	}
}

// TransferLeadership 将 leader 角色转移到指定节点
func (rc *raftNodeRocks) TransferLeadership(targetID uint64) error {
	rc.node.TransferLeadership(context.TODO(), 0, targetID)
	return nil
}

// LeaseManager 返回租约管理器（用于测试）
func (rc *raftNodeRocks) LeaseManager() *lease.LeaseManager {
	return rc.leaseManager
}

// ReadIndexManager 返回读索引管理器（用于测试）
func (rc *raftNodeRocks) ReadIndexManager() *lease.ReadIndexManager {
	return rc.readIndexManager
}

// tryRenewLease 尝试续约租约
// 统计活跃节点数量并调用租约管理器进行续约
// 该方法被以下两个场景调用：
// 1. 单节点场景：定时器触发
// 2. 多节点场景：Ready 事件触发（心跳响应）
func (rc *raftNodeRocks) tryRenewLease() {
	status := rc.node.Status()
	totalNodes := len(status.Progress)
	activeNodes := 0

	// 统计活跃的节点数量（包括自己）
	for _, pr := range status.Progress {
		if pr.RecentActive {
			activeNodes++
		}
	}

	// 调用租约管理器续约
	renewed := rc.leaseManager.RenewLease(activeNodes, totalNodes)

	// 只在首次续约或调试时记录日志
	if renewed && rc.cfg.Server.Raft.LeaseRead.Enable {
		// rc.logger.Info("租约续约成功",
		// 	zap.Int("activeNodes", activeNodes),
		// 	zap.Int("totalNodes", totalNodes))
	}
}

// IsStopped 检查节点是否已停止（用于测试）
func (rc *raftNodeRocks) IsStopped() bool {
	select {
	case <-rc.stopc:
		return true
	default:
		return false
	}
}
