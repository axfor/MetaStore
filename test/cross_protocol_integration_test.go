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
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	"metaStore/pkg/etcdapi"
	"metaStore/pkg/httpapi"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForRaftCommit waits for Raft to commit changes with retry
func waitForRaftCommit(t *testing.T, retries int, interval time.Duration, checkFunc func() bool) {
	for i := 0; i < retries; i++ {
		if checkFunc() {
			return
		}
		time.Sleep(interval)
	}
	t.Helper()
	// Final check after all retries
	assert.True(t, checkFunc(), "Condition not met after waiting")
}

// httpPut performs HTTP PUT request
func httpPut(t *testing.T, port int, key, value string) *http.Response {
	t.Helper()
	httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", port, key)
	req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// httpGet performs HTTP GET request
func httpGet(t *testing.T, port int, key string) (string, int) {
	t.Helper()
	httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", port, key)
	resp, err := http.Get(httpURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body), resp.StatusCode
}

// httpDelete performs HTTP DELETE request
func httpDelete(t *testing.T, port int, key string) int {
	t.Helper()
	httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", port, key)
	req, err := http.NewRequest("DELETE", httpURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// etcdPut performs etcd Put operation
func etcdPut(t *testing.T, client *clientv3.Client, ctx context.Context, key, value string) {
	t.Helper()
	_, err := client.Put(ctx, key, value)
	require.NoError(t, err)
}

// etcdGet performs etcd Get operation and returns the value
func etcdGet(t *testing.T, client *clientv3.Client, ctx context.Context, key string) (string, bool) {
	t.Helper()
	resp, err := client.Get(ctx, key)
	require.NoError(t, err)
	if len(resp.Kvs) == 0 {
		return "", false
	}
	return string(resp.Kvs[0].Value), true
}

// etcdDelete performs etcd Delete operation
func etcdDelete(t *testing.T, client *clientv3.Client, ctx context.Context, key string) {
	t.Helper()
	_, err := client.Delete(ctx, key)
	require.NoError(t, err)
}

// TestCrossProtocolMemoryDataInteroperability tests that data written via HTTP API
// can be read via etcd API, and vice versa (Memory engine)
func TestCrossProtocolMemoryDataInteroperability(t *testing.T) {
	// Setup: Create a single storage instance with both HTTP and etcd interfaces
	peers := []string{"http://127.0.0.1:10300"}

	// Clean up data directory
	os.RemoveAll("data/memory/1")

	proposeC := make(chan string, 1)
	confChangeC := make(chan raftpb.ConfChange, 1)

	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(1, peers, false, getSnapshot, proposeC, confChangeC, "memory")

	kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

	// Start HTTP API server
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpAddr := httpListener.Addr().String()
	httpListener.Close()

	httpPort := 0
	fmt.Sscanf(httpAddr, "127.0.0.1:%d", &httpPort)

	go func() {
		httpapi.ServeHTTPKVAPI(kvs, httpPort, confChangeC, errorC)
	}()

	// Start etcd gRPC server
	etcdListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	etcdAddr := etcdListener.Addr().String()
	etcdListener.Close()

	etcdServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   etcdAddr,
		ClusterID: 1000,
		MemberID:  1,
	})
	require.NoError(t, err)

	go func() {
		if err := etcdServer.Start(); err != nil {
			t.Logf("etcd server error: %v", err)
		}
	}()

	// Wait for servers to start and Raft to become leader
	time.Sleep(3 * time.Second)

	// Create etcd client
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	defer etcdClient.Close()

	ctx := context.Background()

	// Test 1: Write via HTTP API, read via etcd API
	t.Run("HTTP_Write_etcd_Read", func(t *testing.T) {
		key := "http-key-1"
		value := "http-value-1"

		// Write via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode) // HTTP PUT returns 204

		// Wait for Raft to commit
		time.Sleep(1 * time.Second)

		// Read via etcd API
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, etcdResp.Kvs, 1)
		assert.Equal(t, key, string(etcdResp.Kvs[0].Key))
		assert.Equal(t, value, string(etcdResp.Kvs[0].Value))

		t.Logf("✅ HTTP API write -> etcd API read: SUCCESS")
	})

	// Test 2: Write via etcd API, read via HTTP API
	t.Run("etcd_Write_HTTP_Read", func(t *testing.T) {
		key := "etcd-key-1"
		value := "etcd-value-1"

		// Write via etcd API
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)

		// Wait for Raft to commit
		time.Sleep(1 * time.Second)

		// Read via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		resp, err := http.Get(httpURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, value, string(body))

		t.Logf("✅ etcd API write -> HTTP API read: SUCCESS")
	})

	// Test 3: Multiple writes from both protocols
	t.Run("Mixed_Protocol_Writes", func(t *testing.T) {
		testData := map[string]struct {
			protocol string
			key      string
			value    string
		}{
			"http1": {"http", "mixed-http-1", "mixed-http-value-1"},
			"etcd1": {"etcd", "mixed-etcd-1", "mixed-etcd-value-1"},
			"http2": {"http", "mixed-http-2", "mixed-http-value-2"},
			"etcd2": {"etcd", "mixed-etcd-2", "mixed-etcd-value-2"},
		}

		// Write via both protocols
		for _, td := range testData {
			if td.protocol == "http" {
				httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, td.key)
				req, err := http.NewRequest("PUT", httpURL, strings.NewReader(td.value))
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				resp.Body.Close()
			} else {
				_, err := etcdClient.Put(ctx, td.key, td.value)
				require.NoError(t, err)
			}
		}

		// Wait for all writes to commit
		time.Sleep(2 * time.Second)

		// Verify all data is accessible from both protocols
		for _, td := range testData {
			// Read via etcd API
			etcdResp, err := etcdClient.Get(ctx, td.key)
			require.NoError(t, err)
			require.Len(t, etcdResp.Kvs, 1, "etcd should see key %s", td.key)
			assert.Equal(t, td.value, string(etcdResp.Kvs[0].Value),
				"etcd should read correct value for key %s", td.key)

			// Read via HTTP API
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, td.key)
			resp, err := http.Get(httpURL)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			assert.Equal(t, td.value, string(body),
				"HTTP should read correct value for key %s", td.key)
		}

		t.Logf("✅ Mixed protocol writes: All data accessible from both protocols")
	})

	// Test 4: etcd prefix query should see HTTP-written data
	t.Run("etcd_PrefixQuery_Sees_HTTP_Data", func(t *testing.T) {
		prefix := "prefix-test-"

		// Write 5 keys via HTTP API
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("%s%d", prefix, i)
			value := fmt.Sprintf("value-%d", i)
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
			req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
		}

		time.Sleep(2 * time.Second)

		// Query with prefix via etcd API
		etcdResp, err := etcdClient.Get(ctx, prefix, clientv3.WithPrefix())
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(etcdResp.Kvs), 5,
			"etcd prefix query should find all HTTP-written keys")

		t.Logf("✅ etcd prefix query found %d keys written via HTTP", len(etcdResp.Kvs))
	})

	// Test 5: Cross-protocol delete operations
	t.Run("HTTP_Delete_etcd_Verify", func(t *testing.T) {
		key := "delete-test-http"
		value := "to-be-deleted"

		// Write via etcd API
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Verify key exists via HTTP
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		resp, err := http.Get(httpURL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Delete via HTTP API
		req, err := http.NewRequest("DELETE", httpURL, nil)
		require.NoError(t, err)
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		time.Sleep(1 * time.Second)

		// Verify deletion via etcd API
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		assert.Len(t, etcdResp.Kvs, 0, "etcd should not find deleted key")

		t.Logf("✅ HTTP API delete -> etcd API verify deletion: SUCCESS")
	})

	// Test 6: etcd delete, HTTP verify
	t.Run("etcd_Delete_HTTP_Verify", func(t *testing.T) {
		key := "delete-test-etcd"
		value := "to-be-deleted-2"

		// Write via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(1 * time.Second)

		// Verify key exists via etcd
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, etcdResp.Kvs, 1)

		// Delete via etcd API
		_, err = etcdClient.Delete(ctx, key)
		require.NoError(t, err)

		time.Sleep(1 * time.Second)

		// Verify deletion via HTTP API
		resp, err = http.Get(httpURL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "HTTP should return 404 for deleted key")

		t.Logf("✅ etcd API delete -> HTTP API verify deletion: SUCCESS")
	})

	// Test 7: Range query cross-protocol
	t.Run("etcd_RangeQuery_Sees_HTTP_Data", func(t *testing.T) {
		keyPrefix := "range-test-"
		// Write sequential keys via HTTP
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("%s%02d", keyPrefix, i)
			value := fmt.Sprintf("range-value-%d", i)
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
			req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
		}

		time.Sleep(2 * time.Second)

		// Range query from range-test-03 to range-test-07 via etcd
		startKey := fmt.Sprintf("%s%02d", keyPrefix, 3)
		endKey := fmt.Sprintf("%s%02d", keyPrefix, 8) // exclusive
		etcdResp, err := etcdClient.Get(ctx, startKey, clientv3.WithRange(endKey))
		require.NoError(t, err)
		assert.Equal(t, 5, len(etcdResp.Kvs), "Should get keys from 03 to 07 (5 keys)")

		// Verify key order and values
		for i, kv := range etcdResp.Kvs {
			expectedKey := fmt.Sprintf("%s%02d", keyPrefix, i+3)
			expectedValue := fmt.Sprintf("range-value-%d", i+3)
			assert.Equal(t, expectedKey, string(kv.Key))
			assert.Equal(t, expectedValue, string(kv.Value))
		}

		t.Logf("✅ etcd range query correctly retrieved %d HTTP-written keys", len(etcdResp.Kvs))
	})

	// Test 8: Concurrent writes from both protocols
	t.Run("Concurrent_Mixed_Protocol_Writes", func(t *testing.T) {
		numGoroutines := 10
		writesPerGoroutine := 10
		done := make(chan bool, numGoroutines*2)

		// Concurrent HTTP writes
		for g := 0; g < numGoroutines; g++ {
			go func(goroutineID int) {
				for i := 0; i < writesPerGoroutine; i++ {
					key := fmt.Sprintf("concurrent-http-%d-%d", goroutineID, i)
					value := fmt.Sprintf("http-value-%d-%d", goroutineID, i)
					httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
					req, _ := http.NewRequest("PUT", httpURL, strings.NewReader(value))
					http.DefaultClient.Do(req)
				}
				done <- true
			}(g)
		}

		// Concurrent etcd writes
		for g := 0; g < numGoroutines; g++ {
			go func(goroutineID int) {
				for i := 0; i < writesPerGoroutine; i++ {
					key := fmt.Sprintf("concurrent-etcd-%d-%d", goroutineID, i)
					value := fmt.Sprintf("etcd-value-%d-%d", goroutineID, i)
					etcdClient.Put(ctx, key, value)
				}
				done <- true
			}(g)
		}

		// Wait for all goroutines to finish
		for i := 0; i < numGoroutines*2; i++ {
			<-done
		}

		time.Sleep(3 * time.Second)

		// Verify all HTTP writes can be read via etcd
		httpKeysFound := 0
		for g := 0; g < numGoroutines; g++ {
			for i := 0; i < writesPerGoroutine; i++ {
				key := fmt.Sprintf("concurrent-http-%d-%d", g, i)
				resp, err := etcdClient.Get(ctx, key)
				if err == nil && len(resp.Kvs) == 1 {
					httpKeysFound++
				}
			}
		}

		// Verify all etcd writes can be read via HTTP
		etcdKeysFound := 0
		for g := 0; g < numGoroutines; g++ {
			for i := 0; i < writesPerGoroutine; i++ {
				key := fmt.Sprintf("concurrent-etcd-%d-%d", g, i)
				httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
				resp, err := http.Get(httpURL)
				if err == nil && resp.StatusCode == http.StatusOK {
					etcdKeysFound++
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}

		t.Logf("✅ Concurrent writes: %d/%d HTTP keys readable via etcd, %d/%d etcd keys readable via HTTP",
			httpKeysFound, numGoroutines*writesPerGoroutine,
			etcdKeysFound, numGoroutines*writesPerGoroutine)

		assert.Equal(t, numGoroutines*writesPerGoroutine, httpKeysFound,
			"All HTTP writes should be readable via etcd")
		assert.Equal(t, numGoroutines*writesPerGoroutine, etcdKeysFound,
			"All etcd writes should be readable via HTTP")
	})

	// Cleanup
	close(proposeC)
	<-errorC
	etcdServer.Stop()
	os.RemoveAll("data/memory/1")
}

// TestCrossProtocolRocksDBDataInteroperability tests cross-protocol data access with RocksDB
func TestCrossProtocolRocksDBDataInteroperability(t *testing.T) {
	// Setup: Create a single storage instance with both HTTP and etcd interfaces
	peers := []string{"http://127.0.0.1:10301"}

	// Clean up data directory
	os.RemoveAll("data/rocksdb/1")

	proposeC := make(chan string, 1)
	confChangeC := make(chan raftpb.ConfChange, 1)

	// Open RocksDB
	dbPath := "data/rocksdb/1/kv"
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

	commitC, errorC, snapshotterReady, _ := raft.NewNodeRocksDB(1, peers, false, getSnapshot, proposeC, confChangeC, db)

	kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)

	// Start HTTP API server
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpAddr := httpListener.Addr().String()
	httpListener.Close()

	httpPort := 0
	fmt.Sscanf(httpAddr, "127.0.0.1:%d", &httpPort)

	go func() {
		httpapi.ServeHTTPKVAPI(kvs, httpPort, confChangeC, errorC)
	}()

	// Start etcd gRPC server
	etcdListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	etcdAddr := etcdListener.Addr().String()
	etcdListener.Close()

	etcdServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     kvs,
		Address:   etcdAddr,
		ClusterID: 2000,
		MemberID:  1,
	})
	require.NoError(t, err)

	go func() {
		if err := etcdServer.Start(); err != nil {
			t.Logf("etcd server error: %v", err)
		}
	}()

	// Wait for servers to start and Raft to become leader
	time.Sleep(3 * time.Second)

	// Create etcd client
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	defer etcdClient.Close()

	ctx := context.Background()

	// Test 1: Write via HTTP API, read via etcd API
	t.Run("HTTP_Write_etcd_Read", func(t *testing.T) {
		key := "http-key-1"
		value := "http-value-1"

		// Write via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode) // HTTP PUT returns 204

		// Wait for Raft to commit
		time.Sleep(1 * time.Second)

		// Read via etcd API
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, etcdResp.Kvs, 1)
		assert.Equal(t, key, string(etcdResp.Kvs[0].Key))
		assert.Equal(t, value, string(etcdResp.Kvs[0].Value))

		t.Logf("✅ HTTP API write -> etcd API read: SUCCESS")
	})

	// Test 2: Write via etcd API, read via HTTP API
	t.Run("etcd_Write_HTTP_Read", func(t *testing.T) {
		key := "etcd-key-1"
		value := "etcd-value-1"

		// Write via etcd API
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)

		// Wait for Raft to commit
		time.Sleep(1 * time.Second)

		// Read via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		resp, err := http.Get(httpURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, value, string(body))

		t.Logf("✅ etcd API write -> HTTP API read: SUCCESS")
	})

	// Test 3: Multiple writes from both protocols
	t.Run("Mixed_Protocol_Writes", func(t *testing.T) {
		testData := map[string]struct {
			protocol string
			key      string
			value    string
		}{
			"http1": {"http", "mixed-http-1", "mixed-http-value-1"},
			"etcd1": {"etcd", "mixed-etcd-1", "mixed-etcd-value-1"},
			"http2": {"http", "mixed-http-2", "mixed-http-value-2"},
			"etcd2": {"etcd", "mixed-etcd-2", "mixed-etcd-value-2"},
		}

		// Write via both protocols
		for _, td := range testData {
			if td.protocol == "http" {
				httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, td.key)
				req, err := http.NewRequest("PUT", httpURL, strings.NewReader(td.value))
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				resp.Body.Close()
			} else {
				_, err := etcdClient.Put(ctx, td.key, td.value)
				require.NoError(t, err)
			}
		}

		// Wait for all writes to commit
		time.Sleep(2 * time.Second)

		// Verify all data is accessible from both protocols
		for _, td := range testData {
			// Read via etcd API
			etcdResp, err := etcdClient.Get(ctx, td.key)
			require.NoError(t, err)
			require.Len(t, etcdResp.Kvs, 1, "etcd should see key %s", td.key)
			assert.Equal(t, td.value, string(etcdResp.Kvs[0].Value),
				"etcd should read correct value for key %s", td.key)

			// Read via HTTP API
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, td.key)
			resp, err := http.Get(httpURL)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			assert.Equal(t, td.value, string(body),
				"HTTP should read correct value for key %s", td.key)
		}

		t.Logf("✅ Mixed protocol writes: All data accessible from both protocols")
	})

	// Test 4: etcd prefix query should see HTTP-written data
	t.Run("etcd_PrefixQuery_Sees_HTTP_Data", func(t *testing.T) {
		prefix := "prefix-test-"

		// Write 5 keys via HTTP API
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("%s%d", prefix, i)
			value := fmt.Sprintf("value-%d", i)
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
			req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
		}

		time.Sleep(2 * time.Second)

		// Query with prefix via etcd API
		etcdResp, err := etcdClient.Get(ctx, prefix, clientv3.WithPrefix())
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(etcdResp.Kvs), 5,
			"etcd prefix query should find all HTTP-written keys")

		t.Logf("✅ etcd prefix query found %d keys written via HTTP", len(etcdResp.Kvs))
	})

	// Test 5: Cross-protocol delete operations
	t.Run("HTTP_Delete_etcd_Verify", func(t *testing.T) {
		key := "delete-test-http"
		value := "to-be-deleted"

		// Write via etcd API
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Verify key exists via HTTP
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		resp, err := http.Get(httpURL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Delete via HTTP API
		req, err := http.NewRequest("DELETE", httpURL, nil)
		require.NoError(t, err)
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		time.Sleep(1 * time.Second)

		// Verify deletion via etcd API
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		assert.Len(t, etcdResp.Kvs, 0, "etcd should not find deleted key")

		t.Logf("✅ HTTP API delete -> etcd API verify deletion: SUCCESS")
	})

	// Test 6: etcd delete, HTTP verify
	t.Run("etcd_Delete_HTTP_Verify", func(t *testing.T) {
		key := "delete-test-etcd"
		value := "to-be-deleted-2"

		// Write via HTTP API
		httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
		req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		time.Sleep(1 * time.Second)

		// Verify key exists via etcd
		etcdResp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, etcdResp.Kvs, 1)

		// Delete via etcd API
		_, err = etcdClient.Delete(ctx, key)
		require.NoError(t, err)

		time.Sleep(1 * time.Second)

		// Verify deletion via HTTP API
		resp, err = http.Get(httpURL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "HTTP should return 404 for deleted key")

		t.Logf("✅ etcd API delete -> HTTP API verify deletion: SUCCESS")
	})

	// Test 7: Range query cross-protocol
	t.Run("etcd_RangeQuery_Sees_HTTP_Data", func(t *testing.T) {
		keyPrefix := "range-test-"
		// Write sequential keys via HTTP
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("%s%02d", keyPrefix, i)
			value := fmt.Sprintf("range-value-%d", i)
			httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
			req, err := http.NewRequest("PUT", httpURL, strings.NewReader(value))
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
		}

		time.Sleep(2 * time.Second)

		// Range query from range-test-03 to range-test-07 via etcd
		startKey := fmt.Sprintf("%s%02d", keyPrefix, 3)
		endKey := fmt.Sprintf("%s%02d", keyPrefix, 8) // exclusive
		etcdResp, err := etcdClient.Get(ctx, startKey, clientv3.WithRange(endKey))
		require.NoError(t, err)
		assert.Equal(t, 5, len(etcdResp.Kvs), "Should get keys from 03 to 07 (5 keys)")

		// Verify key order and values
		for i, kv := range etcdResp.Kvs {
			expectedKey := fmt.Sprintf("%s%02d", keyPrefix, i+3)
			expectedValue := fmt.Sprintf("range-value-%d", i+3)
			assert.Equal(t, expectedKey, string(kv.Key))
			assert.Equal(t, expectedValue, string(kv.Value))
		}

		t.Logf("✅ etcd range query correctly retrieved %d HTTP-written keys", len(etcdResp.Kvs))
	})

	// Test 8: Concurrent writes from both protocols
	t.Run("Concurrent_Mixed_Protocol_Writes", func(t *testing.T) {
		numGoroutines := 10
		writesPerGoroutine := 10
		done := make(chan bool, numGoroutines*2)

		// Concurrent HTTP writes
		for g := 0; g < numGoroutines; g++ {
			go func(goroutineID int) {
				for i := 0; i < writesPerGoroutine; i++ {
					key := fmt.Sprintf("concurrent-http-%d-%d", goroutineID, i)
					value := fmt.Sprintf("http-value-%d-%d", goroutineID, i)
					httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
					req, _ := http.NewRequest("PUT", httpURL, strings.NewReader(value))
					http.DefaultClient.Do(req)
				}
				done <- true
			}(g)
		}

		// Concurrent etcd writes
		for g := 0; g < numGoroutines; g++ {
			go func(goroutineID int) {
				for i := 0; i < writesPerGoroutine; i++ {
					key := fmt.Sprintf("concurrent-etcd-%d-%d", goroutineID, i)
					value := fmt.Sprintf("etcd-value-%d-%d", goroutineID, i)
					etcdClient.Put(ctx, key, value)
				}
				done <- true
			}(g)
		}

		// Wait for all goroutines to finish
		for i := 0; i < numGoroutines*2; i++ {
			<-done
		}

		time.Sleep(3 * time.Second)

		// Verify all HTTP writes can be read via etcd
		httpKeysFound := 0
		for g := 0; g < numGoroutines; g++ {
			for i := 0; i < writesPerGoroutine; i++ {
				key := fmt.Sprintf("concurrent-http-%d-%d", g, i)
				resp, err := etcdClient.Get(ctx, key)
				if err == nil && len(resp.Kvs) == 1 {
					httpKeysFound++
				}
			}
		}

		// Verify all etcd writes can be read via HTTP
		etcdKeysFound := 0
		for g := 0; g < numGoroutines; g++ {
			for i := 0; i < writesPerGoroutine; i++ {
				key := fmt.Sprintf("concurrent-etcd-%d-%d", g, i)
				httpURL := fmt.Sprintf("http://127.0.0.1:%d/%s", httpPort, key)
				resp, err := http.Get(httpURL)
				if err == nil && resp.StatusCode == http.StatusOK {
					etcdKeysFound++
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}

		t.Logf("✅ Concurrent writes: %d/%d HTTP keys readable via etcd, %d/%d etcd keys readable via HTTP",
			httpKeysFound, numGoroutines*writesPerGoroutine,
			etcdKeysFound, numGoroutines*writesPerGoroutine)

		assert.Equal(t, numGoroutines*writesPerGoroutine, httpKeysFound,
			"All HTTP writes should be readable via etcd")
		assert.Equal(t, numGoroutines*writesPerGoroutine, etcdKeysFound,
			"All etcd writes should be readable via HTTP")
	})

	// Cleanup
	close(proposeC)
	<-errorC
	etcdServer.Stop()
	db.Close()
	os.RemoveAll("data/rocksdb/1")
}

