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

package kvstore

// Store is the interface that all KV stores must implement
type Store interface {
	// 原有方法（保持向后兼容）
	Lookup(key string) (string, bool)
	Propose(k string, v string)
	GetSnapshot() ([]byte, error)

	// etcd 兼容方法

	// Range 执行范围查询
	// key: 起始键
	// rangeEnd: 结束键（空表示单键查询，"\x00" 表示查询所有键）
	// limit: 返回的最大键数（0 表示无限制）
	// revision: 查询指定 revision 的数据（0 表示最新）
	Range(key, rangeEnd string, limit int64, revision int64) (*RangeResponse, error)

	// PutWithLease 存储键值对，可选关联 lease
	// 返回新的 revision 和旧值（如果存在）
	PutWithLease(key, value string, leaseID int64) (revision int64, prevKv *KeyValue, err error)

	// DeleteRange 删除指定范围的键
	// 返回删除的键数量、被删除的键值对和新的 revision
	DeleteRange(key, rangeEnd string) (deleted int64, prevKvs []*KeyValue, revision int64, err error)

	// Txn 执行事务
	// cmps: 比较条件列表
	// thenOps: 比较成功时执行的操作
	// elseOps: 比较失败时执行的操作
	Txn(cmps []Compare, thenOps []Op, elseOps []Op) (*TxnResponse, error)

	// Watch 创建一个 watch，返回事件通道
	// key: 要监听的键
	// rangeEnd: 范围结束键（空表示单键）
	// startRevision: 开始监听的 revision（0 表示当前）
	// watchID: watch 的唯一标识符
	Watch(key, rangeEnd string, startRevision int64, watchID int64) (<-chan WatchEvent, error)

	// CancelWatch 取消一个 watch
	CancelWatch(watchID int64) error

	// Compact 压缩指定 revision 之前的历史数据
	Compact(revision int64) error

	// CurrentRevision 返回当前的 revision
	CurrentRevision() int64

	// Lease 相关方法

	// LeaseGrant 创建一个新的 lease
	LeaseGrant(id int64, ttl int64) (*Lease, error)

	// LeaseRevoke 撤销一个 lease（删除所有关联的键）
	LeaseRevoke(id int64) error

	// LeaseRenew 续约一个 lease
	LeaseRenew(id int64) (*Lease, error)

	// LeaseTimeToLive 获取 lease 的剩余时间
	LeaseTimeToLive(id int64) (*Lease, error)

	// Leases 返回所有 lease
	Leases() ([]*Lease, error)
}

// Commit represents a commit event from raft
type Commit struct {
	Data       []string
	ApplyDoneC chan<- struct{}
}

// KV represents a key-value pair
type KV struct {
	Key string
	Val string
}
