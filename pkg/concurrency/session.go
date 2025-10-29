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
	"fmt"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Session 表示一个基于 Lease 的会话
// 会话会自动续约 Lease，当会话关闭时会自动撤销 Lease
type Session struct {
	client *clientv3.Client
	lease  clientv3.Lease
	id     clientv3.LeaseID

	cancel  context.CancelFunc
	donec   <-chan struct{}
	mu      sync.Mutex
	closed  bool
}

// SessionOption 会话配置选项
type SessionOption func(*sessionOptions)

type sessionOptions struct {
	ttl     int
	leaseID clientv3.LeaseID
	ctx     context.Context
}

// WithTTL 设置会话 TTL（秒）
func WithTTL(ttl int) SessionOption {
	return func(so *sessionOptions) {
		if ttl > 0 {
			so.ttl = ttl
		}
	}
}

// WithLease 使用现有的 Lease ID
func WithLease(leaseID clientv3.LeaseID) SessionOption {
	return func(so *sessionOptions) {
		so.leaseID = leaseID
	}
}

// WithContext 设置 context
func WithContext(ctx context.Context) SessionOption {
	return func(so *sessionOptions) {
		so.ctx = ctx
	}
}

// NewSession 创建新的会话
func NewSession(client *clientv3.Client, opts ...SessionOption) (*Session, error) {
	options := &sessionOptions{
		ttl: 60, // 默认 60 秒
		ctx: context.Background(),
	}

	for _, opt := range opts {
		opt(options)
	}

	ctx, cancel := context.WithCancel(options.ctx)
	s := &Session{
		client: client,
		lease:  clientv3.NewLease(client),
		cancel: cancel,
	}

	// 如果没有提供 LeaseID，创建新的 Lease
	if options.leaseID == clientv3.NoLease {
		resp, err := s.lease.Grant(ctx, int64(options.ttl))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to grant lease: %w", err)
		}
		s.id = resp.ID
	} else {
		// 使用现有 Lease，验证其是否有效
		ttlResp, err := s.lease.TimeToLive(ctx, options.leaseID)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to check lease: %w", err)
		}
		if ttlResp.TTL <= 0 {
			cancel()
			return nil, fmt.Errorf("lease %x expired or not found", options.leaseID)
		}
		s.id = options.leaseID
	}

	// 启动自动续约
	donec := make(chan struct{})
	s.donec = donec
	go s.keepAliveLoop(ctx, donec)

	return s, nil
}

// keepAliveLoop 自动续约循环
func (s *Session) keepAliveLoop(ctx context.Context, donec chan struct{}) {
	defer close(donec)

	// 创建 KeepAlive 通道
	kac, err := s.lease.KeepAlive(ctx, s.id)
	if err != nil {
		return
	}

	// 消费 KeepAlive 响应
	for {
		select {
		case <-ctx.Done():
			return
		case ka, ok := <-kac:
			if !ok {
				// KeepAlive 通道关闭，会话失效
				return
			}
			if ka == nil {
				// 收到 nil 响应，可能是网络问题
				continue
			}
			// 成功续约
		}
	}
}

// Close 关闭会话并撤销 Lease
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// 取消 context，停止 keepalive
	s.cancel()

	// 等待 keepalive 循环结束
	<-s.donec

	// 撤销 Lease（使用新的 context，因为原 context 已取消）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := s.lease.Revoke(ctx, s.id)
	return err
}

// Lease 返回会话的 Lease ID
func (s *Session) Lease() clientv3.LeaseID {
	return s.id
}

// Done 返回一个 channel，当会话失效时会被关闭
func (s *Session) Done() <-chan struct{} {
	return s.donec
}

// Orphan 结束会话但不撤销 Lease
// 用于将资源交给其他进程管理
func (s *Session) Orphan() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	<-s.donec
}
