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

package raft

import (
	"metaStore/pkg/log"

	"go.uber.org/zap"
)

// raftLoggerAdapter bridges etcd/raft logging to the project logger.
// It uses formatted messages because raft logger interface is printf-style.
type raftLoggerAdapter struct {
	logger *zap.SugaredLogger
}

func newRaftLogger(component string) *raftLoggerAdapter {
	return &raftLoggerAdapter{
		logger: log.ZapLogger().Sugar().With("component", component),
	}
}

func (l *raftLoggerAdapter) Debug(args ...interface{}) {
	l.logger.Debug(args...)
}

func (l *raftLoggerAdapter) Debugf(format string, args ...interface{}) {
	l.logger.Debugf(format, args...)
}

func (l *raftLoggerAdapter) Error(args ...interface{}) {
	l.logger.Error(args...)
}

func (l *raftLoggerAdapter) Errorf(format string, args ...interface{}) {
	l.logger.Errorf(format, args...)
}

func (l *raftLoggerAdapter) Info(args ...interface{}) {
	l.logger.Info(args...)
}

func (l *raftLoggerAdapter) Infof(format string, args ...interface{}) {
	l.logger.Infof(format, args...)
}

func (l *raftLoggerAdapter) Warning(args ...interface{}) {
	l.logger.Warn(args...)
}

func (l *raftLoggerAdapter) Warningf(format string, args ...interface{}) {
	l.logger.Warnf(format, args...)
}

func (l *raftLoggerAdapter) Fatal(args ...interface{}) {
	l.logger.Fatal(args...)
}

func (l *raftLoggerAdapter) Fatalf(format string, args ...interface{}) {
	l.logger.Fatalf(format, args...)
}

func (l *raftLoggerAdapter) Panic(args ...interface{}) {
	l.logger.Panic(args...)
}

func (l *raftLoggerAdapter) Panicf(format string, args ...interface{}) {
	l.logger.Panicf(format, args...)
}
