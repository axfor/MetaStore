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

package concurrency

import (
	"context"
	"errors"
	"fmt"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// Mutex implementdistribution式mutex
type Mutex struct {
	s   *Session
	pfx string // key prefix

	myKey string
	myRev int64
	hdr   *pb.ResponseHeader

	mu sync.Mutex
}

// NewMutex createnewmutex
func NewMutex(s *Session, pfx string) *Mutex {
	return &Mutex{
		s:   s,
		pfx: pfx + "/",
	}
}

// Lock acquires the lock, blocking until successful
func (m *Mutex) Lock(ctx context.Context) error {
	s := m.s
	client := m.s.client

	m.mu.Lock()
	// Already holding the lock
	if m.myKey != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// Create a unique key using lease ID
	// Key format: prefix/lease_id
	myKey := fmt.Sprintf("%s%x", m.pfx, s.Lease())

	// Step 1: Use transaction to create key (only if not exists)
	// Note: We cannot query owner in the same transaction because the query
	// would see the state before our Put operation completes
	cmp := clientv3.Compare(clientv3.CreateRevision(myKey), "=", 0)
	put := clientv3.OpPut(myKey, "", clientv3.WithLease(s.Lease()))
	get := clientv3.OpGet(myKey)

	resp, err := client.Txn(ctx).If(cmp).Then(put).Else(get).Commit()
	if err != nil {
		return err
	}

	var myRev int64
	if resp.Succeeded {
		myRev = resp.Header.Revision
	} else {
		// Key already exists, get its revision
		myRev = resp.Responses[0].GetResponseRange().Kvs[0].CreateRevision
	}

	// Save lock info
	m.mu.Lock()
	m.myKey = myKey
	m.myRev = myRev
	m.hdr = resp.Header
	m.mu.Unlock()

	// Step 2: Query for the current lock owner (after key creation)
	// This must be done separately to see the key we just created
	ownerResp, err := client.Get(ctx, m.pfx, clientv3.WithFirstCreate()...)
	if err != nil {
		m.Unlock(ctx)
		return err
	}

	// Check if we already hold the lock (we are the first key)
	if len(ownerResp.Kvs) == 0 || ownerResp.Kvs[0].CreateRevision == myRev {
		return nil
	}

	// Wait for all earlier keys to be deleted
	err = m.waitDeletes(ctx, myKey, myRev)
	if err != nil {
		// Release the key on error
		m.Unlock(ctx)
		return err
	}

	// Verify we still own the key after waiting
	gresp, err := client.Get(ctx, myKey)
	if err != nil {
		m.Unlock(ctx)
		return err
	}
	if len(gresp.Kvs) == 0 {
		return errors.New("session expired")
	}

	m.mu.Lock()
	m.hdr = gresp.Header
	m.mu.Unlock()

	return nil
}

// waitDeletes waits for all keys with CreateRevision <= maxCreateRev to be deleted
// Automatically retries if Watch is canceled or network errors occur
func (m *Mutex) waitDeletes(ctx context.Context, myKey string, myRev int64) error {
	client := m.s.client

	// Use WithLastCreate to get the key with the largest CreateRevision <= myRev-1
	// This is the key we need to wait for
	getOpts := append(clientv3.WithLastCreate(), clientv3.WithMaxCreateRev(myRev-1))
	for {
		// Check if session is still valid
		select {
		case <-m.s.Done():
			return errors.New("session expired")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(ctx, m.pfx, getOpts...)
		if err != nil {
			return err
		}

		// No earlier keys exist, we have the lock
		if len(resp.Kvs) == 0 {
			return nil
		}

		// Wait for this key to be deleted
		lastKey := string(resp.Kvs[0].Key)

		// Watch for deletion with automatic retry on cancellation
		err = m.watchKeyDeletion(ctx, lastKey, resp.Header.Revision)
		if err != nil {
			// If watch was canceled or had network error, retry the loop
			// The loop will recheck if the key still exists
			if isWatchCanceledOrNetworkError(err) {
				continue
			}
			return err
		}

		// Key was deleted, loop will recheck for more earlier keys
	}
}

// watchKeyDeletion watches a specific key for deletion
// Returns nil when key is deleted, error otherwise
func (m *Mutex) watchKeyDeletion(ctx context.Context, key string, revision int64) error {
	client := m.s.client

	// Create a cancellable context for watch
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	// Watch for deletion starting from the current revision
	wch := client.Watch(watchCtx, key, clientv3.WithRev(revision))

	for wresp := range wch {
		if wresp.Canceled {
			// Watch was canceled - could be network error or context cancellation
			if wresp.Err() != nil {
				return wresp.Err()
			}
			return errors.New("watch canceled")
		}
		for _, ev := range wresp.Events {
			if ev.Type == clientv3.EventTypeDelete {
				// Key deleted successfully
				return nil
			}
		}
	}

	// Watch channel closed without delete event, check context
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.s.Done():
		return errors.New("session expired")
	default:
		// Watch channel closed unexpectedly, return error to trigger retry
		return errors.New("watch channel closed")
	}
}

// isWatchCanceledOrNetworkError checks if error is due to watch cancellation or network issue
func isWatchCanceledOrNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for common watch cancellation and network error patterns
	return errStr == "watch canceled" ||
		errStr == "watch channel closed" ||
		errStr == "context canceled" ||
		errStr == "rpc error" ||
		errStr == "connection" ||
		errStr == "EOF"
}

// TryLock attempts to acquire the lock without blocking
func (m *Mutex) TryLock(ctx context.Context) error {
	s := m.s
	client := m.s.client

	m.mu.Lock()
	if m.myKey != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	myKey := fmt.Sprintf("%s%x", m.pfx, s.Lease())

	// Step 1: Create key using transaction
	cmp := clientv3.Compare(clientv3.CreateRevision(myKey), "=", 0)
	put := clientv3.OpPut(myKey, "", clientv3.WithLease(s.Lease()))
	get := clientv3.OpGet(myKey)

	resp, err := client.Txn(ctx).If(cmp).Then(put).Else(get).Commit()
	if err != nil {
		return err
	}

	var myRev int64
	if resp.Succeeded {
		myRev = resp.Header.Revision
	} else {
		myRev = resp.Responses[0].GetResponseRange().Kvs[0].CreateRevision
	}

	// Step 2: Query for the current lock owner
	ownerResp, err := client.Get(ctx, m.pfx, clientv3.WithFirstCreate()...)
	if err != nil {
		_, _ = client.Delete(ctx, myKey)
		return err
	}

	// Check if we are the owner (first key by creation revision)
	if len(ownerResp.Kvs) == 0 || ownerResp.Kvs[0].CreateRevision == myRev {
		// We are the owner
		m.mu.Lock()
		m.myKey = myKey
		m.myRev = myRev
		m.hdr = resp.Header
		m.mu.Unlock()
		return nil
	}

	// Not the owner, delete our key
	_, _ = client.Delete(ctx, myKey)
	return concurrency.ErrLocked
}

// Unlock releaselock
func (m *Mutex) Unlock(ctx context.Context) error {
	m.mu.Lock()
	if m.myKey == "" {
		m.mu.Unlock()
		return nil
	}
	myKey := m.myKey
	m.myKey = ""
	m.mu.Unlock()

	_, err := m.s.client.Delete(ctx, myKey)
	return err
}

// IsOwner checkcurrentisno持有lock
func (m *Mutex) IsOwner() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.myKey != ""
}

// Key returnlock key
func (m *Mutex) Key() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.myKey
}

// Header returnlockcreate时response头
func (m *Mutex) Header() *pb.ResponseHeader {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hdr
}
