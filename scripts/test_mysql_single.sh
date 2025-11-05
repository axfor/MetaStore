#!/bin/bash

# MySQL API Single Node Integration Test
# Tests MySQL protocol with cross-protocol data consistency (HTTP, etcd, MySQL)

set -e

# Disable proxy to avoid interference with local connections
unset http_proxy
unset https_proxy
unset all_proxy
unset HTTP_PROXY
unset HTTPS_PROXY
unset ALL_PROXY

echo "===== MySQL API Single Node Test ====="
echo ""

pre_dir=$(pwd)

# Kill any existing metastore processes
pkill metastore >/dev/null 2>&1 || true

# Cleanup
rm -rf mysql-single-test
mkdir -p mysql-single-test/node1

cd $pre_dir/../

# Build
echo "1. Building metastore..."
make build
echo "✅ Build successful"
echo ""

cd $pre_dir

cp $pre_dir/../metaStore mysql-single-test/
cd mysql-single-test
mkdir -p data/rocksdb/1

# Create config file with MySQL enabled
cat > config.yaml << 'EOF'
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

  log:
    level: "info"
    encoding: "console"
    output_paths: ["stdout"]
    error_output_paths: ["stderr"]
EOF

# Start single node with MySQL enabled
echo "2. Starting single node with MySQL enabled..."
./metastore \
  -config=config.yaml \
  -cluster=http://127.0.0.1:9021 \
  -port=9121 \
  -storage=rocksdb \
  > node1/log.txt 2>&1 &

PID=$!
echo "Node 1 started (PID: $PID)"
echo "  HTTP API: :9121"
echo "  gRPC API: :12379"
echo "  MySQL API: :13306"
sleep 10

# Check if process is running
if ! ps -p $PID > /dev/null; then
    echo "❌ Node startup failed, showing logs:"
    cat node1/log.txt
    exit 1
fi

echo "✅ Node is running"
echo "============================================"

# Test 1: HTTP → MySQL (cross-protocol read)
echo ""
echo "3. Testing HTTP → MySQL cross-protocol..."
echo "Writing data via HTTP API..."
curl -X PUT http://127.0.0.1:9121/http-test-key -d "http-test-value" -s -o /dev/null
sleep 1

echo "Reading data via MySQL..."
MYSQL_RESULT=$(mysql -h 127.0.0.1 -P 13306 -u root -N -B -e \
  "SELECT value FROM metastore.kv WHERE key = 'http-test-key'" 2>/dev/null || echo "ERROR")

if [ "$MYSQL_RESULT" = "http-test-value" ]; then
    echo "✅ HTTP → MySQL: Data consistency verified"
else
    echo "❌ HTTP → MySQL: Data mismatch (got: $MYSQL_RESULT)"
    kill $PID 2>/dev/null || true
    exit 1
fi

# Test 2: etcd → MySQL (cross-protocol read)
echo ""
echo "4. Testing etcd → MySQL cross-protocol..."
export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS="localhost:12379"
chmod a+x $pre_dir/../tools/etcdctl

echo "Writing data via etcd API..."
$pre_dir/../tools/etcdctl put etcd-test-key etcd-test-value > /dev/null
sleep 1

echo "Reading data via MySQL..."
MYSQL_RESULT=$(mysql -h 127.0.0.1 -P 13306 -u root -N -B -e \
  "SELECT value FROM metastore.kv WHERE key = 'etcd-test-key'" 2>/dev/null || echo "ERROR")

if [ "$MYSQL_RESULT" = "etcd-test-value" ]; then
    echo "✅ etcd → MySQL: Data consistency verified"
else
    echo "❌ etcd → MySQL: Data mismatch (got: $MYSQL_RESULT)"
    kill $PID 2>/dev/null || true
    exit 1
fi

# Test 3: MySQL → HTTP (cross-protocol read)
echo ""
echo "5. Testing MySQL → HTTP cross-protocol..."
echo "Writing data via MySQL..."
mysql -h 127.0.0.1 -P 13306 -u root -e \
  "INSERT INTO metastore.kv (key, value) VALUES ('mysql-test-key', 'mysql-test-value')" 2>/dev/null
sleep 1

echo "Reading data via HTTP API..."
HTTP_RESULT=$(curl -s http://127.0.0.1:9121/mysql-test-key)

if [ "$HTTP_RESULT" = "mysql-test-value" ]; then
    echo "✅ MySQL → HTTP: Data consistency verified"
else
    echo "❌ MySQL → HTTP: Data mismatch (got: $HTTP_RESULT)"
    kill $PID 2>/dev/null || true
    exit 1
fi

# Test 4: MySQL → etcd (cross-protocol read)
echo ""
echo "6. Testing MySQL → etcd cross-protocol..."
echo "Writing data via MySQL..."
mysql -h 127.0.0.1 -P 13306 -u root -e \
  "INSERT INTO metastore.kv (key, value) VALUES ('mysql-etcd-key', 'mysql-etcd-value')" 2>/dev/null
sleep 1

echo "Reading data via etcd API..."
ETCD_RESULT=$($pre_dir/../tools/etcdctl get mysql-etcd-key --print-value-only)

if [ "$ETCD_RESULT" = "mysql-etcd-value" ]; then
    echo "✅ MySQL → etcd: Data consistency verified"
else
    echo "❌ MySQL → etcd: Data mismatch (got: $ETCD_RESULT)"
    kill $PID 2>/dev/null || true
    exit 1
fi

# Test 5: MySQL basic operations
echo ""
echo "7. Testing MySQL basic operations..."

# Test INSERT, SELECT, UPDATE, DELETE
mysql -h 127.0.0.1 -P 13306 -u root << 'MYSQLEOF' 2>/dev/null
USE metastore;
INSERT INTO kv (key, value) VALUES ('test1', 'value1');
INSERT INTO kv (key, value) VALUES ('test2', 'value2');
UPDATE kv SET value = 'updated1' WHERE key = 'test1';
SELECT * FROM kv WHERE key = 'test1';
DELETE FROM kv WHERE key = 'test2';
MYSQLEOF

echo "✅ MySQL basic operations: PASS"

# Test 6: MySQL SHOW commands
echo ""
echo "8. Testing MySQL SHOW commands..."
mysql -h 127.0.0.1 -P 13306 -u root << 'MYSQLEOF' 2>/dev/null
SHOW DATABASES;
USE metastore;
SHOW TABLES;
DESCRIBE kv;
MYSQLEOF

echo "✅ MySQL SHOW commands: PASS"

# Test 7: Go client test with all three protocols
echo ""
echo "9. Testing Go client with cross-protocol operations..."

cat > test_cross_protocol.go << 'GOEOF'
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
	// Connect to MySQL
	mysqlDB, err := sql.Open("mysql", "root@tcp(127.0.0.1:13306)/metastore")
	if err != nil {
		log.Fatalf("Failed to connect to MySQL: %v", err)
	}
	defer mysqlDB.Close()

	// Connect to etcd
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:12379"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}
	defer etcdClient.Close()

	ctx := context.Background()

	// Test 1: Write via HTTP, read via MySQL and etcd
	fmt.Println("Test 1: HTTP write → MySQL/etcd read")
	req, _ := http.NewRequest("PUT", "http://127.0.0.1:9121/go-http-key", strings.NewReader("go-http-value"))
	_, err = http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("HTTP PUT failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Read via MySQL
	var mysqlValue string
	err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'go-http-key'").Scan(&mysqlValue)
	if err != nil || mysqlValue != "go-http-value" {
		log.Fatalf("MySQL read failed: got %s, expected go-http-value", mysqlValue)
	}
	fmt.Println("  ✅ MySQL read: OK")

	// Read via etcd
	resp, err := etcdClient.Get(ctx, "go-http-key")
	if err != nil || len(resp.Kvs) == 0 || string(resp.Kvs[0].Value) != "go-http-value" {
		log.Fatalf("etcd read failed")
	}
	fmt.Println("  ✅ etcd read: OK")

	// Test 2: Write via MySQL, read via HTTP and etcd
	fmt.Println("\nTest 2: MySQL write → HTTP/etcd read")
	_, err = mysqlDB.Exec("INSERT INTO kv (key, value) VALUES ('go-mysql-key', 'go-mysql-value')")
	if err != nil {
		log.Fatalf("MySQL INSERT failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Read via HTTP
	httpResp, err := http.Get("http://127.0.0.1:9121/go-mysql-key")
	if err != nil {
		log.Fatalf("HTTP GET failed: %v", err)
	}
	defer httpResp.Body.Close()
	buf := make([]byte, 100)
	n, _ := httpResp.Body.Read(buf)
	if string(buf[:n]) != "go-mysql-value" {
		log.Fatalf("HTTP read failed: got %s", string(buf[:n]))
	}
	fmt.Println("  ✅ HTTP read: OK")

	// Read via etcd
	resp, err = etcdClient.Get(ctx, "go-mysql-key")
	if err != nil || len(resp.Kvs) == 0 || string(resp.Kvs[0].Value) != "go-mysql-value" {
		log.Fatalf("etcd read failed")
	}
	fmt.Println("  ✅ etcd read: OK")

	// Test 3: Write via etcd, read via MySQL and HTTP
	fmt.Println("\nTest 3: etcd write → MySQL/HTTP read")
	_, err = etcdClient.Put(ctx, "go-etcd-key", "go-etcd-value")
	if err != nil {
		log.Fatalf("etcd PUT failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Read via MySQL
	err = mysqlDB.QueryRow("SELECT value FROM kv WHERE key = 'go-etcd-key'").Scan(&mysqlValue)
	if err != nil || mysqlValue != "go-etcd-value" {
		log.Fatalf("MySQL read failed: got %s", mysqlValue)
	}
	fmt.Println("  ✅ MySQL read: OK")

	// Read via HTTP
	httpResp, err = http.Get("http://127.0.0.1:9121/go-etcd-key")
	if err != nil {
		log.Fatalf("HTTP GET failed: %v", err)
	}
	defer httpResp.Body.Close()
	n, _ = httpResp.Body.Read(buf)
	if string(buf[:n]) != "go-etcd-value" {
		log.Fatalf("HTTP read failed")
	}
	fmt.Println("  ✅ HTTP read: OK")

	fmt.Println("\n✅ All cross-protocol tests passed!")
}
GOEOF

go run test_cross_protocol.go
echo ""

# Cleanup
echo "10. Cleaning up..."
kill $PID 2>/dev/null || true
wait $PID 2>/dev/null || true

echo ""
echo "===== MySQL API Single Node Test Complete ====="
echo ""
echo "Summary:"
echo "  ✅ HTTP → MySQL: Data consistency verified"
echo "  ✅ etcd → MySQL: Data consistency verified"
echo "  ✅ MySQL → HTTP: Data consistency verified"
echo "  ✅ MySQL → etcd: Data consistency verified"
echo "  ✅ MySQL basic operations: PASS"
echo "  ✅ MySQL SHOW commands: PASS"
echo "  ✅ Cross-protocol Go client: PASS"
echo ""

cd $pre_dir
rm -rf mysql-single-test
