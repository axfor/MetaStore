#!/bin/bash

# MySQL API 3-Node Cluster Integration Test
# Tests MySQL protocol in cluster with cross-protocol data consistency

set -e

# Disable proxy to avoid interference with local connections
unset http_proxy
unset https_proxy
unset all_proxy
unset HTTP_PROXY
unset HTTPS_PROXY
unset ALL_PROXY

# Use MySQL 8.0 client for compatibility
MYSQL_CMD="mysql"
if [ ! -f "$MYSQL_CMD" ]; then
    MYSQL_CMD="mysql"
fi

echo "===== MySQL API 3-Node Cluster Test ====="
echo ""

pre_dir=$(pwd)

# Kill any existing processes
pkill -9 metastore >/dev/null 2>&1 || true

# Cleanup
rm -rf mysql-cluster-test
mkdir -p mysql-cluster-test/{node1,node2,node3}

cd $pre_dir/../

# Build
echo "1. Building metastore..."
make build
echo "✅ Build successful"
echo ""

cd $pre_dir

cp $pre_dir/../metaStore mysql-cluster-test/
cd mysql-cluster-test
mkdir -p data/rocksdb/{1,2,3}

# Create config files for each node
cat > config1.yaml << 'EOF'
server:
  cluster_id: 1
  member_id: 1

  # Protocol configuration
  etcd:
    address: ":12379"
  http:
    address: ":9121"
  mysql:
    address: ":13306"
    username: "root"
    password: ""

  monitoring:
    enable_prometheus: true
    prometheus_port: 9091
  log:
    level: "info"
    encoding: "console"
    output_paths: ["stdout"]
    error_output_paths: ["stderr"]
EOF

cat > config2.yaml << 'EOF'
server:
  cluster_id: 1
  member_id: 2

  # Protocol configuration
  etcd:
    address: ":12380"
  http:
    address: ":9122"
  mysql:
    address: ":13307"
    username: "root"
    password: ""

  monitoring:
    enable_prometheus: true
    prometheus_port: 9092
  log:
    level: "info"
    encoding: "console"
    output_paths: ["stdout"]
    error_output_paths: ["stderr"]
EOF

cat > config3.yaml << 'EOF'
server:
  cluster_id: 1
  member_id: 3

  # Protocol configuration
  etcd:
    address: ":12381"
  http:
    address: ":9123"
  mysql:
    address: ":13308"
    username: "root"
    password: ""

  monitoring:
    enable_prometheus: true
    prometheus_port: 9093
  log:
    level: "info"
    encoding: "console"
    output_paths: ["stdout"]
    error_output_paths: ["stderr"]
EOF

# Start 3-node cluster
echo "2. Starting 3-node cluster with MySQL enabled..."

CLUSTER="http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023"

# Node 1
./metastore \
  -config=config1.yaml \
  -member-id=1 \
  -cluster=$CLUSTER \
  -port=9121 \
  -storage=rocksdb \
  > node1/log.txt 2>&1 &
PID1=$!
echo "Node 1 started (PID: $PID1, HTTP: 9121, gRPC: 12379, MySQL: 13306)"
sleep 5

# Node 2
./metastore \
  -config=config2.yaml \
  -member-id=2 \
  -cluster=$CLUSTER \
  -port=9122 \
  -storage=rocksdb \
  > node2/log.txt 2>&1 &
PID2=$!
echo "Node 2 started (PID: $PID2, HTTP: 9122, gRPC: 12380, MySQL: 13307)"
sleep 5

# Node 3
./metastore \
  -config=config3.yaml \
  -member-id=3 \
  -cluster=$CLUSTER \
  -port=9123 \
  -storage=rocksdb \
  > node3/log.txt 2>&1 &
PID3=$!
echo "Node 3 started (PID: $PID3, HTTP: 9123, gRPC: 12381, MySQL: 13308)"

sleep 10

# Check node status
echo ""
echo "3. Checking node status..."
FAILED=0

if ! ps -p $PID1 > /dev/null; then
    echo "❌ Node 1 startup failed"
    cat node1/log.txt
    FAILED=1
fi

if ! ps -p $PID2 > /dev/null; then
    echo "❌ Node 2 startup failed"
    cat node2/log.txt
    FAILED=1
fi

if ! ps -p $PID3 > /dev/null; then
    echo "❌ Node 3 startup failed"
    cat node3/log.txt
    FAILED=1
fi

if [ $FAILED -eq 1 ]; then
    kill $PID1 $PID2 $PID3 2>/dev/null || true
    exit 1
fi

echo "✅ All nodes running"
echo ""

# Wait for leader election
echo "4. Waiting for Raft leader election..."
sleep 10
echo "✅ Cluster ready"
echo "============================================"

# Test 1: Write to node 1 via HTTP, read from all nodes via MySQL
echo ""
echo "5. Testing cluster replication (HTTP → MySQL)..."
echo "Writing via HTTP on node 1..."
curl -X PUT http://127.0.0.1:9121/cluster-http-key -d "cluster-http-value" -s -o /dev/null
sleep 2

echo "Reading from all nodes via MySQL..."
for i in 1 2 3; do
    PORT=$((13305 + i))
    RESULT=$($MYSQL_CMD -h 127.0.0.1 -P $PORT -u root -N -B -e \
      "SELECT value FROM kv WHERE key='cluster-http-key'" 2>/dev/null || echo "ERROR")

    if [ "$RESULT" = "cluster-http-value" ]; then
        echo "  ✅ Node $i: Data replicated"
    else
        echo "  ❌ Node $i: Data not replicated (got: $RESULT)"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done

# Test 2: Write via MySQL on node 2, read from all nodes via HTTP
echo ""
echo "6. Testing MySQL write replication..."
echo "Writing via MySQL on node 2..."
$MYSQL_CMD -h 127.0.0.1 -P 13307 -u root -e \
  "INSERT INTO metastore.kv (key, value) VALUES ('cluster-mysql-key', 'cluster-mysql-value')" 2>/dev/null
sleep 2

echo "Reading from all nodes via HTTP..."
for i in 1 2 3; do
    PORT=$((9120 + i))
    RESULT=$(curl -s http://127.0.0.1:$PORT/cluster-mysql-key)

    if [ "$RESULT" = "cluster-mysql-value" ]; then
        echo "  ✅ Node $i: Data replicated"
    else
        echo "  ❌ Node $i: Data not replicated (got: $RESULT)"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done

# Test 3: Write via etcd on node 3, read from all nodes via MySQL
echo ""
echo "7. Testing etcd write replication..."
export ETCDCTL_API=3
# Don't set ETCDCTL_ENDPOINTS to avoid conflict with --endpoints flag
chmod a+x $pre_dir/../tools/etcdctl

echo "Writing via etcd on node 3..."
$pre_dir/../tools/etcdctl --endpoints=localhost:12381 put cluster-etcd-key cluster-etcd-value > /dev/null
sleep 2

echo "Reading from all nodes via MySQL..."
for i in 1 2 3; do
    PORT=$((13305 + i))
    RESULT=$($MYSQL_CMD -h 127.0.0.1 -P $PORT -u root -N -B -e \
      "SELECT value FROM metastore.kv WHERE key = 'cluster-etcd-key'" 2>/dev/null || echo "ERROR")

    if [ "$RESULT" = "cluster-etcd-value" ]; then
        echo "  ✅ Node $i: Data replicated"
    else
        echo "  ❌ Node $i: Data not replicated (got: $RESULT)"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done

# Test 4: Mixed protocol operations
echo ""
echo "8. Testing mixed protocol operations..."

# Write via different protocols on different nodes
echo "Writing via HTTP on node 1..."
curl -X PUT http://127.0.0.1:9121/mixed1 -d "value1" -s -o /dev/null

echo "Writing via MySQL on node 2..."
$MYSQL_CMD -h 127.0.0.1 -P 13307 -u root -e \
  "INSERT INTO metastore.kv (key, value) VALUES ('mixed2', 'value2')" 2>/dev/null

echo "Writing via etcd on node 3..."
$pre_dir/../tools/etcdctl --endpoints=localhost:12381 put mixed3 value3 > /dev/null

sleep 2

# Verify all data is accessible from all nodes via all protocols
echo "Verifying cross-protocol access..."

# Check via MySQL on node 1
for key in mixed1 mixed2 mixed3; do
    RESULT=$($MYSQL_CMD -h 127.0.0.1 -P 13306 -u root -N -B -e \
      "SELECT value FROM metastore.kv WHERE key = '$key'" 2>/dev/null || echo "ERROR")
    if [ -z "$RESULT" ] || [ "$RESULT" = "ERROR" ]; then
        echo "  ❌ Failed to read $key via MySQL"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done
echo "  ✅ All data accessible via MySQL"

# Check via HTTP on node 2
for key in mixed1 mixed2 mixed3; do
    RESULT=$(curl -s http://127.0.0.1:9122/$key)
    if [ -z "$RESULT" ]; then
        echo "  ❌ Failed to read $key via HTTP"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done
echo "  ✅ All data accessible via HTTP"

# Check via etcd on node 3
for key in mixed1 mixed2 mixed3; do
    RESULT=$($pre_dir/../tools/etcdctl --endpoints=localhost:12381 get $key --print-value-only)
    if [ -z "$RESULT" ]; then
        echo "  ❌ Failed to read $key via etcd"
        kill $PID1 $PID2 $PID3 2>/dev/null || true
        exit 1
    fi
done
echo "  ✅ All data accessible via etcd"

# Test 5: Go client cluster test
echo ""
echo "9. Testing Go client with cluster and cross-protocol..."

cat > test_cluster_cross.go << 'GOEOF'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
	// Connect to MySQL on all nodes
	mysqlNodes := []string{
		"root@tcp(127.0.0.1:13306)/metastore",
		"root@tcp(127.0.0.1:13307)/metastore",
		"root@tcp(127.0.0.1:13308)/metastore",
	}

	var mysqlDBs []*sql.DB
	for i, dsn := range mysqlNodes {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			log.Fatalf("Failed to connect to MySQL node %d: %v", i+1, err)
		}
		defer db.Close()
		mysqlDBs = append(mysqlDBs, db)
	}

	// Connect to etcd cluster
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:12379", "localhost:12380", "localhost:12381"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}
	defer etcdClient.Close()

	ctx := context.Background()

	// Test: Write via HTTP node 1, read from all MySQL nodes
	fmt.Println("Test 1: HTTP write → MySQL read on all nodes")
	req, _ := http.NewRequest("PUT", "http://127.0.0.1:9121/go-cluster-key",
		strings.NewReader("go-cluster-value"))
	_, err = http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("HTTP PUT failed: %v", err)
	}
	time.Sleep(1 * time.Second)

	for i, db := range mysqlDBs {
		var value string
		err := db.QueryRow("SELECT value FROM kv WHERE key = 'go-cluster-key'").Scan(&value)
		if err != nil || value != "go-cluster-value" {
			log.Fatalf("MySQL node %d read failed: got %s", i+1, value)
		}
		fmt.Printf("  ✅ MySQL node %d: OK\n", i+1)
	}

	// Test: Write via MySQL node 2, read from all etcd nodes
	fmt.Println("\nTest 2: MySQL write → etcd read")
	_, err = mysqlDBs[1].Exec("INSERT INTO kv (key, value) VALUES ('go-mysql-cluster', 'mysql-cluster-val')")
	if err != nil {
		log.Fatalf("MySQL INSERT failed: %v", err)
	}
	time.Sleep(1 * time.Second)

	resp, err := etcdClient.Get(ctx, "go-mysql-cluster")
	if err != nil || len(resp.Kvs) == 0 || string(resp.Kvs[0].Value) != "mysql-cluster-val" {
		log.Fatalf("etcd read failed")
	}
	fmt.Println("  ✅ etcd read: OK")

	// Test: Write via etcd, read from MySQL on different node
	fmt.Println("\nTest 3: etcd write → MySQL read on different node")
	_, err = etcdClient.Put(ctx, "go-etcd-cluster", "etcd-cluster-val")
	if err != nil {
		log.Fatalf("etcd PUT failed: %v", err)
	}
	time.Sleep(1 * time.Second)

	var value string
	err = mysqlDBs[2].QueryRow("SELECT value FROM kv WHERE key = 'go-etcd-cluster'").Scan(&value)
	if err != nil || value != "etcd-cluster-val" {
		log.Fatalf("MySQL node 3 read failed: got %s", value)
	}
	fmt.Println("  ✅ MySQL node 3: OK")

	fmt.Println("\n✅ All cluster cross-protocol tests passed!")
}
GOEOF

go run test_cluster_cross.go
echo ""

# Show node logs summary
echo "10. Node logs summary..."
echo "=== Node 1 ==="
tail -3 node1/log.txt | grep -E "(leader|follower|MySQL)" || tail -3 node1/log.txt
echo ""
echo "=== Node 2 ==="
tail -3 node2/log.txt | grep -E "(leader|follower|MySQL)" || tail -3 node2/log.txt
echo ""
echo "=== Node 3 ==="
tail -3 node3/log.txt | grep -E "(leader|follower|MySQL)" || tail -3 node3/log.txt
echo ""

# Cleanup
echo "11. Cleaning up..."
kill $PID1 $PID2 $PID3 2>/dev/null || true
wait $PID1 $PID2 $PID3 2>/dev/null || true

echo ""
echo "===== MySQL API 3-Node Cluster Test Complete ====="
echo ""
echo "Summary:"
echo "  ✅ HTTP → MySQL replication across 3 nodes: PASS"
echo "  ✅ MySQL → HTTP replication across 3 nodes: PASS"
echo "  ✅ etcd → MySQL replication across 3 nodes: PASS"
echo "  ✅ Mixed protocol operations: PASS"
echo "  ✅ Cluster cross-protocol Go client: PASS"
echo ""
echo "All tests passed! MySQL API is working correctly in cluster mode"
echo "with full cross-protocol data consistency."
echo ""

cd $pre_dir
rm -rf mysql-cluster-test
