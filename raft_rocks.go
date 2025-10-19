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
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

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
	commitC     chan<- *commit           // entries committed to log (k,v)
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
	raftStorage *RocksDBStorage
	rocksDB     *grocksdb.DB

	snapshotter      *snap.Snapshotter
	snapshotterReady chan *snap.Snapshotter // signals when snapshotter is ready

	snapCount uint64
	transport *rafthttp.Transport
	stopc     chan struct{} // signals proposal channel closed
	httpstopc chan struct{} // signals http server to shutdown
	httpdonec chan struct{} // signals http server shutdown complete

	logger *zap.Logger
}

// newRaftNodeRocks initiates a raft instance backed by RocksDB
func newRaftNodeRocks(id int, peers []string, join bool, getSnapshot func() ([]byte, error),
	proposeC <-chan string, confChangeC <-chan raftpb.ConfChange, rocksDB *grocksdb.DB,
) (<-chan *commit, <-chan error, <-chan *snap.Snapshotter) {
	commitC := make(chan *commit)
	errorC := make(chan error)

	rc := &raftNodeRocks{
		proposeC:    proposeC,
		confChangeC: confChangeC,
		commitC:     commitC,
		errorC:      errorC,
		id:          id,
		peers:       peers,
		join:        join,
		dbdir:       fmt.Sprintf("store-%d-rocksdb", id),
		snapdir:     fmt.Sprintf("store-%d-snap", id),
		getSnapshot: getSnapshot,
		snapCount:   defaultSnapshotCount,
		stopc:       make(chan struct{}),
		httpstopc:   make(chan struct{}),
		httpdonec:   make(chan struct{}),
		rocksDB:     rocksDB,

		logger: newLogger(),

		snapshotterReady: make(chan *snap.Snapshotter, 1),
	}
	go rc.startRaft()
	return commitC, errorC, rc.snapshotterReady
}

func (rc *raftNodeRocks) saveSnap(snap raftpb.Snapshot) error {
	// Save snapshot to file system using snapshotter
	if err := rc.snapshotter.SaveSnap(snap); err != nil {
		return err
	}
	log.Printf("saved snapshot at index %d", snap.Metadata.Index)
	return nil
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

	data := make([]string, 0, len(ents))
	for i := range ents {
		switch ents[i].Type {
		case raftpb.EntryNormal:
			if len(ents[i].Data) == 0 {
				// ignore empty messages
				break
			}
			s := string(ents[i].Data)
			data = append(data, s)
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
		case rc.commitC <- &commit{data, applyDoneC}:
		case <-rc.stopc:
			return nil, false
		}
	}

	// after commit, update appliedIndex
	rc.appliedIndex = ents[len(ents)-1].Index

	return applyDoneC, true
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
	storage, err := NewRocksDBStorage(rc.rocksDB, nodeID)
	if err != nil {
		return fmt.Errorf("failed to create RocksDB storage: %v", err)
	}
	rc.raftStorage = storage

	// Load snapshot and apply to RocksDB storage
	snapshot := rc.loadSnapshot()
	if snapshot != nil && !raft.IsEmptySnap(*snapshot) {
		log.Printf("applying snapshot at term %d and index %d to RocksDB storage", snapshot.Metadata.Term, snapshot.Metadata.Index)
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
	c := &raft.Config{
		ID:                        uint64(rc.id),
		ElectionTick:              10,
		HeartbeatTick:             1,
		Storage:                   rc.raftStorage,
		MaxSizePerMsg:             1024 * 1024,
		MaxInflightMsgs:           256,
		MaxUncommittedEntriesSize: 1 << 30,
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

	// Create an initial snapshot if none exists (for new clusters)
	// This prevents "need non-empty snapshot" panic when leader tries to sync followers
	if !oldNode && !rc.join {
		go func() {
			// Wait a bit for the node to be ready
			time.Sleep(100 * time.Millisecond)

			// Check if we already have a snapshot
			snap, err := rc.raftStorage.Snapshot()
			if err == nil && raft.IsEmptySnap(snap) {
				log.Printf("creating initial snapshot for new cluster")
				data, err := rc.getSnapshot()
				if err != nil {
					log.Printf("failed to get initial snapshot data: %v", err)
					return
				}

				// Create initial snapshot at index 0
				_, err = rc.raftStorage.CreateSnapshot(0, &rc.confState, data)
				if err != nil {
					log.Printf("failed to create initial snapshot: %v", err)
				}
			}
		}()
	}

	go rc.serveRaft()
	go rc.serveChannels()
}

// stop closes http, closes all channels, and stops raft
func (rc *raftNodeRocks) stop() {
	rc.stopHTTP()
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

	log.Printf("publishing snapshot at index %d", rc.snapshotIndex)
	defer log.Printf("finished publishing snapshot at index %d", rc.snapshotIndex)

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

	log.Printf("start snapshot [applied index: %d | last snapshot index: %d]", rc.appliedIndex, rc.snapshotIndex)
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
		log.Printf("compacted log at index %d", compactIndex)
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

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// send proposals over raft
	go func() {
		confChangeCount := uint64(0)

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
		// client closed channel; shutdown raft if not already
		close(rc.stopc)
	}()

	// event loop on raft state machine updates
	for {
		select {
		case <-ticker.C:
			rc.node.Tick()

		// store raft entries to RocksDB, then publish over commit channel
		case rd := <-rc.node.Ready():
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

	ln, err := newStoppableListener(url.Host, rc.httpstopc)
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
