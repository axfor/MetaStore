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
	"fmt"
	"strings"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
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
	if s.authMgr == nil || !s.authMgr.IsEnabled() {
		return handler(ctx, req)
	}

	// Auth API 本身不需要认证（除了 Disable）
	if isAuthAPI(info.FullMethod) {
		// AuthDisable 需要验证 root 权限
		if info.FullMethod == "/etcdserverpb.Auth/AuthDisable" {
			return s.checkRootPermission(ctx, handler, req)
		}
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

	// 检查权限
	key, permType, err := extractPermissionFromRequest(info.FullMethod, req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to extract permission: %v", err)
	}

	// 如果需要权限检查（key 不为 nil）
	if key != nil {
		err = s.authMgr.CheckPermission(tokenInfo.Username, key, permType)
		if err != nil {
			return nil, status.Errorf(codes.PermissionDenied, "permission denied: %v", err)
		}
	}

	// 将用户信息注入 context
	ctx = context.WithValue(ctx, "username", tokenInfo.Username)

	return handler(ctx, req)
}

// checkRootPermission 检查是否是 root 用户
func (s *Server) checkRootPermission(ctx context.Context, handler grpc.UnaryHandler, req interface{}) (interface{}, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	tokens := md["token"]
	if len(tokens) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing token")
	}

	tokenInfo, err := s.authMgr.ValidateToken(tokens[0])
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}

	if tokenInfo.Username != "root" {
		return nil, status.Errorf(codes.PermissionDenied, "only root can disable authentication")
	}

	ctx = context.WithValue(ctx, "username", tokenInfo.Username)
	return handler(ctx, req)
}

// isAuthAPI 判断是否是 Auth API
func isAuthAPI(method string) bool {
	return strings.HasPrefix(method, "/etcdserverpb.Auth/")
}

// extractPermissionFromRequest 从请求中提取需要的权限
func extractPermissionFromRequest(method string, req interface{}) (key []byte, permType PermissionType, err error) {
	switch method {
	case "/etcdserverpb.KV/Range":
		r, ok := req.(*pb.RangeRequest)
		if !ok {
			return nil, PermissionRead, fmt.Errorf("invalid request type for Range")
		}
		return r.Key, PermissionRead, nil

	case "/etcdserverpb.KV/Put":
		r, ok := req.(*pb.PutRequest)
		if !ok {
			return nil, PermissionWrite, fmt.Errorf("invalid request type for Put")
		}
		return r.Key, PermissionWrite, nil

	case "/etcdserverpb.KV/DeleteRange":
		r, ok := req.(*pb.DeleteRangeRequest)
		if !ok {
			return nil, PermissionWrite, fmt.Errorf("invalid request type for DeleteRange")
		}
		return r.Key, PermissionWrite, nil

	case "/etcdserverpb.KV/Txn":
		// 事务需要 ReadWrite 权限
		// TODO: 可以进一步检查事务中的每个操作
		r, ok := req.(*pb.TxnRequest)
		if !ok {
			return nil, PermissionReadWrite, fmt.Errorf("invalid request type for Txn")
		}
		// 简化处理：如果有任何操作，就需要 ReadWrite 权限
		if len(r.Success) > 0 || len(r.Failure) > 0 {
			return []byte(""), PermissionReadWrite, nil
		}
		return nil, PermissionReadWrite, nil

	case "/etcdserverpb.KV/Compact":
		// Compact 需要特殊权限，通常只有管理员可以执行
		return []byte(""), PermissionWrite, nil

	case "/etcdserverpb.Watch/Watch":
		// Watch 需要读权限，但不检查具体 key（由 Watch 本身处理）
		return nil, PermissionRead, nil

	case "/etcdserverpb.Lease/LeaseGrant",
		"/etcdserverpb.Lease/LeaseRevoke",
		"/etcdserverpb.Lease/LeaseKeepAlive",
		"/etcdserverpb.Lease/LeaseTimeToLive",
		"/etcdserverpb.Lease/LeaseLeases":
		// Lease 操作不需要特定 key 权限
		return nil, PermissionRead, nil

	case "/etcdserverpb.Cluster/MemberAdd",
		"/etcdserverpb.Cluster/MemberRemove",
		"/etcdserverpb.Cluster/MemberUpdate",
		"/etcdserverpb.Cluster/MemberPromote":
		// Cluster 操作需要管理员权限
		return []byte(""), PermissionWrite, nil

	case "/etcdserverpb.Cluster/MemberList":
		// MemberList 只需要读权限
		return nil, PermissionRead, nil

	case "/etcdserverpb.Maintenance/Alarm",
		"/etcdserverpb.Maintenance/Status",
		"/etcdserverpb.Maintenance/Hash",
		"/etcdserverpb.Maintenance/HashKV",
		"/etcdserverpb.Maintenance/Snapshot":
		// Maintenance 读操作
		return nil, PermissionRead, nil

	case "/etcdserverpb.Maintenance/Defragment",
		"/etcdserverpb.Maintenance/MoveLeader":
		// Maintenance 写操作
		return []byte(""), PermissionWrite, nil

	default:
		// 默认不检查权限（允许通过）
		return nil, PermissionRead, nil
	}
}
