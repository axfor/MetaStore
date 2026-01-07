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

package log

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// 常用field构造function

// String 字符串field
func String(key, val string) zap.Field {
	return zap.String(key, val)
}

// Int64 整数field
func Int64(key string, val int64) zap.Field {
	return zap.Int64(key, val)
}

// Int 整数field
func Int(key string, val int) zap.Field {
	return zap.Int(key, val)
}

// Uint64 无符号整数field
func Uint64(key string, val uint64) zap.Field {
	return zap.Uint64(key, val)
}

// Bool 布尔field
func Bool(key string, val bool) zap.Field {
	return zap.Bool(key, val)
}

// Duration timeintervalfield
func Duration(key string, val time.Duration) zap.Field {
	return zap.Duration(key, val)
}

// Time timefield
func Time(key string, val time.Time) zap.Field {
	return zap.Time(key, val)
}

// Err incorrectfield
func Err(err error) zap.Field {
	return zap.Error(err)
}

// Any 任意typefield
func Any(key string, val interface{}) zap.Field {
	return zap.Any(key, val)
}

// Namespace namespace（用at分groupfield）
func Namespace(key string) zap.Field {
	return zap.Namespace(key)
}

// 业务相关field

// Key KV 存储key
func Key(key []byte) zap.Field {
	return zap.ByteString("key", key)
}

// KeyString KV 存储key（字符串）
func KeyString(key string) zap.Field {
	return zap.String("key", key)
}

// Value KV 存储value
func Value(value []byte) zap.Field {
	// ifvalue太大，只recordlength
	if len(value) > 1024 {
		return zap.Int("value_size", len(value))
	}
	return zap.ByteString("value", value)
}

// Revision version号
func Revision(rev int64) zap.Field {
	return zap.Int64("revision", rev)
}

// LeaseID lease ID
func LeaseID(id int64) zap.Field {
	return zap.Int64("lease_id", id)
}

// TTL lease TTL
func TTL(ttl int64) zap.Field {
	return zap.Int64("ttl", ttl)
}

// MemberID member ID
func MemberID(id uint64) zap.Field {
	return zap.Uint64("member_id", id)
}

// ClusterID cluster ID
func ClusterID(id uint64) zap.Field {
	return zap.Uint64("cluster_id", id)
}

// Username user名
func Username(name string) zap.Field {
	return zap.String("username", name)
}

// RoleName role名
func RoleName(name string) zap.Field {
	return zap.String("role", name)
}

// Token token（脱敏）
func Token(token string) zap.Field {
	if len(token) > 8 {
		return zap.String("token", token[:8]+"...")
	}
	return zap.String("token", "***")
}

// Method gRPC method
func Method(method string) zap.Field {
	return zap.String("method", method)
}

// RemoteAddr 远程address
func RemoteAddr(addr string) zap.Field {
	return zap.String("remote_addr", addr)
}

// Component component名
func Component(name string) zap.Field {
	return zap.String("component", name)
}

// Phase 阶segment
func Phase(phase string) zap.Field {
	return zap.String("phase", phase)
}

// Count count
func Count(count int64) zap.Field {
	return zap.Int64("count", count)
}

// Goroutine goroutine 名称
func Goroutine(name string) zap.Field {
	return zap.String("goroutine", name)
}

// RequestID request ID
func RequestID(id string) zap.Field {
	return zap.String("request_id", id)
}

// resource相关field

// ResourceStats resourcestatistics（嵌套field）
func ResourceStats(currentConn, maxConn, currentReq, maxReq, mem, maxMem int64) zap.Field {
	return zap.Object("resources", zapResourceStats{
		CurrentConnections: currentConn,
		MaxConnections:     maxConn,
		CurrentRequests:    currentReq,
		MaxRequests:        maxReq,
		MemoryMB:           mem / 1024 / 1024,
		MaxMemoryMB:        maxMem / 1024 / 1024,
	})
}

// zapResourceStats resourcestatisticsobject（implement zapcore.ObjectMarshaler）
type zapResourceStats struct {
	CurrentConnections int64
	MaxConnections     int64
	CurrentRequests    int64
	MaxRequests        int64
	MemoryMB           int64
	MaxMemoryMB        int64
}

func (rs zapResourceStats) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddInt64("current_connections", rs.CurrentConnections)
	enc.AddInt64("max_connections", rs.MaxConnections)
	enc.AddInt64("current_requests", rs.CurrentRequests)
	enc.AddInt64("max_requests", rs.MaxRequests)
	enc.AddInt64("memory_mb", rs.MemoryMB)
	enc.AddInt64("max_memory_mb", rs.MaxMemoryMB)
	return nil
}
