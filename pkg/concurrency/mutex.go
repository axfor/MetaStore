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
)

// Mutex 分布式互斥锁
type Mutex struct {
	s     *Session
	pfx   string
	myKey string
	myRev int64
}

// NewMutex 创建新的 Mutex
func NewMutex(s *Session, pfx string) *Mutex {
	return &Mutex{
		s:   s,
		pfx: pfx + "/",
	}
}

// Lock 获取锁（阻塞）
func (m *Mutex) Lock(ctx context.Context) error {
	// TODO: 实现
	// 1. Put key with Lease
	// 2. Get range to find all waiters
	// 3. If I'm first (lowest revision), acquired lock
	// 4. Else, watch previous waiter
	// 5. Wait for previous waiter to release
	// 6. Repeat from step 2

	// 实现参考：
	// s := m.s
	// client := s.client
	//
	// key := fmt.Sprintf("%s%x", m.pfx, s.Lease())
	//
	// cmp := clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
	// put := clientv3.OpPut(key, "", clientv3.WithLease(s.Lease()))
	// get := clientv3.OpGet(m.pfx, clientv3.WithPrefix())
	//
	// resp, err := client.Txn(ctx).If(cmp).Then(put, get).Else(get).Commit()
	// if err != nil {
	//     return err
	// }
	//
	// m.myRev = resp.Responses[0].GetResponsePut().Header.Revision
	// ownerKey := resp.Responses[1].GetResponseRange().Kvs
	//
	// if len(ownerKey) == 0 || ownerKey[0].CreateRevision == m.myRev {
	//     m.myKey = key
	//     return nil
	// }
	//
	// // Wait for previous holder
	// return m.waitDeletes(ctx, ownerKey[0].Key)

	return nil
}

// TryLock 尝试获取锁（非阻塞）
func (m *Mutex) TryLock(ctx context.Context) error {
	// TODO: 实现
	// Similar to Lock but don't wait, return error if can't acquire
	return nil
}

// Unlock 释放锁
func (m *Mutex) Unlock(ctx context.Context) error {
	// TODO: 实现
	// Delete my key
	// client := m.s.client
	// _, err := client.Delete(ctx, m.myKey)
	// return err
	return nil
}

// IsOwner 检查是否持有锁
func (m *Mutex) IsOwner() bool {
	return m.myKey != ""
}

// Key 返回锁的键
func (m *Mutex) Key() string {
	return m.myKey
}

// waitDeletes 等待指定 key 被删除
func (m *Mutex) waitDeletes(ctx context.Context, key []byte) error {
	// TODO: 实现
	// Watch key until it's deleted
	// client := m.s.client
	// wch := client.Watch(ctx, string(key))
	// for resp := range wch {
	//     for _, ev := range resp.Events {
	//         if ev.Type == mvccpb.DELETE {
	//             return nil
	//         }
	//     }
	// }
	return nil
}
