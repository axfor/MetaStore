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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
)

// TestMySQLAdvancedQueries tests advanced MySQL query features
func TestMySQLAdvancedQueries(t *testing.T) {
	t.Parallel()

	// Setup
	dataDir := "data/memory/mysql_advanced_test"
	os.RemoveAll(dataDir)
	defer os.RemoveAll(dataDir)

	proposeC := make(chan string, 100)
	defer close(proposeC)
	confChangeC := make(chan raftpb.ConfChange)
	defer close(confChangeC)

	peers := []string{"http://127.0.0.1:19995"}

	var kvs *memory.Memory
	getSnapshot := func() ([]byte, error) {
		if kvs == nil {
			return nil, nil
		}
		return kvs.GetSnapshot()
	}

	commitC, errorC, snapshotterReady, _ := raft.NewNode(1, peers, false, getSnapshot, proposeC, confChangeC, dataDir, NewTestConfig(1, 1, ":2385"))

	kvs = memory.NewMemory(<-snapshotterReady, proposeC, commitC, errorC)
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
	mysqlAddr := ":13320"
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

	// Insert test data with common prefixes
	testData := map[string]string{
		"user:1":     "alice",
		"user:2":     "bob",
		"user:3":     "charlie",
		"order:101":  "pending",
		"order:102":  "shipped",
		"order:103":  "delivered",
		"product:a1": "laptop",
		"product:a2": "mouse",
		"config:db":  "postgresql",
		"config:app": "enabled",
	}

	t.Run("InsertTestData", func(t *testing.T) {
		for key, value := range testData {
			query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
			_, err := db.Exec(query)
			require.NoError(t, err)
		}
		time.Sleep(500 * time.Millisecond)
	})

	// Test 1: SELECT specific columns (key only)
	t.Run("SelectKeyOnly", func(t *testing.T) {
		rows, err := db.Query("SELECT key FROM kv WHERE key = 'user:1'")
		require.NoError(t, err)
		defer rows.Close()

		var keys []string
		for rows.Next() {
			var key string
			err := rows.Scan(&key)
			require.NoError(t, err)
			keys = append(keys, key)
		}

		require.Len(t, keys, 1)
		assert.Equal(t, "user:1", keys[0])
	})

	// Test 2: SELECT specific columns (value only)
	t.Run("SelectValueOnly", func(t *testing.T) {
		rows, err := db.Query("SELECT value FROM kv WHERE key = 'user:1'")
		require.NoError(t, err)
		defer rows.Close()

		var values []string
		for rows.Next() {
			var value string
			err := rows.Scan(&value)
			require.NoError(t, err)
			values = append(values, value)
		}

		require.Len(t, values, 1)
		assert.Equal(t, "alice", values[0])
	})

	// Test 3: SELECT multiple columns (key, value)
	t.Run("SelectKeyAndValue", func(t *testing.T) {
		rows, err := db.Query("SELECT key, value FROM kv WHERE key = 'user:2'")
		require.NoError(t, err)
		defer rows.Close()

		count := 0
		for rows.Next() {
			var key, value string
			err := rows.Scan(&key, &value)
			require.NoError(t, err)
			assert.Equal(t, "user:2", key)
			assert.Equal(t, "bob", value)
			count++
		}

		assert.Equal(t, 1, count)
	})

	// Test 4: LIKE query with prefix (user:*)
	t.Run("LikeQueryUserPrefix", func(t *testing.T) {
		rows, err := db.Query("SELECT key, value FROM kv WHERE key LIKE 'user:%'")
		require.NoError(t, err)
		defer rows.Close()

		results := make(map[string]string)
		for rows.Next() {
			var key, value string
			err := rows.Scan(&key, &value)
			require.NoError(t, err)
			results[key] = value
		}

		// Should find all user: prefixed keys
		assert.GreaterOrEqual(t, len(results), 3)
		assert.Equal(t, "alice", results["user:1"])
		assert.Equal(t, "bob", results["user:2"])
		assert.Equal(t, "charlie", results["user:3"])
	})

	// Test 5: LIKE query with prefix (order:*)
	t.Run("LikeQueryOrderPrefix", func(t *testing.T) {
		rows, err := db.Query("SELECT key, value FROM kv WHERE key LIKE 'order:%'")
		require.NoError(t, err)
		defer rows.Close()

		results := make(map[string]string)
		for rows.Next() {
			var key, value string
			err := rows.Scan(&key, &value)
			require.NoError(t, err)
			results[key] = value
		}

		// Should find all order: prefixed keys
		assert.GreaterOrEqual(t, len(results), 3)
		assert.Equal(t, "pending", results["order:101"])
		assert.Equal(t, "shipped", results["order:102"])
		assert.Equal(t, "delivered", results["order:103"])
	})

	// Test 6: LIKE query with key only
	t.Run("LikeQueryKeysOnly", func(t *testing.T) {
		rows, err := db.Query("SELECT key FROM kv WHERE key LIKE 'product:%'")
		require.NoError(t, err)
		defer rows.Close()

		var keys []string
		for rows.Next() {
			var key string
			err := rows.Scan(&key)
			require.NoError(t, err)
			keys = append(keys, key)
		}

		assert.GreaterOrEqual(t, len(keys), 2)
		assert.Contains(t, keys, "product:a1")
		assert.Contains(t, keys, "product:a2")
	})

	// Test 7: LIKE query with value only
	t.Run("LikeQueryValuesOnly", func(t *testing.T) {
		rows, err := db.Query("SELECT value FROM kv WHERE key LIKE 'config:%'")
		require.NoError(t, err)
		defer rows.Close()

		var values []string
		for rows.Next() {
			var value string
			err := rows.Scan(&value)
			require.NoError(t, err)
			values = append(values, value)
		}

		assert.GreaterOrEqual(t, len(values), 2)
		assert.Contains(t, values, "postgresql")
		assert.Contains(t, values, "enabled")
	})

	// Test 8: LIKE query with narrow prefix
	t.Run("LikeQueryNarrowPrefix", func(t *testing.T) {
		rows, err := db.Query("SELECT key, value FROM kv WHERE key LIKE 'user:1%'")
		require.NoError(t, err)
		defer rows.Close()

		count := 0
		for rows.Next() {
			var key, value string
			err := rows.Scan(&key, &value)
			require.NoError(t, err)
			assert.Equal(t, "user:1", key)
			assert.Equal(t, "alice", value)
			count++
		}

		assert.Equal(t, 1, count)
	})

	// Test 9: SELECT * with LIKE
	t.Run("SelectStarWithLike", func(t *testing.T) {
		rows, err := db.Query("SELECT * FROM kv WHERE key LIKE 'order:%'")
		require.NoError(t, err)
		defer rows.Close()

		count := 0
		for rows.Next() {
			var key, value string
			err := rows.Scan(&key, &value)
			require.NoError(t, err)
			assert.True(t, len(key) > 0)
			assert.True(t, len(value) > 0)
			count++
		}

		assert.GreaterOrEqual(t, count, 3)
	})

	// Test 10: Multiple LIKE queries in sequence
	t.Run("MultipleLikeQueries", func(t *testing.T) {
		prefixes := []string{"user:", "order:", "product:", "config:"}

		for _, prefix := range prefixes {
			query := fmt.Sprintf("SELECT key, value FROM kv WHERE key LIKE '%s%%'", prefix)
			rows, err := db.Query(query)
			require.NoError(t, err)

			count := 0
			for rows.Next() {
				var key, value string
				err := rows.Scan(&key, &value)
				require.NoError(t, err)
				count++
			}
			rows.Close()

			assert.GreaterOrEqual(t, count, 1, "Should find at least one result for prefix: %s", prefix)
		}
	})
}
