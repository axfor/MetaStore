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
	"go.etcd.io/etcd/client/v3/concurrency"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// Mutex 实现分布式互斥锁
type Mutex struct {
	s   *Session
	pfx string // key 前缀

	myKey string
	myRev int64
	hdr   *pb.ResponseHeader

	mu sync.Mutex
}

// NewMutex 创建新的互斥锁
func NewMutex(s *Session, pfx string) *Mutex {
	return &Mutex{
		s:   s,
		pfx: pfx + "/",
	}
}

// Lock 获取锁，阻塞直到成功
func (m *Mutex) Lock(ctx context.Context) error {
	s := m.s
	client := m.s.client

	m.mu.Lock()
	// 如果已经持有锁，直接返回
	if m.myKey != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// 使用 Lease 创建临时 key
	// key 格式: prefix/lease_id
	myKey := fmt.Sprintf("%s%x", m.pfx, s.Lease())
	
	// 使用事务创建 key（仅当不存在时）
	cmp := clientv3.Compare(clientv3.CreateRevision(myKey), "=", 0)
	put := clientv3.OpPut(myKey, "", clientv3.WithLease(s.Lease()))
	get := clientv3.OpGet(myKey)
	
	resp, err := client.Txn(ctx).If(cmp).Then(put).Else(get).Commit()
	if err != nil {
		return err
	}

	var myRev int64
	if resp.Succeeded {
		myRev = resp.Header.Revision
	} else {
		// key 已存在，获取其 revision
		myRev = resp.Responses[0].GetResponseRange().Kvs[0].CreateRevision
	}

	// 保存锁信息
	m.mu.Lock()
	m.myKey = myKey
	m.myRev = myRev
	m.hdr = resp.Header
	m.mu.Unlock()

	// 等待获取锁
	return m.waitDeletes(ctx, myKey, myRev)
}

// waitDeletes 等待所有更早的 key 被删除
func (m *Mutex) waitDeletes(ctx context.Context, myKey string, myRev int64) error {
	client := m.s.client

	// 获取所有前缀匹配的 key
	getOpts := append(clientv3.WithFirstCreate(), clientv3.WithMaxCreateRev(myRev-1))
	for {
		// 获取所有 CreateRevision < myRev 的 key
		resp, err := client.Get(ctx, m.pfx, getOpts...)
		if err != nil {
			return err
		}

		// 没有更早的 key，获得锁
		if len(resp.Kvs) == 0 {
			return nil
		}

		// 找到最大的 CreateRevision 小于 myRev 的 key
		lastKey := string(resp.Kvs[0].Key)

		// Watch 该 key，等待其删除
		wch := client.Watch(ctx, lastKey, clientv3.WithRev(myRev))
		for wresp := range wch {
			if wresp.Canceled {
				return errors.New("watch canceled")
			}
			for _, ev := range wresp.Events {
				if ev.Type == clientv3.EventTypeDelete {
					// key 被删除，继续检查
					goto RETRY
				}
			}
		}
		
		RETRY:
		// 检查会话是否还有效
		select {
		case <-m.s.Done():
			return errors.New("session expired")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

// TryLock 尝试获取锁，不阻塞
func (m *Mutex) TryLock(ctx context.Context) error {
	s := m.s
	client := m.s.client

	m.mu.Lock()
	if m.myKey != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	myKey := fmt.Sprintf("%s%x", m.pfx, s.Lease())
	
	// 创建 key
	cmp := clientv3.Compare(clientv3.CreateRevision(myKey), "=", 0)
	put := clientv3.OpPut(myKey, "", clientv3.WithLease(s.Lease()))
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

	// 检查是否有更早的 key
	getOpts := append(clientv3.WithFirstCreate(), clientv3.WithMaxCreateRev(myRev-1))
	gresp, err := client.Get(ctx, m.pfx, getOpts...)
	if err != nil {
		return err
	}

	if len(gresp.Kvs) > 0 {
		// 有更早的 key，删除自己的 key
		_, _ = client.Delete(ctx, myKey)
		return concurrency.ErrLocked
	}

	// 获得锁
	m.mu.Lock()
	m.myKey = myKey
	m.myRev = myRev
	m.hdr = resp.Header
	m.mu.Unlock()

	return nil
}

// Unlock 释放锁
func (m *Mutex) Unlock(ctx context.Context) error {
	m.mu.Lock()
	if m.myKey == "" {
		m.mu.Unlock()
		return nil
	}
	myKey := m.myKey
	m.myKey = ""
	m.mu.Unlock()

	_, err := m.s.client.Delete(ctx, myKey)
	return err
}

// IsOwner 检查当前是否持有锁
func (m *Mutex) IsOwner() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.myKey != ""
}

// Key 返回锁的 key
func (m *Mutex) Key() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.myKey
}

// Header 返回锁创建时的响应头
func (m *Mutex) Header() *pb.ResponseHeader {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hdr
}
