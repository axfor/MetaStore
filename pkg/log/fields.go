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

// 常用字段构造函数

// String 字符串字段
func String(key, val string) zap.Field {
	return zap.String(key, val)
}

// Int64 整数字段
func Int64(key string, val int64) zap.Field {
	return zap.Int64(key, val)
}

// Int 整数字段
func Int(key string, val int) zap.Field {
	return zap.Int(key, val)
}

// Uint64 无符号整数字段
func Uint64(key string, val uint64) zap.Field {
	return zap.Uint64(key, val)
}

// Bool 布尔字段
func Bool(key string, val bool) zap.Field {
	return zap.Bool(key, val)
}

// Duration 时间间隔字段
func Duration(key string, val time.Duration) zap.Field {
	return zap.Duration(key, val)
}

// Time 时间字段
func Time(key string, val time.Time) zap.Field {
	return zap.Time(key, val)
}

// Err 错误字段
func Err(err error) zap.Field {
	return zap.Error(err)
}

// Any 任意类型字段
func Any(key string, val interface{}) zap.Field {
	return zap.Any(key, val)
}

// Namespace 命名空间（用于分组字段）
func Namespace(key string) zap.Field {
	return zap.Namespace(key)
}

// 业务相关字段

// Key KV 存储的键
func Key(key []byte) zap.Field {
	return zap.ByteString("key", key)
}

// KeyString KV 存储的键（字符串）
func KeyString(key string) zap.Field {
	return zap.String("key", key)
}

// Value KV 存储的值
func Value(value []byte) zap.Field {
	// 如果值太大，只记录长度
	if len(value) > 1024 {
		return zap.Int("value_size", len(value))
	}
	return zap.ByteString("value", value)
}

// Revision 版本号
func Revision(rev int64) zap.Field {
	return zap.Int64("revision", rev)
}

// LeaseID 租约 ID
func LeaseID(id int64) zap.Field {
	return zap.Int64("lease_id", id)
}

// TTL 租约 TTL
func TTL(ttl int64) zap.Field {
	return zap.Int64("ttl", ttl)
}

// MemberID 成员 ID
func MemberID(id uint64) zap.Field {
	return zap.Uint64("member_id", id)
}

// ClusterID 集群 ID
func ClusterID(id uint64) zap.Field {
	return zap.Uint64("cluster_id", id)
}

// Username 用户名
func Username(name string) zap.Field {
	return zap.String("username", name)
}

// RoleName 角色名
func RoleName(name string) zap.Field {
	return zap.String("role", name)
}

// Token 令牌（脱敏）
func Token(token string) zap.Field {
	if len(token) > 8 {
		return zap.String("token", token[:8]+"...")
	}
	return zap.String("token", "***")
}

// Method gRPC 方法
func Method(method string) zap.Field {
	return zap.String("method", method)
}

// RemoteAddr 远程地址
func RemoteAddr(addr string) zap.Field {
	return zap.String("remote_addr", addr)
}

// Component 组件名
func Component(name string) zap.Field {
	return zap.String("component", name)
}

// Phase 阶段
func Phase(phase string) zap.Field {
	return zap.String("phase", phase)
}

// Count 计数
func Count(count int64) zap.Field {
	return zap.Int64("count", count)
}

// Goroutine goroutine 名称
func Goroutine(name string) zap.Field {
	return zap.String("goroutine", name)
}

// RequestID 请求 ID
func RequestID(id string) zap.Field {
	return zap.String("request_id", id)
}

// 资源相关字段

// ResourceStats 资源统计（嵌套字段）
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

// zapResourceStats 资源统计对象（实现 zapcore.ObjectMarshaler）
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
