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

package concurrency

import (
	"context"
	"errors"
	"fmt"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
)

var (
	ErrElectionNotLeader = errors.New("not elected as leader")
)

// Election 实现 Leader 选举
type Election struct {
	s   *Session
	pfx string // key 前缀

	leaderKey   string
	leaderRev   int64
	leaderValue string
	hdr         *pb.ResponseHeader

	mu     sync.Mutex
}

// NewElection 创建新的选举
func NewElection(s *Session, pfx string) *Election {
	return &Election{
		s:   s,
		pfx: pfx + "/",
	}
}

// Campaign 参与竞选，阻塞直到成为 Leader
func (e *Election) Campaign(ctx context.Context, val string) error {
	s := e.s
	client := s.client

	e.mu.Lock()
	// 如果已经是 Leader，直接返回
	if e.leaderKey != "" {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	// 创建竞选 key
	// key 格式: prefix/lease_id
	myKey := fmt.Sprintf("%s%x", e.pfx, s.Lease())
	
	// 使用事务创建 key
	cmp := clientv3.Compare(clientv3.CreateRevision(myKey), "=", 0)
	put := clientv3.OpPut(myKey, val, clientv3.WithLease(s.Lease()))
	get := clientv3.OpGet(myKey)
	
	resp, err := client.Txn(ctx).If(cmp).Then(put).Else(get).Commit()
	if err != nil {
		return err
	}

	var myRev int64
	if resp.Succeeded {
		myRev = resp.Header.Revision
	} else {
		myRev = resp.Responses[0].GetResponseRange().Kvs[0].CreateRevision
	}

	// 等待成为 Leader
	err = e.waitLeader(ctx, myKey, myRev)
	if err != nil {
		return err
	}

	// 成为 Leader
	e.mu.Lock()
	e.leaderKey = myKey
	e.leaderRev = myRev
	e.leaderValue = val
	e.hdr = resp.Header
	e.mu.Unlock()

	return nil
}

// waitLeader 等待成为 Leader（所有更早的 key 被删除）
// Automatically retries if Watch is canceled or network errors occur
func (e *Election) waitLeader(ctx context.Context, myKey string, myRev int64) error {
	client := e.s.client

	for {
		// 检查会话是否还有效
		select {
		case <-e.s.Done():
			return errors.New("session expired")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 获取所有前缀匹配的 key，按 CreateRevision 排序
		resp, err := client.Get(ctx, e.pfx,
			clientv3.WithPrefix(),
			clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend))
		if err != nil {
			return err
		}

		// 手动过滤出 CreateRevision < myRev 的 key
		var earlierKeys []*mvccpb.KeyValue
		for _, kv := range resp.Kvs {
			if kv.CreateRevision < myRev {
				earlierKeys = append(earlierKeys, kv)
			}
		}

		// 没有更早的 key，成为 Leader
		if len(earlierKeys) == 0 {
			return nil
		}

		// 找到最早的 key (first one after sorting and filtering)
		lastKey := string(earlierKeys[0].Key)

		// Watch for deletion with automatic retry on cancellation
		err = e.watchKeyDeletion(ctx, lastKey, resp.Header.Revision)
		if err != nil {
			// If watch was canceled or had network error, retry the loop
			// The loop will recheck if the key still exists
			if isElectionWatchCanceledOrNetworkError(err) {
				continue
			}
			return err
		}

		// Key was deleted, loop will recheck for more earlier keys
	}
}

// watchKeyDeletion watches a specific key for deletion
// Returns nil when key is deleted, error otherwise
func (e *Election) watchKeyDeletion(ctx context.Context, key string, revision int64) error {
	client := e.s.client

	// Create a cancellable context for watch
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	// Watch for deletion starting from the current revision
	wch := client.Watch(watchCtx, key, clientv3.WithRev(revision))

	for wresp := range wch {
		if wresp.Canceled {
			// Watch was canceled - could be network error or context cancellation
			if wresp.Err() != nil {
				return wresp.Err()
			}
			return errors.New("watch canceled")
		}
		for _, ev := range wresp.Events {
			if ev.Type == clientv3.EventTypeDelete {
				// Key deleted successfully
				return nil
			}
		}
	}

	// Watch channel closed without delete event, check context
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.s.Done():
		return errors.New("session expired")
	default:
		// Watch channel closed unexpectedly, return error to trigger retry
		return errors.New("watch channel closed")
	}
}

// isElectionWatchCanceledOrNetworkError checks if error is due to watch cancellation or network issue
func isElectionWatchCanceledOrNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for common watch cancellation and network error patterns
	return errStr == "watch canceled" ||
		errStr == "watch channel closed" ||
		errStr == "context canceled" ||
		errStr == "rpc error" ||
		errStr == "connection" ||
		errStr == "EOF"
}

// Resign 主动放弃 Leader 身份
func (e *Election) Resign(ctx context.Context) error {
	e.mu.Lock()
	if e.leaderKey == "" {
		e.mu.Unlock()
		return ErrElectionNotLeader
	}
	leaderKey := e.leaderKey
	e.leaderKey = ""
	e.mu.Unlock()

	_, err := e.s.client.Delete(ctx, leaderKey)
	return err
}

// Leader 返回当前的 Leader 信息
func (e *Election) Leader(ctx context.Context) (*pb.ResponseHeader, string, error) {
	client := e.s.client

	// 获取 CreateRevision 最小的 key（第一个创建的）
	resp, err := client.Get(ctx, e.pfx, clientv3.WithFirstCreate()...)
	if err != nil {
		return nil, "", err
	}

	if len(resp.Kvs) == 0 {
		return resp.Header, "", nil
	}

	return resp.Header, string(resp.Kvs[0].Value), nil
}

// Observe 监听 Leader 变化
// 返回一个 channel，每当 Leader 变化时会发送新的 Leader 值
func (e *Election) Observe(ctx context.Context) <-chan string {
	client := e.s.client
	ch := make(chan string, 1)

	go func() {
		defer close(ch)

		// 先发送当前 Leader
		_, leader, err := e.Leader(ctx)
		if err != nil {
			return
		}
		
		select {
		case ch <- leader:
		case <-ctx.Done():
			return
		}

		// Watch 前缀，监听变化
		wch := client.Watch(ctx, e.pfx, clientv3.WithPrefix())
		for wresp := range wch {
			if wresp.Canceled {
				return
			}

			// 有事件发生，重新查询 Leader
			_, leader, err := e.Leader(ctx)
			if err != nil {
				return
			}

			select {
			case ch <- leader:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

// IsLeader 检查当前是否是 Leader
func (e *Election) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leaderKey != ""
}

// Key 返回 Leader 的 key
func (e *Election) Key() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leaderKey
}

// Rev 返回 Leader key 的 revision
func (e *Election) Rev() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leaderRev
}

// Header 返回竞选成功时的响应头
func (e *Election) Header() *pb.ResponseHeader {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hdr
}
