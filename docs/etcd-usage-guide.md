# MetaStore etcd 兼容层使用指南

本指南说明如何使用 MetaStore 的 etcd 兼容层。

## 快速开始

### 1. 启动 etcd 兼容服务器（演示模式）

```bash
# 编译
go build ./cmd/etcd-demo

# 启动服务器（默认端口 2379）
./etcd-demo

# 或指定端口和 ID
./etcd-demo -grpc-addr=:2379 -cluster-id=1 -member-id=1
```

服务器启动后，您会看到：
```
Starting MetaStore with etcd compatibility layer (demo mode)
Note: This demo version does NOT use Raft consensus
Created in-memory store
etcd-compatible gRPC server listening on [::]:2379
You can now connect with etcd clientv3:
  endpoints: []string{"localhost:2379"}
```

### 2. 使用 etcd 客户端连接

#### Go 客户端示例

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
    // 连接到 MetaStore
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   []string{"localhost:2379"},
        DialTimeout: 5 * time.Second,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer cli.Close()

    // Put 操作
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    _, err = cli.Put(ctx, "foo", "bar")
    cancel()
    if err != nil {
        log.Fatal(err)
    }

    // Get 操作
    ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
    resp, err := cli.Get(ctx, "foo")
    cancel()
    if err != nil {
        log.Fatal(err)
    }

    for _, kv := range resp.Kvs {
        fmt.Printf("%s: %s\n", kv.Key, kv.Value)
    }
}
```

#### 运行官方示例

```bash
# 编译并运行我们提供的示例
go build ./examples/etcd-client
./etcd-client
```

您会看到完整的测试输出，包括：
- Put/Get 操作
- 范围查询
- Delete 操作
- Transaction
- Watch
- Lease
- Status

## 支持的操作

### KV 操作

#### 1. Put - 存储键值对

```go
// 简单 Put
resp, err := cli.Put(ctx, "key", "value")

// Put 并返回旧值
resp, err := cli.Put(ctx, "key", "value", clientv3.WithPrevKV())

// Put 并关联 lease
resp, err := cli.Put(ctx, "key", "value", clientv3.WithLease(leaseID))
```

#### 2. Get - 查询键值

```go
// 获取单个键
resp, err := cli.Get(ctx, "key")

// 前缀查询
resp, err := cli.Get(ctx, "prefix", clientv3.WithPrefix())

// 范围查询
resp, err := cli.Get(ctx, "key1", clientv3.WithRange("key9"))

// 限制返回数量
resp, err := cli.Get(ctx, "key", clientv3.WithPrefix(), clientv3.WithLimit(10))
```

#### 3. Delete - 删除键

```go
// 删除单个键
resp, err := cli.Delete(ctx, "key")

// 删除前缀匹配的所有键
resp, err := cli.Delete(ctx, "prefix", clientv3.WithPrefix())

// 删除并返回被删除的值
resp, err := cli.Delete(ctx, "key", clientv3.WithPrevKV())
```

#### 4. Transaction - 事务操作

```go
// Compare-Then-Else 事务
resp, err := cli.Txn(ctx).
    If(clientv3.Compare(clientv3.Value("key"), "=", "old-value")).
    Then(clientv3.OpPut("key", "new-value")).
    Else(clientv3.OpGet("key")).
    Commit()

if resp.Succeeded {
    fmt.Println("Transaction succeeded")
} else {
    fmt.Println("Transaction failed")
}
```

### Watch 操作

```go
// 监听单个键
watchCh := cli.Watch(ctx, "key")

// 监听前缀
watchCh := cli.Watch(ctx, "prefix", clientv3.WithPrefix())

// 处理事件
for wresp := range watchCh {
    for _, ev := range wresp.Events {
        switch ev.Type {
        case mvccpb.PUT:
            fmt.Printf("PUT: %s = %s\n", ev.Kv.Key, ev.Kv.Value)
        case mvccpb.DELETE:
            fmt.Printf("DELETE: %s\n", ev.Kv.Key)
        }
    }
}
```

### Lease 操作

```go
// 创建 lease（10 秒 TTL）
leaseResp, err := cli.Grant(ctx, 10)
leaseID := leaseResp.ID

// Put with lease
cli.Put(ctx, "key", "value", clientv3.WithLease(leaseID))

// 单次续约
kaResp, err := cli.KeepAliveOnce(ctx, leaseID)

// 自动续约（流式）
kaCh, err := cli.KeepAlive(context.Background(), leaseID)
for ka := range kaCh {
    fmt.Printf("KeepAlive response: TTL=%d\n", ka.TTL)
}

// 撤销 lease（删除所有关联的键）
_, err = cli.Revoke(ctx, leaseID)

// 查询 lease 信息
ttlResp, err := cli.TimeToLive(ctx, leaseID, clientv3.WithAttachedKeys())
fmt.Printf("TTL: %d, Keys: %v\n", ttlResp.TTL, ttlResp.Keys)
```

### Maintenance 操作

```go
// 查询服务器状态
statusResp, err := cli.Status(ctx, "localhost:2379")
fmt.Printf("Version: %s\n", statusResp.Version)
fmt.Printf("DB Size: %d bytes\n", statusResp.DbSize)
fmt.Printf("Leader: %x\n", statusResp.Leader)

// 获取快照
snapshot, err := cli.Snapshot(ctx)
```

## 常见使用场景

### 场景 1：分布式配置管理

```go
// 存储配置
cli.Put(ctx, "/config/app/timeout", "30")
cli.Put(ctx, "/config/app/max-conn", "100")

// 读取所有配置
resp, _ := cli.Get(ctx, "/config/app/", clientv3.WithPrefix())
for _, kv := range resp.Kvs {
    fmt.Printf("%s = %s\n", kv.Key, kv.Value)
}

// 监听配置变化
watchCh := cli.Watch(context.Background(), "/config/app/", clientv3.WithPrefix())
go func() {
    for wresp := range watchCh {
        for _, ev := range wresp.Events {
            fmt.Printf("Config changed: %s = %s\n", ev.Kv.Key, ev.Kv.Value)
        }
    }
}()
```

### 场景 2：服务发现

```go
// 注册服务（使用 lease 确保服务挂掉时自动注销）
lease, _ := cli.Grant(ctx, 10)
cli.Put(ctx, "/services/api-server/node1", "10.0.0.1:8080", clientv3.WithLease(lease.ID))

// 自动续约
kaCh, _ := cli.KeepAlive(context.Background(), lease.ID)
go func() {
    for range kaCh {
        // 续约成功
    }
}()

// 发现所有服务实例
resp, _ := cli.Get(ctx, "/services/api-server/", clientv3.WithPrefix())
for _, kv := range resp.Kvs {
    fmt.Printf("Service instance: %s at %s\n", kv.Key, kv.Value)
}
```

### 场景 3：分布式锁（简化版）

```go
// 尝试获取锁
lockKey := "/locks/my-resource"
txnResp, err := cli.Txn(ctx).
    If(clientv3.Compare(clientv3.CreateRevision(lockKey), "=", 0)).
    Then(clientv3.OpPut(lockKey, "locked")).
    Commit()

if txnResp.Succeeded {
    fmt.Println("Lock acquired")

    // 执行临界区代码
    // ...

    // 释放锁
    cli.Delete(ctx, lockKey)
} else {
    fmt.Println("Lock not acquired")
}
```

## 性能建议

### 1. 使用合适的超时

```go
// 为不同操作设置不同的超时
cli, _ := clientv3.New(clientv3.Config{
    Endpoints:   []string{"localhost:2379"},
    DialTimeout: 5 * time.Second,  // 连接超时
})

// 每个请求使用独立的 context
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()
resp, err := cli.Get(ctx, "key")
```

### 2. 批量操作使用 Transaction

```go
// 不推荐：多次单独 Put
for i := 0; i < 100; i++ {
    cli.Put(ctx, fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
}

// 推荐：使用事务批量操作
ops := make([]clientv3.Op, 100)
for i := 0; i < 100; i++ {
    ops[i] = clientv3.OpPut(fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
}
cli.Txn(ctx).Then(ops...).Commit()
```

### 3. Watch 使用建议

```go
// 使用带缓冲的 channel
watchCh := cli.Watch(context.Background(), "key")

// 异步处理事件，避免阻塞
go func() {
    for wresp := range watchCh {
        for _, ev := range wresp.Events {
            // 快速处理或放入队列
            go handleEvent(ev)
        }
    }
}()
```

## 故障排查

### 连接失败

```bash
# 检查服务器是否运行
ps aux | grep etcd-demo

# 检查端口是否监听
lsof -i :2379
netstat -an | grep 2379
```

### 操作超时

```go
// 增加超时时间
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
```

### 调试模式

启动服务器时启用日志：
```bash
./etcd-demo 2>&1 | tee server.log
```

## 与官方 etcd 的差异

### 当前版本（演示模式）的限制

1. **不支持集群模式**：仅单节点运行
2. **无持久化**：重启后数据丢失
3. **简化的 Watch**：不支持历史事件回放
4. **无认证授权**：所有客户端都有完整权限

详细限制说明请参考 [limitations.md](limitations.md)。

## 下一步

- 查看 [examples/etcd-client](../../examples/etcd-client/main.go) 获取完整示例
- 阅读 [limitations.md](limitations.md) 了解当前限制
- 阅读 [etcd-compatibility-design.md](etcd-compatibility-design.md) 了解架构设计

## 反馈和贡献

如有问题或建议，欢迎提交 Issue 或 PR。
