#!/bin/bash

# Phase 2 测试脚本：启动单节点集群并测试 etcd 兼容性

set -e

echo "===== Phase 2 测试：Raft + etcd 兼容层 ====="
echo""

# 清理
rm -rf raft-cluster-test
mkdir -p raft-cluster-test/node1

cd /Users/bast/code/MetaStore

# 编译（只编译 memory 版本）
echo "1. 编译程序..."
CGO_ENABLED=0 go build -o raft-cluster-test/metastore ./cmd/metastore
echo "✅ 编译成功"
echo""

# 启动单节点
echo "2. 启动单节点集群（后台）..."
cd raft-cluster-test
./metastore \
  --id=1 \
  --cluster=http://127.0.0.1:9021 \
  --port=9121 \
  --grpc-addr=:12379 \
  --storage=memory \
  > node1/log.txt 2>&1 &

PID=$!
echo "节点 1 已启动 (PID: $PID)"
sleep 2

# 检查进程是否还在运行
if ! ps -p $PID > /dev/null; then
    echo "❌ 节点启动失败，查看日志："
    cat node1/log.txt
    exit 1
fi

echo "✅ 节点运行中"
echo""

# 测试 etcd 兼容性
echo "3. 测试 etcd clientv3..."
cd /Users/bast/code/MetaStore

# 创建简单测试程序
cat > raft-cluster-test/test_client.go << 'GOEOF'
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

go run raft-cluster-test/test_client.go
echo ""

# 清理
echo "4. 清理..."
kill $PID 2>/dev/null || true
wait $PID 2>/dev/null || true

echo ""
echo "===== Phase 2 测试完成 ====="

