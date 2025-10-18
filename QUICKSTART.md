# 快速开始指南

## 1. 编译项目

### 默认构建（推荐）
```bash
go build -o store.exe
```

构建完成后，你会得到一个24MB的单二进制可执行文件。

## 2. 启动单节点集群

```bash
./store.exe --id 1 --cluster http://127.0.0.1:12379 --port 12380
```

你会看到类似的输出：
```
2025/10/17 16:00:00 Starting with memory + WAL storage
2025/10/17 16:00:00 replaying WAL of member 1
...
raft2025/10/17 16:00:02 INFO: 1 became leader at term 2
```

## 3. 使用HTTP API

### 写入数据

```bash
curl -L http://127.0.0.1:12380/user:1001 -XPUT -d "Alice"
curl -L http://127.0.0.1:12380/user:1002 -XPUT -d "Bob"
curl -L http://127.0.0.1:12380/order:5001 -XPUT -d "pending"
```

响应：`HTTP 204 No Content` (成功)

### 读取数据

```bash
curl -L http://127.0.0.1:12380/user:1001
# 输出: Alice

curl -L http://127.0.0.1:12380/order:5001
# 输出: pending
```

### 不存在的键

```bash
curl -L http://127.0.0.1:12380/nonexistent
# 输出: HTTP 404 Not Found
```

## 4. 启动三节点集群

打开三个终端：

**终端 1:**
```bash
./store.exe --id 1 \
  --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
  --port 12380
```

**终端 2:**
```bash
./store.exe --id 2 \
  --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
  --port 22380
```

**终端 3:**
```bash
./store.exe --id 3 \
  --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
  --port 32380
```

等待选举完成：
```
raft2025/10/17 16:00:05 INFO: 1 became leader at term 2
```

## 5. 测试集群功能

### 写入到任意节点

```bash
curl -L http://127.0.0.1:12380/test-key -XPUT -d "value-from-node1"
```

### 从其他节点读取

```bash
curl -L http://127.0.0.1:22380/test-key
# 输出: value-from-node1

curl -L http://127.0.0.1:32380/test-key
# 输出: value-from-node1
```

数据已经通过Raft共识复制到所有节点！

## 6. 测试故障容错

### 停止一个节点

在终端2中按 `Ctrl+C` 停止节点2。

### 验证集群仍可用

```bash
curl -L http://127.0.0.1:12380/fault-test -XPUT -d "cluster-still-works"
curl -L http://127.0.0.1:32380/fault-test
# 输出: cluster-still-works
```

即使丢失一个节点，3节点集群仍可用（可容忍1个故障）。

### 恢复节点

重新启动节点2：
```bash
./store.exe --id 2 \
  --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 \
  --port 22380
```

节点2会自动从其他节点同步数据。

```bash
curl -L http://127.0.0.1:22380/fault-test
# 输出: cluster-still-works
```

## 7. 动态添加节点

### 提议添加节点4

```bash
curl -L http://127.0.0.1:12380/4 -XPOST -d http://127.0.0.1:42379
```

### 启动新节点（使用--join标志）

```bash
./store.exe --id 4 \
  --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379,http://127.0.0.1:42379 \
  --port 42380 \
  --join
```

### 验证新节点

```bash
curl -L http://127.0.0.1:42380/test-key
# 输出: value-from-node1
```

新节点已加入集群并同步了所有数据！

## 8. 移除节点

```bash
curl -L http://127.0.0.1:12380/3 -XDELETE
```

节点3会自动关闭。

## 9. 查看持久化数据

```bash
ls -lh store-*
```

你会看到：
- `store-1/` - 节点1的WAL日志
- `store-1-snap/` - 节点1的快照
- `store-2/`, `store-3/`, `store-4/` - 其他节点的数据

## 10. 清理

```bash
rm -rf store-*
./store.exe
```

## 故障排查

### 端口已被占用
```
store: Failed to listen rafthttp (bind: address already in use)
```

解决：更改端口或杀掉占用端口的进程。

### 无法连接到其他节点
```
failed to send message to 2 (error: dial tcp: connection refused)
```

确保所有节点的cluster参数相同，并且都已启动。

### 快照失败
确保有足够的磁盘空间用于存储WAL和快照。

## 下一步

- 阅读 [README.md](README.md) 了解更多功能
- 查看 [IMPLEMENTATION.md](IMPLEMENTATION.md) 了解实现细节
- 如需RocksDB支持，参考README中的RocksDB构建指南

## 性能提示

1. **写入批量化**: 使用脚本批量PUT可提高吞吐量
2. **就近读取**: 读取可以从任何节点进行，选择网络最近的节点
3. **集群大小**: 3-5个节点是推荐的集群大小（平衡性能和可用性）
4. **快照频率**: 默认10000条日志触发快照，可调整以平衡性能和恢复时间

## 示例场景

### 配置管理
```bash
curl -L http://127.0.0.1:12380/config/database/host -XPUT -d "db.example.com"
curl -L http://127.0.0.1:12380/config/database/port -XPUT -d "5432"
curl -L http://127.0.0.1:12380/config/cache/ttl -XPUT -d "3600"
```

### 分布式锁（简单实现）
```bash
# 获取锁
curl -L http://127.0.0.1:12380/lock/resource1 -XPUT -d "node-a-$timestamp"

# 检查锁
curl -L http://127.0.0.1:12380/lock/resource1

# 释放锁（删除键）
# 注意：当前实现不支持DELETE键，只支持DELETE节点
```

### 服务发现
```bash
curl -L http://127.0.0.1:12380/services/api/node1 -XPUT -d "http://10.0.1.1:8080"
curl -L http://127.0.0.1:12380/services/api/node2 -XPUT -d "http://10.0.1.2:8080"
```

享受你的分布式KV存储之旅！
