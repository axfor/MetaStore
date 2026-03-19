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

package etcd

import (
	"testing"

	"go.etcd.io/raft/v3/raftpb"
)

func TestAddMember_NotVisibleBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	member, err := cm.AddMember([]string{"http://127.0.0.1:9021"}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Member should NOT be in the map yet (Raft hasn't committed)
	members := cm.ListMembers()
	for _, m := range members {
		if m.ID == member.ID {
			t.Errorf("member %d should not be visible before ApplyConfChange", member.ID)
		}
	}

	// Drain the confChange and simulate Raft commit via ApplyConfChange
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now it should be visible
	members = cm.ListMembers()
	found := false
	for _, m := range members {
		if m.ID == member.ID {
			found = true
		}
	}
	if !found {
		t.Error("member should be visible after ApplyConfChange")
	}
}

func TestRemoveMember_NotRemovedBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	// Add a member via ApplyConfChange (committed state)
	cm.ApplyConfChange(raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddNode,
		NodeID: 42,
	}, raftpb.ConfState{})

	// Verify member exists
	_, err := cm.GetMember(42)
	if err != nil {
		t.Fatal("member should exist before removal")
	}

	// Call RemoveMember
	if err := cm.RemoveMember(42); err != nil {
		t.Fatal(err)
	}

	// Member should still be in the map (Raft hasn't committed removal)
	_, err = cm.GetMember(42)
	if err != nil {
		t.Error("member should still be visible before ApplyConfChange removes it")
	}

	// Drain confChange and apply
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now it should be gone
	_, err = cm.GetMember(42)
	if err == nil {
		t.Error("member should be gone after ApplyConfChange")
	}
}

func TestPromoteMember_NotPromotedBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	// Add learner via ApplyConfChange
	cm.ApplyConfChange(raftpb.ConfChange{
		Type:   raftpb.ConfChangeAddLearnerNode,
		NodeID: 77,
	}, raftpb.ConfState{})

	// Verify it's a learner
	member, _ := cm.GetMember(77)
	if !member.IsLearner {
		t.Fatal("should be learner")
	}

	// Promote
	if err := cm.PromoteMember(77); err != nil {
		t.Fatal(err)
	}

	// Should still be learner (Raft hasn't committed)
	member, _ = cm.GetMember(77)
	if !member.IsLearner {
		t.Error("member should still be learner before ApplyConfChange")
	}

	// Apply
	cc := <-confChangeC
	cm.ApplyConfChange(cc, raftpb.ConfState{})

	// Now should be voter
	member, _ = cm.GetMember(77)
	if member.IsLearner {
		t.Error("member should be voter after ApplyConfChange")
	}
}

func TestAddWitnessMember_NotVisibleBeforeApply(t *testing.T) {
	confChangeC := make(chan raftpb.ConfChange, 10)
	cm := NewClusterManager(confChangeC)

	member, err := cm.AddWitnessMember([]string{"http://127.0.0.1:9031"})
	if err != nil {
		t.Fatal(err)
	}

	// Should NOT be visible yet
	members := cm.ListMembers()
	for _, m := range members {
		if m.ID == member.ID {
			t.Errorf("witness member %d should not be visible before ApplyConfChange", member.ID)
		}
	}
}
