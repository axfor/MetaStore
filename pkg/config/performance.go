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

package config

import "sync/atomic"

// 全局性能配置（使用 atomic 保证并发安全）
var (
	globalEnableProtobuf         atomic.Bool
	globalEnableSnapshotProtobuf atomic.Bool
	globalEnableLeaseProtobuf    atomic.Bool
)

func init() {
	// 默认启用所有 Protobuf 优化
	globalEnableProtobuf.Store(true)
	globalEnableSnapshotProtobuf.Store(true)
	globalEnableLeaseProtobuf.Store(true)
}

// InitPerformanceConfig 初始化全局性能配置
// 应该在加载配置后立即调用
func InitPerformanceConfig(cfg *Config) {
	globalEnableProtobuf.Store(cfg.Server.Performance.EnableProtobuf)
	globalEnableSnapshotProtobuf.Store(cfg.Server.Performance.EnableSnapshotProtobuf)
	globalEnableLeaseProtobuf.Store(cfg.Server.Performance.EnableLeaseProtobuf)
}

// GetEnableProtobuf 获取是否启用 Raft 操作 Protobuf 序列化
func GetEnableProtobuf() bool {
	return globalEnableProtobuf.Load()
}

// GetEnableSnapshotProtobuf 获取是否启用快照 Protobuf 序列化
func GetEnableSnapshotProtobuf() bool {
	return globalEnableSnapshotProtobuf.Load()
}

// GetEnableLeaseProtobuf 获取是否启用 Lease Protobuf 序列化
func GetEnableLeaseProtobuf() bool {
	return globalEnableLeaseProtobuf.Load()
}

// SetEnableProtobuf 运行时设置是否启用 Raft 操作 Protobuf 序列化
func SetEnableProtobuf(enable bool) {
	globalEnableProtobuf.Store(enable)
}

// SetEnableSnapshotProtobuf 运行时设置是否启用快照 Protobuf 序列化
func SetEnableSnapshotProtobuf(enable bool) {
	globalEnableSnapshotProtobuf.Store(enable)
}

// SetEnableLeaseProtobuf 运行时设置是否启用 Lease Protobuf 序列化
func SetEnableLeaseProtobuf(enable bool) {
	globalEnableLeaseProtobuf.Store(enable)
}
