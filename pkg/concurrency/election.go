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

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Election 实现 leader 选举
type Election struct {
	session *Session
	keyPrefix string

	leaderKey     string
	leaderRev     int64
	leaderSession clientv3.LeaseID
}

// NewElection 创建新的选举
func NewElection(s *Session, pfx string) *Election {
	return &Election{
		session:   s,
		keyPrefix: pfx + "/",
	}
}

// Campaign 参与竞选
func (e *Election) Campaign(ctx context.Context, val string) error {
	// TODO: 实现
	// 1. Put key with lease and value
	// 2. Get all candidates
	// 3. If I have lowest revision, I'm leader
	// 4. Else, watch candidate with lower revision
	// 5. Wait for them to resign
	// 6. Become leader
	return nil
}

// Resign 退出竞选
func (e *Election) Resign(ctx context.Context) error {
	// TODO: 实现
	// Delete my campaign key
	return nil
}

// Leader 获取当前 leader
func (e *Election) Leader(ctx context.Context) (*clientv3.GetResponse, error) {
	// TODO: 实现
	// Get candidate with lowest revision
	return nil, nil
}

// Observe 观察 leader 变化
func (e *Election) Observe(ctx context.Context) <-chan clientv3.GetResponse {
	// TODO: 实现
	// Watch for leader changes
	// Return channel that receives leader updates
	ch := make(chan clientv3.GetResponse)
	return ch
}

// Key 返回当前节点的竞选键
func (e *Election) Key() string {
	return e.leaderKey
}

// Rev 返回当前节点的 revision
func (e *Election) Rev() int64 {
	return e.leaderRev
}
