# Lock/Concurrency 高层接口实现设计文档

## 概述

本文档详细描述了 MetaStore 的 etcd v3 兼容 Lock/Concurrency 高层接口的设计方案和实现步骤。

**目标**: 提供与 `go.etcd.io/etcd/client/v3/concurrency` 包兼容的分布式锁和选举接口。

**优先级**: P0（必须实现，不符合 prompt 要求）

---

## 1. 功能需求

### 1.1 核心功能

根据 etcd concurrency 包规范，需要实现以下功能：

#### Session（会话）
- ✅ **NewSession** - 创建会话（基于 Lease）
- ✅ **Session.Lease** - 获取会话关联的 Lease ID
- ✅ **Session.Done** - 获取会话结束通道
- ✅ **Session.Close** - 关闭会话

#### Mutex（互斥锁）
- ✅ **NewMutex** - 创建互斥锁
- ✅ **Mutex.Lock** - 获取锁
- ✅ **Mutex.TryLock** - 尝试获取锁（非阻塞）
- ✅ **Mutex.Unlock** - 释放锁
- ✅ **Mutex.IsOwner** - 检查是否持有锁
- ✅ **Mutex.Key** - 获取锁对应的键

#### Election（选举）
- ✅ **NewElection** - 创建选举
- ✅ **Election.Campaign** - 参与竞选
- ✅ **Election.Resign** - 主动退出竞选
- ✅ **Election.Leader** - 查询当前 leader
- ✅ **Election.Observe** - 观察 leader 变化

#### STM（软件事务内存，可选）
- ⚠️ **NewSTM** - 创建 STM 事务
- ⚠️ **STM.Get/Put/Del** - 事务内操作

---

## 2. 架构设计

### 2.1 整体架构

```
┌─────────────────────────────────────────────────────────┐
│         MetaStore Client SDK (concurrency 包)           │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐             │
│  │ Session  │  │  Mutex   │  │ Election │             │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘             │
│       │             │              │                    │
│       └─────────────┴──────────────┘                    │
│                     │                                    │
└─────────────────────┼────────────────────────────────────┘
                      │
┌─────────────────────▼────────────────────────────────────┐
│                 etcd v3 gRPC API                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │  Lease   │  │    KV    │  │  Watch   │              │
│  └──────────┘  └──────────┘  └──────────┘              │
└──────────────────────────────────────────────────────────┘
```

### 2.2 实现方式

Lock/Concurrency 功能**不需要在 Server 端实现新的 API**，而是提供 **Client SDK**，利用现有的 KV、Lease、Watch API 实现分布式协调原语。

---

## 3. 数据模型

### 3.1 Session (会话)

```go
type Session struct {
    client *clientv3.Client
    leaseID clientv3.LeaseID
    donec   chan struct{}
    cancel  context.CancelFunc
}
```

**实现原理**：
- 会话基于 Lease
- 创建 Lease 并启动 KeepAlive
- 当 Lease 失效时关闭 donec channel

### 3.2 Mutex (互斥锁)

```go
type Mutex struct {
    s   *Session
    pfx string  // 锁的键前缀，如 "/my-lock/"
    myKey string  // 当前持有者的键，如 "/my-lock/1234567890"
    myRev int64   // 当前持有者的 revision
}
```

**实现原理**：
1. 使用 key-value 表示锁持有者
2. 键格式：`{prefix}/{lease-id}`
3. 值：空或持有者信息
4. 使用 revision 来排序（先创建先获得锁）
5. Watch 前面的 key，等待释放

**Lock 流程**：
```
1. Put key={prefix}/{lease-id}, lease=session.leaseID
2. Get prefix range, 获取所有竞争者的 revision
3. 如果自己的 revision 是最小的，获得锁
4. 否则，Watch 比自己 revision 小的最大 key
5. 等待该 key 删除（前一个持有者释放）
6. 重复步骤 2-5
```

### 3.3 Election (选举)

```go
type Election struct {
    session *Session
    keyPrefix string
    leaderKey string  // 当前 leader 的 key
    leaderRev int64   // 当前 leader 的 revision
    leaderSession clientv3.LeaseID
}
```

**实现原理**：
1. 类似 Mutex，但不是互斥锁
2. revision 最小的成为 leader
3. 其他节点 watch leader key
4. leader 失效时，下一个 revision 自动成为 leader

**Campaign 流程**：
```
1. Put key={prefix}/{lease-id}, value=candidateName
2. Get prefix range
3. 如果自己 revision 最小，成为 leader
4. 否则，Watch 更小 revision 的 key
5. 等待前面的节点退出
6. 成为 leader
```

---

## 4. 接口实现

### 4.1 创建 Client SDK 包

#### 目录结构

```
pkg/concurrency/
├── session.go      - Session 实现
├── mutex.go        - Mutex 实现
├── election.go     - Election 实现
├── stm.go          - STM 实现（可选）
└── concurrency_test.go - 测试
```

### 4.2 Session 实现

#### 文件：pkg/concurrency/session.go

```go
package concurrency

import (
    "context"
    "time"

    clientv3 "go.etcd.io/etcd/client/v3"
)

// Session 表示一个租约会话
type Session struct {
    client  *clientv3.Client
    leaseID clientv3.LeaseID
    donec   chan struct{}
    cancel  context.CancelFunc
}

// NewSession 创建新会话
func NewSession(client *clientv3.Client, opts ...SessionOption) (*Session, error) {
    ctx, cancel := context.WithCancel(client.Ctx())

    // 默认选项
    cfg := &sessionConfig{
        ttl: 60,
        ctx: ctx,
    }

    // 应用选项
    for _, opt := range opts {
        opt(cfg)
    }

    // TODO: 实现
    // 1. 创建 Lease
    // 2. 启动 KeepAlive goroutine
    // 3. 监控 Lease 失效
    // 4. 返回 Session

    return &Session{
        client:  client,
        leaseID: 0, // TODO
        donec:   make(chan struct{}),
        cancel:  cancel,
    }, nil
}

// Lease 返回会话的 Lease ID
func (s *Session) Lease() clientv3.LeaseID {
    return s.leaseID
}

// Done 返回会话结束通道
func (s *Session) Done() <-chan struct{} {
    return s.donec
}

// Close 关闭会话
func (s *Session) Close() error {
    s.cancel()
    // TODO: 撤销 Lease
    return nil
}

// SessionOption 会话选项
type SessionOption func(*sessionConfig)

type sessionConfig struct {
    ttl int
    ctx context.Context
}

// WithTTL 设置 TTL
func WithTTL(ttl int) SessionOption {
    return func(cfg *sessionConfig) {
        cfg.ttl = ttl
    }
}

// WithContext 设置 Context
func WithContext(ctx context.Context) SessionOption {
    return func(cfg *sessionConfig) {
        cfg.cfg = ctx
    }
}
```

### 4.3 Mutex 实现

#### 文件：pkg/concurrency/mutex.go

```go
package concurrency

import (
    "context"
    "fmt"

    clientv3 "go.etcd.io/etcd/client/v3"
)

// Mutex 分布式互斥锁
type Mutex struct {
    s     *Session
    pfx   string
    myKey string
    myRev int64
}

// NewMutex 创建新的 Mutex
func NewMutex(s *Session, pfx string) *Mutex {
    return &Mutex{
        s:   s,
        pfx: pfx + "/",
    }
}

// Lock 获取锁（阻塞）
func (m *Mutex) Lock(ctx context.Context) error {
    // TODO: 实现
    // 1. Put key with Lease
    // 2. Get range to find all waiters
    // 3. If I'm first (lowest revision), acquired lock
    // 4. Else, watch previous waiter
    // 5. Wait for previous waiter to release
    // 6. Repeat from step 2

    // 伪代码：
    // s := m.s
    // client := s.client
    //
    // key := fmt.Sprintf("%s%x", m.pfx, s.Lease())
    //
    // cmp := clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
    // put := clientv3.OpPut(key, "", clientv3.WithLease(s.Lease()))
    // get := clientv3.OpGet(m.pfx, clientv3.WithPrefix())
    //
    // resp, err := client.Txn(ctx).If(cmp).Then(put, get).Else(get).Commit()
    // if err != nil {
    //     return err
    // }
    //
    // m.myRev = resp.Responses[0].GetResponsePut().Header.Revision
    // ownerKey := resp.Responses[1].GetResponseRange().Kvs
    //
    // if len(ownerKey) == 0 || ownerKey[0].CreateRevision == m.myRev {
    //     m.myKey = key
    //     return nil
    // }
    //
    // // Wait for previous holder
    // return m.waitDeletes(ctx, ownerKey[0].Key)

    return nil
}

// TryLock 尝试获取锁（非阻塞）
func (m *Mutex) TryLock(ctx context.Context) error {
    // TODO: 实现
    // Similar to Lock but don't wait, return error if can't acquire
    return nil
}

// Unlock 释放锁
func (m *Mutex) Unlock(ctx context.Context) error {
    // TODO: 实现
    // Delete my key
    // client := m.s.client
    // _, err := client.Delete(ctx, m.myKey)
    // return err
    return nil
}

// IsOwner 检查是否持有锁
func (m *Mutex) IsOwner() bool {
    return m.myKey != ""
}

// Key 返回锁的键
func (m *Mutex) Key() string {
    return m.myKey
}

// waitDeletes 等待指定 key 被删除
func (m *Mutex) waitDeletes(ctx context.Context, key []byte) error {
    // TODO: 实现
    // Watch key until it's deleted
    // client := m.s.client
    // wch := client.Watch(ctx, string(key))
    // for resp := range wch {
    //     for _, ev := range resp.Events {
    //         if ev.Type == mvccpb.DELETE {
    //             return nil
    //         }
    //     }
    // }
    return nil
}
```

### 4.4 Election 实现

#### 文件：pkg/concurrency/election.go

```go
package concurrency

import (
    "context"

    clientv3 "go.etcd.io/etcd/client/v3"
)

// Election 实现 leader 选举
type Election struct {
    session *Session
    keyPrefix string

    leaderKey     string
    leaderRev     int64
    leaderSession clientv3.LeaseID
}

// NewElection 创建新的选举
func NewElection(s *Session, pfx string) *Election {
    return &Election{
        session:   s,
        keyPrefix: pfx + "/",
    }
}

// Campaign 参与竞选
func (e *Election) Campaign(ctx context.Context, val string) error {
    // TODO: 实现
    // 1. Put key with lease and value
    // 2. Get all candidates
    // 3. If I have lowest revision, I'm leader
    // 4. Else, watch candidate with lower revision
    // 5. Wait for them to resign
    // 6. Become leader
    return nil
}

// Resign 退出竞选
func (e *Election) Resign(ctx context.Context) error {
    // TODO: 实现
    // Delete my campaign key
    return nil
}

// Leader 获取当前 leader
func (e *Election) Leader(ctx context.Context) (*clientv3.GetResponse, error) {
    // TODO: 实现
    // Get candidate with lowest revision
    return nil, nil
}

// Observe 观察 leader 变化
func (e *Election) Observe(ctx context.Context) <-chan clientv3.GetResponse {
    // TODO: 实现
    // Watch for leader changes
    // Return channel that receives leader updates
    ch := make(chan clientv3.GetResponse)
    return ch
}

// Key 返回当前节点的竞选键
func (e *Election) Key() string {
    return e.leaderKey
}

// Rev 返回当前节点的 revision
func (e *Election) Rev() int64 {
    return e.leaderRev
}
```

---

## 5. 实现步骤

### 阶段 1：Session 实现（估计 2-3 小时）

1. ✅ **创建 Session 基础结构**
   - [x] Session struct 定义
   - [x] NewSession 构造函数
   - [x] SessionOption 选项模式

2. **实现 Session 功能**
   - [ ] 创建 Lease
   - [ ] 启动 KeepAlive
   - [ ] 监控 Lease 失效
   - [ ] Close 清理资源

### 阶段 2：Mutex 实现（估计 3-4 小时）

3. **实现 Lock 功能**
   - [ ] Put key with Lease
   - [ ] Get range 查找竞争者
   - [ ] 判断是否获得锁
   - [ ] Watch 等待前一个持有者

4. **实现 TryLock**
   - [ ] 非阻塞尝试获取锁

5. **实现 Unlock**
   - [ ] 删除锁键
   - [ ] 清理状态

### 阶段 3：Election 实现（估计 3-4 小时）

6. **实现 Campaign**
   - [ ] Put campaign key
   - [ ] 检查是否成为 leader
   - [ ] Watch 前面的候选者

7. **实现 Leader 查询**
   - [ ] Get 最小 revision 的 candidate

8. **实现 Observe**
   - [ ] Watch leader 变化
   - [ ] 推送更新到 channel

### 阶段 4：测试（估计 3-4 小时）

9. **Mutex 测试**
   - [ ] 单节点获取释放锁
   - [ ] 多节点竞争锁
   - [ ] Lease 过期自动释放

10. **Election 测试**
    - [ ] Campaign 成为 leader
    - [ ] Leader 退出后重新选举
    - [ ] Observe 监控 leader 变化

11. **集成测试**
    - [ ] 与 MetaStore 集成测试
    - [ ] 压力测试

---

## 6. 关键技术点

### 6.1 Revision 排序

- 使用 CreateRevision 来确定获取锁的顺序
- Revision 越小，优先级越高
- 保证公平性（FIFO）

### 6.2 Watch 机制

- Watch 前一个竞争者的 key
- 当前一个释放时，自动获得锁/成为 leader
- 避免惊群效应（每个只 watch 前一个）

### 6.3 Lease 集成

- 锁/选举的 key 关联到 Session Lease
- Lease 过期时自动删除 key
- 实现故障自动恢复

### 6.4 错误处理

- Context 取消时正确清理
- Lease 失效时返回错误
- 网络异常时重试

---

## 7. 使用示例

### 7.1 Mutex 示例

```go
package main

import (
    "context"
    "log"
    "time"

    clientv3 "go.etcd.io/etcd/client/v3"
    "metaStore/pkg/concurrency"
)

func main() {
    cli, _ := clientv3.New(clientv3.Config{
        Endpoints: []string{"localhost:2379"},
    })
    defer cli.Close()

    // 创建会话
    session, err := concurrency.NewSession(cli)
    if err != nil {
        log.Fatal(err)
    }
    defer session.Close()

    // 创建锁
    mutex := concurrency.NewMutex(session, "/my-lock")

    // 获取锁
    if err := mutex.Lock(context.Background()); err != nil {
        log.Fatal(err)
    }
    log.Println("锁已获取")

    // 执行临界区代码
    time.Sleep(5 * time.Second)

    // 释放锁
    if err := mutex.Unlock(context.Background()); err != nil {
        log.Fatal(err)
    }
    log.Println("锁已释放")
}
```

### 7.2 Election 示例

```go
package main

import (
    "context"
    "log"

    clientv3 "go.etcd.io/etcd/client/v3"
    "metaStore/pkg/concurrency"
)

func main() {
    cli, _ := clientv3.New(clientv3.Config{
        Endpoints: []string{"localhost:2379"},
    })
    defer cli.Close()

    session, _ := concurrency.NewSession(cli)
    defer session.Close()

    election := concurrency.NewElection(session, "/my-election")

    // 参与竞选
    if err := election.Campaign(context.Background(), "node1"); err != nil {
        log.Fatal(err)
    }

    log.Println("我是 leader")

    // 执行 leader 工作
    // ...

    // 主动退出
    if err := election.Resign(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

---

## 8. 测试计划

### 8.1 单元测试

```
TestSession
├── TestNewSession
├── TestSessionLease
├── TestSessionClose
└── TestSessionExpiry

TestMutex
├── TestLockUnlock
├── TestConcurrentLock
├── TestTryLock
├── TestLeaseExpiry
└── TestContextCancel

TestElection
├── TestCampaignResign
├── TestLeaderQuery
├── TestObserve
└── TestLeaderFailover
```

### 8.2 集成测试

```
TestConcurrencyIntegration
├── TestMultiNodeMutex
│   ├── 3个节点竞争同一把锁
│   └── 验证互斥性
├── TestMultiNodeElection
│   ├── 3个节点参与选举
│   ├── 验证只有1个 leader
│   └── leader 退出后重新选举
└── TestFailureRecovery
    ├── 持锁节点故障
    └── 下一个节点自动获得锁
```

---

## 9. 性能考虑

- **Lock 获取延迟**：取决于网络 RTT + Raft 提交延迟
- **Watch 延迟**：事件推送延迟
- **Session KeepAlive 频率**：平衡网络开销和故障检测速度

---

## 10. 待完成清单

### 代码实现
- [ ] pkg/concurrency/session.go - Session 实现
- [ ] pkg/concurrency/mutex.go - Mutex 实现
- [ ] pkg/concurrency/election.go - Election 实现
- [ ] pkg/concurrency/stm.go - STM 实现（可选）

### 测试
- [ ] pkg/concurrency/concurrency_test.go - 单元测试
- [ ] test/concurrency_integration_test.go - 集成测试

### 文档和示例
- [ ] docs/CONCURRENCY_USAGE.md - 使用文档
- [ ] examples/concurrency/ - 示例代码
  - [ ] mutex_example.go
  - [ ] election_example.go

---

## 11. 估算工作量

| 任务 | 估计时间 | 优先级 |
|------|---------|--------|
| Session 实现 | 2-3 小时 | P0 |
| Mutex 实现 | 3-4 小时 | P0 |
| Election 实现 | 3-4 小时 | P0 |
| 单元测试 | 2-3 小时 | P0 |
| 集成测试 | 2-3 小时 | P0 |
| 文档和示例 | 2 小时 | P1 |
| STM 实现（可选）| 4-5 小时 | P2 |
| **总计** | **14-19 小时** | - |

约 **2-3 个工作日**

---

## 12. 参考资料

- [etcd concurrency package](https://pkg.go.dev/go.etcd.io/etcd/client/v3/concurrency)
- [etcd Distributed Locks](https://etcd.io/docs/v3.5/learning/locks/)
- [etcd Leader Election](https://etcd.io/docs/v3.5/learning/leader-election/)

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**状态**: 设计完成，待实现
