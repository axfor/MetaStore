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
	"sync"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	etcdapi "metaStore/api/etcd"

	clientv3 "go.etcd.io/etcd/client/v3"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// startTestServer starttestserver
func startTestServer(t *testing.T) (*etcdapi.Server, *clientv3.Client) {
	// creatememorystorage
	store := memory.NewMemoryEtcd()

	// create etcd compatibleserver(randomport)
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     store,
		Address:   "127.0.0.1:0", // userandomport
		ClusterID: 1,
		MemberID:  1,
	})
	require.NoError(t, err)

	// startserver
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	// waitserverstart
	time.Sleep(100 * time.Millisecond)

	// createclient
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// clean upfunction
	t.Cleanup(func() {
		cli.Close()
		server.Stop()
	})

	return server, cli
}

// startTestServerRocksDB start RocksDB testserver(singlenode Raft)
func startTestServerRocksDB(t *testing.T) (*etcdapi.Server, *clientv3.Client, func()) {
	// singlenode Raft clustermustuse nodeID=1，peers arrayfirstelementtoshould ID 1
	nodeID := 1

	// NewNodeRocksDB use data/rocksdb/{id} directory
	dataDir := fmt.Sprintf("data/rocksdb/%d", nodeID)

	// clean upfunction
	cleanup := func() {
		os.RemoveAll(dataDir)
	}

	// clean up
	t.Cleanup(cleanup)

	// Setup RocksDB
	// Allocate dynamic ports to avoid conflicts when running tests in parallel
	peers, listeners := allocatePorts(1)
	releaseListeners(listeners)
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

	commitC, errorC, snapshotterReady, _ := raft.NewNodeRocksDB(nodeID, peers, false, getSnapshot, proposeC, confChangeC, db, dataDir, NewTestConfig(1, 1, ":2379"))
	kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)

	// create etcd compatibleserver(randomport)
	server, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   "127.0.0.1:0", // userandomport
		ClusterID: 1000,
		MemberID:  1,
	})
	require.NoError(t, err)

	// startserver
	go func() {
		if err := server.Start(); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	// waitserverand Raft start
	time.Sleep(3 * time.Second)

	// createclient
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{server.Address()},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// clean upfunction - usesync.Onceduplicateclosechannel
	var cleanupOnce sync.Once
	cleanupAll := func() {
		cleanupOnce.Do(func() {
			cli.Close()
			server.Stop()
			close(proposeC) // insafe，willbecallfirst time
			<-errorC
			db.Close()
			cleanup()
		})
	}

	t.Cleanup(cleanupAll)

	return server, cli, cleanupAll
}

// TestBasicPutGet testbasic Put and Get operation
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

// TestPrefixRange testprefixquery
func TestPrefixRange(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// writemany key
	cli.Put(ctx, "key1", "value1")
	cli.Put(ctx, "key2", "value2")
	cli.Put(ctx, "key3", "value3")
	cli.Put(ctx, "other", "value")

	// prefixquery
	resp, err := cli.Get(ctx, "key", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 3)

	// verifyresult
	keys := make([]string, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		keys[i] = string(kv.Key)
	}
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
	assert.Contains(t, keys, "key3")
}

// TestDelete testdeleteoperation
func TestDelete(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// Put and Delete
	cli.Put(ctx, "foo", "bar")
	delResp, err := cli.Delete(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, int64(1), delResp.Deleted)

	// verifyalready delete
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestTransaction testtransaction
func TestTransaction(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// writefirst value
	cli.Put(ctx, "key", "old-value")

	// successtransaction
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "old-value")).
		Then(clientv3.OpPut("key", "new-value")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	// verifyvaluealready update
	getResp, err := cli.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "new-value", string(getResp.Kvs[0].Value))

	// failuretransaction
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "wrong-value")).
		Then(clientv3.OpPut("key", "should-not-happen")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.False(t, txnResp.Succeeded)
}

// TestWatch test Watch feature
func TestWatch(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// create watch
	watchCh := cli.Watch(ctx, "watch-key")

	// wait watch build
	time.Sleep(50 * time.Millisecond)

	// triggerevent
	go func() {
		time.Sleep(50 * time.Millisecond)
		cli.Put(context.Background(), "watch-key", "watch-value")
	}()

	// receiveevent
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

// TestLease test Lease feature
func TestLease(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// create lease
	leaseResp, err := cli.Grant(ctx, 10)
	require.NoError(t, err)
	assert.Greater(t, leaseResp.ID, int64(0))
	assert.Equal(t, int64(10), leaseResp.TTL)

	// Put with lease
	_, err = cli.Put(ctx, "lease-key", "lease-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	// verifykeyin
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

	// verifykeywas delete
	getResp, err = cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestLeaseExpiry test Lease expiration
func TestLeaseExpiry(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// createshort lease(2seconds)
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)

	// Put with lease
	_, err = cli.Put(ctx, "expiry-key", "expiry-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	// verifykeyin
	getResp, err := cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// wait lease expiration(2seconds + 1secondserror)
	time.Sleep(3 * time.Second)

	// verifykeywas delete
	getResp, err = cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "Key should be deleted after lease expiry")
}

// TestStatus test Status API
func TestStatus(t *testing.T) {
	server, cli := startTestServer(t)

	ctx := context.Background()

	// getstatus
	statusResp, err := cli.Status(ctx, server.Address())
	require.NoError(t, err)
	assert.Equal(t, "3.6.0-compatible", statusResp.Version)
	assert.GreaterOrEqual(t, statusResp.DbSize, int64(0))
}

// TestMultipleOperations testscenarioscene
func TestMultipleOperations(t *testing.T) {
	_, cli := startTestServer(t)

	ctx := context.Background()

	// 1. writedata
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := fmt.Sprintf("value-%d", i)
		_, err := cli.Put(ctx, key, value)
		require.NoError(t, err)
	}

	// 2. rangequery
	resp, err := cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 10)

	// 3. transactionupdate
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key-0"), "=", "value-0")).
		Then(
			clientv3.OpPut("key-0", "updated-0"),
			clientv3.OpPut("key-1", "updated-1"),
		).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	// 4. verifyupdate
	getResp, err := cli.Get(ctx, "key-0")
	require.NoError(t, err)
	assert.Equal(t, "updated-0", string(getResp.Kvs[0].Value))

	// 5. delete
	delResp, err := cli.Delete(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Equal(t, int64(10), delResp.Deleted)

	// 6. verifyalready alldelete
	resp, err = cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 0)
}

// ============================================================================
// RocksDB versiontest
// ============================================================================

// TestBasicPutGet_RocksDB testbasic Put and Get operation (RocksDB)
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

// TestPrefixRange_RocksDB testprefixquery (RocksDB)
func TestPrefixRange_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// writemany key
	cli.Put(ctx, "key1", "value1")
	cli.Put(ctx, "key2", "value2")
	cli.Put(ctx, "key3", "value3")
	cli.Put(ctx, "other", "value")

	// wait Raft commit
	time.Sleep(500 * time.Millisecond)

	// prefixquery
	resp, err := cli.Get(ctx, "key", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 3)

	// verifyresult
	keys := make([]string, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		keys[i] = string(kv.Key)
	}
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
	assert.Contains(t, keys, "key3")
}

// TestDelete_RocksDB testdeleteoperation (RocksDB)
func TestDelete_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// Put and Delete
	cli.Put(ctx, "foo", "bar")
	time.Sleep(500 * time.Millisecond) // wait Raft commit

	delResp, err := cli.Delete(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, int64(1), delResp.Deleted)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// verifyalready delete
	getResp, err := cli.Get(ctx, "foo")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestTransaction_RocksDB testtransaction (RocksDB)
func TestTransaction_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// writefirst value
	cli.Put(ctx, "key", "old-value")
	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// successtransaction
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "old-value")).
		Then(clientv3.OpPut("key", "new-value")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// verifyvaluealready update
	getResp, err := cli.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "new-value", string(getResp.Kvs[0].Value))

	// failuretransaction
	txnResp, err = cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key"), "=", "wrong-value")).
		Then(clientv3.OpPut("key", "should-not-happen")).
		Else(clientv3.OpGet("key")).
		Commit()
	require.NoError(t, err)
	assert.False(t, txnResp.Succeeded)
}

// TestWatch_RocksDB test Watch feature (RocksDB)
func TestWatch_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// create watch
	watchCh := cli.Watch(ctx, "watch-key")

	// wait watch build
	time.Sleep(100 * time.Millisecond)

	// trigger PUT event
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "watch-key", "watch-value")
	}()

	// receive PUT event
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

	// trigger DELETE event
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Delete(context.Background(), "watch-key")
	}()

	// receive DELETE event
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

// TestLease_RocksDB test Lease feature (RocksDB)
func TestLease_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// create lease
	leaseResp, err := cli.Grant(ctx, 10)
	require.NoError(t, err)
	assert.Greater(t, leaseResp.ID, int64(0))
	assert.Equal(t, int64(10), leaseResp.TTL)

	// Put with lease
	_, err = cli.Put(ctx, "lease-key", "lease-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// verifykeyin
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

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// verifykeywas delete
	getResp, err = cli.Get(ctx, "lease-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0)
}

// TestLeaseExpiry_RocksDB test Lease expiration (RocksDB)
func TestLeaseExpiry_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// createshort lease(2seconds)
	leaseResp, err := cli.Grant(ctx, 2)
	require.NoError(t, err)

	// Put with lease
	_, err = cli.Put(ctx, "expiry-key", "expiry-value", clientv3.WithLease(leaseResp.ID))
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// verifykeyin
	getResp, err := cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 1)

	// wait lease expiration(2seconds + 1secondserror)
	time.Sleep(3 * time.Second)

	// verifykeywas delete
	getResp, err = cli.Get(ctx, "expiry-key")
	require.NoError(t, err)
	assert.Len(t, getResp.Kvs, 0, "Key should be deleted after lease expiry")
}

// TestStatus_RocksDB test Status API (RocksDB)
func TestStatus_RocksDB(t *testing.T) {
	server, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// getstatus
	statusResp, err := cli.Status(ctx, server.Address())
	require.NoError(t, err)
	assert.Equal(t, "3.6.0-compatible", statusResp.Version)
	assert.GreaterOrEqual(t, statusResp.DbSize, int64(0))
}

// TestMultipleOperations_RocksDB testscenarioscene (RocksDB)
func TestMultipleOperations_RocksDB(t *testing.T) {
	t.Skip("Transaction not yet implemented for RocksDB (used in this test)")

	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// 1. writedata
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key-%d", i)
		value := fmt.Sprintf("value-%d", i)
		_, err := cli.Put(ctx, key, value)
		require.NoError(t, err)
	}

	time.Sleep(1 * time.Second) // wait allwritecommit

	// 2. rangequery
	resp, err := cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 10)

	// 3. transactionupdate
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("key-0"), "=", "value-0")).
		Then(
			clientv3.OpPut("key-0", "updated-0"),
			clientv3.OpPut("key-1", "updated-1"),
		).
		Commit()
	require.NoError(t, err)
	assert.True(t, txnResp.Succeeded)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// 4. verifyupdate
	getResp, err := cli.Get(ctx, "key-0")
	require.NoError(t, err)
	assert.Equal(t, "updated-0", string(getResp.Kvs[0].Value))

	// 5. delete
	delResp, err := cli.Delete(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Equal(t, int64(10), delResp.Deleted)

	time.Sleep(500 * time.Millisecond) // wait Raft commit

	// 6. verifyalready alldelete
	resp, err = cli.Get(ctx, "key-", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Len(t, resp.Kvs, 0)
}

// TestWatchPrefix_RocksDB test RocksDB Watch rangelisten
func TestWatchPrefix_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx := context.Background()

	// createprefix watch
	watchCh := cli.Watch(ctx, "prefix/", clientv3.WithPrefix())

	// wait watch build
	time.Sleep(100 * time.Millisecond)

	// triggermany event
	go func() {
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "prefix/key1", "value1")
		time.Sleep(100 * time.Millisecond)
		cli.Put(context.Background(), "prefix/key2", "value2")
		time.Sleep(100 * time.Millisecond)
		cli.Delete(context.Background(), "prefix/key1")
	}()

	// receive 3  event
	receivedEvents := 0
	timeout := time.After(5 * time.Second)

	for receivedEvents < 3 {
		select {
		case wresp := <-watchCh:
			require.NotNil(t, wresp)
			require.Len(t, wresp.Events, 1)
			event := wresp.Events[0]

			// verifyevent
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

// TestWatchCancel_RocksDB test RocksDB Watch cancel
func TestWatchCancel_RocksDB(t *testing.T) {
	_, cli, _ := startTestServerRocksDB(t)

	ctx, cancel := context.WithCancel(context.Background())

	// create watch
	watchCh := cli.Watch(ctx, "cancel-key")

	// wait watch build
	time.Sleep(100 * time.Millisecond)

	// cancel watch
	cancel()

	// waitcancel
	time.Sleep(200 * time.Millisecond)

	// triggerevent(notshouldcollectto)
	cli.Put(context.Background(), "cancel-key", "value")

	// verify channel already closeornotwillcollecttoevent
	select {
	case wresp, ok := <-watchCh:
		if ok {
			// ifcollecttoresponse，shouldiscancelresponse
			assert.True(t, wresp.Canceled, "Watch should be canceled")
		}
		// else channel already close，merge
	case <-time.After(500 * time.Millisecond):
		// timeoutmerge，descriptionnonecollecttoevent
	}
}
