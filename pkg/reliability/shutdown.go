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

package reliability

import (
	"context"
	"fmt"
	"metaStore/pkg/log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// ShutdownHook 关闭钩子函数类型
type ShutdownHook func(ctx context.Context) error

// ShutdownPhase 关闭阶段
type ShutdownPhase int

const (
	// PhaseStopAccepting 停止接受新请求
	PhaseStopAccepting ShutdownPhase = iota
	// PhaseDrainConnections 排空现有连接
	PhaseDrainConnections
	// PhasePersistState 持久化状态
	PhasePersistState
	// PhaseCloseResources 关闭资源
	PhaseCloseResources
)

// GracefulShutdown 优雅关闭管理器
type GracefulShutdown struct {
	mu      sync.RWMutex
	hooks   map[ShutdownPhase][]ShutdownHook
	timeout time.Duration
	done    chan struct{}
	signals chan os.Signal
}

// NewGracefulShutdown 创建优雅关闭管理器
func NewGracefulShutdown(timeout time.Duration) *GracefulShutdown {
	if timeout == 0 {
		timeout = 30 * time.Second // 默认 30 秒超时
	}

	gs := &GracefulShutdown{
		hooks:   make(map[ShutdownPhase][]ShutdownHook),
		timeout: timeout,
		done:    make(chan struct{}),
		signals: make(chan os.Signal, 1),
	}

	// 注册系统信号
	signal.Notify(gs.signals, syscall.SIGTERM, syscall.SIGINT)

	return gs
}

// RegisterHook 注册关闭钩子
func (gs *GracefulShutdown) RegisterHook(phase ShutdownPhase, hook ShutdownHook) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	gs.hooks[phase] = append(gs.hooks[phase], hook)
}

// Wait 等待关闭信号
func (gs *GracefulShutdown) Wait() {
	sig := <-gs.signals
	log.Info("Received shutdown signal",
		log.String("signal", sig.String()),
		log.Component("shutdown"))
	gs.Shutdown()
}

// Shutdown 执行优雅关闭
func (gs *GracefulShutdown) Shutdown() {
	gs.mu.Lock()
	select {
	case <-gs.done:
		// 已经在关闭中
		gs.mu.Unlock()
		return
	default:
		close(gs.done)
	}
	gs.mu.Unlock()

	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(context.Background(), gs.timeout)
	defer cancel()

	phases := []ShutdownPhase{
		PhaseStopAccepting,
		PhaseDrainConnections,
		PhasePersistState,
		PhaseCloseResources,
	}

	for _, phase := range phases {
		phaseName := gs.phaseName(phase)
		log.Info("Shutdown phase started",
			log.Phase(phaseName),
			log.Component("shutdown"))

		gs.mu.RLock()
		hooks := gs.hooks[phase]
		gs.mu.RUnlock()

		// 并发执行同一阶段的所有钩子
		if err := gs.executeHooks(ctx, hooks, phaseName); err != nil {
			log.Error("Shutdown phase failed",
				log.Phase(phaseName),
				log.Err(err),
				log.Component("shutdown"))
			// 继续执行后续阶段，确保资源被清理
		}
	}

	log.Info("Graceful shutdown completed",
		log.Component("shutdown"))
}

// executeHooks 执行一组钩子
func (gs *GracefulShutdown) executeHooks(ctx context.Context, hooks []ShutdownHook, phaseName string) error {
	if len(hooks) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(hooks))

	for i, hook := range hooks {
		wg.Add(1)
		go func(idx int, h ShutdownHook) {
			defer wg.Done()
			defer RecoverPanic(fmt.Sprintf("shutdown-hook-%s-%d", phaseName, idx))

			if err := h(ctx); err != nil {
				errChan <- fmt.Errorf("hook %d failed: %w", idx, err)
			}
		}(i, hook)
	}

	// 等待所有钩子完成
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		close(errChan)
		// 收集所有错误
		var errs []error
		for err := range errChan {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			return fmt.Errorf("phase %s had %d errors: %v", phaseName, len(errs), errs[0])
		}
		return nil

	case <-ctx.Done():
		return fmt.Errorf("phase %s timeout: %w", phaseName, ctx.Err())
	}
}

// phaseName 返回阶段名称
func (gs *GracefulShutdown) phaseName(phase ShutdownPhase) string {
	switch phase {
	case PhaseStopAccepting:
		return "Stop Accepting"
	case PhaseDrainConnections:
		return "Drain Connections"
	case PhasePersistState:
		return "Persist State"
	case PhaseCloseResources:
		return "Close Resources"
	default:
		return fmt.Sprintf("Unknown Phase %d", phase)
	}
}

// Done 返回关闭完成 channel
func (gs *GracefulShutdown) Done() <-chan struct{} {
	return gs.done
}

// IsShuttingDown 检查是否正在关闭
func (gs *GracefulShutdown) IsShuttingDown() bool {
	select {
	case <-gs.done:
		return true
	default:
		return false
	}
}
