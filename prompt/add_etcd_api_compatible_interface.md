## 角色（Role）

你是 ETCD 接口层的**真正顶级专家**，对 etcd 的外部 API（尤其是 etcd v3 的 gRPC / 客户端 SDK 行为）非常熟悉，能够辨别和复现细节和边界行为（事务、watch、lease、auth、压缩/快照、可线性化读写、错误码/错误语义等）。

## 项目目标（Project Goal）

将当前已有的「轻量级 KV 存储」项目改造为 **在接口层 100% 兼容 etcd** 的服务，达到可以**直接使用 etcd 官方 Client SDK（例如 go client）对接、读写、watch、lease、txn 等所有常用功能并取得一致行为**的程度。
同时保留现有的 HTTP API（作为额外访问通道），但要求将 HTTP API 与 etcd 兼容层实现为**独立包**。

## 参考资源

* etcd 官方仓库（基础参考）：[https://github.com/etcd-io/etcd](https://github.com/etcd-io/etcd)

---

## 主要约束（硬性要求）

1. **接口兼容性**：实现必须在接口层上与 etcd v3（gRPC API / client SDK）100% 兼容，能被官方或第三方等价的 etcd 客户端直接使用。不得随意裁剪行为或省略关键 API。
2. **包划分**：

   * `pkg/httpapi`：现有 HTTP API 放在单独包并保留（或改造），与兼容层分离。
   * `pkg/etcdcompat`（或类似命名）：把所有与 etcd API / SDK 兼容的实现放在单独包里。
3. **项目布局**：整个仓库必须遵守 `golang-standards/project-layout` 的目录规范（cmd/, pkg/, internal/, api/, configs/, docs/ 等）。
4. **质量优先**：实现需采用最佳实践（清晰接口/文档、完善单元与集成测试、CI、可观测性、错误处理、一致性文档），不能偷工减料。
5. **兼容性声明**：任何无法在当前架构下保证的 etcd 行为/语义须在设计文档中逐条列出并给出替代方案或实现路线（不得模糊带过）。
6. **git提交约束**：整个过程中严格遵守，使用git commit 提交是不能出现claude任何签名与字眼描述
7. **编译**：不能添加如//go:build cgo编译标志，需要保障内存引擎与rocksdb引擎2种存储引擎所有测试案例都通过
8. **集成测试案例**：需要为 etcd 协议创建完整的集成测试，参考 HTTP API 的测试模式，覆盖内存和 RocksDB 两种引擎的单节点和集群测试。 让先查看现有的 HTTP API 测试文件结构，（etcd访问协议接口的兼容集成测试（测试文件夹：test），需要参考http api协议的集成测试方式（参考文件http_api_*.go），同时覆盖内存与rocksdb的2种存储引擎的单节点与集群多节点读写与一致性检查全访问测试）
9. **存储目录**：内存引擎的存储目录可以放在data/memory下，rocksdb引擎的存储目录可以放在data/rocksdb下
10. **多引擎实现要求**：由于同时支持内存与rocksdb的2种存储引擎，在实现所有功能时都需要严格实现2种引擎的功能，不能偷懒
11. **架构要求1**：符合单一存储实例、协议无关的设计原则，实现多个访问协议时，http API 和 etcd API 需要使用不同的接口对象，但存储层只能共享一个存储实例。所有数据仅有一份，通过不同的访问协议来源进行访问。通过 http API 接口写入的数据，可以通过 etcd API 协议（etcd v3 的 gRPC API 或 client SDK）进行访问。不同的访问协议来源与存储层无关，访问层仅负责接收客户请求，并通过存储接口进行查询返回。同一个存储引擎只有一个实例，raft 层也应当是通用的。

12. 
--

## 功能需求（按优先级）

> 以下列出必须支持的 etcd API/能力（对等于 client SDK 的调用路径/语义，需要实现或兼容）：

### 必须实现（最优先）

* KV 基本操作：Range（Get）、Put、Delete（单键/范围）
* Watch：支持创建、取消、事件类型（PUT, DELETE）、历史事件、复用流式 watch 语义
* Lease：grant、revoke、keepalive（单次/流式）、租约绑定到 key、租约过期行为
* Authentication/Authorization（如启用）：用户/角色管理、Token 验证（如果产品需求包含）
* Maintenance/Cluster 管理 API（至少提供与客户端期望的 API 路由/错误码）：snapshot、status/health、defragment 等
* Lock/Concurrency 高层接口（通过 Lease + txn 实现，兼容 etcd 的 concurrency 包行为，至少保证常用锁/会话语义）
* 服务器端错误语义、错误码（例如 gRPC codes）需要与 etcd 客户端的预期一致

# 可选实现

* Txn（事务）：比较-置换组合（Compare, Then, Else）并返回同样的响应结构与错误语义
* Compact（压缩）、Revision 语义：返回 revision、支持 compact 接口并保持 revision 行为一致


### 建议但视实现路径决定（优先级次之）

* gRPC 接口与 HTTP gateway（gRPC-Gateway）同时支持，或者提供完全兼容 gRPC API 的端点（推荐优先确保 gRPC v3 完整兼容）

---

## 技术/架构实现建议（可选方案）

> 你必须在设计文档中列出至少两条可行实现路线（优缺点与实现复杂度）并给出推荐。

1. **参考完整实现路线（推荐用于生产级一致性）**

   * 在 `pkg/etcdcompat` 中实现 etcd v3 gRPC server（与官方 proto 一致），实现所有服务：KV、Auth、Lease、Watch、Maintenance 等。
   * HTTP API 放 `pkg/httpapi`，仅负责 HTTP 客户端路由/兼容层（非 etcd API 的独立接口）。
   * 优点：语义一致、可扩展、客户端透明。缺点：实现复杂，开发/测试成本高。
 

## 交付物（Deliverables）

1. 源代码（遵循 golang-standards/project-layout）

   * `pkg/etcdcompat`：etcd 接口兼容实现（gRPC server、proto、handlers、测试）

2. 文档（`docs/`）

   * 设计文档（架构选型、语义边界、与 etcd 行为的差异清单）
   * 接口文档（可用的 proto、端点、使用示例）
   * 开发/部署说明（如何启动、配置、集群加入、备份/恢复）
  
3. 测试套件与 CI

   * 单元测试覆盖关键逻辑
   * 集成测试：用官方 etcd 客户端（例如 go client）对接并通过典型操作场景（Get/Put/Txn/Watch/Lease）
   * Conformance 测试脚本（自动化执行客户端示例，生成 PASS/FAIL 报告）
   * 
4. 示例/示范代码（`examples/`）

   * 使用 `clientv3`（go） 的样例程序，演示连接、Put/Get/Txn/Watch/Lease、Lock 等操作均能正常工作
  
5. 性能/一致性验证报告（若为生产级交付）

---

## 验收标准（Acceptance Criteria / Tests）

> 每项必须通过自动化测试或可复现的手动步骤：

1. **接口兼容测试**

   * 使用官方 `go.etcd.io/etcd/client/v3`（clientv3）或等价 SDK 的示例代码，对接到本服务并完成：Put、Get（含 range）、Txn（复杂 Compare+Then+Else）、Watch（持续订阅事件）、Lease（grant、keepalive、过期触发）且行为与官方 etcd 客户端对 etcd-server 的交互一致（返回格式、错误码、事件结构等）。
   * 为最重要的 10 个 API 用例提供自动化集成测试脚本（CI 运行）。

2. **行为/语义一致性**
   * txn 的 compare/then/else 语义与 etcd client 的预期一致（原子性、返回字段）。
   * lease 到期必须触发绑定 key 的删除（或和 etcd 行为一致）。

3. **包与代码结构**

   * HTTP API 与 etcd 兼容层代码放在独立包中，符合要求的目录结构并通过 `go build ./...` 无错误。

4. **文档与示例**

   * 提供至少 3 个示例（连接/put-get/txn/watch），示例能直接运行并通过 CI 验收。

5. **测试覆盖与 CI**

   * 单元/集成测试通过率满足项目约定（例如关键模块 >= 80%），并在 CI 上能复现。

---
  

## 实施细节与注意事项（工程级）

* **使用官方 proto**：尽量复用 etcd 的 proto 定义，避免在兼容层产生语义差异。
* **错误码与状态**：必须用与 gRPC 标准一致的 status codes（如 codes.NotFound, codes.FailedPrecondition 等），并保持错误信息结构兼容。
* **性能与资源**：watch 长连接与 lease keepalive 会带来连接管理压力，需设计合理的连接池/限流/回收策略。
* **一致性声明**：如果暂时无法达到 etcd 的分布式一致性（比如只做单节点存储），**必须在文档中明确说明限制**，并说明采用何种策略来弥补或后续计划（例如集成 etcd/raft）。
* **向后兼容**：如果已有 HTTP API 存在旧客户端依赖，保证不破坏现有 HTTP 接口，且把 HTTP 放在独立包，便于逐步替换或适配。

---

## 验证示例（要给开发者/CI 的可运行测试思路）

* 在仓库 `tests/compat/` 下提供一个脚本：使用 `go.etcd.io/etcd/client/v3` 连接到本服务地址，执行以下步骤并断言结果：

  1. Put key `foo` -> value `v1`，Get key `foo` 并检查 value。
  2. Txn: Compare key `foo` == `v1` then Put `foo` -> `v2` else Put `foo` -> `v3`，断言返回与预期一致。
  3. Grant lease（5s），Put key `bar` with lease，等待 6s，确认 `bar` 被删除。
  4. Start Watch from current revision + 1，Put key `watched`，确认 watch 收到事件。
* CI 应在每次 PR 时运行该脚本并阻止回归。

---

## 最终交付说明（写给验收方）

交付时请提供：源代码、构建/部署脚本、设计文档（含语义差异清单）、自动化测试结果（CI 报告）、示例代码（可直接运行）。若任何 etcd 行为无法复现，应在 `docs/limitations.md` 中明确说明并给出替代方案或后续实现计划。
