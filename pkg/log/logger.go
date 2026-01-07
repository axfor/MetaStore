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

package log

import (
	"os"
	"sync"

	"metaStore/pkg/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// globalLogger globalloginstance
	globalLogger *Logger
	once         sync.Once
)

// Logger structure化log器
type Logger struct {
	zap    *zap.Logger
	sugar  *zap.SugaredLogger
	config *Config
}

// Config logconfig
type Config struct {
	// Level loglevel: debug, info, warn, error, dpanic, panic, fatal
	Level string

	// OutputPaths logoutputpath（supported多个）
	// 例如: ["stdout", "/var/log/metastore/app.log"]
	OutputPaths []string

	// ErrorOutputPaths incorrectlogoutputpath
	ErrorOutputPaths []string

	// Encoding encodeformat: json or console
	Encoding string

	// Development isno开发schema（will显示更详fineheapstackinfo）
	Development bool

	// DisableCaller isnodisabledcall者info（file名、row号）
	DisableCaller bool

	// DisableStacktrace isnodisabledheapstacktrace
	DisableStacktrace bool

	// EnableColor isnoenabled颜色output（仅 console encode）
	EnableColor bool
}

// DefaultConfig defaultconfig
var DefaultConfig = &Config{
	Level:             "info",
	OutputPaths:       []string{"stdout"},
	ErrorOutputPaths:  []string{"stderr"},
	Encoding:          "console",
	Development:       false,
	DisableCaller:     false,
	DisableStacktrace: false,
	EnableColor:       true,
}

// ProductionConfig 生产environmentconfig
var ProductionConfig = &Config{
	Level:             "info",
	OutputPaths:       []string{"stdout", "/var/log/metastore/app.log"},
	ErrorOutputPaths:  []string{"stderr", "/var/log/metastore/error.log"},
	Encoding:          "json",
	Development:       false,
	DisableCaller:     false,
	DisableStacktrace: true,
	EnableColor:       false,
}

// DevelopmentConfig 开发environmentconfig
var DevelopmentConfig = &Config{
	Level:             "debug",
	OutputPaths:       []string{"stdout"},
	ErrorOutputPaths:  []string{"stderr"},
	Encoding:          "console",
	Development:       true,
	DisableCaller:     false,
	DisableStacktrace: false,
	EnableColor:       true,
}

// NewLogger createnewlog器
func NewLogger(cfg *Config) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig
	}

	// 解析loglevel
	level := zapcore.InfoLevel
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, err
	}

	// create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// ifis console encode且enabled颜色
	if cfg.Encoding == "console" && cfg.EnableColor {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	// create core
	var cores []zapcore.Core

	// outputpath
	for _, path := range cfg.OutputPaths {
		writer := getWriter(path)
		var encoder zapcore.Encoder
		if cfg.Encoding == "json" {
			encoder = zapcore.NewJSONEncoder(encoderConfig)
		} else {
			encoder = zapcore.NewConsoleEncoder(encoderConfig)
		}

		core := zapcore.NewCore(
			encoder,
			zapcore.AddSync(writer),
			level,
		)
		cores = append(cores, core)
	}

	// incorrectoutputpath
	if len(cfg.ErrorOutputPaths) > 0 {
		for _, path := range cfg.ErrorOutputPaths {
			if contains(cfg.OutputPaths, path) {
				continue // 避免duplicate
			}

			writer := getWriter(path)
			var encoder zapcore.Encoder
			if cfg.Encoding == "json" {
				encoder = zapcore.NewJSONEncoder(encoderConfig)
			} else {
				encoder = zapcore.NewConsoleEncoder(encoderConfig)
			}

			// incorrectlog只record Error and以上level
			core := zapcore.NewCore(
				encoder,
				zapcore.AddSync(writer),
				zapcore.ErrorLevel,
			)
			cores = append(cores, core)
		}
	}

	// mergeall core
	core := zapcore.NewTee(cores...)

	// create zap logger
	opts := []zap.Option{
		zap.AddCaller(),
	}

	if cfg.DisableCaller {
		opts = []zap.Option{}
	}

	if !cfg.DisableStacktrace {
		opts = append(opts, zap.AddStacktrace(zapcore.ErrorLevel))
	}

	if cfg.Development {
		opts = append(opts, zap.Development())
	}

	zapLogger := zap.New(core, opts...)

	return &Logger{
		zap:    zapLogger,
		sugar:  zapLogger.Sugar(),
		config: cfg,
	}, nil
}

// InitGlobalLogger initializegloballog器
func InitGlobalLogger(cfg *Config) error {
	var err error
	once.Do(func() {
		globalLogger, err = NewLogger(cfg)
	})
	return err
}

// InitFromConfig fromconfigfileinitializegloballog器
// will config.LogConfig convertas log.Config 并initialize
func InitFromConfig(cfg *config.LogConfig) error {
	if cfg == nil {
		return InitGlobalLogger(DefaultConfig)
	}

	logCfg := &Config{
		Level:             cfg.Level,
		OutputPaths:       cfg.OutputPaths,
		ErrorOutputPaths:  cfg.ErrorOutputPaths,
		Encoding:          cfg.Encoding,
		Development:       false,
		DisableCaller:     false,
		DisableStacktrace: false,
		EnableColor:       cfg.Encoding == "console", // console schemaenabled颜色
	}

	return InitGlobalLogger(logCfg)
}

// GetLogger getgloballog器
func GetLogger() *Logger {
	if globalLogger == nil {
		// 自动initializeasdefaultconfig
		_ = InitGlobalLogger(DefaultConfig)
	}
	return globalLogger
}

// ReplaceGlobalLogger replacegloballog器
func ReplaceGlobalLogger(logger *Logger) {
	globalLogger = logger
}

// Sync synchronouslogbuffer
func (l *Logger) Sync() error {
	return l.zap.Sync()
}

// With addfield（returnnew logger）
func (l *Logger) With(fields ...zap.Field) *Logger {
	return &Logger{
		zap:    l.zap.With(fields...),
		sugar:  l.sugar.With(fields),
		config: l.config,
	}
}

// Named create命名子log器
func (l *Logger) Named(name string) *Logger {
	return &Logger{
		zap:    l.zap.Named(name),
		sugar:  l.sugar.Named(name),
		config: l.config,
	}
}

// Debug levellog
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	l.zap.Debug(msg, fields...)
}

// Info levellog
func (l *Logger) Info(msg string, fields ...zap.Field) {
	l.zap.Info(msg, fields...)
}

// Warn levellog
func (l *Logger) Warn(msg string, fields ...zap.Field) {
	l.zap.Warn(msg, fields...)
}

// Error levellog
func (l *Logger) Error(msg string, fields ...zap.Field) {
	l.zap.Error(msg, fields...)
}

// DPanic levellog（开发schemawill panic）
func (l *Logger) DPanic(msg string, fields ...zap.Field) {
	l.zap.DPanic(msg, fields...)
}

// Panic levellog（will panic）
func (l *Logger) Panic(msg string, fields ...zap.Field) {
	l.zap.Panic(msg, fields...)
}

// Fatal levellog（will退出程序）
func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	l.zap.Fatal(msg, fields...)
}

// Debugf format化 Debug log
func (l *Logger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

// Infof format化 Info log
func (l *Logger) Infof(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

// Warnf format化 Warn log
func (l *Logger) Warnf(template string, args ...interface{}) {
	l.sugar.Warnf(template, args...)
}

// Errorf format化 Error log
func (l *Logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

// DPanicf format化 DPanic log
func (l *Logger) DPanicf(template string, args ...interface{}) {
	l.sugar.DPanicf(template, args...)
}

// Panicf format化 Panic log
func (l *Logger) Panicf(template string, args ...interface{}) {
	l.sugar.Panicf(template, args...)
}

// Fatalf format化 Fatal log
func (l *Logger) Fatalf(template string, args ...interface{}) {
	l.sugar.Fatalf(template, args...)
}

// getWriter getoutput Writer
func getWriter(path string) zapcore.WriteSyncer {
	switch path {
	case "stdout":
		return zapcore.AddSync(os.Stdout)
	case "stderr":
		return zapcore.AddSync(os.Stderr)
	default:
		// fileoutput（will自动createdirectory）
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// failure时回退to stdout
			return zapcore.AddSync(os.Stdout)
		}
		return zapcore.AddSync(file)
	}
}

// contains check字符串切片isnopackage含element
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// global便捷function（useglobal logger）

// Debug global Debug log
func Debug(msg string, fields ...zap.Field) {
	GetLogger().Debug(msg, fields...)
}

// Info global Info log
func Info(msg string, fields ...zap.Field) {
	GetLogger().Info(msg, fields...)
}

// Warn global Warn log
func Warn(msg string, fields ...zap.Field) {
	GetLogger().Warn(msg, fields...)
}

// Error global Error log
func Error(msg string, fields ...zap.Field) {
	GetLogger().Error(msg, fields...)
}

// Fatal global Fatal log
func Fatal(msg string, fields ...zap.Field) {
	GetLogger().Fatal(msg, fields...)
}

// Debugf globalformat化 Debug log
func Debugf(template string, args ...interface{}) {
	GetLogger().Debugf(template, args...)
}

// Infof globalformat化 Info log
func Infof(template string, args ...interface{}) {
	GetLogger().Infof(template, args...)
}

// Warnf globalformat化 Warn log
func Warnf(template string, args ...interface{}) {
	GetLogger().Warnf(template, args...)
}

// Errorf globalformat化 Error log
func Errorf(template string, args ...interface{}) {
	GetLogger().Errorf(template, args...)
}

// Fatalf globalformat化 Fatal log
func Fatalf(template string, args ...interface{}) {
	GetLogger().Fatalf(template, args...)
}

// Sync synchronousgloballog器
func Sync() error {
	if globalLogger != nil {
		return globalLogger.Sync()
	}
	return nil
}
