# MetaStore Raft 流量优化实施路线图

## 快速参考

### 当前性能
- 吞吐量: 796 ops/sec
- 瓶颈: WAL fsync (1-5ms per operation)
- 根本原因: 每个 Raft Ready 都触发一次磁盘同步

### 优化目标
- 吞吐量: 25,000+ ops/sec (30x 提升)
- 完成时间: 2-3 周
- 风险等级: 低

---

## Phase 1: 关键路径优化 (1-2 周)

### 1.1 重新启用批量 Proposal (1 周)

**状态**: 已设计，部分禁用  
**收益**: 2-5x 吞吐量提升  
**风险**: 低  

#### 步骤 1: 取消禁用
```bash
# cmd/metastore/main.go:23
# 从:
// "metaStore/internal/batch" // 已禁用 BatchProposer

# 改为:
"metaStore/internal/batch"
```

#### 步骤 2: 集成 BatchProposer

**关键文件需要修改**:
- `/internal/memory/kvstore.go` - 集成 BatchProposer
- `/internal/rocksdb/kvstore.go` - 支持批量 Apply
- `/cmd/metastore/main.go` - 配置初始化

**代码实现框架**:
```go
// internal/batch/batch_proposer.go
package batch

type BatchProposer struct {
    inputC      <-chan string
    outputC     chan<- string
    batch       []string
    maxBatchSize int
    maxBatchTime time.Duration
    mu          sync.Mutex
}

// NewBatchProposer 创建新的批量提交器
func NewBatchProposer(inputC <-chan string, outputC chan<- string, 
                      maxSize int, maxTime time.Duration) *BatchProposer {
    return &BatchProposer{
        inputC:       inputC,
        outputC:      outputC,
        batch:        make([]string, 0, maxSize),
        maxBatchSize: maxSize,
        maxBatchTime: maxTime,
    }
}

// Run 启动批量处理循环
func (bp *BatchProposer) Run(stopC <-chan struct{}) {
    ticker := time.NewTicker(bp.maxBatchTime)
    defer ticker.Stop()

    for {
        select {
        case proposal, ok := <-bp.inputC:
            if !ok {
                bp.flush()
                return
            }
            
            bp.mu.Lock()
            bp.batch = append(bp.batch, proposal)
            shouldFlush := len(bp.batch) >= bp.maxBatchSize
            bp.mu.Unlock()

            if shouldFlush {
                bp.flush()
            }

        case <-ticker.C:
            bp.flush()

        case <-stopC:
            bp.flush()
            return
        }
    }
}

// flush 发送批量 proposal
func (bp *BatchProposer) flush() {
    bp.mu.Lock()
    if len(bp.batch) == 0 {
        bp.mu.Unlock()
        return
    }

    // 使用特殊分隔符合并操作
    combined := strings.Join(bp.batch, "|BATCH_SEP|")
    bp.batch = bp.batch[:0]
    bp.mu.Unlock()

    select {
    case bp.outputC <- combined:
    case <-time.After(5 * time.Second):
        log.Warn("Batch proposal timeout")
    }
}
```

#### 步骤 3: 修改 kvstore Apply 层
```go
// internal/memory/kvstore.go

func (m *Memory) applyOperation(op RaftOperation) {
    // 检测批量操作标记
    if strings.Contains(op.Data, "|BATCH_SEP|") {
        ops := strings.Split(op.Data, "|BATCH_SEP|")
        for _, opData := range ops {
            m.applySingleOp(opData)
        }
    } else {
        m.applySingleOp(op.Data)
    }
}

func (m *Memory) applySingleOp(data string) {
    // 原有的单个操作处理逻辑
}
```

#### 步骤 4: 配置支持
```yaml
# configs/config.yaml

raft:
  # 批量提交配置
  enable_batch_proposal: true
  batch_max_size: 50          # 批大小
  batch_max_time: 2ms         # 最大等待时间
```

#### 步骤 5: 测试验证
```bash
# 运行性能测试
go test -bench=BenchmarkPut -run=^$ ./test -v

# 预期结果:
# Before: ~796 ops/sec
# After:  ~3,980 ops/sec (5x improvement)
```

---

### 1.2 消息压缩实现 (4-5 天)

**状态**: 未实现  
**收益**: 1.5-2x 吞吐量 + 网络带宽 -70%  
**风险**: 低 (完全可选)  

#### 步骤 1: 创建压缩框架

```go
// internal/raft/compression/codec.go
package compression

import (
    "github.com/golang/snappy"
    "github.com/klauspost/compress/zstd"
)

type Codec interface {
    Name() string
    Compress(data []byte) ([]byte, error)
    Decompress(data []byte) ([]byte, error)
}

// SnappyCodec 快速压缩
type SnappyCodec struct{}

func (c *SnappyCodec) Name() string { return "snappy" }

func (c *SnappyCodec) Compress(data []byte) ([]byte, error) {
    return snappy.Encode(nil, data), nil
}

func (c *SnappyCodec) Decompress(data []byte) ([]byte, error) {
    return snappy.Decode(nil, data)
}

// ZstdCodec 高效压缩
type ZstdCodec struct {
    enc *zstd.Encoder
    dec *zstd.Decoder
}

func NewZstdCodec() (*ZstdCodec, error) {
    enc, _ := zstd.NewEncoder(nil)
    dec, _ := zstd.NewDecoder(nil)
    return &ZstdCodec{enc, dec}, nil
}

func (c *ZstdCodec) Name() string { return "zstd" }

func (c *ZstdCodec) Compress(data []byte) ([]byte, error) {
    return c.enc.EncodeAll(data, nil), nil
}

func (c *ZstdCodec) Decompress(data []byte) ([]byte, error) {
    return c.dec.DecodeAll(data, nil)
}
```

#### 步骤 2: 集成到 Raft 层

```go
// internal/raft/node_memory.go

type raftNode struct {
    // ... existing fields ...
    compressionCodec compression.Codec
    enableCompression bool
}

// proposeWithCompression 支持压缩的 propose
func (rc *raftNode) proposeWithCompression(ctx context.Context, data string) error {
    var toPropose []byte
    
    if rc.enableCompression {
        compressed, err := rc.compressionCodec.Compress([]byte(data))
        if err != nil {
            return err
        }
        // 添加压缩标记头
        toPropose = append([]byte{1}, compressed...) // 1 = 已压缩
    } else {
        toPropose = append([]byte{0}, data...) // 0 = 未压缩
    }
    
    return rc.node.Propose(ctx, toPropose)
}
```

#### 步骤 3: 修改 Apply 路径

```go
// internal/memory/kvstore.go

func (m *Memory) deserializeOperation(data []byte) (RaftOperation, error) {
    // 检测压缩标记
    var actualData []byte
    if len(data) > 0 && data[0] == 1 {
        // 已压缩，需要解压
        decompressed, err := m.compressionCodec.Decompress(data[1:])
        if err != nil {
            return RaftOperation{}, err
        }
        actualData = decompressed
    } else {
        actualData = data[1:]  // 跳过标记字节
    }
    
    // 原有的反序列化逻辑
    return m.parseOperation(actualData)
}
```

#### 步骤 4: 配置支持

```yaml
# configs/config.yaml

performance:
  # 压缩配置
  enable_compression: true
  compression_codec: "snappy"  # snappy 或 zstd
  compression_min_size: 1024   # 小于此大小不压缩
```

#### 步骤 5: 性能测试

```bash
# 对比测试
go test -bench=BenchmarkCompression -run=^$ ./test -v

# 预期结果:
# Snappy: +40% 网络带宽优化，CPU 开销 <5%
# Zstd:   +70% 网络带宽优化，CPU 开销 +15%
```

---

### 1.3 快速验证清单

```
Phase 1 完成条件:
  [ ] BatchProposer 实现
  [ ] 批量 Apply 集成完成
  [ ] 消息压缩框架实现
  [ ] 配置系统支持新参数
  [ ] 单元测试覆盖率 > 80%
  [ ] 集成测试通过
  [ ] 性能对比 baseline: 5x+ 改进
```

---

## Phase 2: 快照优化 (3-4 周)

### 2.1 显式快照分块 (2-3 天)

**目标**: 支持大于 4MB 的快照

```go
// internal/raft/snapshot_chunker.go

package raft

const (
    SnapshotChunkSize = 4 * 1024 * 1024  // 4MB chunks
)

type SnapshotChunk struct {
    SnapshotID string
    ChunkIndex int
    TotalChunks int
    Data       []byte
    Checksum   uint32  // CRC32 校验
}

// ChunkSender 负责分块发送快照
type ChunkSender struct {
    snapshots map[string]*snap.Snapshotter
    mu sync.Mutex
}

func (cs *ChunkSender) Send(snap raftpb.Snapshot, to uint64) error {
    data := snap.Data
    totalChunks := (len(data) + SnapshotChunkSize - 1) / SnapshotChunkSize
    
    for i := 0; i < totalChunks; i++ {
        start := i * SnapshotChunkSize
        end := start + SnapshotChunkSize
        if end > len(data) {
            end = len(data)
        }
        
        chunk := &SnapshotChunk{
            SnapshotID: fmt.Sprintf("%d-%d", snap.Metadata.Index, snap.Metadata.Term),
            ChunkIndex: i,
            TotalChunks: totalChunks,
            Data: data[start:end],
            Checksum: crc32.ChecksumIEEE(data[start:end]),
        }
        
        // 发送分块
        sendChunk(to, chunk)
    }
}

// ChunkReceiver 负责接收和重组快照
type ChunkReceiver struct {
    chunks map[string]map[int][]byte
    mu sync.Mutex
}

func (cr *ChunkReceiver) Receive(chunk *SnapshotChunk) ([]byte, error) {
    cr.mu.Lock()
    if _, ok := cr.chunks[chunk.SnapshotID]; !ok {
        cr.chunks[chunk.SnapshotID] = make(map[int][]byte)
    }
    
    // 验证校验和
    if crc32.ChecksumIEEE(chunk.Data) != chunk.Checksum {
        return nil, fmt.Errorf("checksum mismatch for chunk %d", chunk.ChunkIndex)
    }
    
    cr.chunks[chunk.SnapshotID][chunk.ChunkIndex] = chunk.Data
    
    // 检查是否接收完整
    if len(cr.chunks[chunk.SnapshotID]) == chunk.TotalChunks {
        combined := cr.assemble(chunk.SnapshotID)
        delete(cr.chunks, chunk.SnapshotID)
        cr.mu.Unlock()
        return combined, nil
    }
    cr.mu.Unlock()
    return nil, nil  // 等待更多分块
}

func (cr *ChunkReceiver) assemble(snapshotID string) []byte {
    chunks := cr.chunks[snapshotID]
    totalSize := 0
    for _, chunk := range chunks {
        totalSize += len(chunk)
    }
    
    result := make([]byte, 0, totalSize)
    for i := 0; i < len(chunks); i++ {
        result = append(result, chunks[i]...)
    }
    return result
}
```

### 2.2 快照压缩 (1-2 天)

**目标**: 将快照大小减少 70-90%

```go
// 在 BatchProposer 或 RaftNode 中添加

func (rc *raftNode) createCompressedSnapshot(data []byte) ([]byte, error) {
    compressed := make([]byte, 0)
    
    // 使用 Zstd 压缩快照
    encoder, _ := zstd.NewWriter(nil)
    compressed = encoder.EncodeAll(data, compressed)
    encoder.Close()
    
    // 添加压缩标记头
    result := append([]byte{1}, compressed...)  // 1 = 压缩
    
    log.Infof("Snapshot: %d → %d bytes (%.1f%% reduction)",
        len(data), len(result), 100*(1-float64(len(result))/float64(len(data))))
    
    return result, nil
}

func (m *Memory) loadCompressedSnapshot(data []byte) ([]byte, error) {
    if len(data) == 0 {
        return nil, fmt.Errorf("empty snapshot data")
    }
    
    if data[0] == 1 {  // 已压缩
        decoder, _ := zstd.NewReader(nil)
        return decoder.DecodeAll(data[1:], nil)
    }
    
    return data[1:], nil  // 未压缩
}
```

### 2.3 配置化策略

```yaml
# configs/config.yaml

maintenance:
  # 快照分块配置
  snapshot_chunk_size: 4194304      # 4MB chunks
  snapshot_chunk_timeout: 30s       # 分块传输超时
  
  # 快照压缩配置
  enable_snapshot_compression: true
  snapshot_compression_codec: "zstd"  # zstd 或 snappy
  
  # 快照触发策略
  snapshot_count: 10000            # 每 10000 条条目触发
  snapshot_min_size: 1048576       # 最小快照大小 (1MB)
  snapshot_retention: 3             # 保留的快照数量
```

---

## Phase 3: 长期优化

### 3.1 异步 WAL fsync (高风险)

**收益**: 5-10x 吞吐量  
**风险**: 需要精心处理故障恢复  
**何时启用**: 仅在明确需要时

```go
// internal/raft/async_wal.go

type AsyncWALWriter struct {
    wal *wal.WAL
    writeC chan *WALEntry
    fsyncTicker *time.Ticker
    fsyncInterval time.Duration
}

func (aw *AsyncWALWriter) WriteEntry(entry []byte) error {
    // 不阻塞，立即返回
    select {
    case aw.writeC <- &WALEntry{data: entry}:
        return nil
    default:
        return fmt.Errorf("WAL write buffer full")
    }
}

func (aw *AsyncWALWriter) Run() {
    batch := make([]*WALEntry, 0, 1000)
    
    for {
        select {
        case entry := <-aw.writeC:
            batch = append(batch, entry)
            
            // 缓冲满时刷新
            if len(batch) >= 1000 {
                aw.flushBatch(batch)
                batch = batch[:0]
            }
            
        case <-aw.fsyncTicker.C:
            // 定期刷新
            if len(batch) > 0 {
                aw.flushBatch(batch)
                batch = batch[:0]
            }
        }
    }
}

func (aw *AsyncWALWriter) flushBatch(batch []*WALEntry) error {
    // 批量写入 + fsync
    // ...
}
```

### 3.2 日志预读优化

```go
// internal/rocksdb/prefetch.go

type PrefetchCache struct {
    cache map[uint64]raftpb.Entry
    mu sync.RWMutex
    hitRate int64
    missCount int64
}

func (pc *PrefetchCache) GetEntries(lo, hi uint64) []raftpb.Entry {
    // 在获取条目前预读
    if lo < hi-100 {  // 预读下 100 条
        pc.prefetch(hi, hi+100)
    }
    return pc.getFromCache(lo, hi)
}

func (pc *PrefetchCache) prefetch(start, end uint64) {
    go func() {
        // 后台预读，不阻塞主路径
    }()
}
```

---

## 验证与测试

### 基准测试

```bash
# 性能对比

# Phase 1 前
go test -bench=BenchmarkPut -benchtime=10s ./test
# Expected: ~796 ops/sec

# Phase 1 后
go test -bench=BenchmarkPut -benchtime=10s ./test
# Expected: ~3,980+ ops/sec (5x)

# Phase 2 后
go test -bench=BenchmarkPut -benchtime=10s ./test  
# Expected: ~15,920+ ops/sec (20x)

# 负载测试
go test -bench=BenchmarkParallelPuts -benchtime=10s ./test -v

# 长期运行测试
go test -run=TestLongRunning -timeout=1h ./test -v
```

### 一致性验证

```bash
# 跨集群验证
go test -run=TestConsistency ./test -v

# 快照恢复验证
go test -run=TestSnapshotRecovery ./test -v

# 失败恢复测试
go test -run=TestNetworkPartition ./test -v
```

---

## 风险与回退策略

### 风险清单

| 优化 | 风险 | 概率 | 缓解 |
|------|------|------|------|
| 批量 Proposal | 延迟增加 | 中 | 可配置 MaxBatchTime |
| 消息压缩 | CPU 尖峰 | 低 | 限制最小压缩大小 |
| 快照分块 | 分块失败 | 低 | 重试 + 校验和 |
| 异步 fsync | 数据丢失 | 极低 | 仅在高吞吐启用 |

### 回退策略

```bash
# 如果性能下降，快速回退
git revert <commit-hash>
make rebuild
# 检查日志确认原因
tail -f logs/metastore.log | grep -i error
```

---

## 成功条件

```
Phase 1 完成:
  [ ] 吞吐量: 796 → 3,980+ ops/sec (5x)
  [ ] 延迟 P99: < 100ms
  [ ] CPU 使用率: 30-50%
  [ ] 数据一致性: 100%
  [ ] 生产就绪

Phase 2 完成:
  [ ] 吞吐量: 3,980 → 15,920+ ops/sec (20x)
  [ ] 快照大小: -70% 以上
  [ ] 快照传输: 支持 100MB+
  [ ] CPU 使用率: 30-50%
  
Phase 3 完成:
  [ ] 吞吐量: 15,920 → 25,000+ ops/sec (30x)
  [ ] 网络带宽: < 5Mbps
  [ ] 延迟 P50: < 10ms
  [ ] 稳定运行 > 1 周
```

---

## 时间预期

| Phase | 任务 | 工作量 | 时间 |
|-------|------|--------|------|
| 1 | 批量 Proposal | 2 days | 1-2 周 |
| 1 | 消息压缩 | 3 days | |
| 2 | 快照分块 | 2 days | 3-4 周 |
| 2 | 快照压缩 | 1 day | |
| 3 | 异步 fsync | 5 days | 后续 |
| 3 | 预读优化 | 2 days | |

**总计**: 2-3 周达到 10,000+ ops/sec，4-6 周达到 25,000+ ops/sec

