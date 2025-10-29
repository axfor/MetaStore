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

// 存储键前缀
const (
	authEnabledKey  = "/__auth/enabled"
	authUserPrefix  = "/__auth/users/"
	authRolePrefix  = "/__auth/roles/"
	authTokenPrefix = "/__auth/tokens/"
)
