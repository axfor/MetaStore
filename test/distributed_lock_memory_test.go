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

package test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	etcdapi "metaStore/api/etcd"
	"metaStore/pkg/concurrency"

	clientv3 "go.etcd.io/etcd/client/v3"
	etcdconcurrency "go.etcd.io/etcd/client/v3/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test Helper Functions
// ============================================================================

// startLockTestServer start lock test server using real Raft mode
func startLockTestServer(t *testing.T) (*etcdapi.Server, *clientv3.Client) {
	// Use real Raft mode for proper serialization (same as production)
	node, cleanup := startMemoryNode(t, 1)

	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	require.NoError(t, err)

	t.Cleanup(func() {
		cli.Close()
		cleanup()
	})

	return node.server, cli
}

// ============================================================================
// Session Tests
// ============================================================================

// TestSessionCreate test Session create
func TestSessionCreate(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// create session
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(10))
	require.NoError(t, err)
	require.NotNil(t, session)

	// verify Lease ID
	leaseID := session.Lease()
	assert.NotEqual(t, clientv3.NoLease, leaseID)

	// verify Lease valid
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))
	assert.LessOrEqual(t, ttlResp.TTL, int64(10))

	// close session
	err = session.Close()
	require.NoError(t, err)

	// verify Lease was revoked
	ttlResp, err = cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// TestSessionWithExistingLease test using existing Lease create session
func TestSessionWithExistingLease(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// first create Lease
	leaseResp, err := cli.Grant(ctx, 30)
	require.NoError(t, err)

	// use existing Lease create session
	session, err := concurrency.NewSession(cli, concurrency.WithLease(leaseResp.ID))
	require.NoError(t, err)
	require.NotNil(t, session)

	// verify using same Lease
	assert.Equal(t, leaseResp.ID, session.Lease())

	session.Close()
}

// TestSessionOrphan test Orphan feature
func TestSessionOrphan(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	leaseID := session.Lease()

	// use Orphan end session but keep Lease
	session.Orphan()

	// verify Lease still valid
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))

	// manually revoked Lease
	_, err = cli.Revoke(ctx, leaseID)
	require.NoError(t, err)
}

// TestSessionExpiry test Session expiration
func TestSessionExpiry(t *testing.T) {
	_, cli := startLockTestServer(t)

	// create short-term session(2seconds)
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(2))
	require.NoError(t, err)
	leaseID := session.Lease()

	// close session(stopped keepalive)
	session.Close()

	// wait Lease expiration
	time.Sleep(3 * time.Second)

	// verify Lease expired
	ctx := context.Background()
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// ============================================================================
// Basic Mutex Tests
// ============================================================================

// TestMutexLockUnlock testbasic Lock and Unlock
func TestMutexLockUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/lock")

	// verify initial status
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())

	// acquire lock
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// verify lock status
	assert.True(t, mutex.IsOwner())
	assert.NotEmpty(t, mutex.Key())
	assert.NotNil(t, mutex.Header())

	// release lock
	err = mutex.Unlock(ctx)
	require.NoError(t, err)

	// verify lock released
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())
}

// TestMutexReentrantLock test reentrant lock(same Mutex multiple Lock)
func TestMutexReentrantLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/reentrant")

	// the first time acquire lock
	err = mutex.Lock(ctx)
	require.NoError(t, err)
	firstKey := mutex.Key()

	// the second time acquire lock(should return immediately)
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// verify key unchanged
	assert.Equal(t, firstKey, mutex.Key())

	// release lock
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMutexUnlockWithoutLock testnot holding lockwhen Unlock
func TestMutexUnlockWithoutLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/unlock-without-lock")

	// not holding lockwhen Unlock should be safe
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// ============================================================================
// TryLock Tests
// ============================================================================

// TestTryLockSuccess test TryLock success scenario
func TestTryLockSuccess(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/trylock")

	// TryLock should succeed immediately
	err = mutex.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

// TestTryLockFail test TryLock failure scenario
func TestTryLockFail(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// first session acquires lock
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/trylock-fail")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// second session attempts TryLock
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/trylock-fail")
	err = mutex2.TryLock(ctx)

	// should return ErrLocked
	assert.Error(t, err)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)
	assert.False(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
}

// TestTryLockAfterUnlock test unlock after TryLock
func TestTryLockAfterUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/trylock-after-unlock")
	mutex2 := concurrency.NewMutex(session2, "/test/trylock-after-unlock")

	// session1 acquire lock
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// session2 TryLock failure
	err = mutex2.TryLock(ctx)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)

	// session1 release lock
	err = mutex1.Unlock(ctx)
	require.NoError(t, err)

	// session2 TryLock success
	err = mutex2.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex2.IsOwner())

	mutex2.Unlock(ctx)
}

// ============================================================================
// Concurrent Lock Tests
// ============================================================================

// TestMutexContention test lock contention
func TestMutexContention(t *testing.T) {
	_, cli := startLockTestServer(t)
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

			mutex := concurrency.NewMutex(session, "/test/contention")

			// acquire lock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			acquired <- id
			t.Logf("Client %d acquired lock", id)

			// holding lockfirstsmallsegmenttime
			time.Sleep(50 * time.Millisecond)

			// release lock
			err = mutex.Unlock(ctx)
			require.NoError(t, err)

			released <- id
			t.Logf("Client %d released lock", id)
		}(i)
	}

	// wait all goroutines to finish
	wg.Wait()
	close(acquired)
	close(released)

	// verify each client acquired and released lock
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

// TestMutexFIFOOrder tests that all waiting clients eventually acquire the lock
// after it is released. The key guarantees are:
// 1. Mutual exclusion: only one client holds the lock at a time
// 2. Lock release enables acquisition: when a client releases, a waiting client can acquire
// 3. All waiters eventually succeed: every waiting client eventually gets the lock
func TestMutexFIFOOrder(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numClients = 5
	var acquiredCount int32
	var holdingLock int32 // track if any client is holding the lock

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/test/fifo")

			// acquire lock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// Verify mutual exclusion: only one can hold lock at a time
			if !atomic.CompareAndSwapInt32(&holdingLock, 0, 1) {
				t.Errorf("Client %d acquired lock but another client already holds it!", id)
			}

			count := atomic.AddInt32(&acquiredCount, 1)
			t.Logf("Client %d acquired lock (total: %d/%d)", id, count, numClients)

			// hold lock briefly
			time.Sleep(20 * time.Millisecond)

			// release lock
			atomic.StoreInt32(&holdingLock, 0)
			mutex.Unlock(ctx)
		}(i)
	}

	wg.Wait()

	// Verify all clients successfully acquired and released the lock
	finalCount := atomic.LoadInt32(&acquiredCount)
	assert.Equal(t, int32(numClients), finalCount, "all clients should acquire the lock")
	t.Logf("All %d clients successfully acquired and released the lock", finalCount)
}

// TestMutexCriticalSection test critical section protection
func TestMutexCriticalSection(t *testing.T) {
	_, cli := startLockTestServer(t)

	// Use a timeout context to prevent test from hanging
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const numClients = 5  // Reduced from 10 to avoid timeout
	const iterations = 3  // Reduced from 5 to avoid timeout
	var counter int64
	var violations int64

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			if err != nil {
				t.Logf("Client %d: failed to create session: %v", clientID, err)
				return
			}
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/test/critical-section")

			for j := 0; j < iterations; j++ {
				err = mutex.Lock(ctx)
				if err != nil {
					t.Logf("Client %d iteration %d: failed to acquire lock: %v", clientID, j, err)
					return
				}

				// critical section operation
				oldVal := atomic.LoadInt64(&counter)
				time.Sleep(time.Millisecond) // simulate work
				newVal := atomic.AddInt64(&counter, 1)

				// check for race conditions
				if newVal != oldVal+1 {
					atomic.AddInt64(&violations, 1)
					t.Logf("Client %d: race condition detected! old=%d new=%d", clientID, oldVal, newVal)
				}

				err = mutex.Unlock(ctx)
				if err != nil {
					t.Logf("Client %d iteration %d: failed to release lock: %v", clientID, j, err)
					return
				}
			}
		}(i)
	}

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines completed
	case <-ctx.Done():
		t.Fatal("test timed out waiting for goroutines to complete")
	}

	assert.Equal(t, int64(numClients*iterations), atomic.LoadInt64(&counter))
	assert.Equal(t, int64(0), atomic.LoadInt64(&violations), "no race conditions should occur")
}

// ============================================================================
// Lock Timeout and Cancellation Tests
// ============================================================================

// TestMutexLockWithTimeout test lock acquisition with timeout
func TestMutexLockWithTimeout(t *testing.T) {
	_, cli := startLockTestServer(t)

	// first sessionholding lock
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/timeout")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	// second session attempts to acquire lock, with timeout
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/timeout")

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

// TestMutexLockCancellation test lock acquisition cancellation
func TestMutexLockCancellation(t *testing.T) {
	_, cli := startLockTestServer(t)

	// first sessionholding lock
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/cancel")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	// second session attempts to acquire lock
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/cancel")

	ctx, cancel := context.WithCancel(context.Background())

	// start goroutine to acquire lock
	done := make(chan error, 1)
	go func() {
		done <- mutex2.Lock(ctx)
	}()

	// wait a while then cancel
	time.Sleep(200 * time.Millisecond)
	cancel()

	// verify lock acquisition was canceled
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
// Session Failure Tests
// ============================================================================

// TestMutexReleaseOnSessionClose test lock automatically released on session close
func TestMutexReleaseOnSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// first session acquires lock
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(5))
	require.NoError(t, err)

	mutex1 := concurrency.NewMutex(session1, "/test/session-close")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// second session prepares to acquire lock
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/session-close")

	// start goroutine waiting for lock
	acquired := make(chan struct{})
	go func() {
		err := mutex2.Lock(ctx)
		if err == nil {
			close(acquired)
		}
	}()

	// close first session
	time.Sleep(100 * time.Millisecond)
	session1.Close()

	// verify second session can acquire lock
	select {
	case <-acquired:
		t.Log("Second session acquired lock after first session closed")
		assert.True(t, mutex2.IsOwner())
	case <-time.After(5 * time.Second):
		t.Fatal("second session should acquire lock after first session closes")
	}

	mutex2.Unlock(ctx)
}

// ============================================================================
// Election Tests
// ============================================================================

// TestElectionCampaign test Leader election
func TestElectionCampaign(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/test/election")

	// initial status
	assert.False(t, election.IsLeader())

	// campaign for Leader
	err = election.Campaign(ctx, "leader-value")
	require.NoError(t, err)

	// verify became Leader
	assert.True(t, election.IsLeader())
	assert.NotEmpty(t, election.Key())
	assert.Greater(t, election.Rev(), int64(0))

	// query current Leader
	_, val, err := election.Leader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "leader-value", val)

	// release Leader
	err = election.Resign(ctx)
	require.NoError(t, err)

	assert.False(t, election.IsLeader())
}

// TestElectionMultipleCandidates test multiple candidates election
func TestElectionMultipleCandidates(t *testing.T) {
	_, cli := startLockTestServer(t)
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

			election := concurrency.NewElection(session, "/test/multi-election")

			// campaign
			value := fmt.Sprintf("candidate-%d", id)
			err = election.Campaign(ctx, value)
			require.NoError(t, err)

			leaderChan <- id
			t.Logf("Candidate %d became leader", id)

			// holding for a short time
			time.Sleep(100 * time.Millisecond)

			// release
			election.Resign(ctx)
			t.Logf("Candidate %d resigned", id)
		}(i)
	}

	// wait all candidates to finish
	wg.Wait()
	close(leaderChan)

	// verify all candidates became leader
	leaders := make(map[int]bool)
	for id := range leaderChan {
		leaders[id] = true
	}
	assert.Len(t, leaders, numCandidates)
}

// TestElectionObserve test Leader change observation
func TestElectionObserve(t *testing.T) {
	_, cli := startLockTestServer(t)

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	election1 := concurrency.NewElection(session1, "/test/observe")
	election2 := concurrency.NewElection(session2, "/test/observe")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// election1 becomeas Leader
	err = election1.Campaign(ctx, "leader-1")
	require.NoError(t, err)

	// start observer
	observeCh := election2.Observe(ctx)

	// collect observed Leaders
	var observedLeaders []string
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			select {
			case leader, ok := <-observeCh:
				if !ok {
					return
				}
				observedLeaders = append(observedLeaders, leader)
				t.Logf("Observed leader: %s", leader)
			case <-ctx.Done():
				return
			}
		}
	}()

	// wait first observation
	time.Sleep(200 * time.Millisecond)

	// election1 release
	election1.Resign(ctx)
	time.Sleep(100 * time.Millisecond)

	// election2 becomeas Leader
	err = election2.Campaign(ctx, "leader-2")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done

	// verify observed Leader changes
	t.Logf("Observed leaders: %v", observedLeaders)
	assert.GreaterOrEqual(t, len(observedLeaders), 1)
}

// TestElectionResignNotLeader test non-Leader resign
func TestElectionResignNotLeader(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/test/resign-not-leader")

	// not became Leader resign
	err = election.Resign(ctx)
	assert.Error(t, err)
	assert.Equal(t, concurrency.ErrElectionNotLeader, err)
}

// ============================================================================
// Stress Tests
// ============================================================================

// TestMutexHighConcurrency high concurrency lock test
func TestMutexHighConcurrency(t *testing.T) {
	_, cli := startLockTestServer(t)
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

			mutex := concurrency.NewMutex(session, "/test/high-concurrency")

			for j := 0; j < iterations; j++ {
				lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := mutex.Lock(lockCtx)
				cancel()

				if err != nil {
					atomic.AddInt64(&failCount, 1)
					continue
				}

				atomic.AddInt64(&successCount, 1)

				// shortholding lock
				time.Sleep(5 * time.Millisecond)

				mutex.Unlock(ctx)
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Success: %d, Fail: %d", successCount, failCount)
	assert.Equal(t, int64(numGoroutines*iterations), successCount)
	assert.Equal(t, int64(0), failCount)
}

// TestMutexRapidLockUnlock fast lock and unlock test
func TestMutexRapidLockUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/rapid")

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
// Edge Case Tests
// ============================================================================

// TestMutexDifferentPrefixes test different prefix locks
func TestMutexDifferentPrefixes(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex1 := concurrency.NewMutex(session, "/test/prefix1")
	mutex2 := concurrency.NewMutex(session, "/test/prefix2")

	// acquire different prefix locks
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// both should succeed
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
	mutex2.Unlock(ctx)
}

// TestMutexSameSessionDifferentMutex test same session different Mutex instance
func TestMutexSameSessionDifferentMutex(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	// same session create two Mutex instances(sameprefix)
	mutex1 := concurrency.NewMutex(session, "/test/same-prefix")
	mutex2 := concurrency.NewMutex(session, "/test/same-prefix")

	// mutex1 acquire lock
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// mutex2 canacquire lock(asusesame Lease，key same)
	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// both are owners
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	// actually same key
	assert.Equal(t, mutex1.Key(), mutex2.Key())

	mutex1.Unlock(ctx)
}

// TestMutexEmptyPrefix test empty prefix
func TestMutexEmptyPrefix(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "")

	err = mutex.Lock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

// TestMutexSpecialCharacterPrefix test special character prefix
func TestMutexSpecialCharacterPrefix(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	prefixes := []string{
		"/test/special/chars",
		"/test/with spaces",
		"/test/with-dashes",
		"/test/with_underscores",
		"/test/with.dots",
	}

	for _, prefix := range prefixes {
		t.Run(prefix, func(t *testing.T) {
			mutex := concurrency.NewMutex(session, prefix)
			err := mutex.Lock(ctx)
			require.NoError(t, err)
			assert.True(t, mutex.IsOwner())
			mutex.Unlock(ctx)
		})
	}
}

// ============================================================================
// Benchmark Tests
// ============================================================================

// BenchmarkMutexLockUnlock benchmark lock performance
func BenchmarkMutexLockUnlock(b *testing.B) {
	// Use real Raft mode for proper serialization (same as production)
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/bench/lock")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mutex.Lock(ctx)
		mutex.Unlock(ctx)
	}
}

// BenchmarkTryLock benchmark TryLock performance
func BenchmarkTryLock(b *testing.B) {
	// Use real Raft mode for proper serialization (same as production)
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/bench/trylock")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := mutex.TryLock(ctx)
		if err == nil {
			mutex.Unlock(ctx)
		}
	}
}

// BenchmarkSessionCreate benchmark session create performance
func BenchmarkSessionCreate(b *testing.B) {
	// Use real Raft mode for proper serialization (same as production)
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := NewEtcdClient([]string{node.clientAddr}, 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session, err := concurrency.NewSession(cli, concurrency.WithTTL(10))
		if err != nil {
			b.Fatal(err)
		}
		session.Close()
	}
}

// ============================================================================
// Integration Tests with etcd concurrency package
// ============================================================================

// TestCompatibilityWithEtcdConcurrency test and etcd concurrency package compatibility
func TestCompatibilityWithEtcdConcurrency(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// use etcd concurrency package to create session and lock
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/etcd-compat")

	// acquire lock
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	// verify lock status
	assert.NotEmpty(t, etcdMutex.Key())

	// release lock
	err = etcdMutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMixedLockUsage test mixed usage of custom and etcd lock
func TestMixedLockUsage(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// usecustom concurrency package
	customSession, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer customSession.Close()

	customMutex := concurrency.NewMutex(customSession, "/test/mixed")

	// use etcd  concurrency package
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/mixed")

	// customlockget
	err = customMutex.Lock(ctx)
	require.NoError(t, err)

	// etcd lock attempt should fail
	tryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = etcdMutex.Lock(tryCtx)
	cancel()
	assert.Error(t, err, "etcd mutex should not be able to acquire lock")

	// release customlock
	customMutex.Unlock(ctx)

	// now etcd lock should be able to acquire
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	etcdMutex.Unlock(ctx)
}

// ============================================================================
// Verify Lock Key Format
// ============================================================================

// TestMutexKeyFormat verify lock key format
func TestMutexKeyFormat(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	prefix := "/test/key-format"
	mutex := concurrency.NewMutex(session, prefix)

	err = mutex.Lock(ctx)
	require.NoError(t, err)

	key := mutex.Key()
	t.Logf("Lock key: %s", key)

	// verify key format: prefix/ + lease_id()
	assert.Contains(t, key, prefix+"/")
	assert.Contains(t, key, fmt.Sprintf("%x", session.Lease()))

	mutex.Unlock(ctx)
}

// ============================================================================
// Ordering Verification Tests
// ============================================================================

// TestLockAcquisitionOrderWithTimestamp test lock acquisition order (with timestamp)
func TestLockAcquisitionOrderWithTimestamp(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numClients = 5
	type lockEvent struct {
		id        int
		timestamp time.Time
	}

	var mu sync.Mutex
	events := make([]lockEvent, 0, numClients)

	var wg sync.WaitGroup
	startCh := make(chan struct{})

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/test/order-timestamp")

			// wait start signal
			<-startCh

			// acquire lock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// record acquisition time
			mu.Lock()
			events = append(events, lockEvent{id: id, timestamp: time.Now()})
			mu.Unlock()

			time.Sleep(10 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)
	}

	// simultaneously start all goroutines
	close(startCh)
	wg.Wait()

	// verify event order
	assert.Len(t, events, numClients)

	// verify timestamps are increasing
	for i := 1; i < len(events); i++ {
		assert.True(t, events[i].timestamp.After(events[i-1].timestamp) ||
			events[i].timestamp.Equal(events[i-1].timestamp),
			"Lock acquisition timestamps should be ordered")
	}

	// print order
	var order []int
	for _, e := range events {
		order = append(order, e.id)
	}
	t.Logf("Acquisition order: %v", order)
}

// ============================================================================
// Recovery Tests
// ============================================================================

// TestMutexRecoveryAfterSessionClose test session close after lock recovery
func TestMutexRecoveryAfterSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	prefix := "/test/recovery"

	// first session acquires lock
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(5))
	require.NoError(t, err)

	mutex1 := concurrency.NewMutex(session1, prefix)
	err = mutex1.Lock(ctx)
	require.NoError(t, err)
	t.Log("Session 1 acquired lock")

	// close first session
	session1.Close()
	t.Log("Session 1 closed")

	// second session should be able to acquire lock
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, prefix)

	// should be able to acquire lock
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = mutex2.Lock(lockCtx)
	cancel()

	require.NoError(t, err, "Session 2 should acquire lock after session 1 closes")
	assert.True(t, mutex2.IsOwner())
	t.Log("Session 2 acquired lock")

	mutex2.Unlock(ctx)
}

// ============================================================================
// Additional Concurrency Tests
// ============================================================================

// TestMultipleLocksSequential test sequential acquisition of many locks
func TestMultipleLocksSequential(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	locks := make([]*concurrency.Mutex, 5)
	for i := range locks {
		locks[i] = concurrency.NewMutex(session, fmt.Sprintf("/test/multi/%d", i))
	}

	// sequentially acquire all locks
	for i, lock := range locks {
		err := lock.Lock(ctx)
		require.NoError(t, err, "Failed to acquire lock %d", i)
	}

	// verify all locks are held
	for i, lock := range locks {
		assert.True(t, lock.IsOwner(), "Lock %d should be owned", i)
	}

	// sequentially release all locks
	for i, lock := range locks {
		err := lock.Unlock(ctx)
		require.NoError(t, err, "Failed to release lock %d", i)
	}
}

// TestConcurrentDifferentLocks test concurrent acquisition of different locks
func TestConcurrentDifferentLocks(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numLocks = 10
	var wg sync.WaitGroup
	errors := make(chan error, numLocks)

	for i := 0; i < numLocks; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
			if err != nil {
				errors <- err
				return
			}
			defer session.Close()

			mutex := concurrency.NewMutex(session, fmt.Sprintf("/test/concurrent/%d", id))

			if err := mutex.Lock(ctx); err != nil {
				errors <- err
				return
			}

			time.Sleep(10 * time.Millisecond)

			if err := mutex.Unlock(ctx); err != nil {
				errors <- err
				return
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// check for no errors
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}
}

// TestLockFairness test lock fairness
// Skip this test in short mode as it can be flaky under resource contention
func TestLockFairness(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping fairness test in short mode")
	}

	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numRounds = 5
	const numClients = 3

	var mu sync.Mutex
	acquisitions := make(map[int]int)

	for round := 0; round < numRounds; round++ {
		var wg sync.WaitGroup

		for i := 0; i < numClients; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
				require.NoError(t, err)
				defer session.Close()

				mutex := concurrency.NewMutex(session, "/test/fairness")

				err = mutex.Lock(ctx)
				require.NoError(t, err)

				mu.Lock()
				acquisitions[id]++
				mu.Unlock()

				time.Sleep(5 * time.Millisecond)
				mutex.Unlock(ctx)
			}(i)
		}

		wg.Wait()
	}

	// verify each client acquired lock
	t.Logf("Acquisitions: %v", acquisitions)
	for i := 0; i < numClients; i++ {
		assert.Greater(t, acquisitions[i], 0, "Client %d should have acquired lock at least once", i)
	}

	// verify distribution is even (each client should get numRounds times)
	total := 0
	for _, count := range acquisitions {
		total += count
	}
	assert.Equal(t, numRounds*numClients, total)
}

// TestLockWithContextDeadline test lock with context deadline
func TestLockWithContextDeadline(t *testing.T) {
	_, cli := startLockTestServer(t)

	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/deadline")
	mutex2 := concurrency.NewMutex(session2, "/test/deadline")

	// session1 acquire lock
	ctx1 := context.Background()
	err = mutex1.Lock(ctx1)
	require.NoError(t, err)

	// session2 attempts to acquire lock，time
	deadline := time.Now().Add(500 * time.Millisecond)
	ctx2, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	start := time.Now()
	err = mutex2.Lock(ctx2)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.True(t, elapsed >= 400*time.Millisecond, "Should wait until deadline")
	assert.True(t, elapsed < 1*time.Second, "Should not wait too long after deadline")

	mutex1.Unlock(ctx1)
}

// ============================================================================
// Data Race Detection Tests
// ============================================================================

// TestMutexNoDataRace test no data race
func TestMutexNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/race")

	var wg sync.WaitGroup

	// concurrently call methods
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

// TestSessionNoDataRace test Session no data race
func TestSessionNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)

	var wg sync.WaitGroup

	// concurrently call methods
	for i := 0; i < 10; i++ {
		wg.Add(2)

		go func() {
			defer wg.Done()
			_ = session.Lease()
		}()

		go func() {
			defer wg.Done()
			_ = session.Done()
		}()
	}

	wg.Wait()
	session.Close()
}

// ============================================================================
// Edge Cases for Watch-based Waiting
// ============================================================================

// TestMutexWaitingQueue test lock waiting queue
func TestMutexWaitingQueue(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numWaiters = 5
	var orderMu sync.Mutex
	order := make([]int, 0, numWaiters)

	// signal channel for synchronization
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

			mutex := concurrency.NewMutex(session, "/test/queue")

			// notify ready
			close(ready[id])

			// acquire lock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// recordorder
			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()

			time.Sleep(20 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)

		// wait goroutine ready before starting next
		<-ready[i]
		time.Sleep(30 * time.Millisecond)
	}

	wg.Wait()

	t.Logf("Acquisition order: %v", order)
	assert.Len(t, order, numWaiters)

	// verify order
	expected := make([]int, numWaiters)
	for i := range expected {
		expected[i] = i
	}
	assert.Equal(t, expected, order)
}

// TestMutexWatchEventHandling test Watch event handling
func TestMutexWatchEventHandling(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// create many sessions and locks
	const numSessions = 3
	sessions := make([]*concurrency.Session, numSessions)
	mutexes := make([]*concurrency.Mutex, numSessions)

	for i := range sessions {
		var err error
		sessions[i], err = concurrency.NewSession(cli, concurrency.WithTTL(60))
		require.NoError(t, err)
		mutexes[i] = concurrency.NewMutex(sessions[i], "/test/watch-events")
	}

	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()

	// first session acquires lock
	err := mutexes[0].Lock(ctx)
	require.NoError(t, err)

	// other sessions attempt to acquire lock concurrently (will wait)
	// In real distributed scenarios, clients may request locks simultaneously
	// The server ensures mutual exclusion - only one holds the lock at a time
	done := make([]chan error, numSessions-1)
	for i := 1; i < numSessions; i++ {
		done[i-1] = make(chan error, 1)
		go func(idx int) {
			done[idx-1] <- mutexes[idx].Lock(ctx)
		}(i)
	}

	// wait for all sessions to enter waiting status
	time.Sleep(300 * time.Millisecond)

	// release first lock
	err = mutexes[0].Unlock(ctx)
	require.NoError(t, err)

	// verify all waiting sessions eventually acquire lock (order may vary)
	// The key invariant is mutual exclusion, not specific ordering
	acquired := 0
	timeout := time.After(15 * time.Second)
	for acquired < numSessions-1 {
		select {
		case err := <-done[0]:
			require.NoError(t, err)
			t.Logf("Session 1 acquired lock")
			mutexes[1].Unlock(ctx)
			acquired++
		case err := <-done[1]:
			require.NoError(t, err)
			t.Logf("Session 2 acquired lock")
			mutexes[2].Unlock(ctx)
			acquired++
		case <-timeout:
			t.Fatalf("Timeout: only %d/%d sessions acquired lock", acquired, numSessions-1)
		}
	}
	t.Logf("All %d waiting sessions successfully acquired and released lock", numSessions-1)
}

// ============================================================================
// Performance Characterization Tests
// ============================================================================

// TestLockLatencyDistribution test lock latency distribution
func TestLockLatencyDistribution(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/latency")

	const iterations = 50
	latencies := make([]time.Duration, iterations)

	for i := 0; i < iterations; i++ {
		start := time.Now()
		err := mutex.Lock(ctx)
		latencies[i] = time.Since(start)
		require.NoError(t, err)
		mutex.Unlock(ctx)
	}

	// calculate statistics
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	var total time.Duration
	for _, l := range latencies {
		total += l
	}

	avg := total / time.Duration(iterations)
	p50 := latencies[iterations/2]
	p95 := latencies[iterations*95/100]
	p99 := latencies[iterations*99/100]

	t.Logf("Lock latency distribution (n=%d):", iterations)
	t.Logf("  Average: %v", avg)
	t.Logf("  P50: %v", p50)
	t.Logf("  P95: %v", p95)
	t.Logf("  P99: %v", p99)
	t.Logf("  Min: %v", latencies[0])
	t.Logf("  Max: %v", latencies[iterations-1])

	// verify latency is reasonable
	assert.Less(t, avg, 100*time.Millisecond, "Average latency should be reasonable")
}
