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

import (
	"context"
	"testing"
)

func TestMemoryStorePutGet(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put a key
	rev, err := store.Put([]byte("foo"), []byte("bar"), 0)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if rev != 1 {
		t.Errorf("Put revision = %d, want 1", rev)
	}

	// Get the key
	kv, err := store.Get([]byte("foo"), 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(kv.Key) != "foo" {
		t.Errorf("Key = %q, want foo", kv.Key)
	}
	if string(kv.Value) != "bar" {
		t.Errorf("Value = %q, want bar", kv.Value)
	}
	if kv.CreateRevision != 1 {
		t.Errorf("CreateRevision = %d, want 1", kv.CreateRevision)
	}
	if kv.ModRevision != 1 {
		t.Errorf("ModRevision = %d, want 1", kv.ModRevision)
	}
	if kv.Version != 1 {
		t.Errorf("Version = %d, want 1", kv.Version)
	}
}

func TestMemoryStorePutUpdate(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put initial value
	store.Put([]byte("foo"), []byte("bar"), 0)

	// Update the value
	rev, err := store.Put([]byte("foo"), []byte("baz"), 0)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if rev != 2 {
		t.Errorf("Put revision = %d, want 2", rev)
	}

	// Get and verify
	kv, err := store.Get([]byte("foo"), 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(kv.Value) != "baz" {
		t.Errorf("Value = %q, want baz", kv.Value)
	}
	if kv.CreateRevision != 1 {
		t.Errorf("CreateRevision = %d, want 1 (original)", kv.CreateRevision)
	}
	if kv.ModRevision != 2 {
		t.Errorf("ModRevision = %d, want 2", kv.ModRevision)
	}
	if kv.Version != 2 {
		t.Errorf("Version = %d, want 2", kv.Version)
	}
}

func TestMemoryStoreGetHistorical(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put multiple versions
	store.Put([]byte("foo"), []byte("v1"), 0)
	store.Put([]byte("foo"), []byte("v2"), 0)
	store.Put([]byte("foo"), []byte("v3"), 0)

	// Get at revision 1
	kv, err := store.Get([]byte("foo"), 1)
	if err != nil {
		t.Fatalf("Get at rev 1 failed: %v", err)
	}
	if string(kv.Value) != "v1" {
		t.Errorf("Value at rev 1 = %q, want v1", kv.Value)
	}

	// Get at revision 2
	kv, err = store.Get([]byte("foo"), 2)
	if err != nil {
		t.Fatalf("Get at rev 2 failed: %v", err)
	}
	if string(kv.Value) != "v2" {
		t.Errorf("Value at rev 2 = %q, want v2", kv.Value)
	}

	// Get at revision 3
	kv, err = store.Get([]byte("foo"), 3)
	if err != nil {
		t.Fatalf("Get at rev 3 failed: %v", err)
	}
	if string(kv.Value) != "v3" {
		t.Errorf("Value at rev 3 = %q, want v3", kv.Value)
	}
}

func TestMemoryStoreGetNotFound(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	_, err := store.Get([]byte("nonexistent"), 0)
	if err != ErrKeyNotFound {
		t.Errorf("Get error = %v, want ErrKeyNotFound", err)
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put and delete
	store.Put([]byte("foo"), []byte("bar"), 0)
	rev, deleted, err := store.Delete([]byte("foo"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Deleted = %d, want 1", deleted)
	}
	if rev != 2 {
		t.Errorf("Revision = %d, want 2", rev)
	}

	// Get should return not found
	_, err = store.Get([]byte("foo"), 0)
	if err != ErrKeyNotFound {
		t.Errorf("Get after delete = %v, want ErrKeyNotFound", err)
	}

	// Get at old revision should still work
	kv, err := store.Get([]byte("foo"), 1)
	if err != nil {
		t.Fatalf("Get at old rev failed: %v", err)
	}
	if string(kv.Value) != "bar" {
		t.Errorf("Value at old rev = %q, want bar", kv.Value)
	}
}

func TestMemoryStoreDeleteNonexistent(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	rev, deleted, err := store.Delete([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Deleted = %d, want 0", deleted)
	}
	if rev != 0 {
		// Current revision is 0 for empty store
	}
}

func TestMemoryStoreRange(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put multiple keys
	store.Put([]byte("a"), []byte("1"), 0)
	store.Put([]byte("b"), []byte("2"), 0)
	store.Put([]byte("c"), []byte("3"), 0)
	store.Put([]byte("d"), []byte("4"), 0)

	// Range all
	kvs, count, err := store.Range([]byte("a"), nil, 0, 0)
	if err != nil {
		t.Fatalf("Range failed: %v", err)
	}
	if count != 4 {
		t.Errorf("Count = %d, want 4", count)
	}
	if len(kvs) != 4 {
		t.Errorf("Len = %d, want 4", len(kvs))
	}

	// Range with end
	kvs, count, err = store.Range([]byte("b"), []byte("d"), 0, 0)
	if err != nil {
		t.Fatalf("Range failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}

	// Range with limit
	kvs, count, err = store.Range([]byte("a"), nil, 0, 2)
	if err != nil {
		t.Fatalf("Range failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}
}

func TestMemoryStoreDeleteRange(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put multiple keys
	store.Put([]byte("a"), []byte("1"), 0)
	store.Put([]byte("b"), []byte("2"), 0)
	store.Put([]byte("c"), []byte("3"), 0)
	store.Put([]byte("d"), []byte("4"), 0)

	// Delete range [b, d)
	rev, deleted, err := store.DeleteRange([]byte("b"), []byte("d"))
	if err != nil {
		t.Fatalf("DeleteRange failed: %v", err)
	}
	if deleted != 2 {
		t.Errorf("Deleted = %d, want 2", deleted)
	}
	if rev != 5 {
		t.Errorf("Revision = %d, want 5", rev)
	}

	// Check remaining keys
	kvs, count, _ := store.Range([]byte("a"), nil, 0, 0)
	if count != 2 {
		t.Errorf("Remaining count = %d, want 2", count)
	}
	if string(kvs[0].Key) != "a" || string(kvs[1].Key) != "d" {
		t.Errorf("Wrong remaining keys")
	}
}

func TestMemoryStoreCompact(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put multiple versions
	store.Put([]byte("foo"), []byte("v1"), 0)
	store.Put([]byte("foo"), []byte("v2"), 0)
	store.Put([]byte("foo"), []byte("v3"), 0)

	// Compact at revision 2
	err := store.Compact(2)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// Get at compacted revision should fail
	_, err = store.Get([]byte("foo"), 1)
	if err != ErrCompacted {
		t.Errorf("Get at compacted rev = %v, want ErrCompacted", err)
	}

	// Get at revision 2 and 3 should still work
	kv, err := store.Get([]byte("foo"), 2)
	if err != nil {
		t.Fatalf("Get at rev 2 failed: %v", err)
	}
	if string(kv.Value) != "v2" {
		t.Errorf("Value at rev 2 = %q, want v2", kv.Value)
	}

	// Get latest should work
	kv, err = store.Get([]byte("foo"), 0)
	if err != nil {
		t.Fatalf("Get latest failed: %v", err)
	}
	if string(kv.Value) != "v3" {
		t.Errorf("Latest value = %q, want v3", kv.Value)
	}
}

func TestMemoryStoreCompactErrors(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	store.Put([]byte("foo"), []byte("bar"), 0)

	// Compact at future revision
	err := store.Compact(100)
	if err != ErrFutureRevision {
		t.Errorf("Compact at future rev = %v, want ErrFutureRevision", err)
	}

	// Compact at current revision
	err = store.Compact(1)
	if err != nil {
		t.Fatalf("Compact at current rev failed: %v", err)
	}

	// Compact at already compacted revision
	err = store.Compact(1)
	if err != ErrCompacted {
		t.Errorf("Compact at compacted rev = %v, want ErrCompacted", err)
	}
}

func TestMemoryStoreTxnSimple(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put a key
	store.Put([]byte("foo"), []byte("bar"), 0)

	// Transaction: if foo.version == 1, set foo = baz
	resp, err := store.Txn(context.Background()).
		If(Condition{
			Key:     []byte("foo"),
			Target:  ConditionTargetVersion,
			Compare: CompareEqual,
			Value:   int64(1),
		}).
		Then(Op{
			Type:  OpTypePut,
			Key:   []byte("foo"),
			Value: []byte("baz"),
		}).
		Commit()

	if err != nil {
		t.Fatalf("Txn failed: %v", err)
	}
	if !resp.Succeeded {
		t.Error("Txn should have succeeded")
	}

	// Verify
	kv, _ := store.Get([]byte("foo"), 0)
	if string(kv.Value) != "baz" {
		t.Errorf("Value = %q, want baz", kv.Value)
	}
}

func TestMemoryStoreTxnConditionFailed(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	store.Put([]byte("foo"), []byte("bar"), 0)

	// Transaction: if foo.version == 2 (false), set foo = baz, else set foo = qux
	resp, err := store.Txn(context.Background()).
		If(Condition{
			Key:     []byte("foo"),
			Target:  ConditionTargetVersion,
			Compare: CompareEqual,
			Value:   int64(2), // Wrong version
		}).
		Then(Op{
			Type:  OpTypePut,
			Key:   []byte("foo"),
			Value: []byte("baz"),
		}).
		Else(Op{
			Type:  OpTypePut,
			Key:   []byte("foo"),
			Value: []byte("qux"),
		}).
		Commit()

	if err != nil {
		t.Fatalf("Txn failed: %v", err)
	}
	if resp.Succeeded {
		t.Error("Txn should not have succeeded")
	}

	// Verify else branch was executed
	kv, _ := store.Get([]byte("foo"), 0)
	if string(kv.Value) != "qux" {
		t.Errorf("Value = %q, want qux", kv.Value)
	}
}

func TestMemoryStoreTxnGet(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	store.Put([]byte("foo"), []byte("bar"), 0)

	resp, err := store.Txn(context.Background()).
		Then(Op{
			Type: OpTypeGet,
			Key:  []byte("foo"),
		}).
		Commit()

	if err != nil {
		t.Fatalf("Txn failed: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("Responses = %d, want 1", len(resp.Responses))
	}
	if len(resp.Responses[0].Kvs) != 1 {
		t.Fatalf("Kvs = %d, want 1", len(resp.Responses[0].Kvs))
	}
	if string(resp.Responses[0].Kvs[0].Value) != "bar" {
		t.Errorf("Value = %q, want bar", resp.Responses[0].Kvs[0].Value)
	}
}

func TestMemoryStoreTxnDelete(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	store.Put([]byte("foo"), []byte("bar"), 0)

	resp, err := store.Txn(context.Background()).
		Then(Op{
			Type: OpTypeDelete,
			Key:  []byte("foo"),
		}).
		Commit()

	if err != nil {
		t.Fatalf("Txn failed: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("Responses = %d, want 1", len(resp.Responses))
	}
	if resp.Responses[0].Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", resp.Responses[0].Deleted)
	}

	// Verify deleted
	_, err = store.Get([]byte("foo"), 0)
	if err != ErrKeyNotFound {
		t.Errorf("Get after delete = %v, want ErrKeyNotFound", err)
	}
}

func TestMemoryStoreEmptyKey(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	_, err := store.Put(nil, []byte("bar"), 0)
	if err != ErrEmptyKey {
		t.Errorf("Put with nil key = %v, want ErrEmptyKey", err)
	}

	_, err = store.Put([]byte{}, []byte("bar"), 0)
	if err != ErrEmptyKey {
		t.Errorf("Put with empty key = %v, want ErrEmptyKey", err)
	}

	_, err = store.Get(nil, 0)
	if err != ErrEmptyKey {
		t.Errorf("Get with nil key = %v, want ErrEmptyKey", err)
	}
}

func TestMemoryStoreClose(t *testing.T) {
	store := NewMemoryStore()

	err := store.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations on closed store should fail
	_, err = store.Put([]byte("foo"), []byte("bar"), 0)
	if err != ErrClosed {
		t.Errorf("Put on closed store = %v, want ErrClosed", err)
	}

	_, err = store.Get([]byte("foo"), 0)
	if err != ErrClosed {
		t.Errorf("Get on closed store = %v, want ErrClosed", err)
	}

	// Double close should fail
	err = store.Close()
	if err != ErrClosed {
		t.Errorf("Double close = %v, want ErrClosed", err)
	}
}

func TestMemoryStoreLease(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Put with lease
	rev, err := store.Put([]byte("foo"), []byte("bar"), 12345)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if rev != 1 {
		t.Errorf("Revision = %d, want 1", rev)
	}

	// Get and verify lease
	kv, err := store.Get([]byte("foo"), 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if kv.Lease != 12345 {
		t.Errorf("Lease = %d, want 12345", kv.Lease)
	}
}

func TestMemoryStoreCurrentRevision(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	if store.CurrentRevision() != 0 {
		t.Errorf("Initial revision = %d, want 0", store.CurrentRevision())
	}

	store.Put([]byte("foo"), []byte("bar"), 0)
	if store.CurrentRevision() != 1 {
		t.Errorf("Revision after put = %d, want 1", store.CurrentRevision())
	}

	store.Put([]byte("foo"), []byte("baz"), 0)
	if store.CurrentRevision() != 2 {
		t.Errorf("Revision after second put = %d, want 2", store.CurrentRevision())
	}
}

func TestMemoryStoreCompactedRevision(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	if store.CompactedRevision() != 0 {
		t.Errorf("Initial compacted rev = %d, want 0", store.CompactedRevision())
	}

	store.Put([]byte("foo"), []byte("bar"), 0)
	store.Put([]byte("foo"), []byte("baz"), 0)
	store.Compact(1)

	if store.CompactedRevision() != 1 {
		t.Errorf("Compacted rev = %d, want 1", store.CompactedRevision())
	}
}
