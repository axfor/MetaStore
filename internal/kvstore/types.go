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

// KeyValue extendkey-value pairstructure，supported etcd 语义
type KeyValue struct {
	Key            []byte // key
	Value          []byte // value
	CreateRevision int64  // create时 revision
	ModRevision    int64  // lastmodify revision
	Version        int64  // 该keymodifytimes（from 1 start）
	Lease          int64  // 关联 lease ID（0 indicates无 lease）
}

// WatchEvent indicates一个 watch event
type WatchEvent struct {
	Type     EventType // eventtype：PUT or DELETE
	Kv       *KeyValue // currentkey-value pair
	PrevKv   *KeyValue // 前一个key-value pair（ifrequest）
	Revision int64     // event发生时 revision
}

// EventType eventtype
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

// Compare indicatestransaction中compareoperation
type Compare struct {
	Target      CompareTarget   // comparetarget：VERSION, CREATE, MOD, VALUE, LEASE
	Result      CompareResult   // compareresult：EQUAL, GREATER, LESS, NOT_EQUAL
	Key         []byte          // key
	TargetUnion CompareUnion    // comparevalue
}

// CompareTarget comparetargettype
type CompareTarget int

const (
	CompareVersion CompareTarget = 0
	CompareCreate  CompareTarget = 1
	CompareMod     CompareTarget = 2
	CompareValue   CompareTarget = 3
	CompareLease   CompareTarget = 4
)

// CompareResult compareresulttype
type CompareResult int

const (
	CompareEqual    CompareResult = 0
	CompareGreater  CompareResult = 1
	CompareLess     CompareResult = 2
	CompareNotEqual CompareResult = 3
)

// CompareUnion comparevalue联合type
type CompareUnion struct {
	Version        int64
	CreateRevision int64
	ModRevision    int64
	Value          []byte
	Lease          int64
}

// Op indicatestransaction中operation
type Op struct {
	Type     OpType // operationtype：RANGE, PUT, DELETE, TXN
	Key      []byte
	RangeEnd []byte
	Value    []byte
	Limit    int64
	LeaseID  int64
}

// OpType operationtype
type OpType int

const (
	OpRange  OpType = 0
	OpPut    OpType = 1
	OpDelete OpType = 2
	OpTxn    OpType = 3
)

// TxnResponse transactionresponse
type TxnResponse struct {
	Succeeded bool              // compareisnosuccess
	Responses []OpResponse      // operationresponselist
	Revision  int64             // transactionexecute后 revision
}

// OpResponse operationresponse
type OpResponse struct {
	Type         OpType
	RangeResp    *RangeResponse
	PutResp      *PutResponse
	DeleteResp   *DeleteResponse
}

// RangeOptions Range operationoption
type RangeOptions struct {
	Limit             int64           // returnkeyquantitylimit
	Revision          int64           // 查询specified revision data
	SortOrder         SortOrder       // sortorder
	SortTarget        SortTarget      // sorttarget
	MaxCreateRevision int64           // maximumcreate revision filter
	MinCreateRevision int64           // minimumcreate revision filter
	MaxModRevision    int64           // maximummodify revision filter
	MinModRevision    int64           // minimummodify revision filter
	CountOnly         bool            // 只returnquantity
	KeysOnly          bool            // 只returnkey
}

// SortOrder sortorder
type SortOrder int

const (
	SortNone    SortOrder = 0
	SortAscend  SortOrder = 1
	SortDescend SortOrder = 2
)

// SortTarget sorttarget
type SortTarget int

const (
	SortByKey     SortTarget = 0
	SortByVersion SortTarget = 1
	SortByCreate  SortTarget = 2
	SortByMod     SortTarget = 3
	SortByValue   SortTarget = 4
)

// RangeResponse Range operationresponse
type RangeResponse struct {
	Kvs      []*KeyValue
	More     bool
	Count    int64
	Revision int64
}

// PutResponse Put operationresponse
type PutResponse struct {
	PrevKv   *KeyValue
	Revision int64
}

// DeleteResponse Delete operationresponse
type DeleteResponse struct {
	Deleted  int64       // deletekeyquantity
	PrevKvs  []*KeyValue // 被deletekey-value pair
	Revision int64
}

// Lease leasestructure
type Lease struct {
	ID        int64              // Lease ID
	TTL       int64              // 生存time（秒）
	GrantTime time.Time          // granttime
	Keys      map[string]bool    // 关联keyset
}

// IsExpired checkleaseisno已expiration
func (l *Lease) IsExpired() bool {
	if l == nil {
		return true
	}
	elapsed := time.Since(l.GrantTime).Seconds()
	return elapsed >= float64(l.TTL)
}

// Remaining return剩余time（秒）
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

// Renew renewal，returnnew剩余time
func (l *Lease) Renew(ttl int64) int64 {
	if l == nil {
		return 0
	}
	l.TTL = ttl
	l.GrantTime = time.Now()
	return l.TTL
}

// RaftStatus Raft statusinfo
type RaftStatus struct {
	NodeID   uint64 `json:"node_id"`   // currentnode ID
	Term     uint64 `json:"term"`      // current Term
	LeaderID uint64 `json:"leader_id"` // Leader node ID (0 indicates无 leader)
	State    string `json:"state"`     // "leader", "follower", "candidate", "pre-candidate"
	Applied  uint64 `json:"applied"`   // 已应用 index
	Commit   uint64 `json:"commit"`    // 已commit index
}
