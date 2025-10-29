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
	"fmt"

	"go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// AuthServer 实现 etcd Auth 服务
type AuthServer struct {
	pb.UnimplementedAuthServer
	server *Server
}

// AuthEnable 启用认证
func (s *AuthServer) AuthEnable(ctx context.Context, req *pb.AuthEnableRequest) (*pb.AuthEnableResponse, error) {
	err := s.server.authMgr.Enable()
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthEnableResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// AuthDisable 禁用认证
func (s *AuthServer) AuthDisable(ctx context.Context, req *pb.AuthDisableRequest) (*pb.AuthDisableResponse, error) {
	// TODO: 验证调用者是 root (从 context 获取用户信息)
	err := s.server.authMgr.Disable()
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthDisableResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// AuthStatus 查询认证状态
func (s *AuthServer) AuthStatus(ctx context.Context, req *pb.AuthStatusRequest) (*pb.AuthStatusResponse, error) {
	enabled := s.server.authMgr.IsEnabled()
	return &pb.AuthStatusResponse{
		Header:  s.server.getResponseHeader(),
		Enabled: enabled,
	}, nil
}

// Authenticate 用户认证
func (s *AuthServer) Authenticate(ctx context.Context, req *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	token, err := s.server.authMgr.Authenticate(req.Name, req.Password)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthenticateResponse{
		Header: s.server.getResponseHeader(),
		Token:  token,
	}, nil
}

// UserAdd 添加用户
func (s *AuthServer) UserAdd(ctx context.Context, req *pb.AuthUserAddRequest) (*pb.AuthUserAddResponse, error) {
	// TODO: 验证权限 (从 context 获取用户信息)
	err := s.server.authMgr.AddUser(req.Name, req.Password)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserAddResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// UserDelete 删除用户
func (s *AuthServer) UserDelete(ctx context.Context, req *pb.AuthUserDeleteRequest) (*pb.AuthUserDeleteResponse, error) {
	err := s.server.authMgr.DeleteUser(req.Name)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserDeleteResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// UserGet 获取用户信息
func (s *AuthServer) UserGet(ctx context.Context, req *pb.AuthUserGetRequest) (*pb.AuthUserGetResponse, error) {
	user, err := s.server.authMgr.GetUser(req.Name)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserGetResponse{
		Header: s.server.getResponseHeader(),
		Roles:  user.Roles,
	}, nil
}

// UserList 列出所有用户
func (s *AuthServer) UserList(ctx context.Context, req *pb.AuthUserListRequest) (*pb.AuthUserListResponse, error) {
	users, err := s.server.authMgr.ListUsers()
	if err != nil {
		return nil, toGRPCError(err)
	}

	userNames := make([]string, len(users))
	for i, user := range users {
		userNames[i] = user.Name
	}

	return &pb.AuthUserListResponse{
		Header: s.server.getResponseHeader(),
		Users:  userNames,
	}, nil
}

// UserChangePassword 修改密码
func (s *AuthServer) UserChangePassword(ctx context.Context, req *pb.AuthUserChangePasswordRequest) (*pb.AuthUserChangePasswordResponse, error) {
	err := s.server.authMgr.ChangePassword(req.Name, req.Password)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserChangePasswordResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// UserGrantRole 授予角色
func (s *AuthServer) UserGrantRole(ctx context.Context, req *pb.AuthUserGrantRoleRequest) (*pb.AuthUserGrantRoleResponse, error) {
	err := s.server.authMgr.GrantRole(req.User, req.Role)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserGrantRoleResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// UserRevokeRole 撤销角色
func (s *AuthServer) UserRevokeRole(ctx context.Context, req *pb.AuthUserRevokeRoleRequest) (*pb.AuthUserRevokeRoleResponse, error) {
	err := s.server.authMgr.RevokeRole(req.Name, req.Role)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthUserRevokeRoleResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// RoleAdd 添加角色
func (s *AuthServer) RoleAdd(ctx context.Context, req *pb.AuthRoleAddRequest) (*pb.AuthRoleAddResponse, error) {
	err := s.server.authMgr.AddRole(req.Name)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthRoleAddResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// RoleDelete 删除角色
func (s *AuthServer) RoleDelete(ctx context.Context, req *pb.AuthRoleDeleteRequest) (*pb.AuthRoleDeleteResponse, error) {
	err := s.server.authMgr.DeleteRole(req.Role)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthRoleDeleteResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// RoleGet 获取角色信息
func (s *AuthServer) RoleGet(ctx context.Context, req *pb.AuthRoleGetRequest) (*pb.AuthRoleGetResponse, error) {
	role, err := s.server.authMgr.GetRole(req.Role)
	if err != nil {
		return nil, toGRPCError(err)
	}

	// 转换权限列表
	pbPerms := make([]*authpb.Permission, len(role.Permissions))
	for i, perm := range role.Permissions {
		pbPerms[i] = &authpb.Permission{
			PermType: authpb.Permission_Type(perm.Type),
			Key:      perm.Key,
			RangeEnd: perm.RangeEnd,
		}
	}

	return &pb.AuthRoleGetResponse{
		Header: s.server.getResponseHeader(),
		Perm:   pbPerms,
	}, nil
}

// RoleList 列出所有角色
func (s *AuthServer) RoleList(ctx context.Context, req *pb.AuthRoleListRequest) (*pb.AuthRoleListResponse, error) {
	roles, err := s.server.authMgr.ListRoles()
	if err != nil {
		return nil, toGRPCError(err)
	}

	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = role.Name
	}

	return &pb.AuthRoleListResponse{
		Header: s.server.getResponseHeader(),
		Roles:  roleNames,
	}, nil
}

// RoleGrantPermission 授予权限
func (s *AuthServer) RoleGrantPermission(ctx context.Context, req *pb.AuthRoleGrantPermissionRequest) (*pb.AuthRoleGrantPermissionResponse, error) {
	if req.Perm == nil {
		return nil, toGRPCError(fmt.Errorf("permission is required"))
	}

	perm := Permission{
		Type:     PermissionType(req.Perm.PermType),
		Key:      req.Perm.Key,
		RangeEnd: req.Perm.RangeEnd,
	}

	err := s.server.authMgr.GrantPermission(req.Name, perm)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthRoleGrantPermissionResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}

// RoleRevokePermission 撤销权限
func (s *AuthServer) RoleRevokePermission(ctx context.Context, req *pb.AuthRoleRevokePermissionRequest) (*pb.AuthRoleRevokePermissionResponse, error) {
	err := s.server.authMgr.RevokePermission(req.Role, req.Key, req.RangeEnd)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &pb.AuthRoleRevokePermissionResponse{
		Header: s.server.getResponseHeader(),
	}, nil
}
