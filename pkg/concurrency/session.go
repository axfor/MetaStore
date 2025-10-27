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

// Session 表示一个租约会话
type Session struct {
	client  *clientv3.Client
	leaseID clientv3.LeaseID
	donec   chan struct{}
	cancel  context.CancelFunc
}

// NewSession 创建新会话
func NewSession(client *clientv3.Client, opts ...SessionOption) (*Session, error) {
	ctx, cancel := context.WithCancel(client.Ctx())

	// 默认选项
	cfg := &sessionConfig{
		ttl: 60,
		ctx: ctx,
	}

	// 应用选项
	for _, opt := range opts {
		opt(cfg)
	}

	// TODO: 实现
	// 1. 创建 Lease
	// 2. 启动 KeepAlive goroutine
	// 3. 监控 Lease 失效
	// 4. 返回 Session

	return &Session{
		client:  client,
		leaseID: 0, // TODO: 从 LeaseGrant 获取
		donec:   make(chan struct{}),
		cancel:  cancel,
	}, nil
}

// Lease 返回会话的 Lease ID
func (s *Session) Lease() clientv3.LeaseID {
	return s.leaseID
}

// Done 返回会话结束通道
func (s *Session) Done() <-chan struct{} {
	return s.donec
}

// Close 关闭会话
func (s *Session) Close() error {
	s.cancel()
	// TODO: 撤销 Lease
	return nil
}

// SessionOption 会话选项
type SessionOption func(*sessionConfig)

type sessionConfig struct {
	ttl int
	ctx context.Context
}

// WithTTL 设置 TTL
func WithTTL(ttl int) SessionOption {
	return func(cfg *sessionConfig) {
		cfg.ttl = ttl
	}
}

// WithContext 设置 Context
func WithContext(ctx context.Context) SessionOption {
	return func(cfg *sessionConfig) {
		cfg.ctx = ctx
	}
}
