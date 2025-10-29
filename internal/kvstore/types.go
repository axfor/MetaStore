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

import "time"

// KeyValue 扩展的键值对结构，支持 etcd 语义
type KeyValue struct {
	Key            []byte // 键
	Value          []byte // 值
	CreateRevision int64  // 创建时的 revision
	ModRevision    int64  // 最后修改的 revision
	Version        int64  // 该键的修改次数（从 1 开始）
	Lease          int64  // 关联的 lease ID（0 表示无 lease）
}

// WatchEvent 表示一个 watch 事件
type WatchEvent struct {
	Type     EventType // 事件类型：PUT 或 DELETE
	Kv       *KeyValue // 当前键值对
	PrevKv   *KeyValue // 前一个键值对（如果请求了）
	Revision int64     // 事件发生时的 revision
}

// EventType 事件类型
type EventType int

const (
	EventTypePut    EventType = 0
	EventTypeDelete EventType = 1
)

// WatchOptions contains options for creating a watch
type WatchOptions struct {
	// PrevKV enables returning the previous key-value for each event
	PrevKV bool

	// ProgressNotify enables periodic progress notifications
	ProgressNotify bool

	// Filters specify which events to filter out
	Filters []WatchFilterType

	// Fragment enables splitting large revisions into multiple responses
	Fragment bool
}

// WatchFilterType represents watch filter types
type WatchFilterType int

const (
	FilterNone WatchFilterType = iota
	FilterNoPut                 // Filter out PUT events
	FilterNoDelete              // Filter out DELETE events
)

// Compare 表示事务中的比较操作
type Compare struct {
	Target      CompareTarget   // 比较目标：VERSION, CREATE, MOD, VALUE, LEASE
	Result      CompareResult   // 比较结果：EQUAL, GREATER, LESS, NOT_EQUAL
	Key         []byte          // 键
	TargetUnion CompareUnion    // 比较的值
}

// CompareTarget 比较目标类型
type CompareTarget int

const (
	CompareVersion CompareTarget = 0
	CompareCreate  CompareTarget = 1
	CompareMod     CompareTarget = 2
	CompareValue   CompareTarget = 3
	CompareLease   CompareTarget = 4
)

// CompareResult 比较结果类型
type CompareResult int

const (
	CompareEqual    CompareResult = 0
	CompareGreater  CompareResult = 1
	CompareLess     CompareResult = 2
	CompareNotEqual CompareResult = 3
)

// CompareUnion 比较值的联合类型
type CompareUnion struct {
	Version        int64
	CreateRevision int64
	ModRevision    int64
	Value          []byte
	Lease          int64
}

// Op 表示事务中的操作
type Op struct {
	Type     OpType // 操作类型：RANGE, PUT, DELETE, TXN
	Key      []byte
	RangeEnd []byte
	Value    []byte
	Limit    int64
	LeaseID  int64
}

// OpType 操作类型
type OpType int

const (
	OpRange  OpType = 0
	OpPut    OpType = 1
	OpDelete OpType = 2
	OpTxn    OpType = 3
)

// TxnResponse 事务响应
type TxnResponse struct {
	Succeeded bool              // 比较是否成功
	Responses []OpResponse      // 操作响应列表
	Revision  int64             // 事务执行后的 revision
}

// OpResponse 操作响应
type OpResponse struct {
	Type         OpType
	RangeResp    *RangeResponse
	PutResp      *PutResponse
	DeleteResp   *DeleteResponse
}

// RangeResponse Range 操作响应
type RangeResponse struct {
	Kvs      []*KeyValue
	More     bool
	Count    int64
	Revision int64
}

// PutResponse Put 操作响应
type PutResponse struct {
	PrevKv   *KeyValue
	Revision int64
}

// DeleteResponse Delete 操作响应
type DeleteResponse struct {
	Deleted  int64       // 删除的键数量
	PrevKvs  []*KeyValue // 被删除的键值对
	Revision int64
}

// Lease 租约结构
type Lease struct {
	ID        int64              // Lease ID
	TTL       int64              // 生存时间（秒）
	GrantTime time.Time          // 授予时间
	Keys      map[string]bool    // 关联的键集合
}

// IsExpired 检查租约是否已过期
func (l *Lease) IsExpired() bool {
	if l == nil {
		return true
	}
	elapsed := time.Since(l.GrantTime).Seconds()
	return elapsed >= float64(l.TTL)
}

// Remaining 返回剩余时间（秒）
func (l *Lease) Remaining() int64 {
	if l == nil {
		return 0
	}
	elapsed := time.Since(l.GrantTime).Seconds()
	remaining := float64(l.TTL) - elapsed
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

// Renew 续约，返回新的剩余时间
func (l *Lease) Renew(ttl int64) int64 {
	if l == nil {
		return 0
	}
	l.TTL = ttl
	l.GrantTime = time.Now()
	return l.TTL
}

// RaftStatus Raft 状态信息
type RaftStatus struct {
	NodeID   uint64 `json:"node_id"`   // 当前节点 ID
	Term     uint64 `json:"term"`      // 当前 Term
	LeaderID uint64 `json:"leader_id"` // Leader 节点 ID (0 表示无 leader)
	State    string `json:"state"`     // "leader", "follower", "candidate", "pre-candidate"
	Applied  uint64 `json:"applied"`   // 已应用的 index
	Commit   uint64 `json:"commit"`    // 已提交的 index
}
