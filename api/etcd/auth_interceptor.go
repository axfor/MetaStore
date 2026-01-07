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

// AuthInterceptor gRPC interceptor for validating request permissions
func (s *Server) AuthInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// If authentication is not enabled, allow all requests
	if s.authMgr == nil || !s.authMgr.IsEnabled() {
		return handler(ctx, req)
	}

	// Auth API itself does not require authentication (except Disable)
	if isAuthAPI(info.FullMethod) {
		// AuthDisable requires root permission
		if info.FullMethod == "/etcdserverpb.Auth/AuthDisable" {
			return s.checkRootPermission(ctx, handler, req)
		}
		return handler(ctx, req)
	}

	// Extract token from metadata
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	tokens := md["token"]
	if len(tokens) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing token")
	}

	// Validate token
	tokenInfo, err := s.authMgr.ValidateToken(tokens[0])
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}

	// Check permission
	key, permType, err := extractPermissionFromRequest(info.FullMethod, req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to extract permission: %v", err)
	}

	// If permission check is needed (key is not nil)
	if key != nil {
		err = s.authMgr.CheckPermission(tokenInfo.Username, key, permType)
		if err != nil {
			return nil, status.Errorf(codes.PermissionDenied, "permission denied: %v", err)
		}
	}

	// Inject user information into context
	ctx = context.WithValue(ctx, "username", tokenInfo.Username)

	return handler(ctx, req)
}

// checkRootPermission checks if user is root
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

// isAuthAPI checks if it's an Auth API
func isAuthAPI(method string) bool {
	return strings.HasPrefix(method, "/etcdserverpb.Auth/")
}

// extractPermissionFromRequest extracts required permission from request
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
		// Transaction needs ReadWrite permission
		// TODO: Can further check each operation in the transaction
		r, ok := req.(*pb.TxnRequest)
		if !ok {
			return nil, PermissionReadWrite, fmt.Errorf("invalid request type for Txn")
		}
		// Simplified handling: if there are any operations, ReadWrite permission is needed
		if len(r.Success) > 0 || len(r.Failure) > 0 {
			return []byte(""), PermissionReadWrite, nil
		}
		return nil, PermissionReadWrite, nil

	case "/etcdserverpb.KV/Compact":
		// Compact needs special permission, usually only admins can execute
		return []byte(""), PermissionWrite, nil

	case "/etcdserverpb.Watch/Watch":
		// Watch needs read permission, but doesn't check specific key (handled by Watch itself)
		return nil, PermissionRead, nil

	case "/etcdserverpb.Lease/LeaseGrant",
		"/etcdserverpb.Lease/LeaseRevoke",
		"/etcdserverpb.Lease/LeaseKeepAlive",
		"/etcdserverpb.Lease/LeaseTimeToLive",
		"/etcdserverpb.Lease/LeaseLeases":
		// Lease operations don't need specific key permissions
		return nil, PermissionRead, nil

	case "/etcdserverpb.Cluster/MemberAdd",
		"/etcdserverpb.Cluster/MemberRemove",
		"/etcdserverpb.Cluster/MemberUpdate",
		"/etcdserverpb.Cluster/MemberPromote":
		// Cluster operations need admin permissions
		return []byte(""), PermissionWrite, nil

	case "/etcdserverpb.Cluster/MemberList":
		// MemberList only needs read permission
		return nil, PermissionRead, nil

	case "/etcdserverpb.Maintenance/Alarm",
		"/etcdserverpb.Maintenance/Status",
		"/etcdserverpb.Maintenance/Hash",
		"/etcdserverpb.Maintenance/HashKV",
		"/etcdserverpb.Maintenance/Snapshot":
		// Maintenance read operations
		return nil, PermissionRead, nil

	case "/etcdserverpb.Maintenance/Defragment",
		"/etcdserverpb.Maintenance/MoveLeader":
		// Maintenance write operations
		return []byte(""), PermissionWrite, nil

	default:
		// Default: no permission check (allow through)
		return nil, PermissionRead, nil
	}
}
