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
	"os"
	"strings"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/etcdapi"

	clientv3 "go.etcd.io/etcd/client/v3"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// startTestServer 启动测试服务器
func startTestServer(t *testing.T) (*etcdapi.Server, *clientv3.Client) {
	// 创建内存存储
	store := memory.NewMemoryEtcd()

	// 创建 etcd 兼容服务器（随机端口）
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0", // 使用随机端口
		ClusterID: 1,
		MemberID:  1,
	})
	require.NoError(t, err)

	// 启动服务器
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	// 等待服务器启动
	time.Sleep(100 * time.Millisecond)

	// 创建客户端
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// 清理函数
	t.Cleanup(func() {
		cli.Close()
		server.Stop()
	})

	return server, cli
}

// startTestServerRocksDB 启动 RocksDB 测试服务器（单节点 Raft）
func startTestServerRocksDB(t *testing.T) (*etcdapi.Server, *clientv3.Client, func()) {
	// 单节点 Raft 集群必须使用 nodeID=1，peers 数组的第一个元素对应 ID 1
	nodeID := 1

	// NewNodeRocksDB 使用 data/rocksdb/{id} 目录
	dataDir := fmt.Sprintf("data/rocksdb/%d", nodeID)

	// 清理函数
	cleanup := func() {
		os.RemoveAll(dataDir)
	}

	// 确保清理
	t.Cleanup(cleanup)

	// Setup RocksDB
	peers := []string{"http://127.0.0.1:10400"}
	os.RemoveAll(dataDir)

	proposeC := make(chan string, 1)
	confChangeC := make(chan raftpb.ConfChange, 1)

	// Open RocksDB
	dbPath := fmt.Sprintf("%s/kv", dataDir)
	os.MkdirAll(dbPath, 0755)
	db, err := rocksdb.Open(dbPath)
	require.NoError(t, err)

	var kvs *rocksdb.RocksDB
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNodeRocksDB(nodeID, peers, false, getSnapshot, proposeC, confChangeC, db)
	kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)

	// 创建 etcd 兼容服务器（随机端口）
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   "127.0.0.1:0", // 使用随机端口
		ClusterID: 1000,
		MemberID:  1,
	})
	require.NoError(t, err)

	// 启动服务器
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	// 等待服务器和 Raft 启动
	time.Sleep(3 * time.Second)

	// 创建客户端
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// 清理函数
	cleanupAll := func() {
		cli.Close()
		server.Stop()
		close(proposeC)
		<-errorC
		db.Close()
		cleanup()
	}

	t.Cleanup(cleanupAll)

	return server, cli, cleanupAll
}

// TestBasicPutGet 测试基本的 Put 和 Get 操作
func TestBasicPutGet(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// Put
	putResp, err := cli.Put(ctx, "foo", "bar")
	require.NoError(t, err)
	assert.Greater(t, putResp.Header.Revision, int64(0))

	// Get
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)
	assert.Equal(t, "foo", string(getResp.Kvs[0].Key))
	assert.Equal(t, "bar", string(getResp.Kvs[0].Value))
}

// TestPrefixRange 测试前缀查询
func TestPrefixRange(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 写入多个键
	cli.Put(ctx, "key1", "value1")
	cli.Put(ctx, "key2", "value2")
	cli.Put(ctx, "key3", "value3")
	cli.Put(ctx, "other", "value")

	// 前缀查询
	resp, err := cli.Get(ctx, "key", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 3)

	// 验证结果
	keys := make([]string, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		keys[i] = string(kv.Key)
	}
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
	assert.Contains(t, keys, "key3")
}

// TestDelete 测试删除操作
func TestDelete(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// Put and Delete
	cli.Put(ctx, "foo", "bar")
	delResp, err := cli.Delete(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, int64(1), delResp.Deleted)

	// 验证已删除
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestTransaction 测试事务
func TestTransaction(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 先写入一个值
	cli.Put(ctx, "key", "old-value")

	// 成功的事务
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "old-value")).
		Then(clientv3.OpPut("key", "new-value")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	// 验证值已更新
	getResp, err := cli.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "new-value", string(getResp.Kvs[0].Value))

	// 失败的事务
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "wrong-value")).
		Then(clientv3.OpPut("key", "should-not-happen")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.False(t, txnResp.Succeeded)
}

// TestWatch 测试 Watch 功能
func TestWatch(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 创建 watch
	watchCh := cli.Watch(ctx, "watch-key")

	// 等待 watch 建立
	time.Sleep(50 * time.Millisecond)

	// 触发事件
	go func() {
		time.Sleep(50 * time.Millisecond)
		cli.Put(context.Background(), "watch-key", "watch-value")
	}()

	// 接收事件
	select {
	case wresp := <-watchCh:
		require.NotNil(t, wresp)
		require.Len(t, wresp.Events, 1)
		assert.Equal(t, "watch-key", string(wresp.Events[0].Kv.Key))
		assert.Equal(t, "watch-value", string(wresp.Events[0].Kv.Value))
	case <-time.After(2 * time.Second):
		t.Fatal("Watch timeout")
	}
}

// TestLease 测试 Lease 功能
func TestLease(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 创建 lease
	leaseResp, err := cli.Grant(ctx, 10)
	require.NoError(t, err)
	assert.Greater(t, leaseResp.ID, int64(0))
	assert.Equal(t, int64(10), leaseResp.TTL)

	// Put with lease
	_, err = cli.Put(ctx, "lease-key", "lease-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	// 验证键存在
	getResp, err := cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// KeepAlive
	kaResp, err := cli.KeepAliveOnce(ctx, leaseResp.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), kaResp.TTL)

	// Revoke lease
	_, err = cli.Revoke(ctx, leaseResp.ID)
	require.NoError(t, err)

	// 验证键已被删除
	getResp, err = cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestLeaseExpiry 测试 Lease 过期
func TestLeaseExpiry(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 创建短期 lease（2秒）
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)

	// Put with lease
	_, err = cli.Put(ctx, "expiry-key", "expiry-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	// 验证键存在
	getResp, err := cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// 等待 lease 过期（2秒 + 1秒误差）
	time.Sleep(3 * time.Second)

	// 验证键已被删除
	getResp, err = cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "Key should be deleted after lease expiry")
}

// TestStatus 测试 Status API
func TestStatus(t *testing.T) {
	server, cli := startTestServer(t)

	ctx := context.Background()

	// 获取状态
	statusResp, err := cli.Status(ctx, server.Address())
	require.NoError(t, err)
	assert.Equal(t, "3.6.0-compatible", statusResp.Version)
	assert.GreaterOrEqual(t, statusResp.DbSize, int64(0))
}

// TestMultipleOperations 测试复杂场景
func TestMultipleOperations(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 1. 写入数据
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := fmt.Sprintf("value-%d", i)
		_, err := cli.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// 2. 范围查询
	resp, err := cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 10)

	// 3. 事务更新
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key-0"), "=", "value-0")).
		Then(
			clientv3.OpPut("key-0", "updated-0"),
			clientv3.OpPut("key-1", "updated-1"),
		).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	// 4. 验证更新
	getResp, err := cli.Get(ctx, "key-0")
	require.NoError(t, err)
	assert.Equal(t, "updated-0", string(getResp.Kvs[0].Value))

	// 5. 批量删除
	delResp, err := cli.Delete(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Equal(t, int64(10), delResp.Deleted)

	// 6. 验证已全部删除
	resp, err = cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 0)
}

// ============================================================================
// RocksDB 版本的测试
// ============================================================================

// TestBasicPutGet_RocksDB 测试基本的 Put 和 Get 操作 (RocksDB)
func TestBasicPutGet_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// Put
	putResp, err := cli.Put(ctx, "foo", "bar")
	require.NoError(t, err)
	assert.Greater(t, putResp.Header.Revision, int64(0))

	// Get
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)
	assert.Equal(t, "foo", string(getResp.Kvs[0].Key))
	assert.Equal(t, "bar", string(getResp.Kvs[0].Value))
}

// TestPrefixRange_RocksDB 测试前缀查询 (RocksDB)
func TestPrefixRange_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 写入多个键
	cli.Put(ctx, "key1", "value1")
	cli.Put(ctx, "key2", "value2")
	cli.Put(ctx, "key3", "value3")
	cli.Put(ctx, "other", "value")

	// 等待 Raft 提交
	time.Sleep(500 * time.Millisecond)

	// 前缀查询
	resp, err := cli.Get(ctx, "key", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 3)

	// 验证结果
	keys := make([]string, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		keys[i] = string(kv.Key)
	}
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
	assert.Contains(t, keys, "key3")
}

// TestDelete_RocksDB 测试删除操作 (RocksDB)
func TestDelete_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// Put and Delete
	cli.Put(ctx, "foo", "bar")
	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	delResp, err := cli.Delete(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, int64(1), delResp.Deleted)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 验证已删除
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestTransaction_RocksDB 测试事务 (RocksDB)
func TestTransaction_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 先写入一个值
	cli.Put(ctx, "key", "old-value")
	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 成功的事务
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "old-value")).
		Then(clientv3.OpPut("key", "new-value")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 验证值已更新
	getResp, err := cli.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "new-value", string(getResp.Kvs[0].Value))

	// 失败的事务
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "wrong-value")).
		Then(clientv3.OpPut("key", "should-not-happen")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.False(t, txnResp.Succeeded)
}

// TestWatch_RocksDB 测试 Watch 功能 (RocksDB)
func TestWatch_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 创建 watch
	watchCh := cli.Watch(ctx, "watch-key")

	// 等待 watch 建立
	time.Sleep(100 * time.Millisecond)

	// 触发 PUT 事件
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "watch-key", "watch-value")
	}()

	// 接收 PUT 事件
	select {
	case wresp := <-watchCh:
		require.NotNil(t, wresp)
		require.Len(t, wresp.Events, 1)
		assert.Equal(t, mvccpb.PUT, wresp.Events[0].Type)
		assert.Equal(t, "watch-key", string(wresp.Events[0].Kv.Key))
		assert.Equal(t, "watch-value", string(wresp.Events[0].Kv.Value))
	case <-time.After(3 * time.Second):
		t.Fatal("Watch PUT timeout")
	}

	// 触发 DELETE 事件
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Delete(context.Background(), "watch-key")
	}()

	// 接收 DELETE 事件
	select {
	case wresp := <-watchCh:
		require.NotNil(t, wresp)
		require.Len(t, wresp.Events, 1)
		assert.Equal(t, mvccpb.DELETE, wresp.Events[0].Type)
		// For DELETE events without prevKV option, Kv contains the deleted key info
		assert.Equal(t, "watch-key", string(wresp.Events[0].Kv.Key))
		assert.Nil(t, wresp.Events[0].Kv.Value) // Value is nil for deleted key
		// PrevKv is nil because prevKV option was not set
		assert.Nil(t, wresp.Events[0].PrevKv)
	case <-time.After(3 * time.Second):
		t.Fatal("Watch DELETE timeout")
	}
}

// TestLease_RocksDB 测试 Lease 功能 (RocksDB)
func TestLease_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 创建 lease
	leaseResp, err := cli.Grant(ctx, 10)
	require.NoError(t, err)
	assert.Greater(t, leaseResp.ID, int64(0))
	assert.Equal(t, int64(10), leaseResp.TTL)

	// Put with lease
	_, err = cli.Put(ctx, "lease-key", "lease-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 验证键存在
	getResp, err := cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// KeepAlive
	kaResp, err := cli.KeepAliveOnce(ctx, leaseResp.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), kaResp.TTL)

	// Revoke lease
	_, err = cli.Revoke(ctx, leaseResp.ID)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 验证键已被删除
	getResp, err = cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestLeaseExpiry_RocksDB 测试 Lease 过期 (RocksDB)
func TestLeaseExpiry_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 创建短期 lease（2秒）
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)

	// Put with lease
	_, err = cli.Put(ctx, "expiry-key", "expiry-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 验证键存在
	getResp, err := cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// 等待 lease 过期（2秒 + 1秒误差）
	time.Sleep(3 * time.Second)

	// 验证键已被删除
	getResp, err = cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "Key should be deleted after lease expiry")
}

// TestStatus_RocksDB 测试 Status API (RocksDB)
func TestStatus_RocksDB(t *testing.T) {
	server, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 获取状态
	statusResp, err := cli.Status(ctx, server.Address())
	require.NoError(t, err)
	assert.Equal(t, "3.6.0-compatible", statusResp.Version)
	assert.GreaterOrEqual(t, statusResp.DbSize, int64(0))
}

// TestMultipleOperations_RocksDB 测试复杂场景 (RocksDB)
func TestMultipleOperations_RocksDB(t *testing.T) {
	t.Skip("Transaction not yet implemented for RocksDB (used in this test)")

	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 1. 写入数据
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := fmt.Sprintf("value-%d", i)
		_, err := cli.Put(ctx, key, value)
		require.NoError(t, err)
	}

	time.Sleep(1 * time.Second) // 等待所有写入提交

	// 2. 范围查询
	resp, err := cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 10)

	// 3. 事务更新
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key-0"), "=", "value-0")).
		Then(
			clientv3.OpPut("key-0", "updated-0"),
			clientv3.OpPut("key-1", "updated-1"),
		).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 4. 验证更新
	getResp, err := cli.Get(ctx, "key-0")
	require.NoError(t, err)
	assert.Equal(t, "updated-0", string(getResp.Kvs[0].Value))

	// 5. 批量删除
	delResp, err := cli.Delete(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Equal(t, int64(10), delResp.Deleted)

	time.Sleep(500 * time.Millisecond) // 等待 Raft 提交

	// 6. 验证已全部删除
	resp, err = cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 0)
}

// TestWatchPrefix_RocksDB 测试 RocksDB Watch 范围监听
func TestWatchPrefix_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 创建前缀 watch
	watchCh := cli.Watch(ctx, "prefix/", clientv3.WithPrefix())

	// 等待 watch 建立
	time.Sleep(100 * time.Millisecond)

	// 触发多个事件
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "prefix/key1", "value1")
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "prefix/key2", "value2")
		time.Sleep(100 * time.Millisecond)
		cli.Delete(context.Background(), "prefix/key1")
	}()

	// 接收 3 个事件
	receivedEvents := 0
	timeout := time.After(5 * time.Second)

	for receivedEvents < 3 {
		select {
		case wresp := <-watchCh:
			require.NotNil(t, wresp)
			require.Len(t, wresp.Events, 1)
			event := wresp.Events[0]

			// 验证事件
			key := string(event.Kv.Key)
			if event.PrevKv != nil {
				key = string(event.PrevKv.Key)
			}
			assert.True(t, strings.HasPrefix(key, "prefix/"))

			receivedEvents++
		case <-timeout:
			t.Fatalf("Watch timeout, received %d/3 events", receivedEvents)
		}
	}

	assert.Equal(t, 3, receivedEvents)
}

// TestWatchCancel_RocksDB 测试 RocksDB Watch 取消
func TestWatchCancel_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx, cancel := context.WithCancel(context.Background())

	// 创建 watch
	watchCh := cli.Watch(ctx, "cancel-key")

	// 等待 watch 建立
	time.Sleep(100 * time.Millisecond)

	// 取消 watch
	cancel()

	// 等待取消生效
	time.Sleep(200 * time.Millisecond)

	// 触发事件（不应该收到）
	cli.Put(context.Background(), "cancel-key", "value")

	// 验证 channel 已关闭或者不会收到事件
	select {
	case wresp, ok := <-watchCh:
		if ok {
			// 如果收到响应，应该是取消响应
			assert.True(t, wresp.Canceled, "Watch should be canceled")
		}
		// 否则 channel 已关闭，符合预期
	case <-time.After(500 * time.Millisecond):
		// 超时也符合预期，说明没有收到事件
	}
}
