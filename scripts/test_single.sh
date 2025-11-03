#!/bin/bash

# Phase 2 测试脚本：启动单节点集群并测试 etcd 兼容性

set -e

echo "===== Phase 2 测试：Raft + etcd 兼容层 ====="
echo""

pre_dir=$(pwd)


l=$(pkill metastore >/dev/null 2>&1 || true)

# 清理
rm -rf raft-single-test 
mkdir -p raft-cluster-test/node1

cd $pre_dir/../
# 编译
echo "1. 编译程序..."
make build
echo "✅ 编译成功"
echo "" 
cd $pre_dir


cp $pre_dir/../metaStore raft-cluster-test/
cd raft-cluster-test
mkdir -p data/rocksdb/1


# 启动单节点
echo "2. 启动单节点集群（后台）..."
./metastore \
  --member-id=1 \
  --cluster=http://127.0.0.1:9021 \
  --port=9121 \
  --grpc-addr=:12379 \
  --storage=rocksdb \
  > node1/log.txt 2>&1 &

PID=$!
echo "节点 1 已启动 (PID: $PID)"
sleep 10

# 检查进程是否还在运行
if ! ps -p $PID > /dev/null; then
    echo "❌ 节点启动失败，查看日志："
    cat node1/log.txt
    exit 1
fi

echo "✅ 节点运行中"
echo "============================================"
echo "============================================"

# 测试 etcd 兼容性
echo "3. 测试 etcdctl..."

export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS="localhost:12379"


chmod a+x $pre_dir/../tools/etcdctl

echo "测试 Cluster 服务..."
$pre_dir/../tools/etcdctl member list --write-out=table
echo ""

echo "测试 KV 操作..."

$pre_dir/../tools/etcdctl  put single-key-2025 2025
$pre_dir/../tools/etcdctl  get single-key-2025
$pre_dir/../tools/etcdctl  get single-key-2025 --prefix

echo "✅ KV 操作测试通过"	


echo "3. 测试 etcd clientv3..."
echo "--------------------------->>>>>>>>>>>>>>>>>>>>>>>"

# 创建简单测试程序
cat > test_client.go << 'GOEOF'
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   []string{"localhost:12379"},
        DialTimeout: 5 * time.Second,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer cli.Close()

    ctx := context.Background()

    // Test Put
    fmt.Println("Testing Put...")
    _, err = cli.Put(ctx, "test-key", "test-value")
    if err != nil {
        log.Fatalf("Put failed: %v", err)
    }
    fmt.Println("✅ Put OK")
    time.Sleep(2 * time.Second) // 等待集群稳定

    // Test Get
    fmt.Println("Testing Get...")
    resp, err := cli.Get(ctx, "test-key")
    if err != nil {
        log.Fatalf("Get failed: %v", err)
    }
    if len(resp.Kvs) == 0 {
        log.Fatal("Key not found")
    }
    fmt.Printf("✅ Get OK: %s = %s\n", resp.Kvs[0].Key, resp.Kvs[0].Value)

    fmt.Println("\n✅ 所有测试通过！")
}
GOEOF

go run test_client.go
echo ""

# 清理
echo "4. 清理..."
kill $PID 2>/dev/null || true
wait $PID 2>/dev/null || true

echo ""
echo "===== Phase 2 测试完成 ====="

cd $pre_dir
rm -rf raft-cluster-test

