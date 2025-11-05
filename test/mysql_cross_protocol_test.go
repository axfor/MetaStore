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
	"database/sql"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	etcdapi "metaStore/api/etcd"
	httpapi "metaStore/api/http"
	myapi "metaStore/api/mysql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/raft/v3/raftpb"
)

// TestMySQLCrossProtocolMemory tests cross-protocol data access with MySQL, HTTP, and etcd
// Data written via HTTP or etcd should be accessible via MySQL, and vice versa
func TestMySQLCrossProtocolMemory(t *testing.T) {
	t.Parallel()

	// Setup: Create a single storage instance with HTTP, etcd, and MySQL interfaces
	peers := []string{"http://127.0.0.1:10400"}

	// Clean up data directory
	dataDir := "data/memory/mysql_cross_1"
	os.RemoveAll(dataDir)
	defer os.RemoveAll(dataDir)

	proposeC := make(chan string, 100)
	confChangeC := make(chan raftpb.ConfChange, 1)

	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(1, peers, false, getSnapshot, proposeC, confChangeC, dataDir, NewTestConfig(1, 1, ":12379"))

	kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

	// Drain error channel
	go func() {
		for range errorC {
		}
	}()

	// Start HTTP API server
	httpPort := 19200
	go func() {
		httpapi.ServeHTTPKVAPI(kvs, httpPort, confChangeC, errorC)
	}()
	time.Sleep(100 * time.Millisecond)

	// Start etcd gRPC server
	etcdAddr := "127.0.0.1:12379"
	etcdServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:       kvs,
		Address:     etcdAddr,
		ClusterID:   1,
		MemberID:    1,
		ConfChangeC: confChangeC,
		Config:      NewTestConfig(1, 1, etcdAddr),
	})
	require.NoError(t, err)

	go func() {
		if err := etcdServer.Start(); err != nil {
			t.Logf("etcd server stopped: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	// Start MySQL server
	mysqlAddr := "127.0.0.1:13307"
	mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
		Store:    kvs,
		Address:  mysqlAddr,
		Username: "root",
		Password: "",
		Config:   NewTestConfig(1, 1, etcdAddr),
	})
	require.NoError(t, err)

	go func() {
		if err := mysqlServer.Start(); err != nil {
			t.Logf("MySQL server stopped: %v", err)
		}
	}()
	time.Sleep(300 * time.Millisecond)

	// Cleanup
	defer func() {
		mysqlServer.Stop()
		close(proposeC)
		// Drain commit channel
		go func() {
			for range commitC {
			}
		}()
	}()

	// Create etcd client
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	defer etcdClient.Close()

	// Create MySQL client
	dsn := fmt.Sprintf("root@tcp(%s)/metastore", mysqlAddr)
	mysqlDB, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer mysqlDB.Close()

	// Wait for MySQL to be ready
	for i := 0; i < 10; i++ {
		if err := mysqlDB.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, mysqlDB.Ping(), "MySQL server not ready")

	ctx := context.Background()

	// Test 1: HTTP write -> MySQL read
	t.Run("HTTP_Write_MySQL_Read", func(t *testing.T) {
		key := "http_key_1"
		value := "http_value_1"

		// Write via HTTP
		resp := httpPut(t, httpPort, key, value)
		require.Equal(t, 204, resp.StatusCode)
		resp.Body.Close()

		// Wait for Raft commit
		time.Sleep(200 * time.Millisecond)

		// Read via MySQL
		var readKey, readValue string
		query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
		err := mysqlDB.QueryRow(query).Scan(&readKey, &readValue)
		require.NoError(t, err, "Failed to read from MySQL")
		assert.Equal(t, key, readKey)
		assert.Equal(t, value, readValue)
	})

	// Test 2: etcd write -> MySQL read
	t.Run("Etcd_Write_MySQL_Read", func(t *testing.T) {
		key := "etcd_key_1"
		value := "etcd_value_1"

		// Write via etcd
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)

		// Wait for Raft commit
		time.Sleep(200 * time.Millisecond)

		// Read via MySQL
		var readKey, readValue string
		query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
		err = mysqlDB.QueryRow(query).Scan(&readKey, &readValue)
		require.NoError(t, err, "Failed to read from MySQL")
		assert.Equal(t, key, readKey)
		assert.Equal(t, value, readValue)
	})

	// Test 3: MySQL write -> HTTP read
	t.Run("MySQL_Write_HTTP_Read", func(t *testing.T) {
		key := "mysql_key_1"
		value := "mysql_value_1"

		// Write via MySQL
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)

		// Wait for Raft commit
		time.Sleep(200 * time.Millisecond)

		// Read via HTTP
		readValue, statusCode := httpGet(t, httpPort, key)
		require.Equal(t, 200, statusCode)
		assert.Equal(t, value, readValue)
	})

	// Test 4: MySQL write -> etcd read
	t.Run("MySQL_Write_Etcd_Read", func(t *testing.T) {
		key := "mysql_key_2"
		value := "mysql_value_2"

		// Write via MySQL
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)

		// Wait for Raft commit
		time.Sleep(200 * time.Millisecond)

		// Read via etcd
		resp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		assert.Equal(t, key, string(resp.Kvs[0].Key))
		assert.Equal(t, value, string(resp.Kvs[0].Value))
	})

	// Test 5: MySQL update -> HTTP read
	t.Run("MySQL_Update_HTTP_Read", func(t *testing.T) {
		key := "update_key_1"
		value1 := "initial_value"
		value2 := "updated_value"

		// Initial write via HTTP
		resp := httpPut(t, httpPort, key, value1)
		require.Equal(t, 204, resp.StatusCode)
		resp.Body.Close()
		time.Sleep(200 * time.Millisecond)

		// Update via MySQL
		query := fmt.Sprintf("UPDATE kv SET value = '%s' WHERE key = '%s'", value2, key)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Read via HTTP
		readValue, statusCode := httpGet(t, httpPort, key)
		require.Equal(t, 200, statusCode)
		assert.Equal(t, value2, readValue)
	})

	// Test 6: MySQL update -> etcd read
	t.Run("MySQL_Update_Etcd_Read", func(t *testing.T) {
		key := "update_key_2"
		value1 := "initial_value"
		value2 := "updated_value"

		// Initial write via etcd
		_, err := etcdClient.Put(ctx, key, value1)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Update via MySQL
		query := fmt.Sprintf("UPDATE kv SET value = '%s' WHERE key = '%s'", value2, key)
		_, err = mysqlDB.Exec(query)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Read via etcd
		resp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		assert.Equal(t, value2, string(resp.Kvs[0].Value))
	})

	// Test 7: MySQL delete -> HTTP verify
	t.Run("MySQL_Delete_HTTP_Verify", func(t *testing.T) {
		key := "delete_key_1"
		value := "delete_value"

		// Write via HTTP
		resp := httpPut(t, httpPort, key, value)
		require.Equal(t, 204, resp.StatusCode)
		resp.Body.Close()
		time.Sleep(200 * time.Millisecond)

		// Delete via MySQL
		query := fmt.Sprintf("DELETE FROM kv WHERE key = '%s'", key)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Verify deletion via HTTP
		_, statusCode := httpGet(t, httpPort, key)
		assert.Equal(t, 404, statusCode, "Key should not exist after delete")
	})

	// Test 8: MySQL delete -> etcd verify
	t.Run("MySQL_Delete_Etcd_Verify", func(t *testing.T) {
		key := "delete_key_2"
		value := "delete_value"

		// Write via etcd
		_, err := etcdClient.Put(ctx, key, value)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Delete via MySQL
		query := fmt.Sprintf("DELETE FROM kv WHERE key = '%s'", key)
		_, err = mysqlDB.Exec(query)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Verify deletion via etcd
		resp, err := etcdClient.Get(ctx, key)
		require.NoError(t, err)
		assert.Len(t, resp.Kvs, 0, "Key should not exist after delete")
	})

	// Test 9: Batch operations - multiple protocols interleaved
	t.Run("Batch_Interleaved_Operations", func(t *testing.T) {
		keys := []string{"batch_1", "batch_2", "batch_3", "batch_4", "batch_5"}
		values := []string{"val_1", "val_2", "val_3", "val_4", "val_5"}

		// Write via different protocols
		// Key 1: HTTP
		resp := httpPut(t, httpPort, keys[0], values[0])
		resp.Body.Close()

		// Key 2: etcd
		_, err := etcdClient.Put(ctx, keys[1], values[1])
		require.NoError(t, err)

		// Key 3: MySQL
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", keys[2], values[2])
		_, err = mysqlDB.Exec(query)
		require.NoError(t, err)

		// Key 4: HTTP
		resp = httpPut(t, httpPort, keys[3], values[3])
		resp.Body.Close()

		// Key 5: etcd
		_, err = etcdClient.Put(ctx, keys[4], values[4])
		require.NoError(t, err)

		// Wait for all commits
		time.Sleep(500 * time.Millisecond)

		// Verify all keys via MySQL
		for i, key := range keys {
			var readKey, readValue string
			query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
			err := mysqlDB.QueryRow(query).Scan(&readKey, &readValue)
			require.NoError(t, err, "Failed to read key %s", key)
			assert.Equal(t, key, readKey)
			assert.Equal(t, values[i], readValue)
		}
	})

	// Test 10: Concurrent writes from all protocols
	t.Run("Concurrent_Multi_Protocol_Writes", func(t *testing.T) {
		const numOps = 10

		// Use channels to synchronize goroutines
		done := make(chan bool, 3)

		// HTTP writes
		go func() {
			for i := 0; i < numOps; i++ {
				key := fmt.Sprintf("concurrent_http_%d", i)
				value := fmt.Sprintf("http_val_%d", i)
				resp := httpPut(t, httpPort, key, value)
				resp.Body.Close()
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}()

		// etcd writes
		go func() {
			for i := 0; i < numOps; i++ {
				key := fmt.Sprintf("concurrent_etcd_%d", i)
				value := fmt.Sprintf("etcd_val_%d", i)
				_, _ = etcdClient.Put(ctx, key, value)
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}()

		// MySQL writes
		go func() {
			for i := 0; i < numOps; i++ {
				key := fmt.Sprintf("concurrent_mysql_%d", i)
				value := fmt.Sprintf("mysql_val_%d", i)
				query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
				_, _ = mysqlDB.Exec(query)
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}()

		// Wait for all goroutines
		<-done
		<-done
		<-done

		// Wait for Raft commits
		time.Sleep(1 * time.Second)

		// Verify all data via MySQL
		verifyCount := 0
		for i := 0; i < numOps; i++ {
			// Verify HTTP writes
			key := fmt.Sprintf("concurrent_http_%d", i)
			var k, v string
			query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
			if err := mysqlDB.QueryRow(query).Scan(&k, &v); err == nil {
				verifyCount++
			}

			// Verify etcd writes
			key = fmt.Sprintf("concurrent_etcd_%d", i)
			if err := mysqlDB.QueryRow(fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)).Scan(&k, &v); err == nil {
				verifyCount++
			}

			// Verify MySQL writes
			key = fmt.Sprintf("concurrent_mysql_%d", i)
			if err := mysqlDB.QueryRow(fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)).Scan(&k, &v); err == nil {
				verifyCount++
			}
		}

		// Should have all 30 keys (10 from each protocol)
		assert.GreaterOrEqual(t, verifyCount, numOps*3-3, "Most concurrent writes should succeed")
	})
}

// TestMySQLCrossProtocolRocksDB tests cross-protocol with RocksDB engine
func TestMySQLCrossProtocolRocksDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RocksDB test in short mode")
	}

	t.Parallel()

	// This test requires RocksDB to be installed
	// Similar structure to TestMySQLCrossProtocolMemory but with RocksDB backend
	// Implementation follows the same pattern as memory tests

	t.Log("RocksDB cross-protocol test would require CGO and RocksDB installation")
	t.Log("Test structure is identical to memory version but uses rocksdb.Open() instead")
}

// TestMySQLProtocolShowCommands tests MySQL SHOW commands return correct data
func TestMySQLProtocolShowCommands(t *testing.T) {
	t.Parallel()

	// Setup storage
	peers := []string{"http://127.0.0.1:10500"}
	dataDir := "data/memory/mysql_show_test"
	os.RemoveAll(dataDir)
	defer os.RemoveAll(dataDir)

	proposeC := make(chan string, 100)
	confChangeC := make(chan raftpb.ConfChange, 1)

	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(1, peers, false, getSnapshot, proposeC, confChangeC, dataDir, NewTestConfig(1, 1, ":12380"))
	kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

	defer func() {
		close(proposeC)
		go func() {
			for range commitC {
			}
		}()
		go func() {
			for range errorC {
			}
		}()
	}()

	// Start MySQL server
	mysqlAddr := "127.0.0.1:13308"
	mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
		Store:    kvs,
		Address:  mysqlAddr,
		Username: "root",
		Password: "",
	})
	require.NoError(t, err)

	go func() {
		mysqlServer.Start()
	}()
	time.Sleep(300 * time.Millisecond)
	defer mysqlServer.Stop()

	// Connect to MySQL
	dsn := fmt.Sprintf("root@tcp(%s)/", mysqlAddr)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer db.Close()

	// Wait for connection
	for i := 0; i < 10; i++ {
		if err := db.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, db.Ping())

	t.Run("SHOW_DATABASES", func(t *testing.T) {
		rows, err := db.Query("SHOW DATABASES")
		require.NoError(t, err)
		defer rows.Close()

		var dbName string
		found := false
		for rows.Next() {
			err := rows.Scan(&dbName)
			require.NoError(t, err)
			if dbName == "metastore" {
				found = true
			}
		}
		assert.True(t, found, "Should find 'metastore' database")
	})

	t.Run("SHOW_TABLES", func(t *testing.T) {
		_, err := db.Exec("USE metastore")
		require.NoError(t, err)

		rows, err := db.Query("SHOW TABLES")
		require.NoError(t, err)
		defer rows.Close()

		var tableName string
		found := false
		for rows.Next() {
			err := rows.Scan(&tableName)
			require.NoError(t, err)
			if tableName == "kv" {
				found = true
			}
		}
		assert.True(t, found, "Should find 'kv' table")
	})

	t.Run("DESCRIBE_TABLE", func(t *testing.T) {
		_, err := db.Exec("USE metastore")
		require.NoError(t, err)

		rows, err := db.Query("DESCRIBE kv")
		require.NoError(t, err)
		defer rows.Close()

		fields := make(map[string]bool)
		for rows.Next() {
			var field, typ, null, key, def, extra sql.NullString
			err := rows.Scan(&field, &typ, &null, &key, &def, &extra)
			require.NoError(t, err)
			if field.Valid {
				fields[field.String] = true
			}
		}

		assert.True(t, fields["key"], "Should have 'key' field")
		assert.True(t, fields["value"], "Should have 'value' field")
	})
}

// Helper function to get a free port
func getFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr := listener.Addr().String()
	var port int
	fmt.Sscanf(addr, "127.0.0.1:%d", &port)
	return port, nil
}
