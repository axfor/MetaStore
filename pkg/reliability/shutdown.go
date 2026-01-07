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

// ShutdownHook close钩子functiontype
type ShutdownHook func(ctx context.Context) error

// ShutdownPhase close阶segment
type ShutdownPhase int

const (
	// PhaseStopAccepting stopped接受newrequest
	PhaseStopAccepting ShutdownPhase = iota
	// PhaseDrainConnections 排empty现有connection
	PhaseDrainConnections
	// PhasePersistState persistent化status
	PhasePersistState
	// PhaseCloseResources closeresource
	PhaseCloseResources
)

// GracefulShutdown 优雅closemanager
type GracefulShutdown struct {
	mu           sync.RWMutex
	hooks        map[ShutdownPhase][]ShutdownHook
	timeout      time.Duration // 总timeouttime
	drainTimeout time.Duration // 排emptyconnection专用timeout
	done         chan struct{}
	signals      chan os.Signal
}

// NewGracefulShutdown create优雅closemanager
// timeout: 总closetimeout，drainTimeout: 排emptyconnectiontimeout（optional）
func NewGracefulShutdown(timeout time.Duration, drainTimeout ...time.Duration) *GracefulShutdown {
	if timeout == 0 {
		timeout = 30 * time.Second // default 30 秒总timeout
	}

	dt := 5 * time.Second // default 5 秒排emptytimeout
	if len(drainTimeout) > 0 && drainTimeout[0] > 0 {
		dt = drainTimeout[0]
	}

	gs := &GracefulShutdown{
		hooks:        make(map[ShutdownPhase][]ShutdownHook),
		timeout:      timeout,
		drainTimeout: dt,
		done:         make(chan struct{}),
		signals:      make(chan os.Signal, 1),
	}

	// register系统信号
	signal.Notify(gs.signals, syscall.SIGTERM, syscall.SIGINT)

	return gs
}

// RegisterHook registerclose钩子
func (gs *GracefulShutdown) RegisterHook(phase ShutdownPhase, hook ShutdownHook) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	gs.hooks[phase] = append(gs.hooks[phase], hook)
}

// Wait waitclose信号
func (gs *GracefulShutdown) Wait() {
	sig := <-gs.signals
	log.Info("Received shutdown signal",
		log.String("signal", sig.String()),
		log.Component("shutdown"))
	gs.Shutdown()
}

// Shutdown execute优雅close
func (gs *GracefulShutdown) Shutdown() {
	gs.mu.Lock()
	select {
	case <-gs.done:
		// alreadyinclose中
		gs.mu.Unlock()
		return
	default:
		close(gs.done)
	}
	gs.mu.Unlock()

	// create带timeout context
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

		// as排emptyconnection阶segmentuse专用timeout
		phaseCtx := ctx
		if phase == PhaseDrainConnections {
			var cancel context.CancelFunc
			phaseCtx, cancel = context.WithTimeout(context.Background(), gs.drainTimeout)
			defer cancel()
		}

		// concurrencyexecute同一阶segmentall钩子
		if err := gs.executeHooks(phaseCtx, hooks, phaseName); err != nil {
			log.Error("Shutdown phase failed",
				log.Phase(phaseName),
				log.Err(err),
				log.Component("shutdown"))
			// continueexecute后续阶segment，确保resource被clean up
		}
	}

	log.Info("Graceful shutdown completed",
		log.Component("shutdown"))
}

// executeHooks execute一group钩子
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

	// waitall钩子done
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		close(errChan)
		// 收集allincorrect
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

// phaseName return阶segment名称
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

// Done returnclosedone channel
func (gs *GracefulShutdown) Done() <-chan struct{} {
	return gs.done
}

// IsShuttingDown checkisnocurrentlyclose
func (gs *GracefulShutdown) IsShuttingDown() bool {
	select {
	case <-gs.done:
		return true
	default:
		return false
	}
}
