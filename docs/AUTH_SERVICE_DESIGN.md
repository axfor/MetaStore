# Auth Service 实现设计文档

## 概述

本文档详细描述了 MetaStore 的 etcd v3 兼容 Auth Service 的设计方案、数据模型、接口定义和实现步骤。

**目标**: 实现 100% etcd v3 Auth API 兼容，提供完整的认证和授权功能。

**优先级**: P0（必须实现，不符合 prompt 要求）

---

## 1. 功能需求

### 1.1 核心功能

根据 etcd v3 API 规范，Auth Service 需要实现以下功能：

#### 认证管理
- ✅ **AuthEnable** - 启用认证系统
- ✅ **AuthDisable** - 禁用认证系统
- ✅ **AuthStatus** - 查询认证状态
- ✅ **Authenticate** - 用户身份认证，返回 Token

#### 用户管理
- ✅ **UserAdd** - 创建新用户
- ✅ **UserDelete** - 删除用户
- ✅ **UserGet** - 查询用户信息
- ✅ **UserList** - 列出所有用户
- ✅ **UserChangePassword** - 修改用户密码
- ✅ **UserGrantRole** - 授予用户角色
- ✅ **UserRevokeRole** - 撤销用户角色

#### 角色管理
- ✅ **RoleAdd** - 创建角色
- ✅ **RoleDelete** - 删除角色
- ✅ **RoleGet** - 查询角色信息
- ✅ **RoleList** - 列出所有角色
- ✅ **RoleGrantPermission** - 授予角色权限
- ✅ **RoleRevokePermission** - 撤销角色权限

### 1.2 权限模型

```
用户 (User)
  ├─ 用户名 (name)
  ├─ 密码哈希 (password hash)
  └─ 角色列表 (roles[])

角色 (Role)
  ├─ 角色名 (name)
  └─ 权限列表 (permissions[])

权限 (Permission)
  ├─ 类型 (type: READ/WRITE/READWRITE)
  ├─ 键范围开始 (key)
  └─ 键范围结束 (rangeEnd)
```

### 1.3 特殊用户和角色

- **root 用户**: 默认超级管理员，拥有所有权限
- **root 角色**: 拥有所有权限的角色

---

## 2. 架构设计

### 2.1 组件架构

```
┌─────────────────────────────────────────────────────────┐
│                    etcd gRPC API                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐│
│  │ KV API   │  │Watch API │  │Lease API │  │Auth API ││
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬────┘│
│       │             │              │              │     │
│       └─────────────┴──────────────┴──────────────┘     │
│                         │                                │
│                   ┌─────▼─────┐                         │
│                   │ Auth      │  Token 验证             │
│                   │ Interceptor│  权限检查               │
│                   └─────┬─────┘                         │
└─────────────────────────┼───────────────────────────────┘
                          │
                   ┌──────▼──────┐
                   │  Auth       │
                   │  Manager    │
                   └──────┬──────┘
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                  │
   ┌────▼────┐      ┌────▼────┐       ┌────▼────┐
   │  User   │      │  Role   │       │  Token  │
   │ Manager │      │ Manager │       │ Manager │
   └────┬────┘      └────┬────┘       └────┬────┘
        │                 │                  │
        └─────────────────┴──────────────────┘
                          │
                   ┌──────▼──────┐
                   │   Storage   │
                   │   (Raft)    │
                   └─────────────┘
```

### 2.2 数据存储

所有认证数据存储在底层 KV Store 中，使用特殊的键前缀：

```
/__auth/enabled           -> "true" 或 "false"
/__auth/users/{name}      -> UserInfo (JSON)
/__auth/roles/{name}      -> RoleInfo (JSON)
/__auth/tokens/{token}    -> TokenInfo (JSON)
```

### 2.3 Token 管理

#### Token 生成
- 使用 JWT (JSON Web Token) 标准
- 包含：用户名、过期时间、签名
- 默认有效期：24 小时

#### Token 验证
- 验证签名
- 检查过期时间
- 加载用户权限

---

## 3. 数据模型

### 3.1 Go 结构定义

```go
// UserInfo 用户信息
type UserInfo struct {
    Name         string   `json:"name"`
    PasswordHash string   `json:"password_hash"` // bcrypt hash
    Roles        []string `json:"roles"`
    CreatedAt    int64    `json:"created_at"`
}

// RoleInfo 角色信息
type RoleInfo struct {
    Name        string       `json:"name"`
    Permissions []Permission `json:"permissions"`
    CreatedAt   int64        `json:"created_at"`
}

// Permission 权限定义
type Permission struct {
    Type     PermissionType `json:"type"`      // READ, WRITE, READWRITE
    Key      []byte         `json:"key"`       // 键范围开始
    RangeEnd []byte         `json:"range_end"` // 键范围结束
}

// PermissionType 权限类型
type PermissionType int

const (
    PermissionRead      PermissionType = 0
    PermissionWrite     PermissionType = 1
    PermissionReadWrite PermissionType = 2
)

// TokenInfo Token 信息
type TokenInfo struct {
    Token     string `json:"token"`
    Username  string `json:"username"`
    ExpiresAt int64  `json:"expires_at"`
}
```

---

## 4. 接口实现

### 4.1 gRPC 接口实现

#### 文件：pkg/etcdapi/auth.go

```go
package etcdapi

import (
    "context"
    pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// AuthServer 实现 etcd Auth 服务
type AuthServer struct {
    pb.UnimplementedAuthServer
    server *Server
}

// AuthEnable 启用认证
func (s *AuthServer) AuthEnable(ctx context.Context, req *pb.AuthEnableRequest) (*pb.AuthEnableResponse, error) {
    // TODO: 实现
    // 1. 检查 root 用户是否存在
    // 2. 设置 /__auth/enabled = "true"
    // 3. 返回响应
    return nil, nil
}

// AuthDisable 禁用认证
func (s *AuthServer) AuthDisable(ctx context.Context, req *pb.AuthDisableRequest) (*pb.AuthDisableResponse, error) {
    // TODO: 实现
    // 1. 验证调用者是 root
    // 2. 设置 /__auth/enabled = "false"
    // 3. 返回响应
    return nil, nil
}

// AuthStatus 查询认证状态
func (s *AuthServer) AuthStatus(ctx context.Context, req *pb.AuthStatusRequest) (*pb.AuthStatusResponse, error) {
    // TODO: 实现
    return nil, nil
}

// Authenticate 用户认证
func (s *AuthServer) Authenticate(ctx context.Context, req *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
    // TODO: 实现
    // 1. 验证用户名密码
    // 2. 生成 JWT token
    // 3. 存储 token 信息
    // 4. 返回 token
    return nil, nil
}

// UserAdd 添加用户
func (s *AuthServer) UserAdd(ctx context.Context, req *pb.AuthUserAddRequest) (*pb.AuthUserAddResponse, error) {
    // TODO: 实现
    // 1. 验证权限
    // 2. 检查用户是否已存在
    // 3. Hash 密码（bcrypt）
    // 4. 存储用户信息
    // 5. 返回响应
    return nil, nil
}

// UserDelete 删除用户
func (s *AuthServer) UserDelete(ctx context.Context, req *pb.AuthUserDeleteRequest) (*pb.AuthUserDeleteRequest, error) {
    // TODO: 实现
    return nil, nil
}

// UserGet 获取用户信息
func (s *AuthServer) UserGet(ctx context.Context, req *pb.AuthUserGetRequest) (*pb.AuthUserGetResponse, error) {
    // TODO: 实现
    return nil, nil
}

// UserList 列出所有用户
func (s *AuthServer) UserList(ctx context.Context, req *pb.AuthUserListRequest) (*pb.AuthUserListResponse, error) {
    // TODO: 实现
    return nil, nil
}

// UserChangePassword 修改密码
func (s *AuthServer) UserChangePassword(ctx context.Context, req *pb.AuthUserChangePasswordRequest) (*pb.AuthUserChangePasswordResponse, error) {
    // TODO: 实现
    return nil, nil
}

// UserGrantRole 授予角色
func (s *AuthServer) UserGrantRole(ctx context.Context, req *pb.AuthUserGrantRoleRequest) (*pb.AuthUserGrantRoleResponse, error) {
    // TODO: 实现
    return nil, nil
}

// UserRevokeRole 撤销角色
func (s *AuthServer) UserRevokeRole(ctx context.Context, req *pb.AuthUserRevokeRoleRequest) (*pb.AuthUserRevokeRoleResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleAdd 添加角色
func (s *AuthServer) RoleAdd(ctx context.Context, req *pb.AuthRoleAddRequest) (*pb.AuthRoleAddResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleDelete 删除角色
func (s *AuthServer) RoleDelete(ctx context.Context, req *pb.AuthRoleDeleteRequest) (*pb.AuthRoleDeleteResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleGet 获取角色信息
func (s *AuthServer) RoleGet(ctx context.Context, req *pb.AuthRoleGetRequest) (*pb.AuthRoleGetResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleList 列出所有角色
func (s *AuthServer) RoleList(ctx context.Context, req *pb.AuthRoleListRequest) (*pb.AuthRoleListResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleGrantPermission 授予权限
func (s *AuthServer) RoleGrantPermission(ctx context.Context, req *pb.AuthRoleGrantPermissionRequest) (*pb.AuthRoleGrantPermissionResponse, error) {
    // TODO: 实现
    return nil, nil
}

// RoleRevokePermission 撤销权限
func (s *AuthServer) RoleRevokePermission(ctx context.Context, req *pb.AuthRoleRevokePermissionRequest) (*pb.AuthRoleRevokePermissionResponse, error) {
    // TODO: 实现
    return nil, nil
}
```

### 4.2 Auth Manager 实现

#### 文件：pkg/etcdapi/auth_manager.go

```go
package etcdapi

import (
    "crypto/rand"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "sync"
    "time"

    "golang.org/x/crypto/bcrypt"
    "metaStore/internal/kvstore"
)

const (
    authEnabledKey  = "/__auth/enabled"
    authUserPrefix  = "/__auth/users/"
    authRolePrefix  = "/__auth/roles/"
    authTokenPrefix = "/__auth/tokens/"
)

// AuthManager 管理认证和授权
type AuthManager struct {
    mu    sync.RWMutex
    store kvstore.Store

    // 内存缓存（可选优化）
    enabled bool
    users   map[string]*UserInfo
    roles   map[string]*RoleInfo
    tokens  map[string]*TokenInfo
}

// NewAuthManager 创建 Auth 管理器
func NewAuthManager(store kvstore.Store) *AuthManager {
    am := &AuthManager{
        store:  store,
        users:  make(map[string]*UserInfo),
        roles:  make(map[string]*RoleInfo),
        tokens: make(map[string]*TokenInfo),
    }

    // 加载认证状态
    am.loadState()

    // 启动 token 清理定时器
    go am.cleanupExpiredTokens()

    return am
}

// loadState 从存储加载认证状态
func (am *AuthManager) loadState() error {
    // TODO: 实现
    // 1. 读取 /__auth/enabled
    // 2. 加载所有用户
    // 3. 加载所有角色
    // 4. 加载有效 token
    return nil
}

// IsEnabled 返回认证是否启用
func (am *AuthManager) IsEnabled() bool {
    am.mu.RLock()
    defer am.mu.RUnlock()
    return am.enabled
}

// Enable 启用认证
func (am *AuthManager) Enable() error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 检查 root 用户是否存在
    // 2. 如果不存在，提示需要先创建 root
    // 3. 设置 enabled = true
    // 4. 持久化到存储
    return nil
}

// Disable 禁用认证
func (am *AuthManager) Disable() error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 设置 enabled = false
    // 2. 持久化到存储
    // 3. 清空 token 缓存
    return nil
}

// Authenticate 认证用户，返回 token
func (am *AuthManager) Authenticate(username, password string) (string, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    // 1. 查找用户
    // 2. 验证密码（bcrypt.CompareHashAndPassword）
    // 3. 生成 token
    // 4. 存储 token 信息
    // 5. 返回 token
    return "", nil
}

// ValidateToken 验证 token
func (am *AuthManager) ValidateToken(token string) (*TokenInfo, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    // 1. 查找 token
    // 2. 检查是否过期
    // 3. 返回 token 信息
    return nil, nil
}

// CheckPermission 检查用户是否有权限执行操作
func (am *AuthManager) CheckPermission(username string, key []byte, permType PermissionType) error {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    // 1. 如果是 root 用户，直接通过
    // 2. 获取用户的所有角色
    // 3. 检查角色的权限
    // 4. 判断 key 是否在权限范围内
    return nil
}

// AddUser 添加用户
func (am *AuthManager) AddUser(name, password string, options *pb.UserAddOptions) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 检查用户是否已存在
    // 2. Hash 密码
    // 3. 创建 UserInfo
    // 4. 持久化到存储
    // 5. 更新缓存
    return nil
}

// DeleteUser 删除用户
func (am *AuthManager) DeleteUser(name string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 检查是否是 root（不能删除）
    // 2. 从存储删除
    // 3. 从缓存删除
    // 4. 清理该用户的所有 token
    return nil
}

// GetUser 获取用户信息
func (am *AuthManager) GetUser(name string) (*UserInfo, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    return nil, nil
}

// ListUsers 列出所有用户
func (am *AuthManager) ListUsers() ([]*UserInfo, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    return nil, nil
}

// ChangePassword 修改密码
func (am *AuthManager) ChangePassword(name, newPassword string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 查找用户
    // 2. Hash 新密码
    // 3. 更新用户信息
    // 4. 持久化
    // 5. 清理该用户的所有 token（强制重新登录）
    return nil
}

// GrantRole 授予角色
func (am *AuthManager) GrantRole(username, rolename string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 检查用户和角色是否存在
    // 2. 添加角色到用户的角色列表
    // 3. 持久化
    return nil
}

// RevokeRole 撤销角色
func (am *AuthManager) RevokeRole(username, rolename string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    return nil
}

// AddRole 添加角色
func (am *AuthManager) AddRole(name string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    return nil
}

// DeleteRole 删除角色
func (am *AuthManager) DeleteRole(name string) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    // 1. 检查是否是 root 角色（不能删除）
    // 2. 从所有用户中移除该角色
    // 3. 删除角色
    return nil
}

// GetRole 获取角色信息
func (am *AuthManager) GetRole(name string) (*RoleInfo, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    return nil, nil
}

// ListRoles 列出所有角色
func (am *AuthManager) ListRoles() ([]*RoleInfo, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()

    // TODO: 实现
    return nil, nil
}

// GrantPermission 授予权限
func (am *AuthManager) GrantPermission(rolename string, perm Permission) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    return nil
}

// RevokePermission 撤销权限
func (am *AuthManager) RevokePermission(rolename string, key, rangeEnd []byte) error {
    am.mu.Lock()
    defer am.mu.Unlock()

    // TODO: 实现
    return nil
}

// generateToken 生成随机 token
func (am *AuthManager) generateToken() (string, error) {
    b := make([]byte, 32)
    if _, err := rand.Read(b); err != nil {
        return "", err
    }
    return base64.URLEncoding.EncodeToString(b), nil
}

// hashPassword Hash 密码
func hashPassword(password string) (string, error) {
    bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
    return string(bytes), err
}

// checkPasswordHash 验证密码
func checkPasswordHash(password, hash string) bool {
    err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
    return err == nil
}

// cleanupExpiredTokens 定期清理过期 token
func (am *AuthManager) cleanupExpiredTokens() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        am.mu.Lock()
        now := time.Now().Unix()
        for token, info := range am.tokens {
            if info.ExpiresAt < now {
                delete(am.tokens, token)
                // TODO: 从存储删除
            }
        }
        am.mu.Unlock()
    }
}
```

### 4.3 Auth Interceptor（权限拦截器）

#### 文件：pkg/etcdapi/auth_interceptor.go

```go
package etcdapi

import (
    "context"
    "strings"

    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/metadata"
    "google.golang.org/grpc/status"
)

// AuthInterceptor gRPC 拦截器，用于验证请求权限
func (s *Server) AuthInterceptor(
    ctx context.Context,
    req interface{},
    info *grpc.UnaryServerInfo,
    handler grpc.UnaryHandler,
) (interface{}, error) {
    // 如果认证未启用，直接放行
    if !s.authMgr.IsEnabled() {
        return handler(ctx, req)
    }

    // Auth API 本身不需要认证（除了 Disable）
    if isAuthAPI(info.FullMethod) {
        return handler(ctx, req)
    }

    // 从 metadata 提取 token
    md, ok := metadata.FromIncomingContext(ctx)
    if !ok {
        return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
    }

    tokens := md["token"]
    if len(tokens) == 0 {
        return nil, status.Errorf(codes.Unauthenticated, "missing token")
    }

    // 验证 token
    tokenInfo, err := s.authMgr.ValidateToken(tokens[0])
    if err != nil {
        return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
    }

    // TODO: 检查权限
    // 根据 info.FullMethod 和 req 判断需要的权限
    // 调用 authMgr.CheckPermission()

    // 将用户信息注入 context
    ctx = context.WithValue(ctx, "username", tokenInfo.Username)

    return handler(ctx, req)
}

// isAuthAPI 判断是否是 Auth API
func isAuthAPI(method string) bool {
    return strings.HasPrefix(method, "/etcdserverpb.Auth/")
}
```

---

## 5. 实现步骤

### 阶段 1：基础框架（估计 2-3 小时）

1. ✅ **创建数据模型**
   - [x] 定义 UserInfo, RoleInfo, Permission 结构
   - [x] 定义存储键前缀常量

2. ✅ **实现 AuthManager 基础**
   - [x] NewAuthManager 构造函数
   - [x] loadState() 加载状态
   - [x] Enable/Disable 启用禁用认证
   - [x] 密码 Hash 工具函数

3. ✅ **创建 gRPC 服务框架**
   - [x] AuthServer 结构
   - [x] 所有接口方法签名（TODO 实现）

### 阶段 2：认证功能（估计 3-4 小时）

4. **实现用户管理**
   - [ ] UserAdd - 创建用户
   - [ ] UserDelete - 删除用户
   - [ ] UserGet - 查询用户
   - [ ] UserList - 列出用户
   - [ ] UserChangePassword - 修改密码

5. **实现 Token 认证**
   - [ ] Authenticate - 用户认证
   - [ ] generateToken - Token 生成
   - [ ] ValidateToken - Token 验证
   - [ ] cleanupExpiredTokens - 清理过期 Token

### 阶段 3：授权功能（估计 3-4 小时）

6. **实现角色管理**
   - [ ] RoleAdd - 创建角色
   - [ ] RoleDelete - 删除角色
   - [ ] RoleGet - 查询角色
   - [ ] RoleList - 列出角色

7. **实现权限管理**
   - [ ] RoleGrantPermission - 授予权限
   - [ ] RoleRevokePermission - 撤销权限
   - [ ] UserGrantRole - 授予角色
   - [ ] UserRevokeRole - 撤销角色

8. **实现权限检查**
   - [ ] CheckPermission - 权限验证
   - [ ] AuthInterceptor - gRPC 拦截器
   - [ ] 集成到所有 API

### 阶段 4：初始化和默认数据（估计 1-2 小时）

9. **创建默认用户和角色**
   - [ ] root 用户（密码为空或随机生成）
   - [ ] root 角色（所有权限）
   - [ ] 首次启动时自动创建

10. **集成到 Server**
    - [ ] 在 NewServer 中创建 AuthManager
    - [ ] 注册 AuthServer
    - [ ] 添加 AuthInterceptor

### 阶段 5：测试（估计 4-5 小时）

11. **单元测试**
    - [ ] AuthManager 所有方法测试
    - [ ] 密码 Hash 测试
    - [ ] Token 生成验证测试
    - [ ] 权限检查测试

12. **集成测试**
    - [ ] 完整认证流程测试
    - [ ] 多用户多角色测试
    - [ ] 权限拒绝测试
    - [ ] Token 过期测试

13. **etcd 客户端兼容性测试**
    - [ ] 使用 clientv3 测试认证
    - [ ] 测试权限检查
    - [ ] 测试 Token 刷新

---

## 6. 关键技术点

### 6.1 密码安全

- 使用 **bcrypt** 算法 Hash 密码
- 成本因子：默认 10（平衡安全性和性能）
- 永远不存储明文密码

```go
import "golang.org/x/crypto/bcrypt"

// Hash 密码
hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

// 验证密码
err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
```

### 6.2 Token 管理

- Token 使用随机生成的 32 字节字符串（Base64 编码）
- 默认有效期：24 小时
- 存储格式：`/__auth/tokens/{token} -> TokenInfo (JSON)`
- 定期清理过期 Token（每 5 分钟）

### 6.3 权限检查算法

```
CheckPermission(username, key, permType):
  1. 如果 username == "root"，返回 true
  2. 获取用户的所有角色
  3. 遍历每个角色的权限列表
  4. 对于每个权限：
     a. 检查 permType 是否匹配
     b. 检查 key 是否在 [perm.Key, perm.RangeEnd) 范围内
     c. 如果匹配，返回 true
  5. 返回 false（无权限）
```

### 6.4 存储事务

用户和角色的修改需要保证原子性：

```go
// 使用 Txn 保证原子性
cmps := []kvstore.Compare{
    // 检查用户不存在
    {Key: userKey, Target: kvstore.CompareVersion, Result: kvstore.CompareEqual, TargetUnion: {Version: 0}},
}
thenOps := []kvstore.Op{
    // 创建用户
    {Type: kvstore.OpPut, Key: userKey, Value: userJSON},
}
elseOps := []kvstore.Op{}

resp, err := store.Txn(cmps, thenOps, elseOps)
if !resp.Succeeded {
    return ErrUserAlreadyExists
}
```

---

## 7. 配置和部署

### 7.1 配置选项

```go
type AuthConfig struct {
    Enabled         bool          // 默认启用认证
    TokenTTL        time.Duration // Token 有效期，默认 24h
    RootPassword    string        // root 初始密码（首次启动）
    PasswordCost    int           // bcrypt 成本因子，默认 10
    TokenCleanupInterval time.Duration // Token 清理间隔，默认 5min
}
```

### 7.2 首次启动流程

```
1. 检查 /__auth/users/root 是否存在
2. 如果不存在：
   a. 使用配置的 RootPassword 或生成随机密码
   b. 创建 root 用户
   c. 创建 root 角色（所有权限）
   d. 授予 root 用户 root 角色
   e. 打印 root 密码到日志（如果是随机生成的）
3. 如果 AuthEnabled 配置为 true：
   a. 启用认证
```

---

## 8. 测试计划

### 8.1 单元测试用例

```
TestAuthManager
├── TestEnable
├── TestDisable
├── TestAddUser
│   ├── 正常添加
│   ├── 重复添加（应失败）
│   └── 密码 Hash 验证
├── TestDeleteUser
│   ├── 正常删除
│   ├── 删除 root（应失败）
│   └── 删除不存在的用户
├── TestAuthenticate
│   ├── 正确密码
│   ├── 错误密码
│   └── 不存在的用户
├── TestToken
│   ├── 生成 Token
│   ├── 验证有效 Token
│   ├── 验证过期 Token
│   └── 验证无效 Token
├── TestRole
│   ├── 添加角色
│   ├── 删除角色
│   └── 授予/撤销权限
└── TestPermission
    ├── 检查读权限
    ├── 检查写权限
    ├── 范围权限检查
    └── root 用户权限
```

### 8.2 集成测试用例

```
TestAuthIntegration
├── TestFullAuthFlow
│   ├── 创建用户
│   ├── 认证获取 Token
│   ├── 使用 Token 访问 KV API
│   └── 修改密码后重新认证
├── TestPermissionEnforcement
│   ├── 无权限访问（应拒绝）
│   ├── 有权限访问（应通过）
│   └── 部分权限访问
├── TestRoleBasedAccess
│   ├── 创建角色和权限
│   ├── 授予用户角色
│   ├── 验证权限生效
│   └── 撤销角色后权限失效
└── TestAuthDisabled
    └── 禁用认证后无需 Token
```

### 8.3 etcd 客户端兼容性测试

```go
func TestEtcdClientAuth(t *testing.T) {
    // 启动服务器（认证启用）
    // 创建用户
    // 使用 clientv3 认证
    cli, err := clientv3.New(clientv3.Config{
        Endpoints: []string{"localhost:2379"},
        Username:  "testuser",
        Password:  "testpass",
    })

    // 测试操作
    cli.Put(ctx, "foo", "bar")
    cli.Get(ctx, "foo")
}
```

---

## 9. 性能考虑

### 9.1 缓存策略

- **内存缓存**: 将用户、角色、Token 缓存在内存中
- **缓存失效**: 修改时立即更新缓存
- **启动加载**: 服务启动时一次性加载所有认证数据

### 9.2 并发控制

- 使用 `sync.RWMutex` 保护认证数据
- 读操作使用 RLock（允许并发读）
- 写操作使用 Lock（互斥写）

### 9.3 性能指标

- Token 验证：< 1ms（内存查找）
- 权限检查：< 2ms（内存遍历）
- 用户认证：< 50ms（bcrypt 验证）
- 创建用户：< 100ms（bcrypt Hash + 存储）

---

## 10. 安全考虑

### 10.1 威胁模型

- **暴力破解**: bcrypt 成本因子 + 速率限制
- **Token 泄露**: 短有效期 + HTTPS 传输
- **权限绕过**: 严格的权限检查 + Interceptor
- **数据泄露**: 密码 Hash + 敏感数据加密

### 10.2 最佳实践

1. **强密码策略**（可选实现）
   - 最小长度：8 字符
   - 包含大小写、数字、特殊字符

2. **Token 安全**
   - 使用 HTTPS
   - 不在日志中记录 Token
   - 定期清理过期 Token

3. **审计日志**（可选实现）
   - 记录所有认证操作
   - 记录权限拒绝事件
   - 记录敏感操作（创建/删除用户）

---

## 11. 与现有代码集成

### 11.1 修改 Server 初始化

```go
// pkg/etcdapi/server.go

func NewServer(cfg ServerConfig) (*Server, error) {
    // ... 现有代码 ...

    // 创建 Auth 管理器
    authMgr := NewAuthManager(cfg.Store)

    s := &Server{
        store:     cfg.Store,
        grpcSrv:   grpcSrv,
        listener:  listener,
        watchMgr:  NewWatchManager(cfg.Store),
        leaseMgr:  NewLeaseManager(cfg.Store),
        authMgr:   authMgr,  // 新增
        clusterID: cfg.ClusterID,
        memberID:  cfg.MemberID,
    }

    // 注册 gRPC 服务
    pb.RegisterKVServer(grpcSrv, &KVServer{server: s})
    pb.RegisterWatchServer(grpcSrv, &WatchServer{server: s})
    pb.RegisterLeaseServer(grpcSrv, &LeaseServer{server: s})
    pb.RegisterMaintenanceServer(grpcSrv, &MaintenanceServer{server: s})
    pb.RegisterAuthServer(grpcSrv, &AuthServer{server: s})  // 新增

    // 添加认证拦截器
    // TODO: 使用 grpc.UnaryInterceptor() 或 grpc.ChainUnaryInterceptor()

    return s, nil
}
```

### 11.2 Server 结构添加字段

```go
type Server struct {
    mu       sync.RWMutex
    store    kvstore.Store
    grpcSrv  *grpc.Server
    listener net.Listener

    watchMgr *WatchManager
    leaseMgr *LeaseManager
    authMgr  *AuthManager  // 新增

    clusterID uint64
    memberID  uint64
}
```

---

## 12. 文档和示例

### 12.1 用户文档

创建 `docs/AUTH_USAGE.md`：
- 如何启用认证
- 如何创建用户和角色
- 如何使用 clientv3 认证
- 常见问题排查

### 12.2 示例代码

创建 `examples/auth/`：
- `basic_auth.go` - 基础认证示例
- `role_based_access.go` - 基于角色的访问控制
- `token_refresh.go` - Token 刷新

---

## 13. 待完成清单

### 代码实现
- [ ] pkg/etcdapi/auth.go - Auth gRPC 服务
- [ ] pkg/etcdapi/auth_manager.go - Auth 管理器
- [ ] pkg/etcdapi/auth_interceptor.go - 认证拦截器
- [ ] pkg/etcdapi/auth_types.go - 数据模型定义

### 测试
- [ ] pkg/etcdapi/auth_test.go - 单元测试
- [ ] test/auth_integration_test.go - 集成测试
- [ ] test/auth_etcd_compatibility_test.go - etcd 兼容性测试

### 文档
- [ ] docs/AUTH_USAGE.md - 用户使用文档
- [ ] docs/AUTH_SECURITY.md - 安全指南
- [ ] examples/auth/ - 示例代码

### 集成
- [ ] 修改 pkg/etcdapi/server.go 集成 Auth
- [ ] 添加 gRPC 拦截器
- [ ] 首次启动初始化 root 用户

---

## 14. 估算工作量

| 任务 | 估计时间 | 优先级 |
|------|---------|--------|
| 数据模型和基础框架 | 2-3 小时 | P0 |
| 用户管理实现 | 3-4 小时 | P0 |
| Token 认证实现 | 2-3 小时 | P0 |
| 角色和权限实现 | 3-4 小时 | P0 |
| 权限拦截器实现 | 2-3 小时 | P0 |
| 单元测试 | 3-4 小时 | P0 |
| 集成测试 | 2-3 小时 | P0 |
| 文档和示例 | 2-3 小时 | P1 |
| **总计** | **19-27 小时** | - |

约 **3-4 个工作日**

---

## 15. 参考资料

- [etcd Authentication Guide](https://etcd.io/docs/v3.5/op-guide/authentication/)
- [etcd API Reference - Auth](https://etcd.io/docs/v3.5/learning/api/)
- [gRPC Authentication](https://grpc.io/docs/guides/auth/)
- [bcrypt Package](https://pkg.go.dev/golang.org/x/crypto/bcrypt)
- [JWT Best Practices](https://tools.ietf.org/html/rfc8725)

---

**文档版本**: v1.0
**创建日期**: 2025-10-27
**状态**: 设计完成，待实现
