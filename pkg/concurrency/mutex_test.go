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
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	etcdapi "metaStore/api/etcd"
	"metaStore/internal/memory"

	clientv3 "go.etcd.io/etcd/client/v3"
	etcdconcurrency "go.etcd.io/etcd/client/v3/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test Helper Functions
// ============================================================================

// startLockTestServer start用atlocktestserver
func startLockTestServer(t *testing.T) (*etcdapi.Server, *clientv3.Client) {
	store := memory.NewMemoryEtcd()
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0",
		ClusterID: 1,
		MemberID:  1,
	})
	require.NoError(t, err)

	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cli.Close()
		server.Stop()
	})

	return server, cli
}

// ============================================================================
// Session Tests
// ============================================================================

// TestSessionCreate test Session create
func TestSessionCreate(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// createsession
	session, err := NewSession(cli, WithTTL(10))
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

	// closesession
	err = session.Close()
	require.NoError(t, err)

	// verify Lease 已被revoke
	ttlResp, err = cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// TestSessionWithExistingLease testuse现有 Lease createsession
func TestSessionWithExistingLease(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 先create Lease
	leaseResp, err := cli.Grant(ctx, 30)
	require.NoError(t, err)

	// use现有 Lease createsession
	session, err := NewSession(cli, WithLease(leaseResp.ID))
	require.NoError(t, err)
	require.NotNil(t, session)

	// verifyuseis同一个 Lease
	assert.Equal(t, leaseResp.ID, session.Lease())

	session.Close()
}

// TestSessionOrphan test Orphan 功能
func TestSessionOrphan(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	leaseID := session.Lease()

	// use Orphan endsession但保留 Lease
	session.Orphan()

	// verify Lease 仍然valid
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))

	// 手动revoke Lease
	_, err = cli.Revoke(ctx, leaseID)
	require.NoError(t, err)
}

// TestSessionExpiry test Session expiration
func TestSessionExpiry(t *testing.T) {
	_, cli := startLockTestServer(t)

	// create短期session（2秒）
	session, err := NewSession(cli, WithTTL(2))
	require.NoError(t, err)
	leaseID := session.Lease()

	// closesession（stopped keepalive）
	session.Close()

	// wait Lease expiration
	time.Sleep(3 * time.Second)

	// verify Lease 已expiration
	ctx := context.Background()
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// ============================================================================
// Basic Mutex Tests
// ============================================================================

// TestMutexLockUnlock test基本 Lock and Unlock
func TestMutexLockUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/lock")

	// verifyinitialstatus
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())

	// getlock
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// verifylockstatus
	assert.True(t, mutex.IsOwner())
	assert.NotEmpty(t, mutex.Key())
	assert.NotNil(t, mutex.Header())

	// releaselock
	err = mutex.Unlock(ctx)
	require.NoError(t, err)

	// verifylock已release
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())
}

// TestMutexReentrantLock test重入lock（同一个 Mutex 多次 Lock）
func TestMutexReentrantLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/reentrant")

	// 第一次getlock
	err = mutex.Lock(ctx)
	require.NoError(t, err)
	firstKey := mutex.Key()

	// 第二次getlock（should立即return）
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// verify key none变化
	assert.Equal(t, firstKey, mutex.Key())

	// releaselock
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMutexUnlockWithoutLock test未持有lock时 Unlock
func TestMutexUnlockWithoutLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/unlock-without-lock")

	// 未持有lock时 Unlock shouldissafe
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// ============================================================================
// TryLock Tests
// ============================================================================

// TestTryLockSuccess test TryLock success场景
func TestTryLockSuccess(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/trylock")

	// TryLock should立即success
	err = mutex.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

// TestTryLockFail test TryLock failure场景
func TestTryLockFail(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 第一个sessiongetlock
	session1, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := NewMutex(session1, "/test/trylock-fail")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// 第二个session尝试 TryLock
	session2, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := NewMutex(session2, "/test/trylock-fail")
	err = mutex2.TryLock(ctx)

	// shouldreturn ErrLocked
	assert.Error(t, err)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)
	assert.False(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
}

// TestTryLockAfterUnlock testunlock后 TryLock
func TestTryLockAfterUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session1, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex1 := NewMutex(session1, "/test/trylock-after-unlock")
	mutex2 := NewMutex(session2, "/test/trylock-after-unlock")

	// session1 getlock
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// session2 TryLock failure
	err = mutex2.TryLock(ctx)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)

	// session1 releaselock
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

// TestMutexContention testlock竞争
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

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := NewMutex(session, "/test/contention")

			// getlock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			acquired <- id
			t.Logf("Client %d acquired lock", id)

			// 持有lock一小segmenttime
			time.Sleep(50 * time.Millisecond)

			// releaselock
			err = mutex.Unlock(ctx)
			require.NoError(t, err)

			released <- id
			t.Logf("Client %d released lock", id)
		}(i)
	}

	// waitall goroutine done
	wg.Wait()
	close(acquired)
	close(released)

	// verifyeachclient都get并releaselock
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

// TestMutexFIFOOrder testlock FIFO order
func TestMutexFIFOOrder(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numClients = 5
	var orderMu sync.Mutex
	acquireOrder := make([]int, 0, numClients)

	// create一个信号channel来控制startorder
	startSignals := make([]chan struct{}, numClients)
	for i := range startSignals {
		startSignals[i] = make(chan struct{})
	}

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// waitstart信号
			<-startSignals[id]

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := NewMutex(session, "/test/fifo")

			// getlock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// recordgetorder
			orderMu.Lock()
			acquireOrder = append(acquireOrder, id)
			orderMu.Unlock()

			t.Logf("Client %d acquired lock at position %d", id, len(acquireOrder))

			// 持有lock一小segmenttime
			time.Sleep(20 * time.Millisecond)

			mutex.Unlock(ctx)
		}(i)
	}

	// 按ordersendstart信号
	for i := 0; i < numClients; i++ {
		close(startSignals[i])
		time.Sleep(30 * time.Millisecond) // 确保按orderregistertolockqueue
	}

	wg.Wait()

	// verifygetorder
	t.Logf("Acquire order: %v", acquireOrder)
	assert.Len(t, acquireOrder, numClients)

	// verifyis FIFO order
	expectedOrder := make([]int, numClients)
	for i := range expectedOrder {
		expectedOrder[i] = i
	}
	assert.Equal(t, expectedOrder, acquireOrder, "Lock acquisition should follow FIFO order")
}

// TestMutexCriticalSection test临界区protected
func TestMutexCriticalSection(t *testing.T) {
	_, cli := startLockTestServer(t)
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

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := NewMutex(session, "/test/critical-section")

			for j := 0; j < iterations; j++ {
				err = mutex.Lock(ctx)
				require.NoError(t, err)

				// 临界区operation
				oldVal := atomic.LoadInt64(&counter)
				time.Sleep(time.Millisecond) // 模拟工作
				newVal := atomic.AddInt64(&counter, 1)

				// checkisno有竞态condition
				if newVal != oldVal+1 {
					atomic.AddInt64(&violations, 1)
				}

				mutex.Unlock(ctx)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(numClients*iterations), atomic.LoadInt64(&counter))
	assert.Equal(t, int64(0), atomic.LoadInt64(&violations), "No race conditions should occur")
}

// ============================================================================
// Lock Timeout and Cancellation Tests
// ============================================================================

// TestMutexLockWithTimeout test带timeoutlockget
func TestMutexLockWithTimeout(t *testing.T) {
	_, cli := startLockTestServer(t)
	bgCtx := context.Background()

	// 第一个session持有lock
	session1, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := NewMutex(session1, "/test/timeout")
	err = mutex1.Lock(bgCtx)
	require.NoError(t, err)

	// 第二个session尝试getlock，带timeout
	session2, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := NewMutex(session2, "/test/timeout")

	ctx, cancel := context.WithTimeout(bgCtx, 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = mutex2.Lock(ctx)

	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.True(t, elapsed >= 400*time.Millisecond && elapsed < 1*time.Second,
		"Lock should timeout around 500ms, got %v", elapsed)

	mutex1.Unlock(bgCtx)
}

// TestMutexLockCancellation testlockgetcancel
func TestMutexLockCancellation(t *testing.T) {
	_, cli := startLockTestServer(t)

	// 第一个session持有lock
	session1, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := NewMutex(session1, "/test/cancel")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	// 第二个session尝试getlock
	session2, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := NewMutex(session2, "/test/cancel")

	ctx, cancel := context.WithCancel(context.Background())

	// start goroutine getlock
	done := make(chan error, 1)
	go func() {
		done <- mutex2.Lock(ctx)
	}()

	// wait一will儿然后cancel
	time.Sleep(200 * time.Millisecond)
	cancel()

	// verifylockget被cancel
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

// TestMutexReleaseOnSessionClose test Session close时lock自动release
func TestMutexReleaseOnSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 第一个sessiongetlock
	session1, err := NewSession(cli, WithTTL(5))
	require.NoError(t, err)

	mutex1 := NewMutex(session1, "/test/session-close")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// 第二个session准备getlock
	session2, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := NewMutex(session2, "/test/session-close")

	// start goroutine waitlock
	acquired := make(chan struct{})
	go func() {
		err := mutex2.Lock(ctx)
		if err == nil {
			close(acquired)
		}
	}()

	// close第一个session
	time.Sleep(100 * time.Millisecond)
	session1.Close()

	// verify第二个session能getlock
	select {
	case <-acquired:
		t.Log("Second session acquired lock after first session closed")
		assert.True(t, mutex2.IsOwner())
	case <-time.After(5 * time.Second):
		t.Fatal("Second session should acquire lock after first session closes")
	}

	mutex2.Unlock(ctx)
}

// ============================================================================
// Election Tests
// ============================================================================

// TestElectionCampaign test Leader 选举
func TestElectionCampaign(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := NewElection(session, "/test/election")

	// initialstatus
	assert.False(t, election.IsLeader())

	// 竞选 Leader
	err = election.Campaign(ctx, "leader-value")
	require.NoError(t, err)

	// verify成as Leader
	assert.True(t, election.IsLeader())
	assert.NotEmpty(t, election.Key())
	assert.Greater(t, election.Rev(), int64(0))

	// 查询current Leader
	_, val, err := election.Leader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "leader-value", val)

	// 放弃 Leader
	err = election.Resign(ctx)
	require.NoError(t, err)

	assert.False(t, election.IsLeader())
}

// TestElectionMultipleCandidates test多候选人选举
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

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			election := NewElection(session, "/test/multi-election")

			// 竞选
			value := fmt.Sprintf("candidate-%d", id)
			err = election.Campaign(ctx, value)
			require.NoError(t, err)

			leaderChan <- id
			t.Logf("Candidate %d became leader", id)

			// 持有一segmenttime
			time.Sleep(100 * time.Millisecond)

			// 放弃
			election.Resign(ctx)
			t.Logf("Candidate %d resigned", id)
		}(i)
	}

	// waitall候选人done
	wg.Wait()
	close(leaderChan)

	// verifyall候选人都成as过 Leader
	leaders := make(map[int]bool)
	for id := range leaderChan {
		leaders[id] = true
	}
	assert.Len(t, leaders, numCandidates)
}

// TestElectionObserve test Leader 变化观察
func TestElectionObserve(t *testing.T) {
	_, cli := startLockTestServer(t)

	session1, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	election1 := NewElection(session1, "/test/observe")
	election2 := NewElection(session2, "/test/observe")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// election1 成as Leader
	err = election1.Campaign(ctx, "leader-1")
	require.NoError(t, err)

	// start观察者
	observeCh := election2.Observe(ctx)

	// 收集观察to Leader
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

	// wait第一次观察
	time.Sleep(200 * time.Millisecond)

	// election1 放弃
	election1.Resign(ctx)
	time.Sleep(100 * time.Millisecond)

	// election2 成as Leader
	err = election2.Campaign(ctx, "leader-2")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done

	// verify观察to Leader 变化
	t.Logf("Observed leaders: %v", observedLeaders)
	assert.GreaterOrEqual(t, len(observedLeaders), 1)
}

// TestElectionResignNotLeader test非 Leader 放弃
func TestElectionResignNotLeader(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := NewElection(session, "/test/resign-not-leader")

	// 未成as Leader 就放弃
	err = election.Resign(ctx)
	assert.Error(t, err)
	assert.Equal(t, ErrElectionNotLeader, err)
}

// ============================================================================
// Stress Tests
// ============================================================================

// TestMutexHighConcurrency highconcurrencylocktest
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

			session, err := NewSession(cli, WithTTL(60))
			if err != nil {
				atomic.AddInt64(&failCount, int64(iterations))
				return
			}
			defer session.Close()

			mutex := NewMutex(session, "/test/high-concurrency")

			for j := 0; j < iterations; j++ {
				lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := mutex.Lock(lockCtx)
				cancel()

				if err != nil {
					atomic.AddInt64(&failCount, 1)
					continue
				}

				atomic.AddInt64(&successCount, 1)

				// 短暂持有lock
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

// TestMutexRapidLockUnlock fast加unlocktest
func TestMutexRapidLockUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/rapid")

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

// TestMutexDifferentPrefixes testdifferentprefixlock互not影响
func TestMutexDifferentPrefixes(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex1 := NewMutex(session, "/test/prefix1")
	mutex2 := NewMutex(session, "/test/prefix2")

	// 同时get两个differentprefixlock
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// 两个都shouldsuccess
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
	mutex2.Unlock(ctx)
}

// TestMutexSameSessionDifferentMutex test同一sessiondifferent Mutex instance
func TestMutexSameSessionDifferentMutex(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	// 同一sessioncreate两个 Mutex instance（sameprefix）
	mutex1 := NewMutex(session, "/test/same-prefix")
	mutex2 := NewMutex(session, "/test/same-prefix")

	// mutex1 getlock
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// mutex2 也能getlock（因asusesame Lease，key same）
	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// 两个都认as自己is owner
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	// 但实际上is同一个 key
	assert.Equal(t, mutex1.Key(), mutex2.Key())

	mutex1.Unlock(ctx)
}

// TestMutexEmptyPrefix testemptyprefix
func TestMutexEmptyPrefix(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "")

	err = mutex.Lock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

// TestMutexSpecialCharacterPrefix test特殊字符prefix
func TestMutexSpecialCharacterPrefix(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
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
			mutex := NewMutex(session, prefix)
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

// BenchmarkMutexLockUnlock 基准testlock性能
func BenchmarkMutexLockUnlock(b *testing.B) {
	store := memory.NewMemoryEtcd()
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0",
		ClusterID: 1,
		MemberID:  1,
	})
	if err != nil {
		b.Fatal(err)
	}

	go server.Start()
	time.Sleep(100 * time.Millisecond)
	defer server.Stop()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	session, err := NewSession(cli, WithTTL(60))
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()

	mutex := NewMutex(session, "/bench/lock")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mutex.Lock(ctx)
		mutex.Unlock(ctx)
	}
}

// BenchmarkTryLock 基准test TryLock 性能
func BenchmarkTryLock(b *testing.B) {
	store := memory.NewMemoryEtcd()
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0",
		ClusterID: 1,
		MemberID:  1,
	})
	if err != nil {
		b.Fatal(err)
	}

	go server.Start()
	time.Sleep(100 * time.Millisecond)
	defer server.Stop()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	session, err := NewSession(cli, WithTTL(60))
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()

	mutex := NewMutex(session, "/bench/trylock")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := mutex.TryLock(ctx)
		if err == nil {
			mutex.Unlock(ctx)
		}
	}
}

// BenchmarkSessionCreate 基准testsessioncreate性能
func BenchmarkSessionCreate(b *testing.B) {
	store := memory.NewMemoryEtcd()
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0",
		ClusterID: 1,
		MemberID:  1,
	})
	if err != nil {
		b.Fatal(err)
	}

	go server.Start()
	time.Sleep(100 * time.Millisecond)
	defer server.Stop()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer cli.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session, err := NewSession(cli, WithTTL(10))
		if err != nil {
			b.Fatal(err)
		}
		session.Close()
	}
}

// ============================================================================
// Integration Tests with etcd concurrency package
// ============================================================================

// TestCompatibilityWithEtcdConcurrency testand etcd 官方 concurrency packagecompatible性
func TestCompatibilityWithEtcdConcurrency(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// use etcd 官方 concurrency packagecreatesessionandlock
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/etcd-compat")

	// getlock
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	// verifylockstatus
	assert.NotEmpty(t, etcdMutex.Key())

	// releaselock
	err = etcdMutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMixedLockUsage test混合usecustomand etcd 官方lock
func TestMixedLockUsage(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// usecustom concurrency package
	customSession, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer customSession.Close()

	customMutex := NewMutex(customSession, "/test/mixed")

	// use etcd 官方 concurrency package
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/mixed")

	// customlockget
	err = customMutex.Lock(ctx)
	require.NoError(t, err)

	// etcd lock尝试getshouldfailure
	tryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = etcdMutex.Lock(tryCtx)
	cancel()
	assert.Error(t, err, "etcd mutex should not be able to acquire lock")

	// releasecustomlock
	customMutex.Unlock(ctx)

	// 现in etcd lockshould能get
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	etcdMutex.Unlock(ctx)
}

// ============================================================================
// Verify Lock Key Format
// ============================================================================

// TestMutexKeyFormat verifylock key format
func TestMutexKeyFormat(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	prefix := "/test/key-format"
	mutex := NewMutex(session, prefix)

	err = mutex.Lock(ctx)
	require.NoError(t, err)

	key := mutex.Key()
	t.Logf("Lock key: %s", key)

	// verify key format: prefix/ + lease_id（十六进制）
	assert.Contains(t, key, prefix+"/")
	assert.Contains(t, key, fmt.Sprintf("%x", session.Lease()))

	mutex.Unlock(ctx)
}

// ============================================================================
// Ordering Verification Tests
// ============================================================================

// TestLockAcquisitionOrderWithTimestamp testlockgetorder（带time戳verify）
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

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := NewMutex(session, "/test/order-timestamp")

			// waitstart信号
			<-startCh

			// getlock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// recordgettime
			mu.Lock()
			events = append(events, lockEvent{id: id, timestamp: time.Now()})
			mu.Unlock()

			time.Sleep(10 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)
	}

	// 同时startall goroutine
	close(startCh)
	wg.Wait()

	// verifyeventorder
	assert.Len(t, events, numClients)

	// verifytime戳is递增
	for i := 1; i < len(events); i++ {
		assert.True(t, events[i].timestamp.After(events[i-1].timestamp) ||
			events[i].timestamp.Equal(events[i-1].timestamp),
			"Lock acquisition timestamps should be ordered")
	}

	// 打印order
	var order []int
	for _, e := range events {
		order = append(order, e.id)
	}
	t.Logf("Acquisition order: %v", order)
}

// ============================================================================
// Recovery Tests
// ============================================================================

// TestMutexRecoveryAfterSessionClose testsessionclose后lockrecovery
func TestMutexRecoveryAfterSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	prefix := "/test/recovery"

	// 第一个sessiongetlock
	session1, err := NewSession(cli, WithTTL(5))
	require.NoError(t, err)

	mutex1 := NewMutex(session1, prefix)
	err = mutex1.Lock(ctx)
	require.NoError(t, err)
	t.Log("Session 1 acquired lock")

	// close第一个session
	session1.Close()
	t.Log("Session 1 closed")

	// 第二个sessionshould能getlock
	session2, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := NewMutex(session2, prefix)

	// shouldcangetlock
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

// TestMultipleLocksSequential testorderget多个lock
func TestMultipleLocksSequential(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	locks := make([]*Mutex, 5)
	for i := range locks {
		locks[i] = NewMutex(session, fmt.Sprintf("/test/multi/%d", i))
	}

	// ordergetalllock
	for i, lock := range locks {
		err := lock.Lock(ctx)
		require.NoError(t, err, "Failed to acquire lock %d", i)
	}

	// verifyalllock都被持有
	for i, lock := range locks {
		assert.True(t, lock.IsOwner(), "Lock %d should be owned", i)
	}

	// orderreleasealllock
	for i, lock := range locks {
		err := lock.Unlock(ctx)
		require.NoError(t, err, "Failed to release lock %d", i)
	}
}

// TestConcurrentDifferentLocks testconcurrencygetdifferentlock
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

			session, err := NewSession(cli, WithTTL(30))
			if err != nil {
				errors <- err
				return
			}
			defer session.Close()

			mutex := NewMutex(session, fmt.Sprintf("/test/concurrent/%d", id))

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

	// checkisno有incorrect
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}
}

// TestLockFairness testlock公平性
func TestLockFairness(t *testing.T) {
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

				session, err := NewSession(cli, WithTTL(60))
				require.NoError(t, err)
				defer session.Close()

				mutex := NewMutex(session, "/test/fairness")

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

	// verifyeachclient都getlock
	t.Logf("Acquisitions: %v", acquisitions)
	for i := 0; i < numClients; i++ {
		assert.Greater(t, acquisitions[i], 0, "Client %d should have acquired lock at least once", i)
	}

	// verifydistribution相对均匀（eachclientshouldget约 numRounds 次）
	total := 0
	for _, count := range acquisitions {
		total += count
	}
	assert.Equal(t, numRounds*numClients, total)
}

// TestLockWithContextDeadline test带截止timelock
func TestLockWithContextDeadline(t *testing.T) {
	_, cli := startLockTestServer(t)

	session1, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	session2, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex1 := NewMutex(session1, "/test/deadline")
	mutex2 := NewMutex(session2, "/test/deadline")

	// session1 getlock
	ctx1 := context.Background()
	err = mutex1.Lock(ctx1)
	require.NoError(t, err)

	// session2 尝试getlock，带截止time
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

// TestMutexNoDataRace test无data竞争
func TestMutexNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/race")

	var wg sync.WaitGroup

	// concurrencycall各种method
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

// TestSessionNoDataRace test Session 无data竞争
func TestSessionNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)

	var wg sync.WaitGroup

	// concurrencycall各种method
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

// TestMutexWaitingQueue testlockwaitqueue
func TestMutexWaitingQueue(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numWaiters = 5
	var orderMu sync.Mutex
	order := make([]int, 0, numWaiters)

	// 信号channel用atsynchronous
	ready := make([]chan struct{}, numWaiters)
	for i := range ready {
		ready[i] = make(chan struct{})
	}

	var wg sync.WaitGroup

	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			session, err := NewSession(cli, WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := NewMutex(session, "/test/queue")

			// notification已准备好
			close(ready[id])

			// getlock
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// recordorder
			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()

			time.Sleep(20 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)

		// wait goroutine 准备好后再startnext
		<-ready[i]
		time.Sleep(30 * time.Millisecond)
	}

	wg.Wait()

	t.Logf("Acquisition order: %v", order)
	assert.Len(t, order, numWaiters)

	// verifyorder
	expected := make([]int, numWaiters)
	for i := range expected {
		expected[i] = i
	}
	assert.Equal(t, expected, order)
}

// TestMutexWatchEventHandling test Watch eventhandle
func TestMutexWatchEventHandling(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// create多个sessionandlock
	const numSessions = 3
	sessions := make([]*Session, numSessions)
	mutexes := make([]*Mutex, numSessions)

	for i := range sessions {
		var err error
		sessions[i], err = NewSession(cli, WithTTL(60))
		require.NoError(t, err)
		mutexes[i] = NewMutex(sessions[i], "/test/watch-events")
	}

	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()

	// 第一个sessiongetlock
	err := mutexes[0].Lock(ctx)
	require.NoError(t, err)

	// 其他session尝试getlock（willwait）
	done := make([]chan error, numSessions-1)
	for i := 1; i < numSessions; i++ {
		done[i-1] = make(chan error, 1)
		go func(idx int) {
			done[idx-1] <- mutexes[idx].Lock(ctx)
		}(i)
	}

	// wait其他session进入waitstatus
	time.Sleep(200 * time.Millisecond)

	// release第一个lock
	err = mutexes[0].Unlock(ctx)
	require.NoError(t, err)

	// verifywaitsession依次getlock
	for i := 1; i < numSessions; i++ {
		select {
		case err := <-done[i-1]:
			require.NoError(t, err)
			t.Logf("Session %d acquired lock", i)
			mutexes[i].Unlock(ctx)
		case <-time.After(5 * time.Second):
			t.Fatalf("Session %d failed to acquire lock", i)
		}
	}
}

// ============================================================================
// Performance Characterization Tests
// ============================================================================

// TestLockLatencyDistribution testlocklatencydistribution
func TestLockLatencyDistribution(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := NewSession(cli, WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := NewMutex(session, "/test/latency")

	const iterations = 50
	latencies := make([]time.Duration, iterations)

	for i := 0; i < iterations; i++ {
		start := time.Now()
		err := mutex.Lock(ctx)
		latencies[i] = time.Since(start)
		require.NoError(t, err)
		mutex.Unlock(ctx)
	}

	// calculatestatisticsinfo
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

	// verifylatency合理
	assert.Less(t, avg, 100*time.Millisecond, "Average latency should be reasonable")
}
