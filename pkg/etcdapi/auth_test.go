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
	"context"
	"testing"
	"time"

	"metaStore/internal/memory"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/authpb"
)

// setupAuthTest 创建测试环境
func setupAuthTest(t *testing.T) (*Server, func()) {
	// 创建内存存储
	store := memory.NewMemoryEtcd()

	// 创建服务器配置
	cfg := ServerConfig{
		Store:     store,
		Address:   ":0", // 随机端口
		ClusterID: 1,
		MemberID:  1,
	}

	// 创建服务器（但不启动）
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	cleanup := func() {
		srv.Stop()
	}

	return srv, cleanup
}

// TestAuthBasicFlow 测试基本认证流程
func TestAuthBasicFlow(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// 1. 添加 root 用户
	t.Run("AddRootUser", func(t *testing.T) {
		_, err := authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{
			Name:     "root",
			Password: "rootpass",
		})
		if err != nil {
			t.Fatalf("Failed to add root user: %v", err)
		}
	})

	// 2. 启用认证
	t.Run("EnableAuth", func(t *testing.T) {
		_, err := authSrv.AuthEnable(ctx, &pb.AuthEnableRequest{})
		if err != nil {
			t.Fatalf("Failed to enable auth: %v", err)
		}
	})

	// 3. 检查状态
	t.Run("CheckAuthStatus", func(t *testing.T) {
		resp, err := authSrv.AuthStatus(ctx, &pb.AuthStatusRequest{})
		if err != nil {
			t.Fatalf("Failed to get auth status: %v", err)
		}
		if !resp.Enabled {
			t.Fatal("Auth should be enabled")
		}
	})

	// 4. 认证
	t.Run("Authenticate", func(t *testing.T) {
		resp, err := authSrv.Authenticate(ctx, &pb.AuthenticateRequest{
			Name:     "root",
			Password: "rootpass",
		})
		if err != nil {
			t.Fatalf("Failed to authenticate: %v", err)
		}
		if resp.Token == "" {
			t.Fatal("Token should not be empty")
		}

		// 验证 token
		tokenInfo, err := srv.authMgr.ValidateToken(resp.Token)
		if err != nil {
			t.Fatalf("Failed to validate token: %v", err)
		}
		if tokenInfo.Username != "root" {
			t.Fatalf("Expected username 'root', got '%s'", tokenInfo.Username)
		}
	})

	// 5. 禁用认证
	t.Run("DisableAuth", func(t *testing.T) {
		_, err := authSrv.AuthDisable(ctx, &pb.AuthDisableRequest{})
		if err != nil {
			t.Fatalf("Failed to disable auth: %v", err)
		}

		// 检查状态
		resp, err := authSrv.AuthStatus(ctx, &pb.AuthStatusRequest{})
		if err != nil {
			t.Fatalf("Failed to get auth status: %v", err)
		}
		if resp.Enabled {
			t.Fatal("Auth should be disabled")
		}
	})
}

// TestUserManagement 测试用户管理
func TestUserManagement(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// 添加用户
	t.Run("AddUser", func(t *testing.T) {
		_, err := authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{
			Name:     "alice",
			Password: "alicepass",
		})
		if err != nil {
			t.Fatalf("Failed to add user: %v", err)
		}
	})

	// 获取用户
	t.Run("GetUser", func(t *testing.T) {
		resp, err := authSrv.UserGet(ctx, &pb.AuthUserGetRequest{
			Name: "alice",
		})
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}
		if len(resp.Roles) != 0 {
			t.Fatalf("Expected 0 roles, got %d", len(resp.Roles))
		}
	})

	// 列出用户
	t.Run("ListUsers", func(t *testing.T) {
		resp, err := authSrv.UserList(ctx, &pb.AuthUserListRequest{})
		if err != nil {
			t.Fatalf("Failed to list users: %v", err)
		}
		if len(resp.Users) != 1 {
			t.Fatalf("Expected 1 user, got %d", len(resp.Users))
		}
		if resp.Users[0] != "alice" {
			t.Fatalf("Expected user 'alice', got '%s'", resp.Users[0])
		}
	})

	// 修改密码
	t.Run("ChangePassword", func(t *testing.T) {
		_, err := authSrv.UserChangePassword(ctx, &pb.AuthUserChangePasswordRequest{
			Name:     "alice",
			Password: "newpass",
		})
		if err != nil {
			t.Fatalf("Failed to change password: %v", err)
		}

		// 验证新密码
		_, err = srv.authMgr.Authenticate("alice", "newpass")
		if err != nil {
			t.Fatalf("Failed to authenticate with new password: %v", err)
		}

		// 验证旧密码失败
		_, err = srv.authMgr.Authenticate("alice", "alicepass")
		if err == nil {
			t.Fatal("Old password should not work")
		}
	})

	// 删除用户
	t.Run("DeleteUser", func(t *testing.T) {
		_, err := authSrv.UserDelete(ctx, &pb.AuthUserDeleteRequest{
			Name: "alice",
		})
		if err != nil {
			t.Fatalf("Failed to delete user: %v", err)
		}

		// 验证用户已删除
		resp, err := authSrv.UserList(ctx, &pb.AuthUserListRequest{})
		if err != nil {
			t.Fatalf("Failed to list users: %v", err)
		}
		if len(resp.Users) != 0 {
			t.Fatalf("Expected 0 users, got %d", len(resp.Users))
		}
	})
}

// TestRoleManagement 测试角色管理
func TestRoleManagement(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// 添加角色
	t.Run("AddRole", func(t *testing.T) {
		_, err := authSrv.RoleAdd(ctx, &pb.AuthRoleAddRequest{
			Name: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to add role: %v", err)
		}
	})

	// 获取角色
	t.Run("GetRole", func(t *testing.T) {
		resp, err := authSrv.RoleGet(ctx, &pb.AuthRoleGetRequest{
			Role: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to get role: %v", err)
		}
		if len(resp.Perm) != 0 {
			t.Fatalf("Expected 0 permissions, got %d", len(resp.Perm))
		}
	})

	// 授予权限
	t.Run("GrantPermission", func(t *testing.T) {
		_, err := authSrv.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{
			Name: "admin",
			Perm: &authpb.Permission{
				PermType: authpb.Permission_READWRITE,
				Key:      []byte("/admin/"),
				RangeEnd: []byte("/admin0"),
			},
		})
		if err != nil {
			t.Fatalf("Failed to grant permission: %v", err)
		}

		// 验证权限
		resp, err := authSrv.RoleGet(ctx, &pb.AuthRoleGetRequest{
			Role: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to get role: %v", err)
		}
		if len(resp.Perm) != 1 {
			t.Fatalf("Expected 1 permission, got %d", len(resp.Perm))
		}
	})

	// 撤销权限
	t.Run("RevokePermission", func(t *testing.T) {
		_, err := authSrv.RoleRevokePermission(ctx, &pb.AuthRoleRevokePermissionRequest{
			Role:     "admin",
			Key:      []byte("/admin/"),
			RangeEnd: []byte("/admin0"),
		})
		if err != nil {
			t.Fatalf("Failed to revoke permission: %v", err)
		}

		// 验证权限已撤销
		resp, err := authSrv.RoleGet(ctx, &pb.AuthRoleGetRequest{
			Role: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to get role: %v", err)
		}
		if len(resp.Perm) != 0 {
			t.Fatalf("Expected 0 permissions, got %d", len(resp.Perm))
		}
	})

	// 列出角色
	t.Run("ListRoles", func(t *testing.T) {
		resp, err := authSrv.RoleList(ctx, &pb.AuthRoleListRequest{})
		if err != nil {
			t.Fatalf("Failed to list roles: %v", err)
		}
		if len(resp.Roles) != 1 {
			t.Fatalf("Expected 1 role, got %d", len(resp.Roles))
		}
	})

	// 删除角色
	t.Run("DeleteRole", func(t *testing.T) {
		_, err := authSrv.RoleDelete(ctx, &pb.AuthRoleDeleteRequest{
			Role: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to delete role: %v", err)
		}

		// 验证角色已删除
		resp, err := authSrv.RoleList(ctx, &pb.AuthRoleListRequest{})
		if err != nil {
			t.Fatalf("Failed to list roles: %v", err)
		}
		if len(resp.Roles) != 0 {
			t.Fatalf("Expected 0 roles, got %d", len(resp.Roles))
		}
	})
}

// TestUserRoleBinding 测试用户角色绑定
func TestUserRoleBinding(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// 创建用户和角色
	_, _ = authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "bob", Password: "bobpass"})
	_, _ = authSrv.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: "viewer"})

	// 授予角色
	t.Run("GrantRole", func(t *testing.T) {
		_, err := authSrv.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{
			User: "bob",
			Role: "viewer",
		})
		if err != nil {
			t.Fatalf("Failed to grant role: %v", err)
		}

		// 验证角色
		resp, err := authSrv.UserGet(ctx, &pb.AuthUserGetRequest{Name: "bob"})
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}
		if len(resp.Roles) != 1 || resp.Roles[0] != "viewer" {
			t.Fatalf("Expected role 'viewer', got %v", resp.Roles)
		}
	})

	// 撤销角色
	t.Run("RevokeRole", func(t *testing.T) {
		_, err := authSrv.UserRevokeRole(ctx, &pb.AuthUserRevokeRoleRequest{
			Name: "bob",
			Role: "viewer",
		})
		if err != nil {
			t.Fatalf("Failed to revoke role: %v", err)
		}

		// 验证角色已撤销
		resp, err := authSrv.UserGet(ctx, &pb.AuthUserGetRequest{Name: "bob"})
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}
		if len(resp.Roles) != 0 {
			t.Fatalf("Expected 0 roles, got %d", len(resp.Roles))
		}
	})
}

// TestTokenExpiration 测试 token 过期
func TestTokenExpiration(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	// 创建用户
	err := srv.authMgr.AddUser("test", "testpass")
	if err != nil {
		t.Fatalf("Failed to add user: %v", err)
	}

	// 生成 token
	token, err := srv.authMgr.Authenticate("test", "testpass")
	if err != nil {
		t.Fatalf("Failed to authenticate: %v", err)
	}

	// 验证 token 有效
	_, err = srv.authMgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("Token should be valid: %v", err)
	}

	// 手动修改 token 过期时间为过去
	srv.authMgr.mu.Lock()
	if tokenInfo, exists := srv.authMgr.tokens[token]; exists {
		tokenInfo.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
	}
	srv.authMgr.mu.Unlock()

	// 验证 token 已过期
	_, err = srv.authMgr.ValidateToken(token)
	if err == nil {
		t.Fatal("Expired token should not be valid")
	}
}

// TestPermissionCheck 测试权限检查
func TestPermissionCheck(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	// 创建用户和角色
	_ = srv.authMgr.AddUser("user1", "pass")
	_ = srv.authMgr.AddRole("role1")

	// 授予权限
	perm := Permission{
		Type:     PermissionReadWrite,
		Key:      []byte("/data/"),
		RangeEnd: []byte("/data0"),
	}
	_ = srv.authMgr.GrantPermission("role1", perm)
	_ = srv.authMgr.GrantRole("user1", "role1")

	// 测试权限检查
	t.Run("AllowedKey", func(t *testing.T) {
		err := srv.authMgr.CheckPermission("user1", []byte("/data/test"), PermissionRead)
		if err != nil {
			t.Fatalf("Should have read permission: %v", err)
		}
	})

	t.Run("DeniedKey", func(t *testing.T) {
		err := srv.authMgr.CheckPermission("user1", []byte("/other/test"), PermissionRead)
		if err == nil {
			t.Fatal("Should not have permission for /other/")
		}
	})

	t.Run("RootUser", func(t *testing.T) {
		// root 用户应该有所有权限
		err := srv.authMgr.CheckPermission("root", []byte("/any/key"), PermissionReadWrite)
		if err != nil {
			t.Fatalf("Root should have all permissions: %v", err)
		}
	})
}

// BenchmarkAuthenticate 基准测试认证性能
func BenchmarkAuthenticate(b *testing.B) {
	srv, cleanup := setupAuthTest(&testing.T{})
	defer cleanup()

	_ = srv.authMgr.AddUser("bench", "benchpass")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = srv.authMgr.Authenticate("bench", "benchpass")
	}
}

// BenchmarkValidateToken 基准测试 token 验证性能
func BenchmarkValidateToken(b *testing.B) {
	srv, cleanup := setupAuthTest(&testing.T{})
	defer cleanup()

	_ = srv.authMgr.AddUser("bench", "benchpass")
	token, _ := srv.authMgr.Authenticate("bench", "benchpass")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = srv.authMgr.ValidateToken(token)
	}
}
