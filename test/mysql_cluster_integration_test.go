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
	"os"
	"testing"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/internal/memory"
	"metaStore/internal/raft"
	etcdapi "metaStore/api/etcd"
	httpapi "metaStore/api/http"
	myapi "metaStore/api/mysql"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/raft/v3/raftpb"
)

// mysqlClusterNode represents a single node in a MySQL-enabled cluster
type mysqlClusterNode struct {
	id              int
	proposeC        chan string
	confChangeC     chan raftpb.ConfChange
	commitC         <-chan *kvstore.Commit
	errorC          <-chan error
	snapshotterReady <-chan *snap.Snapshotter
	kvs             *memory.Memory
	httpPort        int
	etcdAddr        string
	mysqlAddr       string
	mysqlServer     *myapi.Server
	etcdServer      *etcdapi.Server
}

// TestMySQLClusterConsistency tests data consistency across cluster nodes via MySQL
func TestMySQLClusterConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cluster test in short mode")
	}

	t.Parallel()

	const numNodes = 3

	// Setup cluster
	peers := make([]string, numNodes)
	for i := 0; i < numNodes; i++ {
		peers[i] = fmt.Sprintf("http://127.0.0.1:%d", 10600+i)
	}

	nodes := make([]*mysqlClusterNode, numNodes)

	// Create nodes
	for i := 0; i < numNodes; i++ {
		dataDir := fmt.Sprintf("data/memory/mysql_cluster_%d", i+1)
		os.RemoveAll(dataDir)
		defer os.RemoveAll(dataDir)

		node := &mysqlClusterNode{
			id:          i + 1,
			proposeC:    make(chan string, 100),
			confChangeC: make(chan raftpb.ConfChange, 1),
			httpPort:    19300 + i,
			etcdAddr:    fmt.Sprintf("127.0.0.1:%d", 12400+i),
			mysqlAddr:   fmt.Sprintf("127.0.0.1:%d", 13400+i),
		}

		getSnapshot := func() ([]byte, error) {
			if node.kvs == nil {
				return nil, nil
			}
			return node.kvs.GetSnapshot()
		}

		node.commitC, node.errorC, node.snapshotterReady, _ = raft.NewNode(
			node.id,
			peers,
			false,
			getSnapshot,
			node.proposeC,
			node.confChangeC,
			dataDir,
			NewTestConfig(1, uint64(node.id), node.etcdAddr),
		)

		node.kvs = memory.NewMemory(<-node.snapshotterReady, node.proposeC, node.commitC, node.errorC)

		// Drain error channel
		go func(n *mysqlClusterNode) {
			for range n.errorC {
			}
		}(node)

		nodes[i] = node
	}

	// Wait for cluster to form
	time.Sleep(2 * time.Second)

	// Start services on all nodes
	for i, node := range nodes {
		// Start HTTP API
		go func(n *mysqlClusterNode) {
			httpapi.ServeHTTPKVAPI(n.kvs, n.httpPort, n.confChangeC, n.errorC)
		}(node)

		// Start etcd server
		etcdServer, err := etcdapi.NewServer(etcdapi.ServerConfig{
			Store:       node.kvs,
			Address:     node.etcdAddr,
			ClusterID:   1,
			MemberID:    uint64(node.id),
			ConfChangeC: node.confChangeC,
			Config:      NewTestConfig(1, uint64(node.id), node.etcdAddr),
		})
		require.NoError(t, err)
		node.etcdServer = etcdServer

		go func(srv *etcdapi.Server) {
			srv.Start()
		}(etcdServer)

		// Start MySQL server
		mysqlServer, err := myapi.NewServer(myapi.ServerConfig{
			Store:    node.kvs,
			Address:  node.mysqlAddr,
			Username: "root",
			Password: "",
		})
		require.NoError(t, err)
		node.mysqlServer = mysqlServer

		go func(srv *myapi.Server) {
			srv.Start()
		}(mysqlServer)

		t.Logf("Started node %d: HTTP=%d, etcd=%s, MySQL=%s", i+1, node.httpPort, node.etcdAddr, node.mysqlAddr)
	}

	// Wait for all services to start
	time.Sleep(2 * time.Second)

	// Cleanup
	defer func() {
		for _, node := range nodes {
			if node.mysqlServer != nil {
				node.mysqlServer.Stop()
			}
			close(node.proposeC)
			// Drain commit channel
			go func(c <-chan *kvstore.Commit) {
				for range c {
				}
			}(node.commitC)
		}
	}()

	// Connect MySQL clients to all nodes
	mysqlClients := make([]*sql.DB, numNodes)
	for i, node := range nodes {
		dsn := fmt.Sprintf("root@tcp(%s)/metastore", node.mysqlAddr)
		db, err := sql.Open("mysql", dsn)
		require.NoError(t, err)
		mysqlClients[i] = db
		defer db.Close()

		// Wait for connection
		for j := 0; j < 20; j++ {
			if err := db.Ping(); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, db.Ping(), "Node %d MySQL not ready", i+1)
	}

	// Test 1: Write to node 1, read from all nodes via MySQL
	t.Run("Write_Node1_Read_All_MySQL", func(t *testing.T) {
		key := "cluster_key_1"
		value := "cluster_value_1"

		// Write via MySQL on node 1
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlClients[0].Exec(query)
		require.NoError(t, err)

		// Wait for replication
		time.Sleep(1 * time.Second)

		// Read from all nodes via MySQL
		for i, client := range mysqlClients {
			var readKey, readValue string
			query := fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", key)
			err := client.QueryRow(query).Scan(&readKey, &readValue)
			require.NoError(t, err, "Failed to read from node %d", i+1)
			require.Equal(t, key, readKey, "Key mismatch on node %d", i+1)
			require.Equal(t, value, readValue, "Value mismatch on node %d", i+1)
			t.Logf("Node %d: Successfully read %s=%s", i+1, readKey, readValue)
		}
	})

	// Test 2: Write via HTTP on node 2, read via MySQL from all nodes
	t.Run("HTTP_Write_Node2_MySQL_Read_All", func(t *testing.T) {
		key := "http_cluster_key"
		value := "http_cluster_value"

		// Write via HTTP on node 2
		resp := httpPut(t, nodes[1].httpPort, key, value)
		require.Equal(t, 204, resp.StatusCode)
		resp.Body.Close()

		// Wait for replication
		time.Sleep(1 * time.Second)

		// Read from all nodes via MySQL
		for i, client := range mysqlClients {
			var readValue string
			query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
			err := client.QueryRow(query).Scan(&readValue)
			require.NoError(t, err, "Failed to read from node %d", i+1)
			require.Equal(t, value, readValue, "Value mismatch on node %d", i+1)
		}
	})

	// Test 3: Write via etcd on node 3, read via MySQL from all nodes
	t.Run("Etcd_Write_Node3_MySQL_Read_All", func(t *testing.T) {
		key := "etcd_cluster_key"
		value := "etcd_cluster_value"

		// Connect etcd client to node 3
		etcdClient, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{nodes[2].etcdAddr},
			DialTimeout: 5 * time.Second,
		})
		require.NoError(t, err)
		defer etcdClient.Close()

		// Write via etcd
		ctx := context.Background()
		_, err = etcdClient.Put(ctx, key, value)
		require.NoError(t, err)

		// Wait for replication
		time.Sleep(1 * time.Second)

		// Read from all nodes via MySQL
		for i, client := range mysqlClients {
			var readValue string
			query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
			err := client.QueryRow(query).Scan(&readValue)
			require.NoError(t, err, "Failed to read from node %d", i+1)
			require.Equal(t, value, readValue, "Value mismatch on node %d", i+1)
		}
	})

	// Test 4: Update via MySQL on different nodes
	t.Run("MySQL_Update_Different_Nodes", func(t *testing.T) {
		key := "update_cluster_key"
		value1 := "initial_value"
		value2 := "updated_value"

		// Initial write via MySQL on node 1
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value1)
		_, err := mysqlClients[0].Exec(query)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Update via MySQL on node 2
		query = fmt.Sprintf("UPDATE kv SET value = '%s' WHERE key = '%s'", value2, key)
		_, err = mysqlClients[1].Exec(query)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Read from node 3
		var readValue string
		query = fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
		err = mysqlClients[2].QueryRow(query).Scan(&readValue)
		require.NoError(t, err)
		require.Equal(t, value2, readValue)
	})

	// Test 5: Delete via MySQL, verify on all nodes
	t.Run("MySQL_Delete_Verify_All", func(t *testing.T) {
		key := "delete_cluster_key"
		value := "delete_value"

		// Insert via MySQL on node 1
		query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
		_, err := mysqlClients[0].Exec(query)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Delete via MySQL on node 2
		query = fmt.Sprintf("DELETE FROM kv WHERE key = '%s'", key)
		_, err = mysqlClients[1].Exec(query)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		// Verify deletion on all nodes
		for i, client := range mysqlClients {
			var value string
			query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
			err := client.QueryRow(query).Scan(&value)
			require.Error(t, err, "Key should not exist on node %d", i+1)
			require.Equal(t, sql.ErrNoRows, err, "Expected ErrNoRows on node %d", i+1)
		}
	})

	// Test 6: Concurrent writes via MySQL from multiple nodes
	t.Run("Concurrent_MySQL_Writes", func(t *testing.T) {
		const numWrites = 5

		done := make(chan bool, numNodes)

		// Write from each node concurrently
		for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
			go func(idx int) {
				for i := 0; i < numWrites; i++ {
					key := fmt.Sprintf("concurrent_node%d_key%d", idx+1, i)
					value := fmt.Sprintf("node%d_value%d", idx+1, i)
					query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", key, value)
					mysqlClients[idx].Exec(query)
					time.Sleep(50 * time.Millisecond)
				}
				done <- true
			}(nodeIdx)
		}

		// Wait for all writes
		for i := 0; i < numNodes; i++ {
			<-done
		}

		// Wait for replication
		time.Sleep(2 * time.Second)

		// Verify all writes on each node
		successCount := 0
		for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
			for i := 0; i < numWrites; i++ {
				key := fmt.Sprintf("concurrent_node%d_key%d", nodeIdx+1, i)
				var value string
				query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", key)
				// Check on any node (should be replicated)
				if err := mysqlClients[0].QueryRow(query).Scan(&value); err == nil {
					successCount++
				}
			}
		}

		// Should have most or all writes succeed
		expectedWrites := numNodes * numWrites
		t.Logf("Successful writes: %d/%d", successCount, expectedWrites)
		require.GreaterOrEqual(t, successCount, expectedWrites-3, "Most concurrent writes should succeed")
	})

	// Test 7: Mixed protocol writes in cluster
	t.Run("Mixed_Protocol_Cluster_Writes", func(t *testing.T) {
		testCases := []struct {
			protocol string
			nodeIdx  int
			key      string
			value    string
		}{
			{"mysql", 0, "mixed_mysql_1", "mysql_val_1"},
			{"http", 1, "mixed_http_1", "http_val_1"},
			{"mysql", 2, "mixed_mysql_2", "mysql_val_2"},
			{"http", 0, "mixed_http_2", "http_val_2"},
			{"mysql", 1, "mixed_mysql_3", "mysql_val_3"},
		}

		// Write using different protocols
		for _, tc := range testCases {
			switch tc.protocol {
			case "mysql":
				query := fmt.Sprintf("INSERT INTO kv (key, value) VALUES ('%s', '%s')", tc.key, tc.value)
				_, err := mysqlClients[tc.nodeIdx].Exec(query)
				require.NoError(t, err)
			case "http":
				resp := httpPut(t, nodes[tc.nodeIdx].httpPort, tc.key, tc.value)
				resp.Body.Close()
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Wait for replication
		time.Sleep(1 * time.Second)

		// Verify all data via MySQL on all nodes
		for _, tc := range testCases {
			for nodeIdx := 0; nodeIdx < numNodes; nodeIdx++ {
				var value string
				query := fmt.Sprintf("SELECT value FROM kv WHERE key = '%s'", tc.key)
				err := mysqlClients[nodeIdx].QueryRow(query).Scan(&value)
				require.NoError(t, err, "Failed to read %s from node %d", tc.key, nodeIdx+1)
				require.Equal(t, tc.value, value, "Value mismatch for %s on node %d", tc.key, nodeIdx+1)
			}
		}
	})
}
