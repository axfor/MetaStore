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

	"metaStore/internal/memory"
	"metaStore/internal/raft"
	myapi "metaStore/api/mysql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// TestMySQLMemorySingleNodeOperations tests basic MySQL protocol operations with memory storage
func TestMySQLMemorySingleNodeOperations(t *testing.T) {
	t.Parallel()

	// Cleanup data directory
	dataDir := "data/memory/mysql_test_1"
	os.RemoveAll(dataDir)
	defer os.RemoveAll(dataDir)

	// Create Raft node
	proposeC := make(chan string, 1)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	peers := []string{"http://127.0.0.1:19999"}
	getSnapshot := func() ([]byte, error) { return nil, nil }

	commitC, errorC, snapshotterReady, _ := raft.NewNode(1, peers, false, getSnapshot, proposeC, confChangeC, "memory", NewTestConfig(1, 1, ":2379"))

	// Create memory store
	kvs := memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)
	defer func() {
		// Drain commit channel
		go func() {
			for range commitC {
			}
		}()
	}()

	// Create MySQL server
	mysqlAddr := ":13306"
	mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
		Store:    kvs,
		Address:  mysqlAddr,
		Username: "root",
		Password: "",
	})
	require.NoError(t, err, "Failed to create MySQL server")

	// Start MySQL server
	go func() {
		if err := mysqlServer.Start(); err != nil {
			t.Logf("MySQL server stopped: %v", err)
		}
	}()
	defer mysqlServer.Stop()

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	// Connect to MySQL server
	dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err, "Failed to connect to MySQL server")
	defer db.Close()

	// Test connection
	err = db.Ping()
	require.NoError(t, err, "Failed to ping MySQL server")

	t.Run("InsertAndSelect", func(t *testing.T) {
		// Insert data
		_, err := db.Exec("INSERT INTO kv (key, value) VALUES ('test_key', 'test_value')")
		require.NoError(t, err, "Failed to insert data")

		// Select data
		var key, value string
		err = db.QueryRow("SELECT * FROM kv WHERE key = 'test_key'").Scan(&key, &value)
		require.NoError(t, err, "Failed to select data")
		require.Equal(t, "test_key", key)
		require.Equal(t, "test_value", value)
	})

	t.Run("Update", func(t *testing.T) {
		// Update data
		_, err := db.Exec("UPDATE kv SET value = 'updated_value' WHERE key = 'test_key'")
		require.NoError(t, err, "Failed to update data")

		// Verify update
		var value string
		err = db.QueryRow("SELECT value FROM kv WHERE key = 'test_key'").Scan(&value)
		require.NoError(t, err, "Failed to select updated data")
		require.Equal(t, "updated_value", value)
	})

	t.Run("Delete", func(t *testing.T) {
		// Delete data
		_, err := db.Exec("DELETE FROM kv WHERE key = 'test_key'")
		require.NoError(t, err, "Failed to delete data")

		// Verify deletion
		var value string
		err = db.QueryRow("SELECT value FROM kv WHERE key = 'test_key'").Scan(&value)
		require.Error(t, err, "Expected error when selecting deleted key")
		require.Equal(t, sql.ErrNoRows, err)
	})

	t.Run("ShowDatabases", func(t *testing.T) {
		rows, err := db.Query("SHOW DATABASES")
		require.NoError(t, err, "Failed to show databases")
		defer rows.Close()

		var dbName string
		found := false
		for rows.Next() {
			err = rows.Scan(&dbName)
			require.NoError(t, err)
			if dbName == "metastore" {
				found = true
			}
		}
		require.True(t, found, "Expected to find 'metastore' database")
	})

	t.Run("ShowTables", func(t *testing.T) {
		rows, err := db.Query("SHOW TABLES")
		require.NoError(t, err, "Failed to show tables")
		defer rows.Close()

		var tableName string
		found := false
		for rows.Next() {
			err = rows.Scan(&tableName)
			require.NoError(t, err)
			if tableName == "kv" {
				found = true
			}
		}
		require.True(t, found, "Expected to find 'kv' table")
	})

	t.Run("Transactions", func(t *testing.T) {
		// Begin transaction
		tx, err := db.Begin()
		require.NoError(t, err, "Failed to begin transaction")

		// Insert within transaction
		_, err = tx.Exec("INSERT INTO kv (key, value) VALUES ('tx_key', 'tx_value')")
		require.NoError(t, err, "Failed to insert in transaction")

		// Commit transaction
		err = tx.Commit()
		require.NoError(t, err, "Failed to commit transaction")

		// Verify data after commit
		var value string
		err = db.QueryRow("SELECT value FROM kv WHERE key = 'tx_key'").Scan(&value)
		require.NoError(t, err, "Failed to select committed data")
		require.Equal(t, "tx_value", value)
	})
}
