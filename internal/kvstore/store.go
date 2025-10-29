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

package kvstore

import "context"

// Store is the interface that all KV stores must implement
// All methods support context for timeout control and cancellation
type Store interface {
	// Legacy methods (kept for backward compatibility)
	Lookup(key string) (string, bool)
	Propose(k string, v string)
	GetSnapshot() ([]byte, error)

	// etcd-compatible methods with Context support

	// Range executes a range query
	// ctx: context for timeout and cancellation
	// key: start key
	// rangeEnd: end key (empty for single key, "\x00" for all keys)
	// limit: max keys to return (0 for unlimited)
	// revision: query data at specific revision (0 for latest)
	Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*RangeResponse, error)

	// PutWithLease stores a key-value pair with optional lease
	// Returns new revision and previous value (if any)
	PutWithLease(ctx context.Context, key, value string, leaseID int64) (revision int64, prevKv *KeyValue, err error)

	// DeleteRange deletes keys in the specified range
	// Returns number of deleted keys, deleted key-value pairs, and new revision
	DeleteRange(ctx context.Context, key, rangeEnd string) (deleted int64, prevKvs []*KeyValue, revision int64, err error)

	// Txn executes a transaction
	// cmps: comparison conditions
	// thenOps: operations to execute if comparisons succeed
	// elseOps: operations to execute if comparisons fail
	Txn(ctx context.Context, cmps []Compare, thenOps []Op, elseOps []Op) (*TxnResponse, error)

	// Watch creates a watch and returns event channel
	// key: key to watch
	// rangeEnd: range end key (empty for single key)
	// startRevision: revision to start watching from (0 for current)
	// watchID: unique watch identifier
	Watch(ctx context.Context, key, rangeEnd string, startRevision int64, watchID int64) (<-chan WatchEvent, error)

	// CancelWatch cancels a watch
	CancelWatch(watchID int64) error

	// Compact compacts historical data before specified revision
	Compact(ctx context.Context, revision int64) error

	// CurrentRevision returns current revision
	CurrentRevision() int64

	// Lease-related methods

	// LeaseGrant creates a new lease
	LeaseGrant(ctx context.Context, id int64, ttl int64) (*Lease, error)

	// LeaseRevoke revokes a lease (deletes all associated keys)
	LeaseRevoke(ctx context.Context, id int64) error

	// LeaseRenew renews a lease
	LeaseRenew(ctx context.Context, id int64) (*Lease, error)

	// LeaseTimeToLive gets remaining time for a lease
	LeaseTimeToLive(ctx context.Context, id int64) (*Lease, error)

	// Leases returns all leases
	Leases(ctx context.Context) ([]*Lease, error)

	// GetRaftStatus returns Raft status information
	GetRaftStatus() RaftStatus

	// TransferLeadership transfers leadership to another node
	TransferLeadership(targetID uint64) error
}

// Commit represents a commit event from raft
type Commit struct {
	Data       []string
	ApplyDoneC chan<- struct{}
}

// KV represents a key-value pair
type KV struct {
	Key string
	Val string
}
