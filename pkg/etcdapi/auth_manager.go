// Copyright 2025 The axfor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package etcdapi

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"metaStore/internal/kvstore"
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
func (am *AuthManager) AddUser(name, password string) error {
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
