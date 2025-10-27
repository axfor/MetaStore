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
	// TODO: 在实现 AuthManager 后启用
	// 当前返回 handler(ctx, req) 允许所有请求通过

	// 如果认证未启用，直接放行
	// if s.authMgr == nil || !s.authMgr.IsEnabled() {
	//	return handler(ctx, req)
	// }

	// Auth API 本身不需要认证（除了 Disable）
	if isAuthAPI(info.FullMethod) {
		// TODO: AuthDisable 需要验证 root 权限
		return handler(ctx, req)
	}

	// 从 metadata 提取 token
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		// TODO: 启用认证后取消注释
		// return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
		return handler(ctx, req)
	}

	tokens := md["token"]
	if len(tokens) == 0 {
		// TODO: 启用认证后取消注释
		// return nil, status.Errorf(codes.Unauthenticated, "missing token")
		return handler(ctx, req)
	}

	// 验证 token
	// TODO: 实现 token 验证
	// tokenInfo, err := s.authMgr.ValidateToken(tokens[0])
	// if err != nil {
	//	return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	// }

	// TODO: 检查权限
	// 根据 info.FullMethod 和 req 判断需要的权限
	// 调用 authMgr.CheckPermission()

	// 将用户信息注入 context
	// ctx = context.WithValue(ctx, "username", tokenInfo.Username)

	return handler(ctx, req)
}

// isAuthAPI 判断是否是 Auth API
func isAuthAPI(method string) bool {
	return strings.HasPrefix(method, "/etcdserverpb.Auth/")
}

// extractPermissionFromRequest 从请求中提取需要的权限
// TODO: 实现
// 根据不同的 API 调用，判断需要检查的权限类型
func extractPermissionFromRequest(method string, req interface{}) (key []byte, permType PermissionType, err error) {
	// 示例实现：
	// switch method {
	// case "/etcdserverpb.KV/Range":
	//     return req.(*pb.RangeRequest).Key, PermissionRead, nil
	// case "/etcdserverpb.KV/Put":
	//     return req.(*pb.PutRequest).Key, PermissionWrite, nil
	// case "/etcdserverpb.KV/DeleteRange":
	//     return req.(*pb.DeleteRangeRequest).Key, PermissionWrite, nil
	// case "/etcdserverpb.KV/Txn":
	//     // 事务需要检查所有操作的权限
	//     return nil, PermissionReadWrite, nil
	// default:
	//     return nil, PermissionRead, nil
	// }
	return nil, PermissionRead, nil
}
