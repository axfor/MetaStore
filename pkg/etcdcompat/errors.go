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

package etcdcompat

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 定义 etcd 兼容的错误类型
var (
	ErrKeyNotFound      = errors.New("key not found")
	ErrCompacted        = errors.New("required revision has been compacted")
	ErrFutureRev        = errors.New("required revision is a future revision")
	ErrLeaseNotFound    = errors.New("lease not found")
	ErrLeaseExpired     = errors.New("lease expired")
	ErrTxnConflict      = errors.New("transaction conflict")
	ErrPermissionDenied = errors.New("permission denied")
	ErrAuthFailed       = errors.New("authentication failed")
	ErrInvalidArgument  = errors.New("invalid argument")
	ErrWatchCanceled    = errors.New("watch canceled")
)

// errorCodeMap 将内部错误映射到 gRPC 状态码
var errorCodeMap = map[error]codes.Code{
	ErrKeyNotFound:      codes.NotFound,
	ErrCompacted:        codes.OutOfRange,
	ErrFutureRev:        codes.OutOfRange,
	ErrLeaseNotFound:    codes.NotFound,
	ErrLeaseExpired:     codes.NotFound,
	ErrTxnConflict:      codes.FailedPrecondition,
	ErrPermissionDenied: codes.PermissionDenied,
	ErrAuthFailed:       codes.Unauthenticated,
	ErrInvalidArgument:  codes.InvalidArgument,
	ErrWatchCanceled:    codes.Canceled,
}

// toGRPCError 将内部错误转换为 gRPC 错误
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	// 检查是否已经是 gRPC status 错误
	if _, ok := status.FromError(err); ok {
		return err
	}

	// 查找映射的错误码
	for knownErr, code := range errorCodeMap {
		if errors.Is(err, knownErr) {
			return status.Error(code, err.Error())
		}
	}

	// 默认返回 Internal 错误
	return status.Error(codes.Internal, err.Error())
}
