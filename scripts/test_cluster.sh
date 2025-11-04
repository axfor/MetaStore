#!/bin/bash

# Phase 2 集群测试脚本：启动3节点集群并测试 etcd 兼容性

set -e

echo "===== Phase 2 集群测试：3节点 Raft + etcd 兼容层 ====="
echo ""

pre_dir=$(pwd)


# 清理
rm -rf raft-cluster-test
mkdir -p raft-cluster-test/{node1,node2,node3}

l=$(pkill -9 metastore >/dev/null 2>&1 || true)

cd $pre_dir/../
# 编译
echo "1. 编译程序..."
make build
echo "✅ 编译成功"
echo "" 
cd $pre_dir


cp $pre_dir/../metaStore raft-cluster-test/
cd raft-cluster-test 
mkdir -p data/rocksdb/{1,2,3}

# 启动3个节点
echo "2. 启动3节点集群（后台）..." 

CLUSTER="http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023"


# 节点 1
./metastore \
  --member-id=1 \
  --cluster=$CLUSTER \
  --port=9121 \
  --grpc-addr=:12379 \
  --storage=rocksdb \
  > node1/log.txt 2>&1 &
PID1=$!
echo "节点 1 已启动 (PID: $PID1, HTTP: 9121, gRPC: 12379)"
sleep 5

# 节点 2
./metastore \
  --member-id=2 \
  --cluster=$CLUSTER \
  --port=9122 \
  --grpc-addr=:12380 \
  --storage=rocksdb \
  > node2/log.txt 2>&1 &
PID2=$!
echo "节点 2 已启动 (PID: $PID2, HTTP: 9122, gRPC: 12380)"
sleep 5

# 节点 3
./metastore \
  --member-id=3 \
  --cluster=$CLUSTER \
  --port=9123 \
  --grpc-addr=:12381 \
  --storage=rocksdb \
  > node3/log.txt 2>&1 &
PID3=$!
echo "节点 3 已启动 (PID: $PID3, HTTP: 9123, gRPC: 12381)"

sleep 5

# 检查进程
echo ""
echo "3. 检查节点状态..."
FAILED=0

if ! ps -p $PID1 > /dev/null; then
    echo "❌ 节点 1 启动失败"
    cat node1/log.txt
    FAILED=1
fi

if ! ps -p $PID2 > /dev/null; then
    echo "❌ 节点 2 启动失败"
    cat node2/log.txt
    FAILED=1
fi

if ! ps -p $PID3 > /dev/null; then
    echo "❌ 节点 3 启动失败"
    cat node3/log.txt
    FAILED=1
fi

if [ $FAILED -eq 1 ]; then
    kill $PID1 $PID2 $PID3 2>/dev/null || true
    exit 1
fi

echo "✅ 所有节点运行中"
echo ""

# 等待 leader 选举
echo "4. 等待 Raft leader 选举..."
sleep 10
echo ""

# 测试 etcd 客户端（连接到节点1）
echo "5. 测试 etcdctl..."

export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS="localhost:12379,localhost:12380,localhost:12381"


chmod a+x $pre_dir/../tools/etcdctl

echo "测试 Cluster 服务..."
$pre_dir/../tools/etcdctl member list --write-out=table
echo ""

echo "测试 KV 操作..."
$pre_dir/../tools/etcdctl  put cluster-key-2025 2025
$pre_dir/../tools/etcdctl  get cluster-key-2025
$pre_dir/../tools/etcdctl  put cluster-key-20251 2025
$pre_dir/../tools/etcdctl  get cluster-key-20251
$pre_dir/../tools/etcdctl  get cluster --prefix
$pre_dir/../tools/etcdctl  del cluster-key-20251
$pre_dir/../tools/etcdctl  get cluster --prefix
echo "✅ KV 操作测试通过"	

echo "5. 测试 etcd clientv3..."
echo "--------------------------->>>>>>>>>>>>>>>>>>>>>>>"

cat > test_cluster.go << 'GOEOF'
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
	// 连接到所有节点
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:12379", "localhost:12380", "localhost:12381"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()

	ctx := context.Background() 
	// Test Put on node 1
	fmt.Println("Testing Put on cluster...")
	_, err = cli.Put(ctx, "cluster-key", "cluster-value")
	if err != nil {
		log.Fatalf("Put failed: %v", err)
	}
	fmt.Println("✅ Put OK")
    
    time.Sleep(2 * time.Second) // 等待集群稳定
	// Test Get (may be served by any node)
	fmt.Println("Testing Get from cluster...")
	resp, err := cli.Get(ctx, "cluster-key")
	if err != nil {
		log.Fatalf("Get failed: %v", err)
	}
	if len(resp.Kvs) == 0 {
		log.Fatal("Key not found")
	}
	fmt.Printf("✅ Get OK: %s = %s\n", resp.Kvs[0].Key, resp.Kvs[0].Value)

	// Test multiple puts
	fmt.Println("\nTesting multiple operations...")
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			log.Fatalf("Put %s failed: %v", key, err)
		}
	}
	fmt.Println("✅ Multiple Puts OK")

    time.Sleep(2 * time.Second) // 等待集群稳定
	// Test range query
	fmt.Println("\nTesting range query...")
	resp, err = cli.Get(ctx, "key", clientv3.WithPrefix())
	if err != nil {
		log.Fatalf("Range query failed: %v", err)
	}
	fmt.Printf("✅ Found %d keys\n", len(resp.Kvs))

	fmt.Println("\n✅ 所有集群测试通过！")
}
GOEOF

go run test_cluster.go
echo "============================================"
echo "============================================"

# 显示节点日志最后几行
echo "6. 节点日志摘要..."
echo "=== 节点1 ==="
tail -5 node1/log.txt
echo ""
echo "=== 节点2 ==="
tail -5 node2/log.txt
echo ""
echo "=== 节点3 ==="
tail -5 node3/log.txt
echo ""

# 清理
echo "7. 清理..."
kill $PID1 $PID2 $PID3 2>/dev/null || true
wait $PID1 $PID2 $PID3 2>/dev/null || true

echo ""
echo "===== Phase 2 集群测试完成 ====="

cd $pre_dir 
rm -rf raft-cluster-test

