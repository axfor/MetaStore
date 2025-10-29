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
	// PanicCounter 全局 panic 计数器
	PanicCounter int64
	// PanicHandler 全局 panic 处理器
	PanicHandler func(goroutineName string, panicValue interface{}, stack []byte)
)

// RecoverPanic 恢复 panic 的通用函数
// 应在所有 goroutine 开头使用 defer RecoverPanic("goroutine-name")
func RecoverPanic(goroutineName string) {
	if r := recover(); r != nil {
		atomic.AddInt64(&PanicCounter, 1)

		stack := debug.Stack()

		// 记录 panic 信息
		log.Error("Panic recovered",
			log.Goroutine(goroutineName),
			log.String("panic_value", fmt.Sprintf("%v", r)),
			log.String("stack", string(stack)),
			log.Component("panic-recovery"))

		// 调用自定义处理器（如果有）
		if PanicHandler != nil {
			PanicHandler(goroutineName, r, stack)
		}
	}
}

// SafeGo 安全启动 goroutine，自动恢复 panic
func SafeGo(name string, fn func()) {
	go func() {
		defer RecoverPanic(name)
		fn()
	}()
}

// SafeGoWithRestart 启动带自动重启的 goroutine
// maxRestarts: 最大重启次数，0 表示无限重启
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

				// 检查是否应该重启
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

// GetPanicCount 获取 panic 计数
func GetPanicCount() int64 {
	return atomic.LoadInt64(&PanicCounter)
}

// ResetPanicCount 重置 panic 计数
func ResetPanicCount() {
	atomic.StoreInt64(&PanicCounter, 0)
}

// PanicMiddleware gRPC 拦截器的 panic 恢复中间件
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
