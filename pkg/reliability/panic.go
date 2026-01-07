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
	"fmt"
	"metaStore/pkg/log"
	"runtime/debug"
	"sync/atomic"
)

var (
	// PanicCounter global panic count
	PanicCounter int64
	// PanicHandler global panic handle
	PanicHandler func(goroutineName string, panicValue interface{}, stack []byte)
)

// RecoverPanic recovery panic generalfunction
// shouldinall goroutine openuse defer RecoverPanic("goroutine-name")
func RecoverPanic(goroutineName string) {
	if r := recover(); r != nil {
		atomic.AddInt64(&PanicCounter, 1)

		stack := debug.Stack()

		// record panic info
		log.Error("Panic recovered",
			log.Goroutine(goroutineName),
			log.String("panic_value", fmt.Sprintf("%v", r)),
			log.String("stack", string(stack)),
			log.Component("panic-recovery"))

		// callcustomhandle(ifhave)
		if PanicHandler != nil {
			PanicHandler(goroutineName, r, stack)
		}
	}
}

// SafeGo safestart goroutine，recovery panic
func SafeGo(name string, fn func()) {
	go func() {
		defer RecoverPanic(name)
		fn()
	}()
}

// SafeGoWithRestart start goroutine
// maxRestarts: maximumtimes，0 indicatesunlimited
func SafeGoWithRestart(name string, fn func(), maxRestarts int) {
	restartCount := 0

	var worker func()
	worker = func() {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt64(&PanicCounter, 1)
				stack := debug.Stack()

				log.Error("Panic recovered in auto-restart goroutine",
					log.Goroutine(name),
					log.Int("restart_count", restartCount),
					log.String("panic_value", fmt.Sprintf("%v", r)),
					log.String("stack", string(stack)),
					log.Component("panic-recovery"))

				if PanicHandler != nil {
					PanicHandler(name, r, stack)
				}

				// checkisnoshould
				restartCount++
				if maxRestarts == 0 || restartCount < maxRestarts {
					log.Info("Restarting goroutine",
						log.Goroutine(name),
						log.Int("attempt", restartCount+1),
						log.Component("panic-recovery"))
					go worker()
				} else {
					log.Warn("Goroutine reached max restarts, not restarting",
						log.Goroutine(name),
						log.Int("max_restarts", maxRestarts),
						log.Component("panic-recovery"))
				}
			}
		}()

		fn()
	}

	go worker()
}

// GetPanicCount get panic count
func GetPanicCount() int64 {
	return atomic.LoadInt64(&PanicCounter)
}

// ResetPanicCount reset panic count
func ResetPanicCount() {
	atomic.StoreInt64(&PanicCounter, 0)
}

// PanicMiddleware gRPC  panic recoveryintermediateitem
func PanicMiddleware(handler func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			atomic.AddInt64(&PanicCounter, 1)
			stack := debug.Stack()

			log.Error("Panic recovered in handler",
				log.String("panic_value", fmt.Sprintf("%v", r)),
				log.String("stack", string(stack)),
				log.Component("panic-middleware"))

			if PanicHandler != nil {
				PanicHandler("grpc-handler", r, stack)
			}

			err = fmt.Errorf("internal server error: panic recovered")
		}
	}()

	err = handler()
	return
}
