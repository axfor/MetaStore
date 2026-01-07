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
	"metaStore/internal/memory"
	"metaStore/pkg/concurrency"

	clientv3 "go.etcd.io/etcd/client/v3"
	etcdconcurrency "go.etcd.io/etcd/client/v3/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test Helper Functions
// ============================================================================

// startLockTestServer 启动用于锁测试的服务器
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

// TestSessionCreate 测试 Session 创建
func TestSessionCreate(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 创建会话
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(10))
	require.NoError(t, err)
	require.NotNil(t, session)

	// 验证 Lease ID
	leaseID := session.Lease()
	assert.NotEqual(t, clientv3.NoLease, leaseID)

	// 验证 Lease 有效
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))
	assert.LessOrEqual(t, ttlResp.TTL, int64(10))

	// 关闭会话
	err = session.Close()
	require.NoError(t, err)

	// 验证 Lease 已被撤销
	ttlResp, err = cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// TestSessionWithExistingLease 测试使用现有 Lease 创建会话
func TestSessionWithExistingLease(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 先创建 Lease
	leaseResp, err := cli.Grant(ctx, 30)
	require.NoError(t, err)

	// 使用现有 Lease 创建会话
	session, err := concurrency.NewSession(cli, concurrency.WithLease(leaseResp.ID))
	require.NoError(t, err)
	require.NotNil(t, session)

	// 验证使用的是同一个 Lease
	assert.Equal(t, leaseResp.ID, session.Lease())

	session.Close()
}

// TestSessionOrphan 测试 Orphan 功能
func TestSessionOrphan(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	leaseID := session.Lease()

	// 使用 Orphan 结束会话但保留 Lease
	session.Orphan()

	// 验证 Lease 仍然有效
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Greater(t, ttlResp.TTL, int64(0))

	// 手动撤销 Lease
	_, err = cli.Revoke(ctx, leaseID)
	require.NoError(t, err)
}

// TestSessionExpiry 测试 Session 过期
func TestSessionExpiry(t *testing.T) {
	_, cli := startLockTestServer(t)

	// 创建短期会话（2秒）
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(2))
	require.NoError(t, err)
	leaseID := session.Lease()

	// 关闭会话（停止 keepalive）
	session.Close()

	// 等待 Lease 过期
	time.Sleep(3 * time.Second)

	// 验证 Lease 已过期
	ctx := context.Background()
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), ttlResp.TTL)
}

// ============================================================================
// Basic Mutex Tests
// ============================================================================

// TestMutexLockUnlock 测试基本的 Lock 和 Unlock
func TestMutexLockUnlock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/lock")

	// 验证初始状态
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())

	// 获取锁
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// 验证锁状态
	assert.True(t, mutex.IsOwner())
	assert.NotEmpty(t, mutex.Key())
	assert.NotNil(t, mutex.Header())

	// 释放锁
	err = mutex.Unlock(ctx)
	require.NoError(t, err)

	// 验证锁已释放
	assert.False(t, mutex.IsOwner())
	assert.Empty(t, mutex.Key())
}

// TestMutexReentrantLock 测试重入锁（同一个 Mutex 多次 Lock）
func TestMutexReentrantLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/reentrant")

	// 第一次获取锁
	err = mutex.Lock(ctx)
	require.NoError(t, err)
	firstKey := mutex.Key()

	// 第二次获取锁（应该立即返回）
	err = mutex.Lock(ctx)
	require.NoError(t, err)

	// 验证 key 没有变化
	assert.Equal(t, firstKey, mutex.Key())

	// 释放锁
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMutexUnlockWithoutLock 测试未持有锁时 Unlock
func TestMutexUnlockWithoutLock(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/unlock-without-lock")

	// 未持有锁时 Unlock 应该是安全的
	err = mutex.Unlock(ctx)
	require.NoError(t, err)
}

// ============================================================================
// TryLock Tests
// ============================================================================

// TestTryLockSuccess 测试 TryLock 成功场景
func TestTryLockSuccess(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/trylock")

	// TryLock 应该立即成功
	err = mutex.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex.IsOwner())

	mutex.Unlock(ctx)
}

// TestTryLockFail 测试 TryLock 失败场景
func TestTryLockFail(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 第一个会话获取锁
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/trylock-fail")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// 第二个会话尝试 TryLock
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/trylock-fail")
	err = mutex2.TryLock(ctx)

	// 应该返回 ErrLocked
	assert.Error(t, err)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)
	assert.False(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
}

// TestTryLockAfterUnlock 测试解锁后 TryLock
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

	// session1 获取锁
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// session2 TryLock 失败
	err = mutex2.TryLock(ctx)
	assert.Equal(t, etcdconcurrency.ErrLocked, err)

	// session1 释放锁
	err = mutex1.Unlock(ctx)
	require.NoError(t, err)

	// session2 TryLock 成功
	err = mutex2.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, mutex2.IsOwner())

	mutex2.Unlock(ctx)
}

// ============================================================================
// Concurrent Lock Tests
// ============================================================================

// TestMutexContention 测试锁竞争
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

			// 获取锁
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			acquired <- id
			t.Logf("Client %d acquired lock", id)

			// 持有锁一小段时间
			time.Sleep(50 * time.Millisecond)

			// 释放锁
			err = mutex.Unlock(ctx)
			require.NoError(t, err)

			released <- id
			t.Logf("Client %d released lock", id)
		}(i)
	}

	// 等待所有 goroutine 完成
	wg.Wait()
	close(acquired)
	close(released)

	// 验证每个客户端都获取并释放了锁
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

// TestMutexFIFOOrder 测试锁的 FIFO 顺序
func TestMutexFIFOOrder(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numClients = 5
	var orderMu sync.Mutex
	acquireOrder := make([]int, 0, numClients)

	// 创建一个信号通道来控制启动顺序
	startSignals := make([]chan struct{}, numClients)
	for i := range startSignals {
		startSignals[i] = make(chan struct{})
	}

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 等待启动信号
			<-startSignals[id]

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/test/fifo")

			// 获取锁
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// 记录获取顺序
			orderMu.Lock()
			acquireOrder = append(acquireOrder, id)
			orderMu.Unlock()

			t.Logf("Client %d acquired lock at position %d", id, len(acquireOrder))

			// 持有锁一小段时间
			time.Sleep(20 * time.Millisecond)

			mutex.Unlock(ctx)
		}(i)
	}

	// 按顺序发送启动信号
	for i := 0; i < numClients; i++ {
		close(startSignals[i])
		time.Sleep(30 * time.Millisecond) // 确保按顺序注册到锁队列
	}

	wg.Wait()

	// 验证获取顺序
	t.Logf("Acquire order: %v", acquireOrder)
	assert.Len(t, acquireOrder, numClients)

	// 验证是 FIFO 顺序
	expectedOrder := make([]int, numClients)
	for i := range expectedOrder {
		expectedOrder[i] = i
	}
	assert.Equal(t, expectedOrder, acquireOrder, "Lock acquisition should follow FIFO order")
}

// TestMutexCriticalSection 测试临界区保护
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

			session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
			require.NoError(t, err)
			defer session.Close()

			mutex := concurrency.NewMutex(session, "/test/critical-section")

			for j := 0; j < iterations; j++ {
				err = mutex.Lock(ctx)
				require.NoError(t, err)

				// 临界区操作
				oldVal := atomic.LoadInt64(&counter)
				time.Sleep(time.Millisecond) // 模拟工作
				newVal := atomic.AddInt64(&counter, 1)

				// 检查是否有竞态条件
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

// TestMutexLockWithTimeout 测试带超时的锁获取
func TestMutexLockWithTimeout(t *testing.T) {
	_, cli := startLockTestServer(t)

	// 第一个会话持有锁
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/timeout")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	// 第二个会话尝试获取锁，带超时
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

// TestMutexLockCancellation 测试锁获取取消
func TestMutexLockCancellation(t *testing.T) {
	_, cli := startLockTestServer(t)

	// 第一个会话持有锁
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session1.Close()

	mutex1 := concurrency.NewMutex(session1, "/test/cancel")
	err = mutex1.Lock(context.Background())
	require.NoError(t, err)

	// 第二个会话尝试获取锁
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/cancel")

	ctx, cancel := context.WithCancel(context.Background())

	// 启动 goroutine 获取锁
	done := make(chan error, 1)
	go func() {
		done <- mutex2.Lock(ctx)
	}()

	// 等待一会儿然后取消
	time.Sleep(200 * time.Millisecond)
	cancel()

	// 验证锁获取被取消
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

// TestMutexReleaseOnSessionClose 测试 Session 关闭时锁自动释放
func TestMutexReleaseOnSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 第一个会话获取锁
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(5))
	require.NoError(t, err)

	mutex1 := concurrency.NewMutex(session1, "/test/session-close")
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// 第二个会话准备获取锁
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, "/test/session-close")

	// 启动 goroutine 等待锁
	acquired := make(chan struct{})
	go func() {
		err := mutex2.Lock(ctx)
		if err == nil {
			close(acquired)
		}
	}()

	// 关闭第一个会话
	time.Sleep(100 * time.Millisecond)
	session1.Close()

	// 验证第二个会话能获取锁
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

// TestElectionCampaign 测试 Leader 选举
func TestElectionCampaign(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/test/election")

	// 初始状态
	assert.False(t, election.IsLeader())

	// 竞选 Leader
	err = election.Campaign(ctx, "leader-value")
	require.NoError(t, err)

	// 验证成为 Leader
	assert.True(t, election.IsLeader())
	assert.NotEmpty(t, election.Key())
	assert.Greater(t, election.Rev(), int64(0))

	// 查询当前 Leader
	_, val, err := election.Leader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "leader-value", val)

	// 放弃 Leader
	err = election.Resign(ctx)
	require.NoError(t, err)

	assert.False(t, election.IsLeader())
}

// TestElectionMultipleCandidates 测试多候选人选举
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

			// 竞选
			value := fmt.Sprintf("candidate-%d", id)
			err = election.Campaign(ctx, value)
			require.NoError(t, err)

			leaderChan <- id
			t.Logf("Candidate %d became leader", id)

			// 持有一段时间
			time.Sleep(100 * time.Millisecond)

			// 放弃
			election.Resign(ctx)
			t.Logf("Candidate %d resigned", id)
		}(i)
	}

	// 等待所有候选人完成
	wg.Wait()
	close(leaderChan)

	// 验证所有候选人都成为过 Leader
	leaders := make(map[int]bool)
	for id := range leaderChan {
		leaders[id] = true
	}
	assert.Len(t, leaders, numCandidates)
}

// TestElectionObserve 测试 Leader 变化观察
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

	// election1 成为 Leader
	err = election1.Campaign(ctx, "leader-1")
	require.NoError(t, err)

	// 启动观察者
	observeCh := election2.Observe(ctx)

	// 收集观察到的 Leader
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

	// 等待第一次观察
	time.Sleep(200 * time.Millisecond)

	// election1 放弃
	election1.Resign(ctx)
	time.Sleep(100 * time.Millisecond)

	// election2 成为 Leader
	err = election2.Campaign(ctx, "leader-2")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done

	// 验证观察到了 Leader 变化
	t.Logf("Observed leaders: %v", observedLeaders)
	assert.GreaterOrEqual(t, len(observedLeaders), 1)
}

// TestElectionResignNotLeader 测试非 Leader 放弃
func TestElectionResignNotLeader(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/test/resign-not-leader")

	// 未成为 Leader 就放弃
	err = election.Resign(ctx)
	assert.Error(t, err)
	assert.Equal(t, concurrency.ErrElectionNotLeader, err)
}

// ============================================================================
// Stress Tests
// ============================================================================

// TestMutexHighConcurrency 高并发锁测试
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

				// 短暂持有锁
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

// TestMutexRapidLockUnlock 快速加解锁测试
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

// TestMutexDifferentPrefixes 测试不同前缀的锁互不影响
func TestMutexDifferentPrefixes(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	mutex1 := concurrency.NewMutex(session, "/test/prefix1")
	mutex2 := concurrency.NewMutex(session, "/test/prefix2")

	// 同时获取两个不同前缀的锁
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// 两个都应该成功
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	mutex1.Unlock(ctx)
	mutex2.Unlock(ctx)
}

// TestMutexSameSessionDifferentMutex 测试同一会话的不同 Mutex 实例
func TestMutexSameSessionDifferentMutex(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session.Close()

	// 同一会话创建两个 Mutex 实例（相同前缀）
	mutex1 := concurrency.NewMutex(session, "/test/same-prefix")
	mutex2 := concurrency.NewMutex(session, "/test/same-prefix")

	// mutex1 获取锁
	err = mutex1.Lock(ctx)
	require.NoError(t, err)

	// mutex2 也能获取锁（因为使用相同的 Lease，key 相同）
	err = mutex2.Lock(ctx)
	require.NoError(t, err)

	// 两个都认为自己是 owner
	assert.True(t, mutex1.IsOwner())
	assert.True(t, mutex2.IsOwner())

	// 但实际上是同一个 key
	assert.Equal(t, mutex1.Key(), mutex2.Key())

	mutex1.Unlock(ctx)
}

// TestMutexEmptyPrefix 测试空前缀
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

// TestMutexSpecialCharacterPrefix 测试特殊字符前缀
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

// BenchmarkMutexLockUnlock 基准测试锁性能
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

// BenchmarkTryLock 基准测试 TryLock 性能
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

// BenchmarkSessionCreate 基准测试会话创建性能
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

// TestCompatibilityWithEtcdConcurrency 测试与 etcd 官方 concurrency 包的兼容性
func TestCompatibilityWithEtcdConcurrency(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 使用 etcd 官方的 concurrency 包创建会话和锁
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/etcd-compat")

	// 获取锁
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	// 验证锁状态
	assert.NotEmpty(t, etcdMutex.Key())

	// 释放锁
	err = etcdMutex.Unlock(ctx)
	require.NoError(t, err)
}

// TestMixedLockUsage 测试混合使用自定义和 etcd 官方的锁
func TestMixedLockUsage(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 使用自定义的 concurrency 包
	customSession, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer customSession.Close()

	customMutex := concurrency.NewMutex(customSession, "/test/mixed")

	// 使用 etcd 官方的 concurrency 包
	etcdSession, err := etcdconcurrency.NewSession(cli, etcdconcurrency.WithTTL(30))
	require.NoError(t, err)
	defer etcdSession.Close()

	etcdMutex := etcdconcurrency.NewMutex(etcdSession, "/test/mixed")

	// 自定义锁获取
	err = customMutex.Lock(ctx)
	require.NoError(t, err)

	// etcd 锁尝试获取应该失败
	tryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err = etcdMutex.Lock(tryCtx)
	cancel()
	assert.Error(t, err, "etcd mutex should not be able to acquire lock")

	// 释放自定义锁
	customMutex.Unlock(ctx)

	// 现在 etcd 锁应该能获取
	err = etcdMutex.Lock(ctx)
	require.NoError(t, err)

	etcdMutex.Unlock(ctx)
}

// ============================================================================
// Verify Lock Key Format
// ============================================================================

// TestMutexKeyFormat 验证锁 key 格式
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

	// 验证 key 格式: prefix/ + lease_id（十六进制）
	assert.Contains(t, key, prefix+"/")
	assert.Contains(t, key, fmt.Sprintf("%x", session.Lease()))

	mutex.Unlock(ctx)
}

// ============================================================================
// Ordering Verification Tests
// ============================================================================

// TestLockAcquisitionOrderWithTimestamp 测试锁获取顺序（带时间戳验证）
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

			// 等待启动信号
			<-startCh

			// 获取锁
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// 记录获取时间
			mu.Lock()
			events = append(events, lockEvent{id: id, timestamp: time.Now()})
			mu.Unlock()

			time.Sleep(10 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)
	}

	// 同时启动所有 goroutine
	close(startCh)
	wg.Wait()

	// 验证事件顺序
	assert.Len(t, events, numClients)

	// 验证时间戳是递增的
	for i := 1; i < len(events); i++ {
		assert.True(t, events[i].timestamp.After(events[i-1].timestamp) ||
			events[i].timestamp.Equal(events[i-1].timestamp),
			"Lock acquisition timestamps should be ordered")
	}

	// 打印顺序
	var order []int
	for _, e := range events {
		order = append(order, e.id)
	}
	t.Logf("Acquisition order: %v", order)
}

// ============================================================================
// Recovery Tests
// ============================================================================

// TestMutexRecoveryAfterSessionClose 测试会话关闭后的锁恢复
func TestMutexRecoveryAfterSessionClose(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	prefix := "/test/recovery"

	// 第一个会话获取锁
	session1, err := concurrency.NewSession(cli, concurrency.WithTTL(5))
	require.NoError(t, err)

	mutex1 := concurrency.NewMutex(session1, prefix)
	err = mutex1.Lock(ctx)
	require.NoError(t, err)
	t.Log("Session 1 acquired lock")

	// 关闭第一个会话
	session1.Close()
	t.Log("Session 1 closed")

	// 第二个会话应该能获取锁
	session2, err := concurrency.NewSession(cli, concurrency.WithTTL(30))
	require.NoError(t, err)
	defer session2.Close()

	mutex2 := concurrency.NewMutex(session2, prefix)

	// 应该能够获取锁
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

// TestMultipleLocksSequential 测试顺序获取多个锁
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

	// 顺序获取所有锁
	for i, lock := range locks {
		err := lock.Lock(ctx)
		require.NoError(t, err, "Failed to acquire lock %d", i)
	}

	// 验证所有锁都被持有
	for i, lock := range locks {
		assert.True(t, lock.IsOwner(), "Lock %d should be owned", i)
	}

	// 顺序释放所有锁
	for i, lock := range locks {
		err := lock.Unlock(ctx)
		require.NoError(t, err, "Failed to release lock %d", i)
	}
}

// TestConcurrentDifferentLocks 测试并发获取不同的锁
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

	// 检查是否有错误
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}
}

// TestLockFairness 测试锁公平性
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

	// 验证每个客户端都获取了锁
	t.Logf("Acquisitions: %v", acquisitions)
	for i := 0; i < numClients; i++ {
		assert.Greater(t, acquisitions[i], 0, "Client %d should have acquired lock at least once", i)
	}

	// 验证分布相对均匀（每个客户端应该获取约 numRounds 次）
	total := 0
	for _, count := range acquisitions {
		total += count
	}
	assert.Equal(t, numRounds*numClients, total)
}

// TestLockWithContextDeadline 测试带截止时间的锁
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

	// session1 获取锁
	ctx1 := context.Background()
	err = mutex1.Lock(ctx1)
	require.NoError(t, err)

	// session2 尝试获取锁，带截止时间
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

// TestMutexNoDataRace 测试无数据竞争
func TestMutexNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)
	defer session.Close()

	mutex := concurrency.NewMutex(session, "/test/race")

	var wg sync.WaitGroup

	// 并发调用各种方法
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

// TestSessionNoDataRace 测试 Session 无数据竞争
func TestSessionNoDataRace(t *testing.T) {
	_, cli := startLockTestServer(t)

	session, err := concurrency.NewSession(cli, concurrency.WithTTL(60))
	require.NoError(t, err)

	var wg sync.WaitGroup

	// 并发调用各种方法
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

// TestMutexWaitingQueue 测试锁等待队列
func TestMutexWaitingQueue(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	const numWaiters = 5
	var orderMu sync.Mutex
	order := make([]int, 0, numWaiters)

	// 信号通道用于同步
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

			// 通知已准备好
			close(ready[id])

			// 获取锁
			err = mutex.Lock(ctx)
			require.NoError(t, err)

			// 记录顺序
			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()

			time.Sleep(20 * time.Millisecond)
			mutex.Unlock(ctx)
		}(i)

		// 等待 goroutine 准备好后再启动下一个
		<-ready[i]
		time.Sleep(30 * time.Millisecond)
	}

	wg.Wait()

	t.Logf("Acquisition order: %v", order)
	assert.Len(t, order, numWaiters)

	// 验证顺序
	expected := make([]int, numWaiters)
	for i := range expected {
		expected[i] = i
	}
	assert.Equal(t, expected, order)
}

// TestMutexWatchEventHandling 测试 Watch 事件处理
func TestMutexWatchEventHandling(t *testing.T) {
	_, cli := startLockTestServer(t)
	ctx := context.Background()

	// 创建多个会话和锁
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

	// 第一个会话获取锁
	err := mutexes[0].Lock(ctx)
	require.NoError(t, err)

	// 其他会话尝试获取锁（会等待）
	done := make([]chan error, numSessions-1)
	for i := 1; i < numSessions; i++ {
		done[i-1] = make(chan error, 1)
		go func(idx int) {
			done[idx-1] <- mutexes[idx].Lock(ctx)
		}(i)
	}

	// 等待其他会话进入等待状态
	time.Sleep(200 * time.Millisecond)

	// 释放第一个锁
	err = mutexes[0].Unlock(ctx)
	require.NoError(t, err)

	// 验证等待的会话依次获取锁
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

// TestLockLatencyDistribution 测试锁延迟分布
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

	// 计算统计信息
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

	// 验证延迟合理
	assert.Less(t, avg, 100*time.Millisecond, "Average latency should be reasonable")
}
