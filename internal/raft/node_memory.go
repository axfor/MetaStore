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
	"metaStore/pkg/config"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	stats "go.etcd.io/etcd/server/v3/etcdserver/api/v2stats"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type commit = kvstore.Commit

// Legacy struct (using kvstore.Commit now)
type _commit struct {
	data       []string
	applyDoneC chan<- struct{}
}

// A key-value stream backed by raft
type raftNode struct {
	proposeC    <-chan string            // proposed messages (k,v)
	confChangeC <-chan raftpb.ConfChange // proposed cluster config changes
	commitC     chan<- *kvstore.Commit   // entries committed to log (k,v)
	errorC      chan<- error             // errors from raft session

	id          int      // client ID for raft session
	peers       []string // raft peer URLs
	join        bool     // node is joining an existing cluster
	waldir      string   // path to WAL directory
	snapdir     string   // path to snapshot directory
	getSnapshot func() ([]byte, error)

	confState     raftpb.ConfState
	snapshotIndex uint64
	appliedIndex  uint64

	// raft backing for the commit/error channel
	node        raft.Node
	raftStorage *raft.MemoryStorage
	wal         *wal.WAL

	snapshotter      *snap.Snapshotter
	snapshotterReady chan *snap.Snapshotter // signals when snapshotter is ready

	snapCount uint64
	transport *rafthttp.Transport
	stopc     chan struct{} // signals proposal channel closed
	httpstopc chan struct{} // signals http server to shutdown
	httpdonec chan struct{} // signals http server shutdown complete

	// 批量提案系统（optional）
	batcher         *batch.ProposalBatcher // 批量提案器（ifenabled）
	batchedProposeC <-chan []byte          // 批量提案channel（ifenabled批量，from batcher get）

	// Lease Read 系统（optional）
	smartLeaseConfig *lease.SmartLeaseConfig // 智能configmanager（supporteddynamic扩缩容）
	leaseManager     *lease.LeaseManager     // leasemanager（ifenabled）
	readIndexManager *lease.ReadIndexManager // ReadIndex manager（ifenabled）

	logger *zap.Logger
	cfg    *config.Config // Raft configuration
}

var defaultSnapshotCount uint64 = 10000

// isWitness returns true if this node is configured as a witness node
// Witness nodes participate in Raft voting but don't store data
func (rc *raftNode) isWitness() bool {
	return rc.cfg != nil && rc.cfg.Server.Raft.IsWitness()
}

// newRaftNode initiates a raft instance and returns a committed log entry
// channel and error channel. Proposals for log updates are sent over the
// provided the proposal channel. All log entries are replayed over the
// commit channel, followed by a nil message (to indicate the channel is
// current), then new log entries. To shutdown, close proposeC and read errorC.
// storageType: "memory" or "rocksdb" to separate data directories
func NewNode(id int, peers []string, join bool, getSnapshot func() ([]byte, error), proposeC <-chan string,
	confChangeC <-chan raftpb.ConfChange, storageType string, cfg *config.Config,
) (<-chan *kvstore.Commit, <-chan error, <-chan *snap.Snapshotter, *raftNode) {
	commitC := make(chan *kvstore.Commit)
	errorC := make(chan error)

	// Default to "memory" if not specified
	if storageType == "" {
		storageType = "memory"
	}

	rc := &raftNode{
		proposeC:    proposeC,
		confChangeC: confChangeC,
		commitC:     commitC,
		errorC:      errorC,
		id:          id,
		peers:       peers,
		join:        join,
		waldir:      fmt.Sprintf("data/%s/%d/wal", storageType, id),
		snapdir:     fmt.Sprintf("data/%s/%d/snap", storageType, id),
		getSnapshot: getSnapshot,
		snapCount:   defaultSnapshotCount,
		stopc:       make(chan struct{}),
		httpstopc:   make(chan struct{}),
		httpdonec:   make(chan struct{}),

		logger: newLogger(),
		cfg:    cfg, // Store config reference

		snapshotterReady: make(chan *snap.Snapshotter, 1),
		// rest of structure populated after WAL replay
	}
	go rc.startRaft()
	return commitC, errorC, rc.snapshotterReady, rc
}

func newLogger(options ...zap.Option) *zap.Logger {
	encoderCfg := zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		NameKey:        "logger",
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encoderCfg), os.Stdout, zap.InfoLevel)
	return zap.New(core).WithOptions(options...)
}

func (rc *raftNode) saveSnap(snap raftpb.Snapshot) error {
	walSnap := walpb.Snapshot{
		Index:     snap.Metadata.Index,
		Term:      snap.Metadata.Term,
		ConfState: &snap.Metadata.ConfState,
	}
	// save the snapshot file before writing the snapshot to the wal.
	// This makes it possible for the snapshot file to become orphaned, but prevents
	// a WAL snapshot entry from having no corresponding snapshot file.
	if err := rc.snapshotter.SaveSnap(snap); err != nil {
		return err
	}
	if err := rc.wal.SaveSnapshot(walSnap); err != nil {
		return err
	}
	return rc.wal.ReleaseLockTo(snap.Metadata.Index)
}

func (rc *raftNode) entriesToApply(ents []raftpb.Entry) (nents []raftpb.Entry) {
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

// publishEntries writes committed log entries to commit channel and returns
// whether all entries could be published.
func (rc *raftNode) publishEntries(ents []raftpb.Entry) (<-chan struct{}, bool) {
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

			// ifenabled批量提案，needdecode批量提案
			if rc.cfg.Server.Raft.Batch.Enable {
				proposals, err := batch.DecodeBatch(ents[i].Data)
				if err != nil {
					rc.logger.Error("failed to decode batch proposal",
						zap.Error(err),
						zap.Uint64("index", ents[i].Index),
						zap.String("component", "raft-memory"))
					continue
				}
				data = append(data, proposals...)
			} else {
				// notenabled批量提案，直接use字符串
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

	// Lease Read: notification ReadIndexManager 应用进度
	if rc.cfg.Server.Raft.LeaseRead.Enable && rc.readIndexManager != nil {
		rc.readIndexManager.NotifyApplied(rc.appliedIndex)
	}

	return applyDoneC, true
}

// publishEntriesAsWitness handles entries for witness nodes
// Witness nodes only process ConfChange entries (cluster membership changes)
// They skip all data entries since they don't store data
func (rc *raftNode) publishEntriesAsWitness(ents []raftpb.Entry) (<-chan struct{}, bool) {
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
					zap.String("component", "raft-memory-witness"))

			case raftpb.ConfChangeRemoveNode:
				if cc.NodeID == uint64(rc.id) {
					rc.logger.Warn("witness: I've been removed from the cluster! Shutting down.",
						zap.String("component", "raft-memory-witness"))
					return nil, false
				}
				rc.transport.RemovePeer(types.ID(cc.NodeID))
				rc.logger.Info("witness: removed peer",
					zap.Uint64("node_id", cc.NodeID),
					zap.String("component", "raft-memory-witness"))
			}
		}
	}

	// Update appliedIndex even for witness nodes (for Raft protocol correctness)
	rc.appliedIndex = ents[len(ents)-1].Index

	return nil, true
}

func (rc *raftNode) loadSnapshot() *raftpb.Snapshot {
	if wal.Exist(rc.waldir) {
		walSnaps, err := wal.ValidSnapshotEntries(rc.logger, rc.waldir)
		if err != nil {
			log.Fatalf("store: error listing snapshots (%v)", err)
		}
		snapshot, err := rc.snapshotter.LoadNewestAvailable(walSnaps)
		if err != nil && !errors.Is(err, snap.ErrNoSnapshot) {
			log.Fatalf("store: error loading snapshot (%v)", err)
		}
		return snapshot
	}
	return &raftpb.Snapshot{}
}

// openWAL returns a WAL ready for reading.
func (rc *raftNode) openWAL(snapshot *raftpb.Snapshot) *wal.WAL {
	if !wal.Exist(rc.waldir) {
		if err := os.MkdirAll(rc.waldir, 0o750); err != nil {
			log.Fatalf("store: cannot create dir for wal (%v)", err)
		}

		w, err := wal.Create(newLogger(), rc.waldir, nil)
		if err != nil {
			log.Fatalf("store: create wal error (%v)", err)
		}
		w.Close()
	}

	walsnap := walpb.Snapshot{}
	if snapshot != nil {
		walsnap.Index, walsnap.Term = snapshot.Metadata.Index, snapshot.Metadata.Term
	}
	rc.logger.Info("loading WAL", zap.Uint64("term", walsnap.Term), zap.Uint64("index", walsnap.Index), zap.String("component", "raft-memory"))
	w, err := wal.Open(newLogger(), rc.waldir, walsnap)
	if err != nil {
		log.Fatalf("store: error loading wal (%v)", err)
	}

	return w
}

// replayWAL replays WAL entries into the raft instance.
func (rc *raftNode) replayWAL() *wal.WAL {
	rc.logger.Info("replaying WAL", zap.Int("member_id", rc.id), zap.String("component", "raft-memory"))
	snapshot := rc.loadSnapshot()
	w := rc.openWAL(snapshot)
	_, st, ents, err := w.ReadAll()
	if err != nil {
		log.Fatalf("store: failed to read WAL (%v)", err)
	}
	rc.raftStorage = raft.NewMemoryStorage()
	if snapshot != nil {
		rc.raftStorage.ApplySnapshot(*snapshot)
	}
	rc.raftStorage.SetHardState(st)

	// append to storage so raft starts at the right place in log
	rc.raftStorage.Append(ents)

	return w
}

func (rc *raftNode) writeError(err error) {
	rc.stopHTTP()
	close(rc.commitC)
	rc.errorC <- err
	close(rc.errorC)
	rc.node.Stop()
}

func (rc *raftNode) startRaft() {
	if !fileutil.Exist(rc.snapdir) {
		if err := os.MkdirAll(rc.snapdir, 0o750); err != nil {
			log.Fatalf("store: cannot create dir for snapshot (%v)", err)
		}
	}
	rc.snapshotter = snap.New(newLogger(), rc.snapdir)

	oldwal := wal.Exist(rc.waldir)
	rc.wal = rc.replayWAL()

	// signal replay has finished
	rc.snapshotterReady <- rc.snapshotter

	rpeers := make([]raft.Peer, len(rc.peers))
	for i := range rpeers {
		rpeers[i] = raft.Peer{ID: uint64(i + 1)}
	}
	// Raft config - fromconfigfileread（基at业界最佳实践：etcd、TiKV、CockroachDB）
	c := &raft.Config{
		ID:            uint64(rc.id),
		ElectionTick:  rc.cfg.Server.Raft.ElectionTick,  // fromconfigread
		HeartbeatTick: rc.cfg.Server.Raft.HeartbeatTick, // fromconfigread

		Storage: rc.raftStorage,

		// 性能optimizeargument（fromconfigread）
		MaxSizePerMsg:             rc.cfg.Server.Raft.MaxSizePerMsg,
		MaxInflightMsgs:           rc.cfg.Server.Raft.MaxInflightMsgs,
		MaxUncommittedEntriesSize: rc.cfg.Server.Raft.MaxUncommittedEntriesSize,

		// stable性optimize（fromconfigread）
		PreVote:     rc.cfg.Server.Raft.PreVote,
		CheckQuorum: rc.cfg.Server.Raft.CheckQuorum,

		// 避免innetworkpartition时立即degradation leader
		// DisableProposalForwarding: false, // allow follower 转发提案（defaultrowas）
	}

	if oldwal || rc.join {
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

	// initialize批量提案系统（ifenabled）
	// Witness nodes don't propose data, so batch system is not needed
	if rc.cfg.Server.Raft.Batch.Enable && !rc.isWitness() {
		batchConfig := batch.BatchConfig{
			MinBatchSize:  rc.cfg.Server.Raft.Batch.MinBatchSize,
			MaxBatchSize:  rc.cfg.Server.Raft.Batch.MaxBatchSize,
			MinTimeout:    rc.cfg.Server.Raft.Batch.MinTimeout,
			MaxTimeout:    rc.cfg.Server.Raft.Batch.MaxTimeout,
			LoadThreshold: rc.cfg.Server.Raft.Batch.LoadThreshold,
		}
		// batcher 拥有并managementoutputchannel，via ProposeC() get
		rc.batcher = batch.NewProposalBatcher(batchConfig, rc.proposeC, rc.logger)
		rc.batcher.Start(context.Background())
		rc.batchedProposeC = rc.batcher.ProposeC() // get batcher outputchannel
		rc.logger.Info("batch proposal system enabled",
			zap.Int("min_batch_size", batchConfig.MinBatchSize),
			zap.Int("max_batch_size", batchConfig.MaxBatchSize),
			zap.Duration("min_timeout", batchConfig.MinTimeout),
			zap.Duration("max_timeout", batchConfig.MaxTimeout),
			zap.Float64("load_threshold", batchConfig.LoadThreshold),
			zap.String("component", "raft-memory"))
	} else if rc.isWitness() {
		rc.logger.Info("batch proposal system skipped (witness node)",
			zap.String("component", "raft-memory-witness"))
	} else {
		rc.logger.Info("batch proposal system disabled", zap.String("component", "raft-memory"))
	}

	// initialize Lease Read 系统（ifenabled）
	if rc.cfg.Server.Raft.LeaseRead.Enable {
		// calculate选举timeoutand心跳interval
		electionTimeout := time.Duration(rc.cfg.Server.Raft.ElectionTick) * rc.cfg.Server.Raft.TickInterval
		heartbeatInterval := time.Duration(rc.cfg.Server.Raft.HeartbeatTick) * rc.cfg.Server.Raft.TickInterval

		// 1. create智能configmanager（supporteddynamic扩缩容）
		rc.smartLeaseConfig = lease.NewSmartLeaseConfig(true, rc.logger)

		// 2. 检测initialcluster规模
		initialClusterSize := lease.DetectClusterSizeFromPeers(rc.peers)
		rc.smartLeaseConfig.UpdateClusterSize(initialClusterSize)

		// 3. ✅ alwayscreatecomponent（即使单node）- supporteddynamic扩缩容
		leaseConfig := lease.LeaseConfig{
			ElectionTimeout: electionTimeout,
			HeartbeatTick:   heartbeatInterval,
			ClockDrift:      rc.cfg.Server.Raft.LeaseRead.ClockDrift,
		}
		rc.leaseManager = lease.NewLeaseManager(leaseConfig, rc.smartLeaseConfig, rc.logger)
		rc.readIndexManager = lease.NewReadIndexManager(rc.smartLeaseConfig, rc.logger)

		// 4. start自动检测cluster规模变化（每60秒检测一次）
		go rc.smartLeaseConfig.StartAutoDetection(
			func() int {
				// from Raft nodestatusgetcurrentcluster规模
				status := rc.node.Status()
				clusterSize := len(status.Progress)

				// 容错：if Raft status还未ready（Progress asempty），use peers 作as后备
				if clusterSize == 0 {
					clusterSize = len(rc.peers)
				}

				return clusterSize
			},
			60*time.Second, // 检测interval
			rc.stopc,       // stopped信号
		)

		rc.logger.Info("lease read system enabled with smart scaling",
			zap.Duration("election_timeout", electionTimeout),
			zap.Duration("heartbeat_interval", heartbeatInterval),
			zap.Duration("clock_drift", rc.cfg.Server.Raft.LeaseRead.ClockDrift),
			zap.Duration("read_timeout", rc.cfg.Server.Raft.LeaseRead.ReadTimeout),
			zap.Int("initial_cluster_size", initialClusterSize),
			zap.Bool("currently_enabled", rc.smartLeaseConfig.IsEnabled()),
			zap.String("component", "raft-memory"))
	} else {
		rc.logger.Info("lease read system disabled", zap.String("component", "raft-memory"))
	}

	// Log witness node startup
	if rc.isWitness() {
		rc.logger.Info("witness node started",
			zap.Int("id", rc.id),
			zap.Int("peer_count", len(rc.peers)),
			zap.Bool("persist_vote", rc.cfg.Server.Raft.Witness.PersistVote),
			zap.String("role", "witness"),
			zap.String("component", "raft-memory-witness"))
	}

	go rc.serveRaft()
	go rc.serveChannels()
}

// stop closes http, closes all channels, and stops raft.
func (rc *raftNode) stop() {
	rc.stopHTTP()

	// stopped批量提案器（ifenabled）
	if rc.batcher != nil {
		rc.batcher.Stop()
	}

	close(rc.commitC)
	close(rc.errorC)
	rc.node.Stop()
}

func (rc *raftNode) stopHTTP() {
	rc.transport.Stop()
	close(rc.httpstopc)
	<-rc.httpdonec
}

func (rc *raftNode) publishSnapshot(snapshotToSave raftpb.Snapshot) {
	if raft.IsEmptySnap(snapshotToSave) {
		return
	}

	rc.logger.Info("publishing snapshot", zap.Uint64("index", rc.snapshotIndex), zap.String("component", "raft-memory"))
	defer rc.logger.Info("finished publishing snapshot", zap.Uint64("index", rc.snapshotIndex), zap.String("component", "raft-memory"))

	if snapshotToSave.Metadata.Index <= rc.appliedIndex {
		log.Fatalf("snapshot index [%d] should > progress.appliedIndex [%d]", snapshotToSave.Metadata.Index, rc.appliedIndex)
	}
	rc.commitC <- nil // trigger kvstore to load snapshot

	rc.confState = snapshotToSave.Metadata.ConfState
	rc.snapshotIndex = snapshotToSave.Metadata.Index
	rc.appliedIndex = snapshotToSave.Metadata.Index
}

var snapshotCatchUpEntriesN uint64 = 10000

func (rc *raftNode) maybeTriggerSnapshot(applyDoneC <-chan struct{}) {
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
		zap.String("component", "raft-memory"))
	data, err := rc.getSnapshot()
	if err != nil {
		log.Panic(err)
	}
	snap, err := rc.raftStorage.CreateSnapshot(rc.appliedIndex, &rc.confState, data)
	if err != nil {
		panic(err)
	}
	if err := rc.saveSnap(snap); err != nil {
		panic(err)
	}

	compactIndex := uint64(1)
	if rc.appliedIndex > snapshotCatchUpEntriesN {
		compactIndex = rc.appliedIndex - snapshotCatchUpEntriesN
	}
	if err := rc.raftStorage.Compact(compactIndex); err != nil {
		if !errors.Is(err, raft.ErrCompacted) {
			panic(err)
		}
	} else {
		rc.logger.Info("compacted log", zap.Uint64("index", compactIndex), zap.String("component", "raft-memory"))
	}

	rc.snapshotIndex = rc.appliedIndex
}

func (rc *raftNode) serveChannels() {
	snap, err := rc.raftStorage.Snapshot()
	if err != nil {
		panic(err)
	}
	rc.confState = snap.Metadata.ConfState
	rc.snapshotIndex = snap.Metadata.Index
	rc.appliedIndex = snap.Metadata.Index

	defer rc.wal.Close()

	// useconfigfile中 tick interval
	ticker := time.NewTicker(rc.cfg.Server.Raft.TickInterval)
	defer ticker.Stop()

	// send proposals over raft
	go func() {
		confChangeCount := uint64(0)

		// ifenabled批量提案，from batchedProposeC read
		if rc.cfg.Server.Raft.Batch.Enable {
			for rc.batchedProposeC != nil && rc.confChangeC != nil {
				select {
				case batchedProp, ok := <-rc.batchedProposeC:
					if !ok {
						rc.batchedProposeC = nil
					} else {
						// 批量提案alreadyencodeas []byte，直接commit
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
			// notenabled批量提案，use原始逻辑
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

	// 单nodelease续期schedule器（方案3：单node特殊handle）
	// 用at单node场景下定期renewallease,因as单nodenone心跳messagetriggerReadyevent
	heartbeatInterval := time.Duration(rc.cfg.Server.Raft.HeartbeatTick) * rc.cfg.Server.Raft.TickInterval
	leaseRenewTicker := time.NewTicker(heartbeatInterval / 2)
	defer leaseRenewTicker.Stop()

	// event loop on raft state machine updates
	for {
		select {
		case <-ticker.C:
			rc.node.Tick()

		// 单nodelease续期schedule器trigger
		case <-leaseRenewTicker.C:
			// 仅in单node场景下executelease续期
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil && rc.leaseManager.IsLeader() {
				status := rc.node.Status()
				totalNodes := len(status.Progress)

				// 仅对单nodeexecuteschedulerenewal
				if totalNodes == 1 {
					rc.tryRenewLease()
				}
			}

		// store raft entries to wal, then publish over commit channel
		case rd := <-rc.node.Ready():
			// Lease Read: handlerolechange
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil {
				if rd.SoftState != nil {
					// checkrolechange
					if rd.SoftState.RaftState == raft.StateLeader {
						rc.leaseManager.OnBecomeLeader()
					} else {
						rc.leaseManager.OnBecomeFollower()
					}
				}
			}

			// Must save the snapshot file and WAL snapshot entry before saving any other entries
			// or hardstate to ensure that recovery after a snapshot restore is possible.
			if !raft.IsEmptySnap(rd.Snapshot) {
				rc.saveSnap(rd.Snapshot)
			}
			rc.wal.Save(rd.HardState, rd.Entries)
			if !raft.IsEmptySnap(rd.Snapshot) {
				rc.raftStorage.ApplySnapshot(rd.Snapshot)
				rc.publishSnapshot(rd.Snapshot)
			}
			rc.raftStorage.Append(rd.Entries)
			rc.transport.Send(rc.processMessages(rd.Messages))

			// Lease Read: handle心跳response以renewallease(多node场景)
			if rc.cfg.Server.Raft.LeaseRead.Enable && rc.leaseManager != nil && rc.leaseManager.IsLeader() {
				rc.tryRenewLease()
			}

			applyDoneC, ok := rc.publishEntries(rc.entriesToApply(rd.CommittedEntries))
			if !ok {
				rc.stop()
				return
			}
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

// When there is a `raftpb.EntryConfChange` after creating the snapshot,
// then the confState included in the snapshot is out of date. so We need
// to update the confState before sending a snapshot to a follower.
func (rc *raftNode) processMessages(ms []raftpb.Message) []raftpb.Message {
	for i := 0; i < len(ms); i++ {
		if ms[i].Type == raftpb.MsgSnap {
			ms[i].Snapshot.Metadata.ConfState = rc.confState
		}
	}
	return ms
}

func (rc *raftNode) serveRaft() {
	// edge界check：确保nodeIDinvalidrange内
	peerIndex := rc.id - 1
	if peerIndex < 0 || peerIndex >= len(rc.peers) {
		log.Fatalf("store: Invalid node ID %d for %d peers", rc.id, len(rc.peers))
		return
	}

	url, err := url.Parse(rc.peers[peerIndex])
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

func (rc *raftNode) Process(ctx context.Context, m raftpb.Message) error {
	return rc.node.Step(ctx, m)
}
func (rc *raftNode) IsIDRemoved(_ uint64) bool   { return false }
func (rc *raftNode) ReportUnreachable(id uint64) { rc.node.ReportUnreachable(id) }
func (rc *raftNode) ReportSnapshot(id uint64, status raft.SnapshotStatus) {
	rc.node.ReportSnapshot(id, status)
}

// Status return Raft statusinfo
func (rc *raftNode) Status() kvstore.RaftStatus {
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

// TransferLeadership will leader role转移tospecifiednode
func (rc *raftNode) TransferLeadership(targetID uint64) error {
	rc.node.TransferLeadership(context.TODO(), 0, targetID)
	return nil
}

// LeaseManager returnleasemanager（用attest）
func (rc *raftNode) LeaseManager() *lease.LeaseManager {
	return rc.leaseManager
}

// ReadIndexManager return读indexmanager（用attest）
func (rc *raftNode) ReadIndexManager() *lease.ReadIndexManager {
	return rc.readIndexManager
}

// tryRenewLease 尝试renewallease
// statisticsactivenodequantity并callleasemanager进rowrenewal
// 该method被以下两个场景call：
// 1. 单node场景：schedule器trigger
// 2. 多node场景：Ready eventtrigger（心跳response）
func (rc *raftNode) tryRenewLease() {
	status := rc.node.Status()
	totalNodes := len(status.Progress)
	activeNodes := 0

	// statisticsactivenodequantity（package括自己）
	for _, pr := range status.Progress {
		if pr.RecentActive {
			activeNodes++
		}
	}

	// callleasemanagerrenewal
	renewed := rc.leaseManager.RenewLease(activeNodes, totalNodes)

	// 只in首次renewalordebug时recordlog
	if renewed && rc.cfg.Server.Raft.LeaseRead.Enable {
		// log.Printf("[Lease] leaserenewalsuccess - activeNodes=%d, totalNodes=%d", activeNodes, totalNodes)
	}
}

// IsStopped checknodeisno已stopped（用attest）
func (rc *raftNode) IsStopped() bool {
	select {
	case <-rc.stopc:
		return true
	default:
		return false
	}
}
