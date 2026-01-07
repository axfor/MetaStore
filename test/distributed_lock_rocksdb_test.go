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

//go:build cgo
// +build cgo

package test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"metaStore/pkg/concurrency"

	clientv3 "go.etcd.io/etcd/client/v3"
	etcdconcurrency "go.etcd.io/etcd/client/v3/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// RocksDB Distributed Lock Test Helper
// ============================================================================

// startRocksDBLockTestServer starts a RocksDB-backed server for lock testing
func startRocksDBLockTestServer(t *testing.T) (*clientv3.Client, func()) {
	node, cleanup := startRocksDBNode(t, 1)

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	return cli, func() {
		// closekey：to Session clean uptime， "connection is closing" incorrect
		time.Sleep(500 * time.Millisecond)
		cleanup() // closeserver
		cli.Close() // closeclient
	}
}

// ============================================================================
// RocksDB Session Tests
// ============================================================================

func TestRocksDB_SessionCreate(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(10))
	require.NoError(t, err)
	require.NotNil(t, session)

	leaseID := session.Lease()
	assert.NotEqual(t, clientv3.NoLease, leaseID)

	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))
	assert.LessOrEqual(t, ttlResp.TTL, int64(10))

	err = session.Close()
	require.NoError(t, err)

	ttlResp, err = cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

func TestRocksDB_SessionWithExistingLease(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	leaseResp, err := cli.Grant(ctx, 30)
	require.NoError(t, err)

	session, err := concurrency.NewSession(cli, concurrency.WithLease(leaseResp.ID))
	require.NoError(t, err)
	require.NotNil(t, session)

	assert.Equal(t, leaseResp.ID, session.Lease())
	session.Close()
}

// ============================================================================
// RocksDB Basic Mutex Tests
// ============================================================================

func TestRocksDB_MutexLockUnlock(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/rocksdb/test/lock")

	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())

	err = mutex.Lock(ctx)
	require.NoError(t, err)

	assert.True(t, mutex.IsOwner())
	assert.NotEmpty(t, mutex.Key())
	assert.NotNil(t, mutex.Header())

	err = mutex.Unlock(ctx)
	require.NoError(t, err)

	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())
}

func TestRocksDB_MutexReentrantLock(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/rocksdb/test/reentrant")

	err = mutex.Lock(ctx)
	require.NoError(t, err)
	firstKey := mutex.Key()

	err = mutex.Lock(ctx)
	require.NoError(t, err)

	assert.Equal(t, firstKey, mutex.Key())

	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// ============================================================================
// RocksDB TryLock Tests
// ============================================================================

func TestRocksDB_TryLockSuccess(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/rocksdb/test/trylock")

	err = mutex.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

func TestRocksDB_TryLockFail(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/rocksdb/test/trylock-fail")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/rocksdb/test/trylock-fail")
	err = mutex2.TryLock(ctx)

	assert.Error(t, err)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)
	assert.False(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
}

func TestRocksDB_TryLockAfterUnlock(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex1 := concurrency.NewMutex(session1, "/rocksdb/test/trylock-after-unlock")
	mutex2 := concurrency.NewMutex(session2, "/rocksdb/test/trylock-after-unlock")

	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	err = mutex2.TryLock(ctx)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)

	err = mutex1.Unlock(ctx)
	require.NoError(t, err)

	err = mutex2.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex2.IsOwner())

	mutex2.Unlock(ctx)
}

// ============================================================================
// RocksDB Concurrent Lock Tests
// ============================================================================

func TestRocksDB_MutexContention(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numClients = 5
	var wg sync.WaitGroup
	acquired := make(chan int, numClients)
	released := make(chan int, numClients)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/rocksdb/test/contention")

			err = mutex.Lock(ctx)
			require.NoError(t, err)

			acquired <- id
			t.Logf("RocksDB Client %d acquired lock", id)

			time.Sleep(50 * time.Millisecond)

			err = mutex.Unlock(ctx)
			require.NoError(t, err)

			released <- id
			t.Logf("RocksDB Client %d released lock", id)
		}(i)
	}

	wg.Wait()
	close(acquired)
	close(released)

	acquiredClients := make(map[int]bool)
	for id := range acquired {
		acquiredClients[id] = true
	}
	assert.Len(t, acquiredClients, numClients)

	releasedClients := make(map[int]bool)
	for id := range released {
		releasedClients[id] = true
	}
	assert.Len(t, releasedClients, numClients)
}

func TestRocksDB_MutexFIFOOrder(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numClients = 5
	var orderMu sync.Mutex
	acquireOrder := make([]int, 0, numClients)

	// closekey：usesegment
	// 1. startSignals: notification goroutine startcreate Session
	// 2. sessionReady: Session createdone，canstartnext
	startSignals := make([]chan struct{}, numClients)
	sessionReady := make([]chan struct{}, numClients)
	for i := range startSignals {
		startSignals[i] = make(chan struct{})
		sessionReady[i] = make(chan struct{})
	}

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startSignals[id]

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			// Session createdone，notificationthread
			close(sessionReady[id])

			mutex := concurrency.NewMutex(session, "/rocksdb/test/fifo")

			err = mutex.Lock(ctx)
			require.NoError(t, err)

			orderMu.Lock()
			acquireOrder = append(acquireOrder, id)
			orderMu.Unlock()

			t.Logf("RocksDB Client %d acquired lock at position %d", id, len(acquireOrder))

			time.Sleep(20 * time.Millisecond)

			mutex.Unlock(ctx)
		}(i)
	}

	// orderstartclient，andwaiteach Session createdonestartnext
	for i := 0; i < numClients; i++ {
		close(startSignals[i])
		<-sessionReady[i] // wait Session createdone(when Lease alreadyvia Raft )
		time.Sleep(10 * time.Millisecond) // smalllatencyorder
	}

	wg.Wait()

	t.Logf("RocksDB Acquire order: %v", acquireOrder)
	assert.Len(t, acquireOrder, numClients)

	expectedOrder := make([]int, numClients)
	for i := range expectedOrder {
		expectedOrder[i] = i
	}
	assert.Equal(t, expectedOrder, acquireOrder, "lock acquisition should follow FIFO order")
}

func TestRocksDB_MutexCriticalSection(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numClients = 10
	const iterations = 5
	var counter int64
	var violations int64

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/rocksdb/test/critical-section")

			for j := 0; j < iterations; j++ {
				err = mutex.Lock(ctx)
				require.NoError(t, err)

				oldVal := atomic.LoadInt64(&counter)
				time.Sleep(time.Millisecond)
				newVal := atomic.AddInt64(&counter, 1)

				if newVal != oldVal+1 {
					atomic.AddInt64(&violations, 1)
				}

				mutex.Unlock(ctx)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(numClients*iterations), atomic.LoadInt64(&counter))
	assert.Equal(t, int64(0), atomic.LoadInt64(&violations), "no race conditions should occur")
}

// ============================================================================
// RocksDB Lock Timeout and Cancellation Tests
// ============================================================================

func TestRocksDB_MutexLockWithTimeout(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/rocksdb/test/timeout")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/rocksdb/test/timeout")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = mutex2.Lock(ctx)

	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.True(t, elapsed >= 400*time.Millisecond && elapsed < 1*time.Second,
		"Lock should timeout around 500ms, got %v", elapsed)

	mutex1.Unlock(context.Background())
}

func TestRocksDB_MutexLockCancellation(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/rocksdb/test/cancel")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/rocksdb/test/cancel")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- mutex2.Lock(ctx)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "context canceled")
	case <-time.After(2 * time.Second):
		t.Fatal("Lock should be canceled")
	}

	mutex1.Unlock(context.Background())
}

// ============================================================================
// RocksDB Session Failure Tests
// ============================================================================

func TestRocksDB_MutexReleaseOnSessionClose(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(3)) // short TTL to 3 seconds
	require.NoError(t, err)
	lease1ID := session1.Lease()
	t.Logf("Session1 created with lease ID: %x", lease1ID)

	mutex1 := concurrency.NewMutex(session1, "/rocksdb/test/session-close")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)
	lockKey1 := mutex1.Key()
	t.Logf("Session1 acquired lock with key: %s", lockKey1)

	// Verify lease is alive
	ttl1, err := cli.TimeToLive(ctx, lease1ID)
	require.NoError(t, err)
	t.Logf("Session1 lease TTL before close: %d seconds", ttl1.TTL)

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()
	t.Logf("Session2 created with lease ID: %x", session2.Lease())

	mutex2 := concurrency.NewMutex(session2, "/rocksdb/test/session-close")

	acquired := make(chan struct{})
	go func() {
		t.Log("Session2 trying to acquire lock...")
		err := mutex2.Lock(ctx)
		if err == nil {
			t.Log("Session2 acquired lock!")
			close(acquired)
		} else {
			t.Logf("Session2 failed to acquire lock: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	t.Log("Closing Session1...")
	err = session1.Close()
	t.Logf("Session1 closed, error: %v", err)

	// Check if lease was revokedd
	ttl2, err := cli.TimeToLive(ctx, lease1ID)
	require.NoError(t, err)
	t.Logf("Session1 lease TTL after close: %d seconds (should be -1)", ttl2.TTL)

	// Check if lock key still exists
	getResp, err := cli.Get(ctx, lockKey1)
	require.NoError(t, err)
	t.Logf("Session1 lock key after close: exists=%v, count=%d", len(getResp.Kvs) > 0, len(getResp.Kvs))

	select {
	case <-acquired:
		t.Log("RocksDB Second session acquired lock after first session closed")
		assert.True(t, mutex2.IsOwner())
	case <-time.After(10 * time.Second): // increasetimeoutto 10 seconds，to Raft and Lease revokedtime
		// Check state before failing
		ttl3, _ := cli.TimeToLive(ctx, lease1ID)
		t.Logf("Final Session1 lease TTL: %d", ttl3.TTL)
		getResp2, _ := cli.Get(ctx, lockKey1)
		t.Logf("Final Session1 lock key: exists=%v", len(getResp2.Kvs) > 0)

		t.Fatal("second session should acquire lock after first session closes")
	}

	mutex2.Unlock(ctx)
}

// ============================================================================
// RocksDB Election Tests
// ============================================================================

func TestRocksDB_ElectionCampaign(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/rocksdb/test/election")

	assert.False(t, election.IsLeader())

	err = election.Campaign(ctx, "leader-value")
	require.NoError(t, err)

	assert.True(t, election.IsLeader())
	assert.NotEmpty(t, election.Key())
	assert.Greater(t, election.Rev(), int64(0))

	_, val, err := election.Leader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "leader-value", val)

	err = election.Resign(ctx)
	require.NoError(t, err)

	assert.False(t, election.IsLeader())
}

func TestRocksDB_ElectionMultipleCandidates(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numCandidates = 3
	var wg sync.WaitGroup
	leaderChan := make(chan int, numCandidates)

	for i := 0; i < numCandidates; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			election := concurrency.NewElection(session, "/rocksdb/test/multi-election")

			value := fmt.Sprintf("candidate-%d", id)
			err = election.Campaign(ctx, value)
			require.NoError(t, err)

			leaderChan <- id
			t.Logf("RocksDB Candidate %d became leader", id)

			time.Sleep(100 * time.Millisecond)

			election.Resign(ctx)
			t.Logf("RocksDB Candidate %d resigned", id)
		}(i)
	}

	wg.Wait()
	close(leaderChan)

	leaders := make(map[int]bool)
	for id := range leaderChan {
		leaders[id] = true
	}
	assert.Len(t, leaders, numCandidates)
}

// ============================================================================
// RocksDB Stress Tests
// ============================================================================

func TestRocksDB_MutexHighConcurrency(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numGoroutines = 20
	const iterations = 10
	var wg sync.WaitGroup
	var successCount int64
	var failCount int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			if err != nil {
				atomic.AddInt64(&failCount, int64(iterations))
				return
			}
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/rocksdb/test/high-concurrency")

			for j := 0; j < iterations; j++ {
				lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := mutex.Lock(lockCtx)
				cancel()

				if err != nil {
					atomic.AddInt64(&failCount, 1)
					continue
				}

				atomic.AddInt64(&successCount, 1)

				time.Sleep(5 * time.Millisecond)

				mutex.Unlock(ctx)
			}
		}(i)
	}

	wg.Wait()

	t.Logf("RocksDB Success: %d, Fail: %d", successCount, failCount)
	assert.Equal(t, int64(numGoroutines*iterations), successCount)
	assert.Equal(t, int64(0), failCount)
}

func TestRocksDB_MutexRapidLockUnlock(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/rocksdb/test/rapid")

	const iterations = 100
	for i := 0; i < iterations; i++ {
		err := mutex.Lock(ctx)
		require.NoError(t, err, "Lock failed at iteration %d", i)

		assert.True(t, mutex.IsOwner())

		err = mutex.Unlock(ctx)
		require.NoError(t, err, "Unlock failed at iteration %d", i)

		assert.False(t, mutex.IsOwner())
	}
}

// ============================================================================
// RocksDB Compatibility Tests
// ============================================================================

func TestRocksDB_CompatibilityWithEtcdConcurrency(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/rocksdb/test/etcd-compat")

	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	assert.NotEmpty(t, etcdMutex.Key())

	err = etcdMutex.Unlock(ctx)
	require.NoError(t, err)
}

func TestRocksDB_MixedLockUsage(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	customSession, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer customSession.Close()

	customMutex := concurrency.NewMutex(customSession, "/rocksdb/test/mixed")

	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/rocksdb/test/mixed")

	err = customMutex.Lock(ctx)
	require.NoError(t, err)

	tryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = etcdMutex.Lock(tryCtx)
	cancel()
	assert.Error(t, err, "etcd mutex should not be able to acquire lock")

	customMutex.Unlock(ctx)

	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	etcdMutex.Unlock(ctx)
}

// ============================================================================
// RocksDB Key Format Verification
// ============================================================================

func TestRocksDB_MutexKeyFormat(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	prefix := "/rocksdb/test/key-format"
	mutex := concurrency.NewMutex(session, prefix)

	err = mutex.Lock(ctx)
	require.NoError(t, err)

	key := mutex.Key()
	t.Logf("RocksDB Lock key: %s", key)

	assert.Contains(t, key, prefix+"/")
	assert.Contains(t, key, fmt.Sprintf("%x", session.Lease()))

	mutex.Unlock(ctx)
}

// ============================================================================
// RocksDB Recovery Tests
// ============================================================================

func TestRocksDB_MutexRecoveryAfterSessionClose(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	prefix := "/rocksdb/test/recovery"

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(5))
	require.NoError(t, err)

	mutex1 := concurrency.NewMutex(session1, prefix)
	err = mutex1.Lock(ctx)
	require.NoError(t, err)
	t.Log("RocksDB Session 1 acquired lock")

	session1.Close()
	t.Log("RocksDB Session 1 closed")

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, prefix)

	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = mutex2.Lock(lockCtx)
	cancel()

	require.NoError(t, err, "Session 2 should acquire lock after session 1 closes")
	assert.True(t, mutex2.IsOwner())
	t.Log("RocksDB Session 2 acquired lock")

	mutex2.Unlock(ctx)
}

// ============================================================================
// RocksDB Edge Case Tests
// ============================================================================

func TestRocksDB_MutexDifferentPrefixes(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex1 := concurrency.NewMutex(session, "/rocksdb/test/prefix1")
	mutex2 := concurrency.NewMutex(session, "/rocksdb/test/prefix2")

	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
	mutex2.Unlock(ctx)
}

func TestRocksDB_MutexWaitingQueue(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()
	ctx := context.Background()

	const numWaiters = 5
	var orderMu sync.Mutex
	order := make([]int, 0, numWaiters)

	ready := make([]chan struct{}, numWaiters)
	for i := range ready {
		ready[i] = make(chan struct{})
	}

	var wg sync.WaitGroup

	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/rocksdb/test/queue")

			close(ready[id])

			err = mutex.Lock(ctx)
			require.NoError(t, err)

			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()

			time.Sleep(20 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)

		<-ready[i]
		time.Sleep(30 * time.Millisecond)
	}

	wg.Wait()

	t.Logf("RocksDB Acquisition order: %v", order)
	assert.Len(t, order, numWaiters)

	expected := make([]int, numWaiters)
	for i := range expected {
		expected[i] = i
	}
	assert.Equal(t, expected, order)
}

// ============================================================================
// RocksDB Data Race Detection Tests
// ============================================================================

func TestRocksDB_MutexNoDataRace(t *testing.T) {
	cli, cleanup := startRocksDBLockTestServer(t)
	defer cleanup()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/rocksdb/test/race")

	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(3)

		go func() {
			defer wg.Done()
			_ = mutex.IsOwner()
		}()

		go func() {
			defer wg.Done()
			_ = mutex.Key()
		}()

		go func() {
			defer wg.Done()
			_ = mutex.Header()
		}()
	}

	wg.Wait()
}
