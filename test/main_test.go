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

package test

import (
	"os"
	"testing"

	"go.etcd.io/etcd/client/pkg/v3/logutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TestMain is the entry point for all tests in this package
// It configures global etcd client logging to suppress lease keep-alive warnings
func TestMain(m *testing.M) {
	// Configure etcd client logger to suppress warnings
	// This prevents "error occurred during lease keep alive loop" messages
	// which are normal during test cleanup when connections are closed
	zapConfig := zap.NewProductionConfig()
	zapConfig.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	logger, err := zapConfig.Build()
	if err != nil {
		panic(err)
	}

	// Set global etcd client logger
	logutil.DefaultZapLoggerConfig = zapConfig
	_ = logger // Suppress unused variable warning

	// Run tests
	code := m.Run()

	// Cleanup and exit
	os.Exit(code)
}
