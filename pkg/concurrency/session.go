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

// Session indicates一个基at Lease session
// sessionwill自动renewal Lease，whensessionclose时will自动revoke Lease
type Session struct {
	client *clientv3.Client
	lease  clientv3.Lease
	id     clientv3.LeaseID

	cancel  context.CancelFunc
	donec   <-chan struct{}
	mu      sync.Mutex
	closed  bool
}

// SessionOption sessionconfigoption
type SessionOption func(*sessionOptions)

type sessionOptions struct {
	ttl     int
	leaseID clientv3.LeaseID
	ctx     context.Context
}

// WithTTL setsession TTL（秒）
func WithTTL(ttl int) SessionOption {
	return func(so *sessionOptions) {
		if ttl > 0 {
			so.ttl = ttl
		}
	}
}

// WithLease use现有 Lease ID
func WithLease(leaseID clientv3.LeaseID) SessionOption {
	return func(so *sessionOptions) {
		so.leaseID = leaseID
	}
}

// WithContext set context
func WithContext(ctx context.Context) SessionOption {
	return func(so *sessionOptions) {
		so.ctx = ctx
	}
}

// NewSession createnewsession
func NewSession(client *clientv3.Client, opts ...SessionOption) (*Session, error) {
	options := &sessionOptions{
		ttl: 60, // default 60 秒
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

	// ifnone提供 LeaseID，createnew Lease
	if options.leaseID == clientv3.NoLease {
		resp, err := s.lease.Grant(ctx, int64(options.ttl))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to grant lease: %w", err)
		}
		s.id = resp.ID
	} else {
		// use现有 Lease，verify其isnovalid
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

	// start自动renewal
	donec := make(chan struct{})
	s.donec = donec
	go s.keepAliveLoop(ctx, donec)

	return s, nil
}

// keepAliveLoop 自动renewal循环
func (s *Session) keepAliveLoop(ctx context.Context, donec chan struct{}) {
	defer close(donec)

	// create KeepAlive channel
	kac, err := s.lease.KeepAlive(ctx, s.id)
	if err != nil {
		return
	}

	// 消费 KeepAlive response
	for {
		select {
		case <-ctx.Done():
			return
		case ka, ok := <-kac:
			if !ok {
				// KeepAlive channelclose，session失效
				return
			}
			if ka == nil {
				// 收to nil response，mayisnetwork问题
				continue
			}
			// successrenewal
		}
	}
}

// Close closesession并revoke Lease
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// cancel context，stopped keepalive
	s.cancel()

	// wait keepalive 循环end
	<-s.donec

	// revoke Lease（usenew context，因as原 context 已cancel）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := s.lease.Revoke(ctx, s.id)
	return err
}

// Lease returnsession Lease ID
func (s *Session) Lease() clientv3.LeaseID {
	return s.id
}

// Done return一个 channel，whensession失效时will被close
func (s *Session) Done() <-chan struct{} {
	return s.donec
}

// Orphan endsession但notrevoke Lease
// 用atwillresource交给其他processmanagement
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
