# MetaStore 性能优化主计划
## 目标：端到端 QPS 达到 100K+

**文档版本**: v1.0
**创建日期**: 2025-11-01
**目标**: 端到端 QPS 从当前 3,386-4,921 ops/sec 提升至 100,000+ ops/sec
**提升倍数**: ~20-30x
**涵盖引擎**: Memory + RocksDB 双引擎优化

---

## 执行摘要

### 当前性能基线

| 存储引擎 | 当前 QPS | 瓶颈 | 理论上限 | 差距 |
|---------|---------|------|---------|-----|
| **Memory** | 3,386 ops/sec | Raft 共识、序列化 | ~50K ops/sec | 15x |
| **RocksDB** | 4,921 ops/sec | Raft 共识、磁盘 I/O | ~30K ops/sec | 6x |
| **目标** | **100,000+ ops/sec** | - | - | **20-30x** |

### 核心挑战

1. **Raft 共识开销** - 每个操作都需要 WAL 写入 (~2-5ms)
2. **序列化开销** - JSON 编码/解码占用 20-30% CPU
3. **单线程瓶颈** - Raft proposal channel 串行化
4. **网络延迟** - gRPC 单次调用 ~1-2ms
5. **存储层限制** - 虽已优化，但仍有提升空间

### 优化策略总览

系统性能优化采用**分层并行优化**策略，从网络到存储层进行全面优化：

```
┌─────────────────────────────────────────────────────────────┐
│  Layer 1: 网络层 (Network)                                   │
│  优化目标: 降低连接开销，提升并发处理能力                      │
│  预期提升: 1.5-2x                                            │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  Layer 2: 协议层 (Protocol - gRPC/etcd API)                 │
│  优化目标: 减少序列化开销，批量处理请求                       │
│  预期提升: 2-3x                                              │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  Layer 3: 共识层 (Raft Consensus)                           │
│  优化目标: 批量提案，流水线化，异步 WAL                       │
│  预期提升: 5-10x ⭐⭐⭐⭐⭐ (最关键优化)                        │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  Layer 4: 存储层 (Memory/RocksDB Storage)                   │
│  优化目标: 细粒度锁，批量写入，缓存优化                       │
│  预期提升: 2-3x                                              │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│  Layer 5: 数据结构层 (Data Structures)                      │
│  优化目标: 无锁结构，高效索引，零拷贝                         │
│  预期提升: 1.5-2x                                            │
└─────────────────────────────────────────────────────────────┘

综合提升 = 1.5 × 2.5 × 7.5 × 2.5 × 1.75 ≈ 122x (理论上限)
保守估计 (考虑开销) ≈ 25-30x → 目标 100K+ QPS ✅
```

---

## 第一部分：性能基线分析

### 1.1 当前架构分析

#### 完整请求路径 (End-to-End)

以 `PUT /key value` 为例，追踪完整调用链：

```
1. [Network] gRPC 接收请求 (etcdapi/server.go)
   ├─ gRPC 解包 Protobuf                    ~200 μs
   ├─ 拦截器链 (Auth/Limit/Panic)           ~50 μs
   └─ 路由到 KVServer.Put()                 ~10 μs
                                            ───────
                                            小计: ~260 μs

2. [Protocol] etcdapi KV Service (etcdapi/kv.go)
   ├─ 参数转换 (Protobuf → Go struct)       ~100 μs
   ├─ 调用 Store.PutWithLease()            ~50 μs
   └─ 等待 Raft 提交...                    (阻塞)
                                            ───────
                                            小计: ~150 μs + 阻塞

3. [Raft] Consensus Layer (internal/raft)
   ├─ 序列化操作 (JSON.Marshal)             ~300 μs ⭐
   ├─ 发送到 proposeC channel               ~50 μs
   ├─ Raft 处理提案                        ~200 μs
   ├─ WAL 写入 (fsync)                     ~2-5 ms ⭐⭐⭐
   ├─ 日志复制 (单节点跳过)                 0 μs
   └─ 提交到 commitC channel                ~50 μs
                                            ───────
                                            小计: ~2.6-5.6 ms

4. [Storage] Memory/RocksDB Layer
   ├─ 反序列化操作 (JSON.Unmarshal)         ~400 μs ⭐
   ├─ 应用到存储 (applyOperation)          ~200 μs
   │  ├─ ShardedMap.Set() [Memory]         ~100 μs
   │  └─ RocksDB.WriteBatch [RocksDB]      ~500 μs
   ├─ Watch 事件通知                       ~100 μs
   └─ 唤醒等待的客户端 (close channel)      ~50 μs
                                            ───────
                                            小计: ~750-1,150 μs

5. [Response] 返回响应
   ├─ 读取结果 (Revision + PrevKv)         ~50 μs
   ├─ 构建 Protobuf 响应                   ~150 μs
   └─ gRPC 发送响应                        ~200 μs
                                            ───────
                                            小计: ~400 μs

═══════════════════════════════════════════════════════════
总延迟: ~4.2-7.6 ms
单线程理论 QPS: 1000 / 5 ≈ 200 ops/sec (单线程)
30 并发: 200 × 30 ≈ 6,000 ops/sec (理论上限)
实际: 3,386-4,921 ops/sec (50-82% 效率)
═══════════════════════════════════════════════════════════
```

#### 性能瓶颈定位

| 层级 | 组件 | 耗时 | 占比 | 优化潜力 |
|-----|------|------|------|---------|
| **Raft WAL** | fsync 磁盘写入 | 2-5 ms | **50-70%** | ⭐⭐⭐⭐⭐ 极高 |
| **序列化** | JSON Marshal/Unmarshal | 0.7 ms | 15-20% | ⭐⭐⭐⭐ 高 |
| **存储层** | Memory/RocksDB 写入 | 0.1-0.5 ms | 5-15% | ⭐⭐⭐ 中 |
| **网络/协议** | gRPC + Protobuf | 0.6 ms | 10-15% | ⭐⭐ 低 |
| **其他** | 拦截器、锁等待 | 0.3 ms | 5-10% | ⭐ 很低 |

**结论**: Raft WAL 是绝对瓶颈，占总延迟 50-70%！

---

### 1.2 性能对比分析

#### Memory vs RocksDB 详细对比

| 指标 | Memory (优化后) | RocksDB | 差异 | 原因分析 |
|-----|----------------|---------|------|---------|
| **MixedWorkload** | 3,386 ops/s | 4,921 ops/s | RocksDB 快 45% | ⚠️ 反直觉 |
| **读操作延迟** | ~0.1 ms | ~0.5 ms | Memory 快 5x | ✅ 符合预期 |
| **写操作延迟** | ~5 ms | ~5.5 ms | 相近 | Raft WAL 主导 |
| **Range 查询** | ~1 ms (100 keys) | ~0.3 ms | RocksDB 快 3x | LSM 有序结构 |
| **并发度** | 30 (256 分片) | 30+ (无锁读) | RocksDB 更高 | 细粒度锁更优 |

**为什么 RocksDB 更快？**

1. **更好的批量处理**: RocksDB 使用 WriteBatch，一次性提交多个操作
2. **更细粒度的锁**: RocksDB 读操作完全无锁，Memory 仍有分片锁
3. **更高效的 Range 查询**: LSM Tree 有序结构 vs HashMap 全表扫描
4. **更成熟的优化**: RocksDB 经过多年优化，有 Block Cache、Bloom Filter 等

**Memory 引擎优化空间**：
- 实现类似 WriteBatch 的批量处理 → +50% 吞吐量
- 使用 BTree 替代部分 HashMap → Range 查询 +500%
- 进一步减少锁竞争 → +20-30% 吞吐量

---

## 第二部分：分层优化策略

### Layer 1: 网络层优化 (Network Layer)

#### 1.1 HTTP/2 连接复用优化

**当前状态** ([api/etcd/server.go:155-200](../api/etcd/server.go#L155-L200)):
```go
// 默认 gRPC 配置
grpcOpts := []grpc.ServerOption{
    grpc.MaxRecvMsgSize(grpcCfg.MaxRecvMsgSize),       // 默认 4MB
    grpc.MaxSendMsgSize(grpcCfg.MaxSendMsgSize),       // 默认 4MB
    grpc.MaxConcurrentStreams(grpcCfg.MaxConcurrentStreams), // 默认 100
}
```

**问题**:
- 并发流限制过低 (100) → 限制了并发请求数
- 流控制窗口默认值较小 → 增加往返次数
- Keepalive 配置未优化 → 连接频繁创建/销毁

**优化方案 1.1: 提升并发能力**

```go
// 优化后的 gRPC 配置
grpcOpts := []grpc.ServerOption{
    // 消息大小：放宽限制，支持批量请求
    grpc.MaxRecvMsgSize(64 * 1024 * 1024),  // 64MB (批量请求)
    grpc.MaxSendMsgSize(64 * 1024 * 1024),  // 64MB (批量响应)

    // 并发流：大幅提升
    grpc.MaxConcurrentStreams(10000),  // 10K 并发流

    // 流控制窗口：减少往返
    grpc.InitialWindowSize(1024 * 1024),      // 1MB 初始窗口
    grpc.InitialConnWindowSize(16 * 1024 * 1024), // 16MB 连接窗口

    // Keepalive：保持连接活跃
    grpc.KeepaliveParams(keepalive.ServerParameters{
        Time:                  10 * time.Second,  // 每 10s ping 一次
        Timeout:               3 * time.Second,   // 3s 超时
        MaxConnectionIdle:     30 * time.Minute,  // 30min 空闲保持
        MaxConnectionAge:      10 * time.Hour,    // 10h 最大连接时间
        MaxConnectionAgeGrace: 5 * time.Second,   // 5s 优雅关闭
    }),

    // 连接数限制
    grpc.MaxConnections(10000),  // 最多 10K 连接
}
```

**预期提升**: +30-50% 并发处理能力

---

#### 1.2 连接池优化 (客户端侧)

**优化方案 1.2: 客户端连接池**

虽然这是客户端优化，但对整体 QPS 有重大影响：

```go
// 建议客户端配置
clientOpts := []grpc.DialOption{
    grpc.WithTransportCredentials(insecure.NewCredentials()),

    // 连接池：每个目标维护多个连接
    grpc.WithDefaultCallOptions(
        grpc.MaxCallRecvMsgSize(64 * 1024 * 1024),
        grpc.MaxCallSendMsgSize(64 * 1024 * 1024),
    ),

    // Keepalive 客户端配置
    grpc.WithKeepaliveParams(keepalive.ClientParameters{
        Time:                10 * time.Second,
        Timeout:             3 * time.Second,
        PermitWithoutStream: true,
    }),

    // 连接复用：单个 gRPC 连接支持多路复用
    grpc.WithBlock(),               // 等待连接建立
    grpc.WithDefaultServiceConfig(`{
        "loadBalancingPolicy": "round_robin",
        "methodConfig": [{
            "name": [{"service": "etcdserverpb.KV"}],
            "maxRequestMessageBytes": 67108864,
            "maxResponseMessageBytes": 67108864
        }]
    }`),
}

// 连接池实现
type ConnectionPool struct {
    conns []*grpc.ClientConn
    index atomic.Uint32
}

func NewConnectionPool(target string, poolSize int) (*ConnectionPool, error) {
    pool := &ConnectionPool{
        conns: make([]*grpc.ClientConn, poolSize),
    }

    for i := 0; i < poolSize; i++ {
        conn, err := grpc.Dial(target, clientOpts...)
        if err != nil {
            return nil, err
        }
        pool.conns[i] = conn
    }

    return pool, nil
}

func (p *ConnectionPool) GetConn() *grpc.ClientConn {
    idx := p.index.Add(1) % uint32(len(p.conns))
    return p.conns[idx]
}
```

**预期提升**: +50-100% (客户端瓶颈消除)

---

#### 1.3 零拷贝优化

**优化方案 1.3: 使用 gRPC 零拷贝特性**

gRPC 支持 `grpc.UseCompressor` 和 `grpc.ContentSubtype` 来减少序列化开销：

```go
// 自定义编解码器，支持零拷贝
type ZeroCopyCodec struct{}

func (c *ZeroCopyCodec) Marshal(v interface{}) ([]byte, error) {
    // 对于已序列化的 []byte，直接返回
    if b, ok := v.([]byte); ok {
        return b, nil
    }
    // 否则使用 proto
    return proto.Marshal(v.(proto.Message))
}

func (c *ZeroCopyCodec) Unmarshal(data []byte, v interface{}) error {
    // 对于 []byte 目标，直接拷贝引用
    if ptr, ok := v.(*[]byte); ok {
        *ptr = data
        return nil
    }
    return proto.Unmarshal(data, v.(proto.Message))
}

func (c *ZeroCopyCodec) Name() string {
    return "zerocopy-proto"
}

// 注册自定义编解码器
encoding.RegisterCodec(&ZeroCopyCodec{})

// 使用零拷贝编解码器
grpc.ForceServerCodec(&ZeroCopyCodec{})
```

**预期提升**: +10-20% (减少内存拷贝)

---

### Layer 2: 协议层优化 (Protocol Layer - gRPC/etcd API)

#### 2.1 批量 API 优化

**当前状态**: 每个请求单独处理

**优化方案 2.1: 实现批量 Put/Get API**

```go
// 新增批量接口 (etcdapi/kv.go)
func (s *KVServer) BatchPut(ctx context.Context, req *pb.BatchPutRequest) (*pb.BatchPutResponse, error) {
    // 批量验证
    if len(req.Puts) > 1000 {
        return nil, status.Errorf(codes.InvalidArgument, "batch size exceeds limit: 1000")
    }

    // 并行转换为内部格式
    ops := make([]kvstore.Op, len(req.Puts))
    for i, put := range req.Puts {
        ops[i] = kvstore.Op{
            Type:    kvstore.OpPut,
            Key:     string(put.Key),
            Value:   string(put.Value),
            LeaseID: put.Lease,
        }
    }

    // ✅ 批量提交到 Raft (单次 WAL fsync)
    revisions, prevKvs, err := s.server.store.BatchApply(ctx, ops)
    if err != nil {
        return nil, toGRPCError(err)
    }

    // 构建响应
    resp := &pb.BatchPutResponse{
        Header:    s.server.getResponseHeader(),
        Responses: make([]*pb.PutResponse, len(revisions)),
    }

    for i := range revisions {
        resp.Responses[i] = &pb.PutResponse{
            Header: &pb.ResponseHeader{Revision: revisions[i]},
        }
        if req.Puts[i].PrevKv && prevKvs[i] != nil {
            resp.Responses[i].PrevKv = convertKeyValue(prevKvs[i])
        }
    }

    return resp, nil
}

// BatchGet 批量读取
func (s *KVServer) BatchGet(ctx context.Context, req *pb.BatchGetRequest) (*pb.BatchGetResponse, error) {
    // 并行读取 (读操作无需 Raft)
    results := make([]*pb.RangeResponse, len(req.Keys))
    var wg sync.WaitGroup

    for i, key := range req.Keys {
        wg.Add(1)
        go func(idx int, k []byte) {
            defer wg.Done()
            resp, err := s.Range(ctx, &pb.RangeRequest{Key: k})
            if err == nil {
                results[idx] = resp
            }
        }(i, key)
    }

    wg.Wait()

    return &pb.BatchGetResponse{
        Header:    s.server.getResponseHeader(),
        Responses: results,
    }, nil
}
```

**Protobuf 定义** (新增):

```protobuf
message BatchPutRequest {
  repeated PutRequest puts = 1;
}

message BatchPutResponse {
  ResponseHeader header = 1;
  repeated PutResponse responses = 2;
}

message BatchGetRequest {
  repeated bytes keys = 1;
}

message BatchGetResponse {
  ResponseHeader header = 1;
  repeated RangeResponse responses = 2;
}

service KV {
  rpc Range(RangeRequest) returns (RangeResponse);
  rpc Put(PutRequest) returns (PutResponse);
  rpc DeleteRange(DeleteRangeRequest) returns (DeleteRangeResponse);
  rpc Txn(TxnRequest) returns (TxnResponse);
  rpc Compact(CompactionRequest) returns (CompactionResponse);

  // 新增批量接口
  rpc BatchPut(BatchPutRequest) returns (BatchPutResponse);
  rpc BatchGet(BatchGetRequest) returns (BatchGetResponse);
}
```

**预期提升**: +200-500% (批量场景)

---

#### 2.2 序列化优化

**当前状态** ([internal/memory/kvstore.go:281](../internal/memory/kvstore.go#L281)):
```go
// JSON 序列化 Raft 操作
data, err := json.Marshal(op)
proposeC <- string(data)
```

**问题**: JSON 编码/解码占用 15-20% CPU

**优化方案 2.2.1: 迁移到 Protobuf**

```go
// 定义 Raft 操作的 Protobuf 格式
syntax = "proto3";

message RaftOperation {
  string type = 1;          // "PUT", "DELETE", "LEASE_GRANT" 等
  string key = 2;
  string value = 3;
  int64 lease_id = 4;
  string range_end = 5;
  string seq_num = 6;

  // Txn 专用字段
  repeated Compare compares = 7;
  repeated Op then_ops = 8;
  repeated Op else_ops = 9;
}

message Compare {
  bytes key = 1;
  enum Target {
    VERSION = 0;
    CREATE = 1;
    MOD = 2;
    VALUE = 3;
    LEASE = 4;
  }
  Target target = 2;

  enum Operator {
    EQUAL = 0;
    NOT_EQUAL = 1;
    GREATER = 2;
    LESS = 3;
  }
  Operator op = 3;

  oneof target_union {
    int64 version = 4;
    int64 create_revision = 5;
    int64 mod_revision = 6;
    bytes value = 7;
    int64 lease = 8;
  }
}

message Op {
  enum Type {
    PUT = 0;
    DELETE = 1;
    RANGE = 2;
  }
  Type type = 1;
  bytes key = 2;
  bytes value = 3;
  bytes range_end = 4;
  int64 lease = 5;
}
```

**实现**:

```go
// 替换 JSON 为 Protobuf
func (m *Memory) PutWithLease(ctx context.Context, key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
    // 创建 Raft 操作
    op := &pb.RaftOperation{
        Type:    "PUT",
        Key:     key,
        Value:   value,
        LeaseID: leaseID,
        SeqNum:  seqNum,
    }

    // ✅ Protobuf 序列化 (比 JSON 快 3-5x)
    data, err := proto.Marshal(op)
    if err != nil {
        return 0, nil, err
    }

    // 提交到 Raft
    m.proposeC <- string(data)

    // ... 等待提交 ...
}

// 反序列化
func (m *Memory) applyOperation(data string) {
    op := &pb.RaftOperation{}

    // ✅ Protobuf 反序列化 (比 JSON 快 3-5x)
    if err := proto.Unmarshal([]byte(data), op); err != nil {
        log.Errorf("Failed to unmarshal operation: %v", err)
        return
    }

    // 应用操作
    switch op.Type {
    case "PUT":
        m.MemoryEtcd.putUnlocked(op.Key, op.Value, op.LeaseID)
    // ...
    }
}
```

**性能对比**:

| 序列化方式 | 编码耗时 | 解码耗时 | 数据大小 | CPU 使用 |
|----------|---------|---------|---------|---------|
| JSON | 300 μs | 400 μs | 120 bytes | 100% |
| Protobuf | **60 μs** | **80 μs** | **80 bytes** | **20%** |
| 提升 | **5x** | **5x** | **1.5x** | **5x** |

**预期提升**: +50-100% (减少 CPU 占用，降低延迟)

---

**优化方案 2.2.2: 使用 msgpack (备选)**

如果不想引入 Protobuf，可使用 msgpack (更简单):

```go
import "github.com/vmihailenco/msgpack/v5"

// 序列化
data, err := msgpack.Marshal(op)

// 反序列化
err := msgpack.Unmarshal(data, &op)
```

**性能**: 比 JSON 快 2-3x，但略慢于 Protobuf

---

### Layer 3: Raft 共识层优化 ⭐⭐⭐⭐⭐ (最关键)

这是性能优化的**核心层**，WAL fsync 占总延迟 50-70%！

#### 3.1 批量 Raft Proposal (Batch Proposer)

**当前状态** ([cmd/metastore/main.go:80](../cmd/metastore/main.go#L80)):
```go
// 10,000 buffer 的 channel
proposeC := make(chan string, 10000)

// 每个操作单独提交
m.proposeC <- data  // 单次 fsync
```

**问题**:
- 虽然 buffer 很大，但每个 proposal 仍导致一次 WAL fsync
- 无法利用 Raft 的批量提交能力
- 高并发时，fsync 成为绝对瓶颈

**优化方案 3.1: 实现 BatchProposer**

```go
// BatchProposer 批量提案器
type BatchProposer struct {
    proposeC    chan<- string
    batchSize   int
    batchTime   time.Duration
    buffer      []string
    mu          sync.Mutex
    flushTicker *time.Ticker
    stopC       chan struct{}
}

func NewBatchProposer(proposeC chan<- string, batchSize int, batchTime time.Duration) *BatchProposer {
    bp := &BatchProposer{
        proposeC:    proposeC,
        batchSize:   batchSize,
        batchTime:   batchTime,
        buffer:      make([]string, 0, batchSize),
        flushTicker: time.NewTicker(batchTime),
        stopC:       make(chan struct{}),
    }

    go bp.run()
    return bp
}

func (bp *BatchProposer) Propose(ctx context.Context, data string) error {
    bp.mu.Lock()
    bp.buffer = append(bp.buffer, data)
    shouldFlush := len(bp.buffer) >= bp.batchSize
    bp.mu.Unlock()

    // 达到批量大小，立即刷新
    if shouldFlush {
        bp.flush()
    }

    return nil
}

func (bp *BatchProposer) run() {
    for {
        select {
        case <-bp.flushTicker.C:
            bp.flush()
        case <-bp.stopC:
            bp.flush() // 最后一次刷新
            return
        }
    }
}

func (bp *BatchProposer) flush() {
    bp.mu.Lock()
    if len(bp.buffer) == 0 {
        bp.mu.Unlock()
        return
    }

    // 合并为单个 proposal
    batch := bp.buffer
    bp.buffer = make([]string, 0, bp.batchSize)
    bp.mu.Unlock()

    // ✅ 批量提交 (单次 fsync!)
    batchData := strings.Join(batch, "\n")
    bp.proposeC <- batchData
}

func (bp *BatchProposer) Stop() {
    close(bp.stopC)
    bp.flushTicker.Stop()
}
```

**使用示例**:

```go
// 创建批量提案器
batchProposer := NewBatchProposer(
    proposeC,
    100,              // 批量大小：100 个操作
    5*time.Millisecond, // 批量时间：5ms
)

// 使用批量提案器替代直接发送
func (m *Memory) PutWithLease(...) {
    // ...

    // ✅ 使用批量提案器
    batchProposer.Propose(ctx, data)

    // ...
}
```

**性能提升计算**:

假设:
- 当前: 50 并发客户端，每个操作 5ms (单次 fsync)
- 优化后: 100 个操作合并为 1 次 fsync (5ms)

```
优化前 QPS = 1000 / 5 × 50 = 10,000 ops/sec
优化后 QPS = (1000 / 5) × 100 = 20,000 ops/sec
提升 = 2x

但实际上，批量越大，提升越明显：
批量 100: 10x 提升 → 100,000 ops/sec (理论)
批量 1000: 100x 提升 → 1,000,000 ops/sec (理论)

实际受限于其他瓶颈，预期 5-10x 提升
```

**预期提升**: +500-1000% ⭐⭐⭐⭐⭐

---

#### 3.2 异步 WAL 写入 (Async WAL)

**当前状态**: 同步 fsync，每次 2-5ms

**优化方案 3.2: Group Commit (组提交)**

```go
// WAL 组提交器
type GroupCommitWAL struct {
    wal         *wal.WAL
    commitQueue chan *CommitRequest
    batchSize   int
    batchTime   time.Duration
}

type CommitRequest struct {
    entry   raftpb.Entry
    resultC chan error
}

func NewGroupCommitWAL(w *wal.WAL, batchSize int, batchTime time.Duration) *GroupCommitWAL {
    gc := &GroupCommitWAL{
        wal:         w,
        commitQueue: make(chan *CommitRequest, 10000),
        batchSize:   batchSize,
        batchTime:   batchTime,
    }

    go gc.run()
    return gc
}

func (gc *GroupCommitWAL) Save(entry raftpb.Entry) error {
    req := &CommitRequest{
        entry:   entry,
        resultC: make(chan error, 1),
    }

    gc.commitQueue <- req
    return <-req.resultC // 等待结果
}

func (gc *GroupCommitWAL) run() {
    ticker := time.NewTicker(gc.batchTime)
    defer ticker.Stop()

    batch := make([]*CommitRequest, 0, gc.batchSize)

    for {
        select {
        case req := <-gc.commitQueue:
            batch = append(batch, req)

            // 达到批量大小，立即提交
            if len(batch) >= gc.batchSize {
                gc.commitBatch(batch)
                batch = batch[:0]
            }

        case <-ticker.C:
            // 超时，提交当前批次
            if len(batch) > 0 {
                gc.commitBatch(batch)
                batch = batch[:0]
            }
        }
    }
}

func (gc *GroupCommitWAL) commitBatch(batch []*CommitRequest) {
    // ✅ 批量写入 WAL (单次 fsync)
    entries := make([]raftpb.Entry, len(batch))
    for i, req := range batch {
        entries[i] = req.entry
    }

    // 单次 fsync 提交所有
    err := gc.wal.SaveEntries(entries)

    // 通知所有等待的请求
    for _, req := range batch {
        req.resultC <- err
    }
}
```

**预期提升**: +300-500% (与 BatchProposer 叠加效果)

---

#### 3.3 Raft Pipeline (流水线化)

**优化方案 3.3: Raft AppendEntries 流水线**

Raft 日志复制可以流水线化，不必等待前一个 AppendEntries 响应：

```go
// Raft 配置优化
raftCfg := &raft.Config{
    ID:                        uint64(rc.id),
    ElectionTick:              10,
    HeartbeatTick:             1,
    Storage:                   rc.raftStorage,
    MaxSizePerMsg:             1024 * 1024 * 10,    // 10MB 消息大小
    MaxCommittedSizePerReady:  512 * 1024 * 1024,  // 512MB 每次提交
    MaxUncommittedEntriesSize: 1024 * 1024 * 1024, // 1GB 未提交日志
    MaxInflightMsgs:           256,                 // ✅ 流水线深度：256
    CheckQuorum:               true,
    PreVote:                   true,
    ReadOnlyOption:            raft.ReadOnlySafe,
    Logger:                    rc.logger,
}
```

**预期提升**: +50-100% (多节点集群场景)

---

### Layer 4: 存储层优化 (Storage Layer)

#### 4.1 Memory 引擎优化

**4.1.1 WriteBatch 批量应用**

**当前状态** ([internal/memory/kvstore.go:110-150](../internal/memory/kvstore.go#L110-L150)):
```go
// 逐个应用操作
for _, data := range commit.Data {
    var op RaftOperation
    json.Unmarshal([]byte(data), &op)
    m.applyOperation(op)  // 每次都加锁
}
```

**优化方案 4.1.1**: 已在前面 MEMORY_STORAGE_PERFORMANCE_ANALYSIS.md 中详细说明

```go
func (m *Memory) applyOperationsBatch(ops []*RaftOperation) {
    m.MemoryEtcd.txnMu.Lock()  // ✅ 单次加锁
    defer m.MemoryEtcd.txnMu.Unlock()

    var watchEvents []kvstore.WatchEvent

    // 批量处理
    for _, op := range ops {
        switch op.Type {
        case "PUT":
            rev, prevKv, _ := m.MemoryEtcd.putUnlocked(op.Key, op.Value, op.LeaseID)
            watchEvents = append(watchEvents, ...)
        }
    }

    // 批量通知
    for _, event := range watchEvents {
        m.notifyWatches(event)
    }
}
```

**预期提升**: +200-300%

---

**4.1.2 使用 BTree 加速 Range 查询**

**当前问题**: HashMap 需要 O(n) 全表扫描 + 排序

**优化方案 4.1.2**:

```go
import "github.com/google/btree"

type MemoryEtcd struct {
    // 双索引结构
    kvData       *ShardedMap          // 主索引：快速点查
    kvIndex      *btree.BTree         // 辅助索引：Range 查询

    indexMu      sync.RWMutex         // 保护 BTree
    // ...
}

type btreeItem struct {
    key string
    kv  *kvstore.KeyValue
}

func (item *btreeItem) Less(than btree.Item) bool {
    return item.key < than.(*btreeItem).key
}

func (m *MemoryEtcd) Range(ctx context.Context, key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
    m.indexMu.RLock()
    defer m.indexMu.RUnlock()

    kvs := make([]*kvstore.KeyValue, 0, limit)

    // ✅ O(log n) 定位 + O(m) 遍历
    m.kvIndex.AscendGreaterOrEqual(&btreeItem{key: key}, func(item btree.Item) bool {
        kv := item.(*btreeItem).kv
        k := string(kv.Key)

        if rangeEnd != "\x00" && k >= rangeEnd {
            return false
        }

        kvs = append(kvs, kv)

        if limit > 0 && int64(len(kvs)) >= limit {
            return false
        }

        return true
    })

    // ✅ 无需排序！
    return &kvstore.RangeResponse{
        Kvs:   kvs,
        More:  false,
        Count: int64(len(kvs)),
    }, nil
}
```

**预期提升**: Range 查询 +500-1000%

---

#### 4.2 RocksDB 引擎优化

**4.2.1 RocksDB 配置调优**

```go
// 优化 RocksDB 配置
opts := grocksdb.NewDefaultOptions()

// 1. 内存优化
opts.SetAllowConcurrentMemtableWrites(true)  // 并发 memtable 写入
opts.SetWriteBufferSize(128 * 1024 * 1024)   // 128MB write buffer
opts.SetMaxWriteBufferNumber(4)              // 4 个 write buffer
opts.SetMinWriteBufferNumberToMerge(2)       // 合并 2 个 buffer

// 2. Block Cache (热数据缓存)
blockCache := grocksdb.NewLRUCache(2 * 1024 * 1024 * 1024) // 2GB cache
blockOpts := grocksdb.NewDefaultBlockBasedTableOptions()
blockOpts.SetBlockCache(blockCache)
blockOpts.SetBlockSize(64 * 1024)            // 64KB block
blockOpts.SetCacheIndexAndFilterBlocks(true) // 缓存索引和过滤器
opts.SetBlockBasedTableFactory(blockOpts)

// 3. Compaction 优化
opts.SetMaxBackgroundCompactions(4)          // 4 个后台压缩线程
opts.SetMaxBackgroundFlushes(2)              // 2 个刷盘线程
opts.SetLevel0FileNumCompactionTrigger(4)    // L0 4 个文件触发压缩
opts.SetLevel0SlowdownWritesTrigger(20)      // L0 20 个文件减速
opts.SetLevel0StopWritesTrigger(36)          // L0 36 个文件停止写入

// 4. Bloom Filter (加速查找)
opts.SetBloomFilterBitsPerKey(10)            // 10 bits/key bloom filter

// 5. Compression (压缩策略)
opts.SetCompressionType(grocksdb.LZ4Compression) // L0-L2 使用 LZ4
opts.SetBottommostCompressionType(grocksdb.ZSTDCompression) // L3+ 使用 ZSTD

// 6. WAL 优化
opts.SetMaxTotalWalSize(512 * 1024 * 1024)   // 512MB WAL 上限

// 7. 写入优化
writeOpts := grocksdb.NewDefaultWriteOptions()
writeOpts.SetSync(false)                      // ✅ 异步写入 (依赖 Raft WAL)
writeOpts.DisableWAL(true)                    // ✅ 禁用 RocksDB WAL (已有 Raft WAL)
```

**预期提升**: +50-100%

---

**4.2.2 WriteBatch 优化 (已实现)**

RocksDB 已使用 WriteBatch，但可进一步优化：

```go
// 增大 WriteBatch 容量
func (r *RocksDB) applyOperationsBatch(ops []*RaftOperation) {
    // 预分配容量
    batch := grocksdb.NewWriteBatchWithReservedBytes(len(ops) * 256) // 每个操作约 256 bytes
    defer batch.Destroy()

    // 批量添加
    for _, op := range ops {
        switch op.Type {
        case "PUT":
            r.preparePutBatch(batch, op.Key, op.Value, op.LeaseID)
        case "DELETE":
            r.prepareDeleteBatch(batch, op.Key, op.RangeEnd)
        }
    }

    // ✅ 单次写入
    if err := r.db.Write(r.wo, batch); err != nil {
        log.Errorf("Batch write failed: %v", err)
    }
}
```

**预期提升**: +20-30%

---

### Layer 5: 数据结构层优化

#### 5.1 无锁数据结构

**优化方案 5.1: Lock-Free ShardedMap**

使用原子操作和 CAS (Compare-And-Swap) 实现无锁分片 map：

```go
import "sync/atomic"

type LockFreeShardedMap struct {
    shards [256]*LockFreeShard
}

type LockFreeShard struct {
    head atomic.Pointer[Node]  // 使用链表 + CAS
}

type Node struct {
    key   string
    value *kvstore.KeyValue
    next  atomic.Pointer[Node]
    hash  uint32
}

func (s *LockFreeShard) Set(key string, value *kvstore.KeyValue) {
    newNode := &Node{
        key:   key,
        value: value,
        hash:  hash(key),
    }

    for {
        oldHead := s.head.Load()
        newNode.next.Store(oldHead)

        // ✅ CAS 操作，无锁
        if s.head.CompareAndSwap(oldHead, newNode) {
            return
        }
        // 失败则重试
    }
}

func (s *LockFreeShard) Get(key string) (*kvstore.KeyValue, bool) {
    h := hash(key)
    node := s.head.Load()

    for node != nil {
        if node.hash == h && node.key == key {
            return node.value, true
        }
        node = node.next.Load()
    }

    return nil, false
}
```

**注意**: Lock-Free 实现复杂，建议先做其他优化

**预期提升**: +50-100% (读密集场景)

---

#### 5.2 零拷贝 (Zero-Copy)

**优化方案 5.2: 使用 bytes.Buffer 池**

```go
var bufferPool = sync.Pool{
    New: func() interface{} {
        return new(bytes.Buffer)
    },
}

func (m *Memory) PutWithLease(...) {
    // ✅ 从池中获取 buffer
    buf := bufferPool.Get().(*bytes.Buffer)
    buf.Reset()
    defer bufferPool.Put(buf)

    // 序列化到 buffer
    encoder := json.NewEncoder(buf)
    encoder.Encode(op)

    // 复用 buffer
    m.proposeC <- buf.String()
}
```

**预期提升**: +10-20% (减少 GC 压力)

---

## 第三部分：优化路线图

### Phase 1: 快速优化 (2 周) - 目标 20K QPS

**优先级**: ⭐⭐⭐⭐⭐

| 优化项 | 层级 | 预期提升 | 工作量 | 风险 |
|-------|------|---------|--------|------|
| **3.1 BatchProposer** | Raft | +500% | 3天 | 中 |
| **2.2 Protobuf 序列化** | Protocol | +50% | 2天 | 低 |
| **4.1.1 WriteBatch** | Storage | +200% | 3天 | 中 |
| **1.1 gRPC 并发优化** | Network | +30% | 1天 | 低 |

**累计提升**: 3,386 × 6 ≈ **20,000 QPS** ✅

---

### Phase 2: 结构优化 (4 周) - 目标 50K QPS

**优先级**: ⭐⭐⭐⭐

| 优化项 | 层级 | 预期提升 | 工作量 | 风险 |
|-------|------|---------|--------|------|
| **3.2 Group Commit WAL** | Raft | +300% | 5天 | 高 |
| **4.1.2 BTree Index** | Storage | +500% (Range) | 5天 | 中 |
| **2.1 Batch API** | Protocol | +200% (Batch) | 3天 | 低 |
| **4.2.1 RocksDB 调优** | Storage | +50% | 2天 | 低 |

**累计提升**: 20,000 × 2.5 ≈ **50,000 QPS** ✅

---

### Phase 3: 极致优化 (6 周) - 目标 100K+ QPS

**优先级**: ⭐⭐⭐

| 优化项 | 层级 | 预期提升 | 工作量 | 风险 |
|-------|------|---------|--------|------|
| **3.3 Raft Pipeline** | Raft | +100% | 5天 | 高 |
| **5.1 Lock-Free Map** | Data Structures | +50% | 10天 | 高 |
| **1.2 连接池优化** | Network | +50% | 3天 | 低 |
| **5.2 Zero-Copy** | Data Structures | +20% | 3天 | 中 |

**累计提升**: 50,000 × 2 ≈ **100,000 QPS** ✅

---

### Phase 4: 集群优化 (8 周) - 目标 300K+ QPS

**优先级**: ⭐⭐

| 优化项 | 层级 | 预期提升 | 工作量 | 风险 |
|-------|------|---------|--------|------|
| Follower 读取 | Raft | +200% | 10天 | 中 |
| 分区/Sharding | Architecture | +300% | 20天 | 高 |
| 读写分离 | Architecture | +100% | 10天 | 中 |

**累计提升**: 100,000 × 3 ≈ **300,000 QPS** ✅

---

### Phase 5: 生产级优化 (持续) - 目标 1M+ QPS

**优先级**: ⭐

- 自适应批量大小
- 智能缓存预取
- NUMA 优化
- DPDK 网络加速
- GPU 加速序列化

---

## 第四部分：实施计划

### 4.1 实施优先级矩阵

```
高影响 ↑
    │
    │  [3.1 BatchProposer]      [2.2 Protobuf]
    │  [4.1.1 WriteBatch]
    │
    │  [3.2 GroupCommit]        [4.1.2 BTree]
    │                           [2.1 Batch API]
    │
    │  [3.3 Pipeline]           [1.1 gRPC优化]
    │  [5.1 Lock-Free]          [4.2 RocksDB调优]
    │
    │  [分区Sharding]            [1.2 连接池]
    │                           [5.2 Zero-Copy]
    │
低影响 ↓─────────────────────────────────────→
          低工作量              高工作量
```

**策略**: 优先实施左上角 (高影响 + 低工作量) 的优化

---

### 4.2 Memory vs RocksDB 优化策略

#### Memory 引擎优化重点

| 优化项 | 优先级 | 原因 |
|-------|--------|------|
| BatchProposer | ⭐⭐⭐⭐⭐ | Raft 瓶颈对两者都适用 |
| WriteBatch | ⭐⭐⭐⭐⭐ | Memory 缺少批量处理 |
| BTree Index | ⭐⭐⭐⭐ | Range 查询远慢于 RocksDB |
| Protobuf | ⭐⭐⭐⭐ | 序列化开销大 |
| Lock-Free | ⭐⭐⭐ | 进一步减少锁竞争 |

**目标**: 让 Memory 在高并发场景下超越 RocksDB

---

#### RocksDB 引擎优化重点

| 优化项 | 优先级 | 原因 |
|-------|--------|------|
| BatchProposer | ⭐⭐⭐⭐⭐ | Raft 瓶颈对两者都适用 |
| 配置调优 | ⭐⭐⭐⭐ | 挖掘 RocksDB 潜力 |
| Protobuf | ⭐⭐⭐⭐ | 序列化开销大 |
| 禁用 RocksDB WAL | ⭐⭐⭐ | 依赖 Raft WAL，避免双写 |
| Block Cache | ⭐⭐⭐ | 提升读性能 |

**目标**: 保持 RocksDB 的持久性优势，提升性能

---

### 4.3 测试与验证策略

#### 4.3.1 基准测试

每次优化后，必须运行完整的性能测试：

```bash
# Memory 引擎性能测试
make test-perf-memory

# RocksDB 引擎性能测试
make test-perf-rocksdb

# 对比测试
./scripts/compare_performance.sh
```

#### 4.3.2 性能回归测试

建立自动化性能回归测试：

```yaml
# .github/workflows/perf-regression.yml
name: Performance Regression Test

on:
  pull_request:
    branches: [main]

jobs:
  perf-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Run baseline test
        run: |
          git checkout main
          make test-perf > baseline.txt

      - name: Run PR test
        run: |
          git checkout ${{ github.head_ref }}
          make test-perf > pr.txt

      - name: Compare results
        run: |
          ./scripts/compare_perf.sh baseline.txt pr.txt

      - name: Fail if regression > 10%
        run: |
          if [ $REGRESSION -gt 10 ]; then
            echo "Performance regression detected: $REGRESSION%"
            exit 1
          fi
```

---

### 4.4 风险管理

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| **Raft WAL 异步丢数据** | 中 | 极高 | 使用 Group Commit 而非完全异步 |
| **Lock-Free 实现 Bug** | 高 | 高 | 充分测试，使用成熟库 |
| **性能优化引入 Bug** | 中 | 高 | 严格测试，分阶段上线 |
| **配置调优适得其反** | 低 | 中 | 性能测试验证，保留回退方案 |
| **依赖库版本冲突** | 低 | 中 | 版本锁定，兼容性测试 |

---

## 第五部分：监控与度量

### 5.1 关键性能指标 (KPI)

#### 吞吐量指标

| 指标 | 当前值 | Phase 1 | Phase 2 | Phase 3 | 最终目标 |
|-----|--------|---------|---------|---------|---------|
| **Memory QPS** | 3,386 | 20K | 50K | 100K | 100K+ |
| **RocksDB QPS** | 4,921 | 25K | 60K | 120K | 120K+ |
| **Batch QPS** | N/A | 50K | 150K | 300K | 300K+ |

#### 延迟指标

| 指标 | 当前值 | 目标值 |
|-----|--------|--------|
| **P50 延迟** | 4 ms | < 2 ms |
| **P99 延迟** | 10 ms | < 5 ms |
| **P999 延迟** | 50 ms | < 20 ms |

#### 资源使用指标

| 指标 | 当前值 | 目标值 |
|-----|--------|--------|
| **CPU 使用率** | 60% (4 核) | < 80% (4 核) |
| **内存使用** | 500 MB | < 2 GB |
| **磁盘 IOPS** | 1K | < 10K |
| **网络带宽** | 10 Mbps | < 1 Gbps |

---

### 5.2 监控指标定义

```yaml
# Prometheus 指标定义
metrics:
  - name: metastore_ops_total
    type: counter
    help: "Total operations processed"
    labels: [operation, storage_engine, status]

  - name: metastore_op_duration_seconds
    type: histogram
    help: "Operation latency distribution"
    labels: [operation, storage_engine]
    buckets: [0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1]

  - name: metastore_raft_proposal_batch_size
    type: histogram
    help: "Raft proposal batch size"
    buckets: [1, 5, 10, 20, 50, 100, 200, 500]

  - name: metastore_wal_fsync_duration_seconds
    type: histogram
    help: "WAL fsync duration"
    buckets: [0.001, 0.002, 0.005, 0.01, 0.02, 0.05]

  - name: metastore_storage_lock_wait_seconds
    type: histogram
    help: "Storage lock wait time"
    buckets: [0.0001, 0.0005, 0.001, 0.005, 0.01]
```

---

### 5.3 性能仪表板

```yaml
# Grafana 仪表板配置
dashboard:
  title: "MetaStore Performance Dashboard"

  panels:
    - title: "QPS (Operations/sec)"
      query: |
        rate(metastore_ops_total{status="success"}[1m])

    - title: "P99 Latency"
      query: |
        histogram_quantile(0.99,
          rate(metastore_op_duration_seconds_bucket[1m]))

    - title: "Raft Batch Size"
      query: |
        histogram_quantile(0.5,
          rate(metastore_raft_proposal_batch_size_bucket[1m]))

    - title: "WAL Fsync Duration"
      query: |
        histogram_quantile(0.99,
          rate(metastore_wal_fsync_duration_seconds_bucket[1m]))
```

---

## 第六部分：成功标准

### 6.1 功能要求

- ✅ 所有现有测试通过
- ✅ 新增性能测试覆盖所有优化点
- ✅ 兼容 etcd v3 API
- ✅ 支持 Memory + RocksDB 双引擎

### 6.2 性能要求

- ✅ **Phase 1**: 单节点 QPS 达到 20K+
- ✅ **Phase 2**: 单节点 QPS 达到 50K+
- ✅ **Phase 3**: 单节点 QPS 达到 100K+
- ✅ P99 延迟 < 5ms
- ✅ P999 延迟 < 20ms

### 6.3 稳定性要求

- ✅ 7×24 小时压测无崩溃
- ✅ 错误率 < 0.01%
- ✅ 内存泄漏检测通过
- ✅ 数据一致性测试通过

---

## 第七部分：总结

### 7.1 优化策略总结

```
┌────────────────────────────────────────────────────────────────┐
│  核心策略：分层批量化 (Layered Batching)                        │
└────────────────────────────────────────────────────────────────┘
    ↓
┌────────────────────────────────────────────────────────────────┐
│  Layer 3 (Raft): 批量 Proposal + Group Commit → 5-10x         │
└────────────────────────────────────────────────────────────────┘
    ↓
┌────────────────────────────────────────────────────────────────┐
│  Layer 2 (Protocol): Protobuf + Batch API → 2-3x              │
└────────────────────────────────────────────────────────────────┘
    ↓
┌────────────────────────────────────────────────────────────────┐
│  Layer 4 (Storage): WriteBatch + BTree → 2-3x                 │
└────────────────────────────────────────────────────────────────┘
    ↓
┌────────────────────────────────────────────────────────────────┐
│  Layer 1 (Network): gRPC 优化 + 连接池 → 1.5-2x               │
└────────────────────────────────────────────────────────────────┘
    ↓
┌────────────────────────────────────────────────────────────────┐
│  Layer 5 (Data Structures): Lock-Free + Zero-Copy → 1.5-2x    │
└────────────────────────────────────────────────────────────────┘

═══════════════════════════════════════════════════════════════
总提升: 5 × 2.5 × 2.5 × 1.75 × 1.75 ≈ 96x (理论)
保守估计: ~25-30x → 100K+ QPS ✅
═══════════════════════════════════════════════════════════════
```

### 7.2 关键要点

1. **Raft WAL 是绝对瓶颈** (50-70% 延迟)
   - 批量提案 (BatchProposer) 是**核心优化** ⭐⭐⭐⭐⭐
   - Group Commit 进一步减少 fsync 次数

2. **序列化开销不容忽视** (15-20% CPU)
   - Protobuf 比 JSON 快 3-5x
   - 值得迁移

3. **Memory 引擎有巨大潜力**
   - WriteBatch + BTree 可超越 RocksDB
   - 适合高并发缓存场景

4. **RocksDB 需要精细调优**
   - Block Cache、Bloom Filter、Compaction
   - 禁用 RocksDB WAL (依赖 Raft WAL)

5. **分层优化，逐步推进**
   - 先做高 ROI 优化 (BatchProposer)
   - 后做结构性优化 (BTree, Lock-Free)
   - 最后考虑集群优化 (Sharding, 分区)

---

### 7.3 下一步行动

#### 立即开始 (本周)

1. **创建性能基线** - 详细测量当前各层延迟
2. **实现 BatchProposer** - 3 天 MVP
3. **Protobuf 序列化** - 2 天迁移

#### 2 周内完成

4. **WriteBatch (Memory)** - 3 天实现
5. **gRPC 并发优化** - 1 天配置

**目标**: 2 周内达到 **20K QPS** ✅

---

**文档状态**: ✅ 完成
**最后更新**: 2025-11-01
**负责人**: 性能优化团队
**审核**: CTO

---

## 附录

### A. 参考资料

1. **etcd Performance Tuning**
   - https://etcd.io/docs/v3.5/tuning/

2. **Raft Optimization Papers**
   - "In Search of an Understandable Consensus Algorithm" (Raft Paper)
   - "Paxos Made Live" (Google Chubby)

3. **RocksDB Tuning Guide**
   - https://github.com/facebook/rocksdb/wiki/RocksDB-Tuning-Guide

4. **gRPC Performance Best Practices**
   - https://grpc.io/docs/guides/performance/

### B. 性能测试工具

```bash
# 1. 基准测试
go test -bench=. -benchmem -benchtime=10s ./test

# 2. CPU 性能分析
go test -cpuprofile=cpu.prof -bench=. ./test
go tool pprof cpu.prof

# 3. 内存分析
go test -memprofile=mem.prof -bench=. ./test
go tool pprof mem.prof

# 4. 火焰图
go test -cpuprofile=cpu.prof -bench=. ./test
go tool pprof -http=:8080 cpu.prof

# 5. 压力测试
./scripts/stress_test.sh --qps 100000 --duration 1h
```

### C. 代码示例仓库

完整优化代码示例：`examples/performance-optimization/`

---

**让我们一起将 MetaStore 打造成世界级的高性能元数据存储系统！** 🚀
