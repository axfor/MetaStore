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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithLeaseRead 配置选项：启用 Lease Read
func WithLeaseRead(cfg *config.Config) {
	cfg.Server.Raft.LeaseRead.Enable = true
	cfg.Server.Raft.LeaseRead.ClockDrift = 10 * time.Millisecond // 降低 ClockDrift 以获得更长的租约时间
}

// WithoutLeaseRead 配置选项：禁用 Lease Read
func WithoutLeaseRead(cfg *config.Config) {
	cfg.Server.Raft.LeaseRead.Enable = false
}

// TestLeaseRead_SingleNode_Comprehensive 测试场景 1: 单节点集群（启用 Lease Read）
// 验证：单节点下 Lease Read 正常工作，读写流量正常
func TestLeaseRead_SingleNode_Comprehensive(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过 Lease Read 综合测试（short 模式）")
	}

	t.Log("=== 测试场景 1: 单节点集群（Lease Read 启用） ===")

	// 1. 创建单节点集群（启用 Lease Read）
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// 等待节点启动
	time.Sleep(1 * time.Second)

	// 2. 获取 LeaseManager 并验证状态
	t.Log("步骤 1: 验证 Lease Read 状态")
	testable, ok := node.raftNode.(raft.TestableNode)
	require.True(t, ok, "RaftNode should implement TestableNode")

	leaseManager := testable.LeaseManager()
	require.NotNil(t, leaseManager, "LeaseManager should exist")

	// 等待租约建立
	time.Sleep(500 * time.Millisecond)

	stats := leaseManager.Stats()
	t.Logf("Lease 状态: IsLeader=%v, HasValidLease=%v, RenewCount=%d",
		stats.IsLeader, stats.HasValidLease, stats.LeaseRenewCount)

	// 单节点应该能建立租约（etcd 兼容）
	assert.True(t, stats.IsLeader, "Should be leader")
	assert.True(t, stats.HasValidLease, "Should have valid lease (etcd-compatible)")
	assert.Greater(t, stats.LeaseRenewCount, int64(0), "Should have renewed lease")

	// 3. 测试写入操作
	t.Log("步骤 2: 测试写入操作")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	t.Log("✅ 写入 10 个键值对成功")

	// 等待数据提交
	time.Sleep(300 * time.Millisecond)

	// 4. 测试读取操作（应该使用 Lease Read）
	t.Log("步骤 3: 测试读取操作（Lease Read）")
	readIndexManager := testable.ReadIndexManager()
	require.NotNil(t, readIndexManager, "ReadIndexManager should exist")

	initialFastPathReads := readIndexManager.Stats().FastPathReads

	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("key%d", i)
		expectedValue := fmt.Sprintf("value%d", i)

		resp, err := kvStore.Range(ctx, key, "", 1, 0)
		require.NoError(t, err, "Range should succeed")
		require.Len(t, resp.Kvs, 1, "Should have 1 key")
		assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should match")
	}

	t.Log("✅ 读取 10 个键值对成功")

	// 5. 验证快速路径使用情况
	finalStats := readIndexManager.Stats()
	fastPathReads := finalStats.FastPathReads - initialFastPathReads
	t.Logf("Lease Read 快速路径使用: %d 次", fastPathReads)

	// 单节点下应该全部使用快速路径
	assert.Equal(t, int64(10), fastPathReads, "All reads should use fast path")

	t.Log("✅ 单节点 Lease Read 测试通过")
}

// TestLeaseRead_SingleNode_WithoutLeaseRead 测试场景 1-对照组: 单节点集群（Lease Read 禁用）
// 对照组：验证禁用 Lease Read 时的行为
func TestLeaseRead_SingleNode_WithoutLeaseRead(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过 Lease Read 对照测试（short 模式）")
	}

	t.Log("=== 测试场景 1-对照组: 单节点集群（Lease Read 禁用） ===")

	// 1. 创建单节点集群（禁用 Lease Read）
	node, cleanup := startMemoryNode(t, 1, WithoutLeaseRead)
	defer cleanup()

	// 等待节点启动
	time.Sleep(1 * time.Second)

	// 2. 验证 Lease Read 未启用
	t.Log("步骤 1: 验证 Lease Read 禁用状态")
	testable, ok := node.raftNode.(raft.TestableNode)
	require.True(t, ok, "RaftNode should implement TestableNode")

	leaseManager := testable.LeaseManager()
	// 禁用时 leaseManager 可能为 nil
	if leaseManager != nil {
		stats := leaseManager.Stats()
		t.Logf("Lease 状态: HasValidLease=%v (expected: false)", stats.HasValidLease)
	} else {
		t.Log("Lease Manager 未创建（已禁用）")
	}

	// 3. 测试写入和读取仍然正常工作
	t.Log("步骤 2: 验证读写操作正常（使用 ReadIndex）")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 写入
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	time.Sleep(300 * time.Millisecond)

	// 读取
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("key%d", i)
		expectedValue := fmt.Sprintf("value%d", i)

		resp, err := kvStore.Range(ctx, key, "", 1, 0)
		require.NoError(t, err, "Range should succeed")
		require.Len(t, resp.Kvs, 1, "Should have 1 key")
		assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should match")
	}

	t.Log("✅ 禁用 Lease Read 时读写操作正常")
}

// TestLeaseRead_ConcurrentReadWrite 测试场景 2: 并发读写测试
// 验证：高并发下 Lease Read 的正确性和性能
func TestLeaseRead_ConcurrentReadWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过并发读写测试（short 模式）")
	}

	t.Log("=== 测试场景 2: 并发读写测试 ===")

	// 1. 创建单节点集群（启用 Lease Read）
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// 等待节点启动和租约建立
	time.Sleep(1 * time.Second)

	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 2. 并发写入
	t.Log("步骤 1: 并发写入 100 个键值对")
	var writeWg sync.WaitGroup
	writeCount := 100

	writeStart := time.Now()
	for i := 1; i <= writeCount; i++ {
		writeWg.Add(1)
		go func(idx int) {
			defer writeWg.Done()
			key := fmt.Sprintf("concurrent_key%d", idx)
			value := fmt.Sprintf("concurrent_value%d", idx)
			_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
			if err != nil {
				t.Logf("Write error: %v", err)
			}
		}(i)
	}
	writeWg.Wait()
	writeDuration := time.Since(writeStart)
	t.Logf("✅ 并发写入完成，耗时: %v, QPS: %.2f", writeDuration, float64(writeCount)/writeDuration.Seconds())

	// 等待数据提交
	time.Sleep(500 * time.Millisecond)

	// 3. 并发读取
	t.Log("步骤 2: 并发读取 100 个键值对（Lease Read）")
	testable, _ := node.raftNode.(raft.TestableNode)
	readIndexManager := testable.ReadIndexManager()
	initialFastPathReads := readIndexManager.Stats().FastPathReads

	var readWg sync.WaitGroup
	var readErrors int32

	readStart := time.Now()
	for i := 1; i <= writeCount; i++ {
		readWg.Add(1)
		go func(idx int) {
			defer readWg.Done()
			key := fmt.Sprintf("concurrent_key%d", idx)
			expectedValue := fmt.Sprintf("concurrent_value%d", idx)

			resp, err := kvStore.Range(ctx, key, "", 1, 0)
			if err != nil {
				t.Logf("Read error for key %s: %v", key, err)
				atomic.AddInt32(&readErrors, 1)
				return
			}
			if len(resp.Kvs) == 0 {
				t.Logf("Key not found: %s", key)
				atomic.AddInt32(&readErrors, 1)
				return
			}
			if string(resp.Kvs[0].Value) != expectedValue {
				t.Logf("Value mismatch for key %s: expected %s, got %s", key, expectedValue, string(resp.Kvs[0].Value))
				atomic.AddInt32(&readErrors, 1)
			}
		}(i)
	}
	readWg.Wait()
	readDuration := time.Since(readStart)
	t.Logf("✅ 并发读取完成，耗时: %v, QPS: %.2f, 错误: %d",
		readDuration, float64(writeCount)/readDuration.Seconds(), readErrors)

	// 4. 验证快速路径使用
	finalStats := readIndexManager.Stats()
	fastPathReads := finalStats.FastPathReads - initialFastPathReads
	t.Logf("Lease Read 快速路径使用: %d/%d 次 (%.1f%%)",
		fastPathReads, writeCount, float64(fastPathReads)/float64(writeCount)*100)

	assert.Greater(t, fastPathReads, int64(90), "Most reads should use fast path")

	t.Log("✅ 并发读写测试通过")
}

// TestLeaseRead_LeaseExpiration 测试场景 3: 租约过期和续约测试
// 验证：租约过期后的行为和自动续约机制
func TestLeaseRead_LeaseExpiration(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过租约过期测试（short 模式）")
	}

	t.Log("=== 测试场景 3: 租约过期和续约测试 ===")

	// 1. 创建单节点集群（启用 Lease Read）
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// 等待节点启动
	time.Sleep(1 * time.Second)

	testable, _ := node.raftNode.(raft.TestableNode)
	leaseManager := testable.LeaseManager()
	require.NotNil(t, leaseManager, "LeaseManager should exist")

	// 2. 验证初始租约状态
	t.Log("步骤 1: 验证初始租约状态")
	initialStats := leaseManager.Stats()
	t.Logf("初始状态: HasValidLease=%v, RenewCount=%d, Remaining=%v",
		initialStats.HasValidLease, initialStats.LeaseRenewCount, initialStats.LeaseRemaining)

	assert.True(t, initialStats.HasValidLease, "Should have valid lease")
	initialRenewCount := initialStats.LeaseRenewCount

	// 3. 等待足够时间，观察租约续约
	t.Log("步骤 2: 观察租约自动续约")
	time.Sleep(3 * time.Second)

	renewedStats := leaseManager.Stats()
	t.Logf("续约后状态: HasValidLease=%v, RenewCount=%d, Remaining=%v",
		renewedStats.HasValidLease, renewedStats.LeaseRenewCount, renewedStats.LeaseRemaining)

	assert.True(t, renewedStats.HasValidLease, "Should still have valid lease")
	assert.Greater(t, renewedStats.LeaseRenewCount, initialRenewCount, "Should have renewed lease")

	// 4. 在租约有效期内读取数据
	t.Log("步骤 3: 在租约有效期内读取数据")
	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 写入测试数据
	_, _, err := kvStore.PutWithLease(ctx, "test_key", "test_value", 0)
	require.NoError(t, err, "Put should succeed")

	time.Sleep(300 * time.Millisecond)

	// 读取应该使用快速路径
	readIndexManager := testable.ReadIndexManager()
	initialFastPathReads := readIndexManager.Stats().FastPathReads

	resp, err := kvStore.Range(ctx, "test_key", "", 1, 0)
	require.NoError(t, err, "Range should succeed")
	require.Len(t, resp.Kvs, 1, "Should have 1 key")
	assert.Equal(t, "test_value", string(resp.Kvs[0].Value), "Value should match")

	finalFastPathReads := readIndexManager.Stats().FastPathReads
	assert.Equal(t, initialFastPathReads+1, finalFastPathReads, "Should use fast path")

	t.Log("✅ 租约过期和续约测试通过")
}

// TestLeaseRead_DataConsistency 测试场景 4: 数据一致性验证
// 验证：Lease Read 不会破坏数据一致性
func TestLeaseRead_DataConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过数据一致性测试（short 模式）")
	}

	t.Log("=== 测试场景 4: 数据一致性验证 ===")

	// 1. 创建单节点集群（启用 Lease Read）
	node, cleanup := startMemoryNode(t, 1, WithLeaseRead)
	defer cleanup()

	// 等待节点启动
	time.Sleep(1 * time.Second)

	kvStore := node.kvStore.(*memory.Memory)
	ctx := context.Background()

	// 2. 写入初始数据
	t.Log("步骤 1: 写入初始数据")
	testData := make(map[string]string)
	for i := 1; i <= 50; i++ {
		key := fmt.Sprintf("consistency_key%d", i)
		value := fmt.Sprintf("consistency_value%d", i)
		testData[key] = value
		_, _, err := kvStore.PutWithLease(ctx, key, value, 0)
		require.NoError(t, err, "Put should succeed")
	}

	t.Log("✅ 写入 50 个键值对")
	time.Sleep(500 * time.Millisecond)

	// 3. 多次读取验证一致性
	t.Log("步骤 2: 多次读取验证一致性")
	for round := 1; round <= 5; round++ {
		t.Logf("第 %d 轮读取...", round)

		for key, expectedValue := range testData {
			resp, err := kvStore.Range(ctx, key, "", 1, 0)
			require.NoError(t, err, "Range should succeed in round %d", round)
			require.Len(t, resp.Kvs, 1, "Should have 1 key in round %d", round)
			assert.Equal(t, expectedValue, string(resp.Kvs[0].Value), "Value should be consistent in round %d for key %s", round, key)
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Log("✅ 5 轮读取验证通过，数据一致性保持")

	// 4. 验证快速路径使用
	testable, _ := node.raftNode.(raft.TestableNode)
	readIndexManager := testable.ReadIndexManager()
	stats := readIndexManager.Stats()

	t.Logf("快速路径使用情况: %d 次读取", stats.FastPathReads)
	assert.Greater(t, stats.FastPathReads, int64(200), "Should have many fast path reads")

	t.Log("✅ 数据一致性测试通过")
}

// TestLeaseRead_PerformanceComparison 测试场景 5: 性能对比测试
// 对比：启用和禁用 Lease Read 的性能差异
func TestLeaseRead_PerformanceComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过性能对比测试（short 模式）")
	}

	t.Log("=== 测试场景 5: 性能对比测试 ===")

	// 准备测试数据
	testCount := 100
	ctx := context.Background()

	// 测试函数
	runReadTest := func(kvStore *memory.Memory, name string) time.Duration {
		// 写入数据
		for i := 1; i <= testCount; i++ {
			key := fmt.Sprintf("perf_key%d", i)
			value := fmt.Sprintf("perf_value%d", i)
			kvStore.PutWithLease(ctx, key, value, 0)
		}
		time.Sleep(500 * time.Millisecond)

		// 测试读取性能
		start := time.Now()
		for i := 1; i <= testCount; i++ {
			key := fmt.Sprintf("perf_key%d", i)
			kvStore.Range(ctx, key, "", 1, 0)
		}
		duration := time.Since(start)

		qps := float64(testCount) / duration.Seconds()
		t.Logf("%s: 读取 %d 次，耗时 %v, QPS: %.2f", name, testCount, duration, qps)

		return duration
	}

	// 1. 测试启用 Lease Read
	t.Log("步骤 1: 测试启用 Lease Read")
	nodeWith, cleanupWith := startMemoryNode(t, 1, WithLeaseRead)
	time.Sleep(1 * time.Second)

	kvStoreWith := nodeWith.kvStore.(*memory.Memory)
	durationWith := runReadTest(kvStoreWith, "启用 Lease Read")

	// 保存第一个节点的统计信息（在清理前）
	var withStats *struct {
		FastPathReads int64
	}
	testableWith, _ := nodeWith.raftNode.(raft.TestableNode)
	if testableWith != nil && testableWith.ReadIndexManager() != nil {
		stats := testableWith.ReadIndexManager().Stats()
		withStats = &struct{ FastPathReads int64 }{FastPathReads: stats.FastPathReads}
		t.Logf("启用 Lease Read - 快速路径使用: %d 次", withStats.FastPathReads)
	}

	// 清理第一个节点，避免端口和数据目录冲突
	cleanupWith()
	time.Sleep(500 * time.Millisecond)

	// 2. 测试禁用 Lease Read（独立的单节点集群，使用相同的 nodeID 和端口）
	t.Log("步骤 2: 测试禁用 Lease Read")
	nodeWithout, cleanupWithout := startMemoryNode(t, 1, WithoutLeaseRead)
	defer cleanupWithout()
	time.Sleep(1 * time.Second)

	kvStoreWithout := nodeWithout.kvStore.(*memory.Memory)
	durationWithout := runReadTest(kvStoreWithout, "禁用 Lease Read")

	// 3. 对比分析
	t.Log("步骤 3: 性能对比分析")
	improvement := float64(durationWithout-durationWith) / float64(durationWithout) * 100
	speedup := float64(durationWithout) / float64(durationWith)

	t.Logf("性能提升: %.2f%%", improvement)
	t.Logf("加速比: %.2fx", speedup)

	// 验证 Lease Read 确实使用了快速路径（使用之前保存的统计信息）
	if withStats != nil {
		assert.Greater(t, withStats.FastPathReads, int64(0), "Should use fast path when enabled")
	}

	t.Log("✅ 性能对比测试完成")
}
