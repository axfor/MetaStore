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

package etcd

import (
	"context"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/pkg/config"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/authpb"
)

// setupAuthTest creates test environment
func setupAuthTest(t *testing.T) (*Server, func()) {
	// Create memory storage
	store := memory.NewMemoryEtcd()

	// Create test configuration
	testCfg := createAuthTestConfig()

	// Create server configuration
	cfg := ServerConfig{
		Store:     store,
		Address:   ":0", // random port
		ClusterID: 1,
		MemberID:  1,
		Config:    testCfg, // use configuration
	}

	// Create server (but don't start)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	cleanup := func() {
		srv.Stop()
	}

	return srv, cleanup
}

// createAuthTestConfig creates authentication test configuration
func createAuthTestConfig() *config.Config {
	cfg := config.DefaultConfig(1, 1, ":2379")

	// Test environment optimization: use lower bcrypt cost to speed up tests
	cfg.Server.Auth.BcryptCost = 4  // default 10, use 4 for testing
	cfg.Server.Auth.TokenTTL = 10 * time.Minute
	cfg.Server.Auth.TokenCleanupInterval = 1 * time.Minute
	cfg.Server.Auth.EnableAudit = false // test environment doesn't need audit logs

	// Configure limits
	cfg.Server.Limits.MaxWatchCount = 1000
	cfg.Server.Limits.MaxLeaseCount = 10000

	// Disable monitoring to avoid port conflicts
	cfg.Server.Monitoring.EnablePrometheus = false

	// Fast timeouts
	cfg.Server.Reliability.ShutdownTimeout = 5 * time.Second
	cfg.Server.Reliability.DrainTimeout = 2 * time.Second

	return cfg
}

// TestAuthBasicFlow tests basic authentication flow
func TestAuthBasicFlow(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// 1. Add root user
	t.Run("AddRootUser", func(t *testing.T) {
		_, err := authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{
			Name:     "root",
			Password: "rootpass",
		})
		if err != nil {
			t.Fatalf("Failed to add root user: %v", err)
		}
	})

	// 2. Enable authentication
	t.Run("EnableAuth", func(t *testing.T) {
		_, err := authSrv.AuthEnable(ctx, &pb.AuthEnableRequest{})
		if err != nil {
			t.Fatalf("Failed to enable auth: %v", err)
		}
	})

	// 3. Check status
	t.Run("CheckAuthStatus", func(t *testing.T) {
		resp, err := authSrv.AuthStatus(ctx, &pb.AuthStatusRequest{})
		if err != nil {
			t.Fatalf("Failed to get auth status: %v", err)
		}
		if !resp.Enabled {
			t.Fatal("Auth should be enabled")
		}
	})

	// 4. Authenticate
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

		// Validate token
		tokenInfo, err := srv.authMgr.ValidateToken(resp.Token)
		if err != nil {
			t.Fatalf("Failed to validate token: %v", err)
		}
		if tokenInfo.Username != "root" {
			t.Fatalf("Expected username 'root', got '%s'", tokenInfo.Username)
		}
	})

	// 5. Disable authentication
	t.Run("DisableAuth", func(t *testing.T) {
		_, err := authSrv.AuthDisable(ctx, &pb.AuthDisableRequest{})
		if err != nil {
			t.Fatalf("Failed to disable auth: %v", err)
		}

		// Check status
		resp, err := authSrv.AuthStatus(ctx, &pb.AuthStatusRequest{})
		if err != nil {
			t.Fatalf("Failed to get auth status: %v", err)
		}
		if resp.Enabled {
			t.Fatal("Auth should be disabled")
		}
	})
}

// TestUserManagement tests user management
func TestUserManagement(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// Add user
	t.Run("AddUser", func(t *testing.T) {
		_, err := authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{
			Name:     "alice",
			Password: "alicepass",
		})
		if err != nil {
			t.Fatalf("Failed to add user: %v", err)
		}
	})

	// Get user
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

	// List users
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

	// Change password
	t.Run("ChangePassword", func(t *testing.T) {
		_, err := authSrv.UserChangePassword(ctx, &pb.AuthUserChangePasswordRequest{
			Name:     "alice",
			Password: "newpass",
		})
		if err != nil {
			t.Fatalf("Failed to change password: %v", err)
		}

		// Verify new password
		_, err = srv.authMgr.Authenticate("alice", "newpass")
		if err != nil {
			t.Fatalf("Failed to authenticate with new password: %v", err)
		}

		// Verify old password fails
		_, err = srv.authMgr.Authenticate("alice", "alicepass")
		if err == nil {
			t.Fatal("Old password should not work")
		}
	})

	// Delete user
	t.Run("DeleteUser", func(t *testing.T) {
		_, err := authSrv.UserDelete(ctx, &pb.AuthUserDeleteRequest{
			Name: "alice",
		})
		if err != nil {
			t.Fatalf("Failed to delete user: %v", err)
		}

		// Verify user is deleted
		resp, err := authSrv.UserList(ctx, &pb.AuthUserListRequest{})
		if err != nil {
			t.Fatalf("Failed to list users: %v", err)
		}
		if len(resp.Users) != 0 {
			t.Fatalf("Expected 0 users, got %d", len(resp.Users))
		}
	})
}

// TestRoleManagement tests role management
func TestRoleManagement(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// Add role
	t.Run("AddRole", func(t *testing.T) {
		_, err := authSrv.RoleAdd(ctx, &pb.AuthRoleAddRequest{
			Name: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to add role: %v", err)
		}
	})

	// Get role
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

	// Grant permission
	t.Run("GrantPermission", func(t *testing.T) {
		_, err := authSrv.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{
			Name: "admin",
			Perm: &authpb.Permission{
				PermType: authpb.Permission_Type(PermissionReadWrite),
				Key:      []byte("/admin/"),
				RangeEnd: []byte("/admin0"),
			},
		})
		if err != nil {
			t.Fatalf("Failed to grant permission: %v", err)
		}

		// Verify permission
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

	// Revoke permission
	t.Run("RevokePermission", func(t *testing.T) {
		_, err := authSrv.RoleRevokePermission(ctx, &pb.AuthRoleRevokePermissionRequest{
			Role:     "admin",
			Key:      []byte("/admin/"),
			RangeEnd: []byte("/admin0"),
		})
		if err != nil {
			t.Fatalf("Failed to revoke permission: %v", err)
		}

		// Verify permission is revoked
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

	// List roles
	t.Run("ListRoles", func(t *testing.T) {
		resp, err := authSrv.RoleList(ctx, &pb.AuthRoleListRequest{})
		if err != nil {
			t.Fatalf("Failed to list roles: %v", err)
		}
		if len(resp.Roles) != 1 {
			t.Fatalf("Expected 1 role, got %d", len(resp.Roles))
		}
	})

	// Delete role
	t.Run("DeleteRole", func(t *testing.T) {
		_, err := authSrv.RoleDelete(ctx, &pb.AuthRoleDeleteRequest{
			Role: "admin",
		})
		if err != nil {
			t.Fatalf("Failed to delete role: %v", err)
		}

		// Verify role is deleted
		resp, err := authSrv.RoleList(ctx, &pb.AuthRoleListRequest{})
		if err != nil {
			t.Fatalf("Failed to list roles: %v", err)
		}
		if len(resp.Roles) != 0 {
			t.Fatalf("Expected 0 roles, got %d", len(resp.Roles))
		}
	})
}

// TestUserRoleBinding tests user role binding
func TestUserRoleBinding(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	ctx := context.Background()
	authSrv := &AuthServer{server: srv}

	// Create user and role
	_, _ = authSrv.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "bob", Password: "bobpass"})
	_, _ = authSrv.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: "viewer"})

	// Grant role
	t.Run("GrantRole", func(t *testing.T) {
		_, err := authSrv.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{
			User: "bob",
			Role: "viewer",
		})
		if err != nil {
			t.Fatalf("Failed to grant role: %v", err)
		}

		// Verify role
		resp, err := authSrv.UserGet(ctx, &pb.AuthUserGetRequest{Name: "bob"})
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}
		if len(resp.Roles) != 1 || resp.Roles[0] != "viewer" {
			t.Fatalf("Expected role 'viewer', got %v", resp.Roles)
		}
	})

	// Revoke role
	t.Run("RevokeRole", func(t *testing.T) {
		_, err := authSrv.UserRevokeRole(ctx, &pb.AuthUserRevokeRoleRequest{
			Name: "bob",
			Role: "viewer",
		})
		if err != nil {
			t.Fatalf("Failed to revoke role: %v", err)
		}

		// Verify role is revoked
		resp, err := authSrv.UserGet(ctx, &pb.AuthUserGetRequest{Name: "bob"})
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}
		if len(resp.Roles) != 0 {
			t.Fatalf("Expected 0 roles, got %d", len(resp.Roles))
		}
	})
}

// TestTokenExpiration tests token expiration
func TestTokenExpiration(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	// Create user
	err := srv.authMgr.AddUser("test", "testpass")
	if err != nil {
		t.Fatalf("Failed to add user: %v", err)
	}

	// Generate token
	token, err := srv.authMgr.Authenticate("test", "testpass")
	if err != nil {
		t.Fatalf("Failed to authenticate: %v", err)
	}

	// Verify token is valid
	_, err = srv.authMgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("Token should be valid: %v", err)
	}

	// Manually modify token expiration time to the past
	if tokenInfo, exists := srv.authMgr.tokens.Load(token); exists {
		tokenInfo.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
		srv.authMgr.tokens.Store(token, tokenInfo)
	}

	// Verify token has expired
	_, err = srv.authMgr.ValidateToken(token)
	if err == nil {
		t.Fatal("Expired token should not be valid")
	}
}

// TestPermissionCheck tests permission checking
func TestPermissionCheck(t *testing.T) {
	srv, cleanup := setupAuthTest(t)
	defer cleanup()

	// Create user and role
	_ = srv.authMgr.AddUser("user1", "pass")
	_ = srv.authMgr.AddRole("role1")

	// Grant permission
	perm := Permission{
		Type:     PermissionReadWrite,
		Key:      []byte("/data/"),
		RangeEnd: []byte("/data0"),
	}
	_ = srv.authMgr.GrantPermission("role1", perm)
	_ = srv.authMgr.GrantRole("user1", "role1")

	// Test permission check
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
		// root user should have all permissions
		err := srv.authMgr.CheckPermission("root", []byte("/any/key"), PermissionReadWrite)
		if err != nil {
			t.Fatalf("Root should have all permissions: %v", err)
		}
	})
}

// BenchmarkAuthenticate benchmark authentication performance
func BenchmarkAuthenticate(b *testing.B) {
	srv, cleanup := setupAuthTest(&testing.T{})
	defer cleanup()

	_ = srv.authMgr.AddUser("bench", "benchpass")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = srv.authMgr.Authenticate("bench", "benchpass")
	}
}

// BenchmarkValidateToken benchmark token validation performance
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
