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
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"metaStore/internal/raft"
	"metaStore/internal/pebbledb"
	myapi "metaStore/api/mysql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// TestMySQLPebbleSingleNodeOperations tests basic MySQL operations with Pebble storage
func TestMySQLPebbleSingleNodeOperations(t *testing.T) {
	t.Parallel()

	// Setup Pebble
	dbPath := "data/pebble/mysql_test_1"
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	// Allocate dynamic port for Raft peer communication
	peers, listeners := allocatePorts(1)
	releaseListeners(listeners)

	cfg := NewTestConfig(1, 1, peers[0])
	db, err := pebbledb.Open(dbPath, &cfg.Server.Pebble)
	require.NoError(t, err, "Failed to open Pebble")
	defer db.Close()

	// Create Raft node
	proposeC := make(chan string, 100)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	var kvs *pebbledb.PebbleDB
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNodePebble(1, peers, false, getSnapshot, proposeC, confChangeC, db, dbPath, cfg)

	kvs = pebbledb.NewPebbleDB(db, <-snapshotterReady, proposeC, commitC, errorC)
	defer func() {
		// Drain commit channel
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
	mysqlAddr := ":13310"
	mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
		Store:    kvs,
		Address:  mysqlAddr,
		Username: "root",
		Password: "",
	})
	require.NoError(t, err, "Failed to create MySQL server")

	go func() {
		if err := mysqlServer.Start(); err != nil {
			t.Logf("MySQL server stopped: %v", err)
		}
	}()
	defer mysqlServer.Stop()

	// Wait for Raft cluster to stabilize and elect leader
	// Single-node Raft needs more time for election and leader establishment
	time.Sleep(5 * time.Second)

	// Connect to MySQL server
	dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
	mysqlDB, err := sql.Open("mysql", dsn)
	require.NoError(t, err, "Failed to connect to MySQL server")
	defer mysqlDB.Close()

	// Wait for connection
	for i := 0; i < 20; i++ {
		if err := mysqlDB.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, mysqlDB.Ping(), "Failed to ping MySQL server")

	t.Run("InsertAndSelect_Pebble", func(t *testing.T) {
		// Insert data
		_, err := mysqlDB.Exec("INSERT INTO kv (key, value) VALUES ('pebble_key1', 'pebble_value1')")
		require.NoError(t, err, "Failed to insert data")

		// Wait for Raft commit
		time.Sleep(300 * time.Millisecond)

		// Select data
		var key, value string
		err = mysqlDB.QueryRow("SELECT * FROM kv WHERE key = 'pebble_key1'").Scan(&key, &value)
		require.NoError(t, err, "Failed to select data")
		require.Equal(t, "pebble_key1", key)
		require.Equal(t, "pebble_value1", value)
	})

	t.Run("Update_Pebble", func(t *testing.T) {
		// Insert initial data
		_, err := mysqlDB.Exec("INSERT INTO kv (key, value) VALUES ('update_key', 'initial_value')")
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)

		// Update data
		_, err = mysqlDB.Exec("UPDATE kv SET value = 'updated_value' WHERE key = 'update_key'")
		require.NoError(t, err, "Failed to update data")
		time.Sleep(300 * time.Millisecond)

		// Verify update
		var value string
		err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'update_key'").Scan(&value)
		require.NoError(t, err, "Failed to select updated data")
		require.Equal(t, "updated_value", value)
	})

	t.Run("Delete_Pebble", func(t *testing.T) {
		// Insert data
		_, err := mysqlDB.Exec("INSERT INTO kv (key, value) VALUES ('delete_key', 'delete_value')")
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)

		// Delete data
		_, err = mysqlDB.Exec("DELETE FROM kv WHERE key = 'delete_key'")
		require.NoError(t, err, "Failed to delete data")
		time.Sleep(300 * time.Millisecond)

		// Verify deletion
		var value string
		err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'delete_key'").Scan(&value)
		require.Error(t, err, "Expected error when selecting deleted key")
		require.Equal(t, sql.ErrNoRows, err)
	})

	t.Run("MultipleOperations_Pebble", func(t *testing.T) {
		// Insert multiple keys
		keys := []string{"multi_1", "multi_2", "multi_3", "multi_4", "multi_5"}
		values := []string{"val_1", "val_2", "val_3", "val_4", "val_5"}

		for i := range keys {
			query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", keys[i], values[i])
			_, err := mysqlDB.Exec(query)
			require.NoError(t, err)
		}

		// Wait for commits
		time.Sleep(1 * time.Second)

		// Verify all keys
		for i, key := range keys {
			var readKey, readValue string
			query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
			err := mysqlDB.QueryRow(query).Scan(&readKey, &readValue)
			require.NoError(t, err, "Failed to read key %s", key)
			require.Equal(t, key, readKey)
			require.Equal(t, values[i], readValue)
		}
	})

	t.Run("Persistence_Pebble", func(t *testing.T) {
		// Insert data that should persist
		_, err := mysqlDB.Exec("INSERT INTO kv (key, value) VALUES ('persist_key', 'persist_value')")
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)

		// Read it back
		var value string
		err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'persist_key'").Scan(&value)
		require.NoError(t, err)
		require.Equal(t, "persist_value", value)

		// Note: Full persistence test would require restarting the server
		// and reading the data again, which is more complex for this test
	})

	t.Run("Transaction_Pebble", func(t *testing.T) {
		// Begin transaction
		tx, err := mysqlDB.Begin()
		require.NoError(t, err, "Failed to begin transaction")

		// Insert within transaction
		_, err = tx.Exec("INSERT INTO kv (key, value) VALUES ('tx_key_pebble', 'tx_value_pebble')")
		require.NoError(t, err, "Failed to insert in transaction")

		// Commit transaction
		err = tx.Commit()
		require.NoError(t, err, "Failed to commit transaction")

		// Wait for commit
		time.Sleep(300 * time.Millisecond)

		// Verify data after commit
		var value string
		err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'tx_key_pebble'").Scan(&value)
		require.NoError(t, err, "Failed to select committed data")
		require.Equal(t, "tx_value_pebble", value)
	})

	t.Run("SpecialCharacters_Pebble", func(t *testing.T) {
		// Test with special characters
		specialKeys := []string{
			"key-with-dash",
			"key_with_underscore",
			"key.with.dot",
			"key:with:colon",
		}
		specialValue := "special_value_123"

		for _, key := range specialKeys {
			query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, specialValue)
			_, err := mysqlDB.Exec(query)
			require.NoError(t, err, "Failed to insert key with special chars: %s", key)
		}

		time.Sleep(500 * time.Millisecond)

		// Verify all special keys
		for _, key := range specialKeys {
			var value string
			query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
			err := mysqlDB.QueryRow(query).Scan(&value)
			require.NoError(t, err, "Failed to read special key: %s", key)
			require.Equal(t, specialValue, value)
		}
	})
}

// TestMySQLPebbleLargeValues tests handling of large values
func TestMySQLPebbleLargeValues(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large value test in short mode")
	}

	t.Parallel()

	// Setup Pebble
	dbPath := "data/pebble/mysql_large_test"
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	cfg := NewTestConfig(1, 1, ":2382")
	db, err := pebbledb.Open(dbPath, &cfg.Server.Pebble)
	require.NoError(t, err)
	defer db.Close()

	proposeC := make(chan string, 100)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	peers := []string{"http://127.0.0.1:19997"}

	var kvs *pebbledb.PebbleDB
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNodePebble(1, peers, false, getSnapshot, proposeC, confChangeC, db, dbPath, cfg)
	kvs = pebbledb.NewPebbleDB(db, <-snapshotterReady, proposeC, commitC, errorC)

	defer func() {
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
	mysqlAddr := ":13311"
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
	defer mysqlServer.Stop()

	time.Sleep(500 * time.Millisecond)

	// Connect to MySQL
	dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
	mysqlDB, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer mysqlDB.Close()

	for i := 0; i < 20; i++ {
		if err := mysqlDB.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, mysqlDB.Ping())

	t.Run("LargeValue_1KB", func(t *testing.T) {
		key := "large_key_1kb"
		// Create 1KB value
		value := string(make([]byte, 1024))
		for i := range value {
			value = value[:i] + "x" + value[i+1:]
		}

		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)

		time.Sleep(300 * time.Millisecond)

		var readValue string
		err = mysqlDB.QueryRow(fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)).Scan(&readValue)
		require.NoError(t, err)
		require.Len(t, readValue, 1024)
	})

	t.Run("LargeValue_10KB", func(t *testing.T) {
		key := "large_key_10kb"
		// Create 10KB value
		value := string(make([]byte, 10*1024))
		for i := 0; i < len(value); i++ {
			value = value[:i] + "y" + value[i+1:]
		}

		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlDB.Exec(query)
		require.NoError(t, err)

		time.Sleep(500 * time.Millisecond)

		var readValue string
		err = mysqlDB.QueryRow(fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)).Scan(&readValue)
		require.NoError(t, err)
		require.Len(t, readValue, 10*1024)
	})
}
