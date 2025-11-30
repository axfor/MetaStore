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

package mvcc

import "context"

// Store is the interface for MVCC storage.
// It provides versioned key-value operations compatible with etcd.
type Store interface {
	// Put stores a key-value pair and returns the new revision.
	// If lease > 0, the key is attached to the lease.
	Put(key, value []byte, lease int64) (rev int64, err error)

	// Get retrieves the value for a key at a specific revision.
	// If rev is 0, returns the latest version.
	// Returns ErrKeyNotFound if the key doesn't exist.
	// Returns ErrCompacted if the revision has been compacted.
	Get(key []byte, rev int64) (*KeyValue, error)

	// Range retrieves key-value pairs in the range [start, end).
	// If end is nil, it returns all keys >= start.
	// If rev is 0, returns the latest versions.
	// limit specifies the maximum number of keys to return (0 = no limit).
	Range(start, end []byte, rev int64, limit int64) ([]*KeyValue, int64, error)

	// Delete deletes a key and returns the revision and number of deleted keys.
	Delete(key []byte) (rev int64, deleted int64, err error)

	// DeleteRange deletes all keys in the range [start, end).
	// Returns the revision and number of deleted keys.
	DeleteRange(start, end []byte) (rev int64, deleted int64, err error)

	// Txn executes a transaction.
	Txn(ctx context.Context) Txn

	// CurrentRevision returns the current revision.
	CurrentRevision() int64

	// CompactedRevision returns the revision that has been compacted.
	CompactedRevision() int64

	// Compact compacts all revisions before the given revision.
	// Returns ErrCompacted if rev <= CompactedRevision.
	// Returns ErrFutureRevision if rev > CurrentRevision.
	Compact(rev int64) error

	// Close closes the store.
	Close() error
}

// Txn represents a transaction.
type Txn interface {
	// If sets the conditions for the transaction.
	If(conds ...Condition) Txn

	// Then sets the operations to execute if all conditions are true.
	Then(ops ...Op) Txn

	// Else sets the operations to execute if any condition is false.
	Else(ops ...Op) Txn

	// Commit executes the transaction.
	Commit() (*TxnResponse, error)
}

// Condition represents a condition in a transaction.
type Condition struct {
	Key     []byte
	Target  ConditionTarget
	Compare CompareType
	Value   interface{} // int64 for Version/CreateRevision/ModRevision, []byte for Value
}

// ConditionTarget specifies what to compare.
type ConditionTarget int

const (
	ConditionTargetVersion ConditionTarget = iota
	ConditionTargetCreateRevision
	ConditionTargetModRevision
	ConditionTargetValue
)

// CompareType specifies the comparison type.
type CompareType int

const (
	CompareEqual CompareType = iota
	CompareNotEqual
	CompareLess
	CompareGreater
)

// Op represents an operation in a transaction.
type Op struct {
	Type  OpType
	Key   []byte
	End   []byte // For range operations
	Value []byte // For Put
	Lease int64  // For Put
}

// OpType specifies the operation type.
type OpType int

const (
	OpTypePut OpType = iota
	OpTypeGet
	OpTypeDelete
	OpTypeDeleteRange
)

// TxnResponse is the response from a transaction.
type TxnResponse struct {
	// Succeeded is true if all conditions were true.
	Succeeded bool

	// Revision is the revision after the transaction.
	Revision int64

	// Responses contains the responses from each operation.
	Responses []OpResponse
}

// OpResponse is the response from a single operation.
type OpResponse struct {
	// Type is the type of operation.
	Type OpType

	// Kvs contains the key-value pairs for Get operations.
	Kvs []*KeyValue

	// Deleted is the count of deleted keys for Delete operations.
	Deleted int64

	// PrevKv is the previous key-value for Put/Delete operations (if requested).
	PrevKv *KeyValue
}

// WatchEvent represents a change event for Watch.
type WatchEvent struct {
	// Type is the type of event.
	Type EventType

	// Kv is the key-value after the event.
	Kv *KeyValue

	// PrevKv is the key-value before the event.
	PrevKv *KeyValue
}
