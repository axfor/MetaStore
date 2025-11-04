# ✅ MySQL 协议访问层开发任务说明书（Best-Practice Prompt）

## 🧠 角色（Role Definition）

你是一位对 **MySQL 接口层 / 协议层协议实现** 有深入研究的 **顶级系统工程师与协议专家**。
你精通：

* MySQL 外部访问协议（包括握手、认证、SQL 解析、事务语义、错误码与行为一致性）；
* MySQL 网络协议帧格式、MySQL CLI/驱动与 Server 的交互机制；
* SQL 标准兼容性、事务隔离级别语义、ACID 实现一致性；
* 客户端行为（如连接复用、prepared statement、元数据缓存、错误码期望）。

你的输出应体现出：

* **系统架构意识**（组件解耦、包层次清晰）；
* **协议细节准确性**；
* **工程可落地性**；
* **严格遵守实现边界与规范约束**。

---

## 🎯 项目目标（Project Goal）

为现有的轻量级 **KV 存储项目** 新增一个 **MySQL 协议访问层**，
要求在访问协议层做到 **100% 协议级兼容** MySQL 服务器（即可直接用标准 MySQL 客户端连接并执行操作）。

现有系统已具备：

* HTTP API 访问层；
* etcd v3 协议访问层。

要求保持：

* ✅ 现有两种协议访问层功能不受破坏；
* ✅ 访问层独立封装（包/模块隔离）；
* ✅ 底层存储层实例共享；
* ✅ 各协议层操作数据一致。

目标是：
**支持 MySQL Client / JDBC / Python MySQL Connector / ORM 等客户端的直接访问与操作。**

---

## 📚 参考实现与资料（Reference）

* 起点代码参考库：[https://github.com/go-mysql-org/go-mysql](https://github.com/go-mysql-org/go-mysql)
* 官方 MySQL 文档参考：

  * [MySQL Client/Server Protocol](https://dev.mysql.com/doc/internals/en/client-server-protocol.html)
  * [MySQL Error Codes and Messages](https://dev.mysql.com/doc/refman/en/server-error-reference.html)

---

## 🧩 实现要求（Implementation Requirements）

### 1️⃣ 协议层架构设计

* 新增 `mysql_protocol` 模块，封装 MySQL 协议访问逻辑；
* 保持与 HTTP、etcd v3 协议层结构平行；
* 模块职责单一：仅负责**协议解析与请求转发**；
* 通过统一接口访问底层 KV 存储服务。

### 2️⃣ 功能要求

* 支持以下 MySQL 协议阶段与行为：

  1. 握手与认证阶段（包括用户名密码验证、capabilities 协商）；
  2. SQL 命令解析与执行映射（至少支持：`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `BEGIN`, `COMMIT`, `ROLLBACK`）；
  3. 错误码返回与行为一致性；
  4. 支持事务的基本语义（读写隔离、ACID 一致性层面的最小保障）；
  5. 支持 `SHOW DATABASES`, `SHOW TABLES`, `DESCRIBE`, `PING`, `QUIT` 等常用命令。

* **SQL 解析可选择**轻量级方案（如 go-mysql 中 parser 模块），或自行实现简单解析器。

### 3️⃣ 一致性要求

* 各访问协议层共享同一存储层实例；
* 保证 HTTP / etcd / MySQL 协议访问同一数据；
* 对于事务写入行为，确保跨协议事务可见性与提交一致。

### 4️⃣ 错误与行为一致性

* 返回 MySQL 标准错误码（如 `ER_ACCESS_DENIED_ERROR`, `ER_SYNTAX_ERROR` 等）；
* 保持与 MySQL Server 一致的交互行为（包括连接关闭、超时、无效 SQL 响应）；
* 对于不支持的命令，返回标准错误：`ER_UNKNOWN_COM_ERROR`。

---

## 🚫 禁止与风险控制（Prohibited Behaviors）

禁止出现以下情况：

1. ❌ **破坏性修改**：改动或重构现有 HTTP / etcd API 层逻辑；
2. ❌ **混合职责**：在 MySQL 协议实现中直接访问底层存储逻辑；
3. ❌ **非兼容实现**：实现与 MySQL 客户端握手或认证不兼容；
4. ❌ **跳过错误处理**：未严格返回 MySQL 标准错误码；
5. ❌ **自定义 SQL 方言**：不得扩展或改变 MySQL 语义；
6. ❌ **数据不一致风险**：不同协议访问同一数据出现读写不一致；
7. ❌ **未文档化行为**：任何协议行为偏离 MySQL 标准而未记录说明；
8. ❌ **安全隐患**：明文密码存储、固定凭证、无身份验证访问。

---

## ✅ 验收标准（Acceptance Criteria）

以下测试全部通过方可验收：

### A. 协议兼容性测试

* [ ] 可使用官方 `mysql` CLI 工具通过 TCP 连接；
* [ ] 可通过用户名密码认证；
* [ ] `SHOW DATABASES`, `SHOW TABLES` 等命令输出符合预期；
* [ ] 执行标准 CRUD SQL 命令正常返回；
* [ ] 异常语法返回标准 MySQL 错误码。

### B. 事务与一致性测试

* [ ] `BEGIN / COMMIT / ROLLBACK` 语义符合事务要求；
* [ ] 在 HTTP / etcd / MySQL 三协议间交叉读写结果一致；
* [ ] 并发写入无脏读或丢失更新。

### C. 模块与代码结构

* [ ] `mysql_protocol` 模块独立；
* [ ] 无循环依赖；
* [ ] 所有公共接口有注释与单元测试；
* [ ] 存储层访问通过统一接口实现。

### D. 安全与健壮性

* [ ] 异常断开后资源自动释放；
* [ ] 无 panic；
* [ ] 认证逻辑可配置；
* [ ] 无明文凭证或调试后门。

---

## 🧪 推荐测试工具

| 工具               | 用途                  |
| ------------------ | --------------------- |
| `mysql` CLI        | 基础连接与SQL执行验证 |
| `mysqlslap`        | 并发连接压力测试      |
| `go test`          | 单元测试              |
| `wireshark`        | 验证MySQL协议帧一致性 |
| `etcdctl` / `curl` | 跨协议一致性验证      |

---

## 💬 输出要求（For Model or Engineer）

当生成设计文档、代码框架或伪代码时，请明确说明：

* 模块结构；
* 每层职责；
* 关键协议字段解释；
* 事务语义映射逻辑；
* 错误码与 MySQL 一致性策略。

---

是否希望我帮你把这个 prompt 再转换成「给模型执行的版本」（比如适合放入 system prompt 的可直接用模板）？
