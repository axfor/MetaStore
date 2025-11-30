# MVCC (Multi-Version Concurrency Control) 设计

## 概述

MetaStore 需要实现与 etcd 兼容的 MVCC 机制，支持：
- 多版本数据存储
- 历史版本查询
- 版本压缩（Compaction）
- 可配置的版本保留策略

## 设计目标

1. **etcd 兼容性**：与 etcd 的 MVCC 语义完全一致
2. **性能**：最小化版本管理的性能开销
3. **可配置**：版本保留数量可配置（默认 1000）
4. **存储引擎适配**：Memory 和 RocksDB 引擎使用不同的实现策略

## 核心概念

### Revision（修订版本）

```
┌─────────────────────────────────────────────────────────────┐
│                      Revision 结构                          │
├─────────────────────────────────────────────────────────────┤
│  main: int64    - 主版本号（全局递增，每次事务+1）            │
│  sub:  int64    - 子版本号（事务内操作序号，从0开始）         │
├─────────────────────────────────────────────────────────────┤
│  示例：                                                      │
│    Put("a", "1")  → Revision{main: 1, sub: 0}               │
│    Put("b", "2")  → Revision{main: 2, sub: 0}               │
│    Txn:                                                      │
│      Put("c", "3") → Revision{main: 3, sub: 0}              │
│      Put("d", "4") → Revision{main: 3, sub: 1}              │
│      Del("a")      → Revision{main: 3, sub: 2}              │
└─────────────────────────────────────────────────────────────┘
```

### KeyValue 元数据

```go
type KeyValue struct {
    Key            []byte  // 键
    Value          []byte  // 值
    CreateRevision int64   // 创建时的 revision（首次 Put）
    ModRevision    int64   // 最后修改的 revision
    Version        int64   // 键的版本号（每次 Put +1，Delete 后重置为0）
    Lease          int64   // 关联的 Lease ID
}
```

### 版本索引

```
┌─────────────────────────────────────────────────────────────┐
│                     索引结构                                 │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  KeyIndex (内存索引)                                         │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  key: "foo"                                          │   │
│  │  generations:                                         │   │
│  │    [0]: {created: 2, revisions: [2, 5, 8]}           │   │
│  │    [1]: {created: 12, revisions: [12, 15]}  ← 当前   │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  Generation（世代）                                          │
│  - 每次 Delete 后 Put 开启新世代                             │
│  - 用于正确处理键的生命周期                                   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## 存储设计

### Memory 引擎 MVCC

```
┌─────────────────────────────────────────────────────────────┐
│                  Memory MVCC 结构                            │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌─────────────┐    ┌──────────────────────────────────┐   │
│  │  KeyIndex   │    │         RevisionStore            │   │
│  │  (B-Tree)   │    │         (B-Tree)                 │   │
│  ├─────────────┤    ├──────────────────────────────────┤   │
│  │ key → gens  │    │ revision → KeyValue              │   │
│  │             │    │                                  │   │
│  │ "foo" → [   │    │ {1,0} → {key:"foo", val:"a"}    │   │
│  │   gen0,     │    │ {2,0} → {key:"foo", val:"b"}    │   │
│  │   gen1      │    │ {3,0} → {key:"bar", val:"x"}    │   │
│  │ ]           │    │ {4,0} → {key:"foo", val:"c"}    │   │
│  └─────────────┘    └──────────────────────────────────┘   │
│                                                              │
│  内存占用估算（1000 版本 × 10000 键）：                       │
│  - KeyIndex: ~10MB (索引结构)                                │
│  - RevisionStore: ~100MB-1GB (取决于值大小)                  │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### RocksDB 引擎 MVCC

RocksDB 方案：利用 RocksDB 的 **User-defined Timestamp** 或 **Key 编码** 实现 MVCC。

#### 方案 A：Key 编码 MVCC（推荐，与 etcd/bbolt 一致）

```
┌─────────────────────────────────────────────────────────────┐
│                RocksDB Key 编码方案                          │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Key 格式: <bucket>/<user_key>/<revision>                   │
│                                                              │
│  Column Families:                                            │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  "key" CF - 存储所有版本的 KV 数据                    │   │
│  │  ┌────────────────────────────────────────────────┐  │   │
│  │  │  key: "k/foo/\x00\x00\x00\x01\x00\x00\x00\x00"  │  │   │
│  │  │  val: {value:"a", create:1, mod:1, ver:1}      │  │   │
│  │  ├────────────────────────────────────────────────┤  │   │
│  │  │  key: "k/foo/\x00\x00\x00\x04\x00\x00\x00\x00"  │  │   │
│  │  │  val: {value:"b", create:1, mod:4, ver:2}      │  │   │
│  │  └────────────────────────────────────────────────┘  │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  "meta" CF - 存储元数据                               │   │
│  │  - current_revision: 当前最新 revision                │   │
│  │  - compacted_revision: 已压缩到的 revision            │   │
│  │  - scheduled_compact: 计划压缩的 revision             │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

#### 方案 B：RocksDB User-defined Timestamp（备选）

```
┌─────────────────────────────────────────────────────────────┐
│            RocksDB Timestamp 方案（RocksDB 6.x+）            │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  优点：                                                      │
│  - RocksDB 原生支持，性能最优                                │
│  - 自动 GC 旧版本                                           │
│  - 读取时指定 timestamp 即可获取历史版本                     │
│                                                              │
│  缺点：                                                      │
│  - 需要 RocksDB 6.x+                                        │
│  - API 与 etcd 有差异，需要适配                              │
│  - Column Family 级别的 timestamp 比较器                    │
│                                                              │
│  API:                                                        │
│  - Put(key, value, timestamp)                               │
│  - Get(key, read_timestamp)                                 │
│  - CompactRange(start, end, before_timestamp)               │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

**选择方案 A**：Key 编码方案与 etcd 实现一致，兼容性更好。

## 配置设计

```yaml
server:
  # MVCC 配置
  mvcc:
    # 版本保留策略
    retention:
      # 保留的最大版本数（默认 1000，与 etcd 一致）
      max_revisions: 1000

      # 保留时间（可选，0 表示仅按版本数）
      # 如果同时设置，取两者中更严格的条件
      max_age: 0  # 例如: 24h, 7d

    # 自动压缩配置
    auto_compaction:
      enable: true
      # 压缩模式: "revision" 或 "periodic"
      mode: "revision"
      # revision 模式：保留最近 N 个 revision
      retention: 1000
      # periodic 模式：保留最近 N 小时的数据
      # period: 1h

    # 压缩性能配置
    compaction:
      # 每批压缩的 key 数量
      batch_size: 1000
      # 压缩间隔（避免影响正常请求）
      batch_interval: 10ms
```

## 接口设计

### MVCC Store 接口

```go
// MVCCStore MVCC 存储接口
type MVCCStore interface {
    // 基础操作（带版本）
    Put(key, value []byte, lease int64) (rev int64, err error)
    Get(key []byte, rev int64) (*KeyValue, error)
    Range(start, end []byte, rev int64, limit int64) ([]*KeyValue, error)
    Delete(key []byte) (rev int64, deleted int64, err error)
    DeleteRange(start, end []byte) (rev int64, deleted int64, err error)

    // 事务支持
    Txn(ctx context.Context) Txn

    // 版本管理
    CurrentRevision() int64
    CompactedRevision() int64
    Compact(rev int64) error

    // Watch 支持
    Watch(key []byte, rev int64, prefix bool) <-chan WatchEvent
}

// Txn 事务接口
type Txn interface {
    If(conds ...Condition) Txn
    Then(ops ...Op) Txn
    Else(ops ...Op) Txn
    Commit() (*TxnResponse, error)
}
```

### Revision 编解码

```go
// Revision 修订版本
type Revision struct {
    Main int64 // 主版本号
    Sub  int64 // 子版本号
}

// 编码为 16 字节（用于 RocksDB key）
func (r Revision) Bytes() []byte {
    buf := make([]byte, 16)
    binary.BigEndian.PutUint64(buf[0:8], uint64(r.Main))
    binary.BigEndian.PutUint64(buf[8:16], uint64(r.Sub))
    return buf
}

// 解码
func ParseRevision(b []byte) Revision {
    return Revision{
        Main: int64(binary.BigEndian.Uint64(b[0:8])),
        Sub:  int64(binary.BigEndian.Uint64(b[8:16])),
    }
}
```

## 实现计划

### Phase 1: 核心数据结构

- [ ] 实现 `Revision` 类型和编解码
- [ ] 实现 `KeyValue` 扩展元数据
- [ ] 实现 `KeyIndex` 内存索引（Memory 引擎）
- [ ] 添加配置项到 `pkg/config`

### Phase 2: Memory 引擎 MVCC

- [ ] 实现 `MemoryMVCCStore`
- [ ] 实现版本化 Put/Get/Delete
- [ ] 实现 Range 查询（支持历史版本）
- [ ] 实现 `Generation` 管理
- [ ] 单元测试

### Phase 3: RocksDB 引擎 MVCC

- [ ] 设计 Key 编码格式
- [ ] 实现 `RocksDBMVCCStore`
- [ ] 实现版本化 Put/Get/Delete
- [ ] 实现 Range 查询（使用 Iterator）
- [ ] 单元测试

### Phase 4: Compaction

- [ ] 实现手动 Compact API
- [ ] 实现自动压缩调度器
- [ ] Memory 引擎：删除旧版本数据
- [ ] RocksDB 引擎：删除旧版本 + 触发 RocksDB Compaction
- [ ] 压缩进度 Metrics

### Phase 5: 集成与测试

- [ ] 集成到现有 KV 接口
- [ ] etcd 客户端兼容性测试
- [ ] 性能基准测试
- [ ] 压力测试

## 性能考虑

### Memory 引擎

```
写入：O(log N) - B-Tree 插入
读取：O(log N) - 先查 KeyIndex，再查 RevisionStore
Range：O(log N + M) - M 为返回的 key 数量
Compact：O(K × V) - K 为 key 数量，V 为需删除的版本数
```

### RocksDB 引擎

```
写入：O(1) 摊销 - LSM-Tree 写入
读取：O(log N) - 使用 Seek 定位
Range：O(log N + M) - Iterator 扫描
Compact：
  - 删除旧版本：O(K × V) DeleteRange
  - RocksDB Compaction：后台异步执行
```

### 内存优化

1. **版本数限制**：默认 1000 版本，防止内存无限增长
2. **懒加载**：RocksDB 引擎不需要全量加载索引
3. **压缩批处理**：分批压缩，避免长时间阻塞
4. **Bloom Filter**：RocksDB 使用 Bloom Filter 加速点查询

## 与 etcd 的兼容性

| 特性 | etcd | MetaStore | 说明 |
|------|------|-----------|------|
| Revision 结构 | main + sub | main + sub | 完全一致 |
| 默认版本保留 | 1000 | 1000 | 可配置 |
| Compact API | ✅ | ✅ | 兼容 |
| 历史版本查询 | ✅ | ✅ | 支持 |
| Watch from revision | ✅ | ✅ | 支持 |
| Txn 事务 | ✅ | ✅ | 支持 |

## 文件结构

```
internal/
├── mvcc/
│   ├── revision.go          # Revision 类型
│   ├── key_index.go          # KeyIndex 内存索引
│   ├── key_value.go          # KeyValue 扩展
│   ├── store.go              # MVCCStore 接口
│   ├── memory_store.go       # Memory MVCC 实现
│   ├── rocksdb_store.go      # RocksDB MVCC 实现
│   ├── compaction.go         # 压缩逻辑
│   ├── watcher.go            # Watch 集成
│   └── *_test.go             # 测试文件
└── ...
```

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 内存增长过快 | OOM | 版本数限制 + 自动压缩 |
| Compact 影响性能 | 延迟抖动 | 分批压缩 + 限流 |
| RocksDB 版本过多 | 空间膨胀 | 定期触发 RocksDB Compaction |
| 历史版本查询慢 | 高延迟 | 添加二级索引 |
