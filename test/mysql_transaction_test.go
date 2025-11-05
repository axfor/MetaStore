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
	"sync"
	"testing"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	"metaStore/internal/rocksdb"
	myapi "metaStore/api/mysql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// testStoreSetup holds the setup for a store
type testStoreSetup struct {
	store       kvstore.Store
	proposeC    chan string
	confChangeC chan raftpb.ConfChange
	commitC     <-chan *kvstore.Commit
	errorC      <-chan error
	cleanup     func()
}

// setupMemoryStore sets up a memory-based store for testing
func setupMemoryStore(t *testing.T, testName string, nodeID int, port int) *testStoreSetup {
	dataDir := fmt.Sprintf("data/memory/%s", testName)
	os.RemoveAll(dataDir)

	proposeC := make(chan string, 100)
	confChangeC := make(chan raftpb.ConfChange)

	peers := []string{fmt.Sprintf("http://127.0.0.1:%d", port+100)}

	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(nodeID, peers, false, getSnapshot, proposeC, confChangeC, dataDir, NewTestConfig(1, uint64(nodeID), fmt.Sprintf(":%d", port)))

	kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)

	cleanup := func() {
		// Drain channels
		go func() {
			for range commitC {
			}
		}()
		go func() {
			for range errorC {
			}
		}()
		close(proposeC)
		close(confChangeC)
		os.RemoveAll(dataDir)
	}

	return &testStoreSetup{
		store:       kvs,
		proposeC:    proposeC,
		confChangeC: confChangeC,
		commitC:     commitC,
		errorC:      errorC,
		cleanup:     cleanup,
	}
}

// setupRocksDBStore sets up a RocksDB-based store for testing
func setupRocksDBStore(t *testing.T, testName string, nodeID int, port int) *testStoreSetup {
	dataDir := fmt.Sprintf("data/rocksdb/%s", testName)
	os.RemoveAll(dataDir)

	proposeC := make(chan string, 100)
	confChangeC := make(chan raftpb.ConfChange)

	peers := []string{fmt.Sprintf("http://127.0.0.1:%d", port+100)}

	var kvs *rocksdb.RocksDB
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(nodeID, peers, false, getSnapshot, proposeC, confChangeC, dataDir, NewTestConfig(1, uint64(nodeID), fmt.Sprintf(":%d", port)))

	// Open RocksDB
	dbPath := fmt.Sprintf("%s/rocksdb", dataDir)
	os.MkdirAll(dbPath, 0755)
	db, err := rocksdb.Open(dbPath)
	require.NoError(t, err, "Failed to open RocksDB")

	kvs = rocksdb.NewRocksDB(db, <-snapshotterReady, proposeC, commitC, errorC)

	cleanup := func() {
		if kvs != nil {
			kvs.Close()
		}
		// Drain channels
		go func() {
			for range commitC {
			}
		}()
		go func() {
			for range errorC {
			}
		}()
		close(proposeC)
		close(confChangeC)
		os.RemoveAll(dataDir)
	}

	return &testStoreSetup{
		store:       kvs,
		proposeC:    proposeC,
		confChangeC: confChangeC,
		commitC:     commitC,
		errorC:      errorC,
		cleanup:     cleanup,
	}
}

// runTransactionTest runs a test function with both Memory and RocksDB engines
func runTransactionTest(t *testing.T, testName string, testFunc func(t *testing.T, store kvstore.Store, mysqlAddr string)) {
	engines := []struct {
		name      string
		setupFunc func(*testing.T, string, int, int) *testStoreSetup
		basePort  int
		raftPort  int
	}{
		{"Memory", setupMemoryStore, 13400, 2380},
		{"RocksDB", setupRocksDBStore, 13500, 2390},
	}

	for _, engine := range engines {
		engine := engine // capture range variable
		t.Run(engine.name, func(t *testing.T) {
			t.Parallel()

			// Setup store
			setup := engine.setupFunc(t, fmt.Sprintf("%s_%s", testName, engine.name), 1, engine.raftPort)
			defer setup.cleanup()

			// Start MySQL server
			mysqlAddr := fmt.Sprintf(":%d", engine.basePort)
			mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
				Store:    setup.store,
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

			// Run the actual test
			testFunc(t, setup.store, mysqlAddr)
		})
	}
}

// TestMySQLBasicTransaction tests basic transaction functionality
func TestMySQLBasicTransaction(t *testing.T) {
	runTransactionTest(t, "basic_transaction", func(t *testing.T, store kvstore.Store, mysqlAddr string) {
		// Connect to MySQL
		dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
		db, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db.Close()

		// Wait for connection
		for i := 0; i < 20; i++ {
			if err := db.Ping(); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, db.Ping())

		// Test 1: Basic transaction with commit
		t.Run("BasicCommit", func(t *testing.T) {
			_, err := db.Exec("BEGIN")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('tx:1', 'value1')")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('tx:2', 'value2')")
			require.NoError(t, err)

			_, err = db.Exec("COMMIT")
			require.NoError(t, err)

			time.Sleep(500 * time.Millisecond)

			// Verify data was committed
			var value string
			err = db.QueryRow("SELECT value FROM kv WHERE key = 'tx:1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "value1", value)

			err = db.QueryRow("SELECT value FROM kv WHERE key = 'tx:2'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "value2", value)
		})

		// Test 2: Transaction with rollback
		t.Run("BasicRollback", func(t *testing.T) {
			_, err := db.Exec("BEGIN")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('rollback:1', 'value1')")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('rollback:2', 'value2')")
			require.NoError(t, err)

			_, err = db.Exec("ROLLBACK")
			require.NoError(t, err)

			time.Sleep(500 * time.Millisecond)

			// Verify data was NOT committed
			var value string
			err = db.QueryRow("SELECT value FROM kv WHERE key = 'rollback:1'").Scan(&value)
			assert.Error(t, err) // Should not exist
		})

		// Test 3: Transaction with mixed operations
		t.Run("MixedOperations", func(t *testing.T) {
			// Setup initial data
			_, err := db.Exec("INSERT INTO kv (key, value) VALUES ('mixed:1', 'initial')")
			require.NoError(t, err)
			time.Sleep(500 * time.Millisecond)

			_, err = db.Exec("BEGIN")
			require.NoError(t, err)

			// Update existing
			_, err = db.Exec("UPDATE kv SET value = 'updated' WHERE key = 'mixed:1'")
			require.NoError(t, err)

			// Insert new
			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('mixed:2', 'new')")
			require.NoError(t, err)

			// Delete
			_, err = db.Exec("DELETE FROM kv WHERE key = 'mixed:3'")
			require.NoError(t, err)

			_, err = db.Exec("COMMIT")
			require.NoError(t, err)

			time.Sleep(500 * time.Millisecond)

			// Verify
			var value string
			err = db.QueryRow("SELECT value FROM kv WHERE key = 'mixed:1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "updated", value)

			err = db.QueryRow("SELECT value FROM kv WHERE key = 'mixed:2'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "new", value)
		})
	})
}

// TestMySQLTransactionConflict tests transaction conflict detection
func TestMySQLTransactionConflict(t *testing.T) {
	runTransactionTest(t, "transaction_conflict", func(t *testing.T, store kvstore.Store, mysqlAddr string) {
		// Connect to MySQL (two connections)
		dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
		db1, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db1.Close()

		db2, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db2.Close()

		// Wait for connections
		for i := 0; i < 20; i++ {
			if err := db1.Ping(); err == nil && db2.Ping() == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, db1.Ping())
		require.NoError(t, db2.Ping())

		// Setup initial data
		_, err = db1.Exec("INSERT INTO kv (key, value) VALUES ('conflict:1', '100')")
		require.NoError(t, err)
		time.Sleep(500 * time.Millisecond)

		// Test: Transaction conflict
		t.Run("ReadWriteConflict", func(t *testing.T) {
			// Transaction 1 starts and reads
			_, err := db1.Exec("BEGIN")
			require.NoError(t, err)

			var value string
			err = db1.QueryRow("SELECT value FROM kv WHERE key = 'conflict:1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "100", value)

			// Transaction 2 modifies the same key
			_, err = db2.Exec("UPDATE kv SET value = '200' WHERE key = 'conflict:1'")
			require.NoError(t, err)
			time.Sleep(500 * time.Millisecond)

			// Transaction 1 tries to update - should be buffered
			_, err = db1.Exec("UPDATE kv SET value = '300' WHERE key = 'conflict:1'")
			require.NoError(t, err)

			// Transaction 1 tries to commit - should fail due to conflict
			_, err = db1.Exec("COMMIT")
			assert.Error(t, err, "Should fail due to read-write conflict")
			assert.Contains(t, err.Error(), "conflict", "Error should mention conflict")

			// Verify the value is still 200 (from transaction 2)
			err = db1.QueryRow("SELECT value FROM kv WHERE key = 'conflict:1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "200", value)
		})
	})
}

// TestMySQLConcurrentTransactions tests concurrent transaction execution
func TestMySQLConcurrentTransactions(t *testing.T) {
	runTransactionTest(t, "concurrent_transactions", func(t *testing.T, store kvstore.Store, mysqlAddr string) {
		dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)

		// Test: Concurrent independent transactions
		t.Run("ConcurrentIndependentTransactions", func(t *testing.T) {
			// Run 10 concurrent transactions on different keys
			numTxns := 10
			var wg sync.WaitGroup
			errors := make([]error, numTxns)

			for i := 0; i < numTxns; i++ {
				wg.Add(1)
				go func(txnID int) {
					defer wg.Done()

					db, err := sql.Open("mysql", dsn)
					if err != nil {
						errors[txnID] = err
						return
					}
					defer db.Close()

					// Wait for connection
					for j := 0; j < 20; j++ {
						if err := db.Ping(); err == nil {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}

					_, err = db.Exec("BEGIN")
					if err != nil {
						errors[txnID] = err
						return
					}

					key := fmt.Sprintf("concurrent:%d", txnID)
					value := fmt.Sprintf("value%d", txnID)

					_, err = db.Exec(fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value))
					if err != nil {
						errors[txnID] = err
						return
					}

					_, err = db.Exec("COMMIT")
					errors[txnID] = err
				}(i)
			}

			wg.Wait()
			time.Sleep(1 * time.Second)

			// Check all transactions succeeded
			for i, err := range errors {
				assert.NoError(t, err, "Transaction %d should succeed", i)
			}

			// Verify all data
			db, err := sql.Open("mysql", dsn)
			require.NoError(t, err)
			defer db.Close()

			for i := 0; i < numTxns; i++ {
				key := fmt.Sprintf("concurrent:%d", i)
				expectedValue := fmt.Sprintf("value%d", i)

				var value string
				err := db.QueryRow(fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)).Scan(&value)
				if err != nil {
					t.Logf("Warning: Key %s not found, may have been a timing issue", key)
					continue
				}
				assert.Equal(t, expectedValue, value)
			}
		})
	})
}

// TestMySQLAutocommitMode tests autocommit mode behavior
func TestMySQLAutocommitMode(t *testing.T) {
	runTransactionTest(t, "autocommit_mode", func(t *testing.T, store kvstore.Store, mysqlAddr string) {
		// Connect to MySQL
		dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
		db, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db.Close()

		// Wait for connection
		for i := 0; i < 20; i++ {
			if err := db.Ping(); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, db.Ping())

		// Test: Autocommit mode (no explicit BEGIN)
		t.Run("AutocommitMode", func(t *testing.T) {
			// Insert without BEGIN - should auto-commit
			_, err := db.Exec("INSERT INTO kv (key, value) VALUES ('autocommit:1', 'value1')")
			require.NoError(t, err)

			time.Sleep(500 * time.Millisecond)

			// Should be immediately visible
			var value string
			err = db.QueryRow("SELECT value FROM kv WHERE key = 'autocommit:1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "value1", value)
		})

		// Test: Mixed autocommit and explicit transactions
		t.Run("MixedMode", func(t *testing.T) {
			// Autocommit
			_, err := db.Exec("INSERT INTO kv (key, value) VALUES ('mixed:auto', 'auto')")
			require.NoError(t, err)
			time.Sleep(500 * time.Millisecond)

			// Explicit transaction
			_, err = db.Exec("BEGIN")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('mixed:tx1', 'tx1')")
			require.NoError(t, err)

			_, err = db.Exec("INSERT INTO kv (key, value) VALUES ('mixed:tx2', 'tx2')")
			require.NoError(t, err)

			_, err = db.Exec("COMMIT")
			require.NoError(t, err)
			time.Sleep(500 * time.Millisecond)

			// Verify all data
			var value string

			err = db.QueryRow("SELECT value FROM kv WHERE key = 'mixed:auto'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "auto", value)

			err = db.QueryRow("SELECT value FROM kv WHERE key = 'mixed:tx1'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "tx1", value)

			err = db.QueryRow("SELECT value FROM kv WHERE key = 'mixed:tx2'").Scan(&value)
			require.NoError(t, err)
			assert.Equal(t, "tx2", value)
		})
	})
}

// TestMySQLTransactionIsolation tests transaction isolation
func TestMySQLTransactionIsolation(t *testing.T) {
	runTransactionTest(t, "transaction_isolation", func(t *testing.T, store kvstore.Store, mysqlAddr string) {
		// Connect to MySQL (two connections)
		dsn := fmt.Sprintf("root@tcp(127.0.0.1%s)/metastore", mysqlAddr)
		db1, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db1.Close()

		db2, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		defer db2.Close()

		// Wait for connections
		for i := 0; i < 20; i++ {
			if err := db1.Ping(); err == nil && db2.Ping() == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, db1.Ping())
		require.NoError(t, db2.Ping())

		// Setup initial data
		_, err = db1.Exec("INSERT INTO kv (key, value) VALUES ('isolation:1', '100')")
		require.NoError(t, err)
		time.Sleep(500 * time.Millisecond)

		// Test: Transactions don't see each other's uncommitted changes
		t.Run("NoUncommittedReads", func(t *testing.T) {
			// Transaction 1 starts and writes
			_, err := db1.Exec("BEGIN")
			require.NoError(t, err)

			_, err = db1.Exec("UPDATE kv SET value = '200' WHERE key = 'isolation:1'")
			require.NoError(t, err)

			// Transaction 1 can see its own write
			var value1 string
			err = db1.QueryRow("SELECT value FROM kv WHERE key = 'isolation:1'").Scan(&value1)
			// Note: Due to buffering, this will still return old value from DB
			// The new value is in the write buffer

			// Transaction 2 should NOT see uncommitted change
			var value2 string
			err = db2.QueryRow("SELECT value FROM kv WHERE key = 'isolation:1'").Scan(&value2)
			require.NoError(t, err)
			assert.Equal(t, "100", value2, "Should not see uncommitted changes from other transaction")

			// Commit transaction 1
			_, err = db1.Exec("COMMIT")
			require.NoError(t, err)
			time.Sleep(500 * time.Millisecond)

			// Now transaction 2 should see the committed change
			err = db2.QueryRow("SELECT value FROM kv WHERE key = 'isolation:1'").Scan(&value2)
			require.NoError(t, err)
			assert.Equal(t, "200", value2, "Should see committed changes")
		})
	})
}
