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

// globalperformanceconfig(use atomic certifyconcurrencysafe)
var (
	globalEnableProtobuf         atomic.Bool
	globalEnableSnapshotProtobuf atomic.Bool
	globalEnableLeaseProtobuf    atomic.Bool
)

func init() {
	// defaultenabledall Protobuf optimize
	globalEnableProtobuf.Store(true)
	globalEnableSnapshotProtobuf.Store(true)
	globalEnableLeaseProtobuf.Store(true)
}

// InitPerformanceConfig initializeglobalperformanceconfig
// shouldinload configaftercall
func InitPerformanceConfig(cfg *Config) {
	globalEnableProtobuf.Store(cfg.Server.Performance.EnableProtobuf)
	globalEnableSnapshotProtobuf.Store(cfg.Server.Performance.EnableSnapshotProtobuf)
	globalEnableLeaseProtobuf.Store(cfg.Server.Performance.EnableLeaseProtobuf)
}

// GetEnableProtobuf getisnoenabled Raft operation Protobuf serialize
func GetEnableProtobuf() bool {
	return globalEnableProtobuf.Load()
}

// GetEnableSnapshotProtobuf getisnoenabledsnapshot Protobuf serialize
func GetEnableSnapshotProtobuf() bool {
	return globalEnableSnapshotProtobuf.Load()
}

// GetEnableLeaseProtobuf getisnoenabled Lease Protobuf serialize
func GetEnableLeaseProtobuf() bool {
	return globalEnableLeaseProtobuf.Load()
}

// SetEnableProtobuf runningwhensetisnoenabled Raft operation Protobuf serialize
func SetEnableProtobuf(enable bool) {
	globalEnableProtobuf.Store(enable)
}

// SetEnableSnapshotProtobuf runningwhensetisnoenabledsnapshot Protobuf serialize
func SetEnableSnapshotProtobuf(enable bool) {
	globalEnableSnapshotProtobuf.Store(enable)
}

// SetEnableLeaseProtobuf runningwhensetisnoenabled Lease Protobuf serialize
func SetEnableLeaseProtobuf(enable bool) {
	globalEnableLeaseProtobuf.Store(enable)
}
