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
	// globalLogger 全局日志实例
	globalLogger *Logger
	once         sync.Once
)

// Logger 结构化日志器
type Logger struct {
	zap    *zap.Logger
	sugar  *zap.SugaredLogger
	config *Config
}

// Config 日志配置
type Config struct {
	// Level 日志级别: debug, info, warn, error, dpanic, panic, fatal
	Level string

	// OutputPaths 日志输出路径（支持多个）
	// 例如: ["stdout", "/var/log/metastore/app.log"]
	OutputPaths []string

	// ErrorOutputPaths 错误日志输出路径
	ErrorOutputPaths []string

	// Encoding 编码格式: json 或 console
	Encoding string

	// Development 是否开发模式（会显示更详细的堆栈信息）
	Development bool

	// DisableCaller 是否禁用调用者信息（文件名、行号）
	DisableCaller bool

	// DisableStacktrace 是否禁用堆栈跟踪
	DisableStacktrace bool

	// EnableColor 是否启用颜色输出（仅 console 编码）
	EnableColor bool
}

// DefaultConfig 默认配置
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

// ProductionConfig 生产环境配置
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

// DevelopmentConfig 开发环境配置
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

// NewLogger 创建新的日志器
func NewLogger(cfg *Config) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig
	}

	// 解析日志级别
	level := zapcore.InfoLevel
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, err
	}

	// 创建 encoder 配置
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

	// 如果是 console 编码且启用颜色
	if cfg.Encoding == "console" && cfg.EnableColor {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	// 创建 core
	var cores []zapcore.Core

	// 输出路径
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

	// 错误输出路径
	if len(cfg.ErrorOutputPaths) > 0 {
		for _, path := range cfg.ErrorOutputPaths {
			if contains(cfg.OutputPaths, path) {
				continue // 避免重复
			}

			writer := getWriter(path)
			var encoder zapcore.Encoder
			if cfg.Encoding == "json" {
				encoder = zapcore.NewJSONEncoder(encoderConfig)
			} else {
				encoder = zapcore.NewConsoleEncoder(encoderConfig)
			}

			// 错误日志只记录 Error 及以上级别
			core := zapcore.NewCore(
				encoder,
				zapcore.AddSync(writer),
				zapcore.ErrorLevel,
			)
			cores = append(cores, core)
		}
	}

	// 合并所有 core
	core := zapcore.NewTee(cores...)

	// 创建 zap logger
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

// InitGlobalLogger 初始化全局日志器
func InitGlobalLogger(cfg *Config) error {
	var err error
	once.Do(func() {
		globalLogger, err = NewLogger(cfg)
	})
	return err
}

// InitFromConfig 从配置文件初始化全局日志器
// 将 config.LogConfig 转换为 log.Config 并初始化
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
		EnableColor:       cfg.Encoding == "console", // console 模式启用颜色
	}

	return InitGlobalLogger(logCfg)
}

// GetLogger 获取全局日志器
func GetLogger() *Logger {
	if globalLogger == nil {
		// 自动初始化为默认配置
		_ = InitGlobalLogger(DefaultConfig)
	}
	return globalLogger
}

// ReplaceGlobalLogger 替换全局日志器
func ReplaceGlobalLogger(logger *Logger) {
	globalLogger = logger
}

// Sync 同步日志缓冲区
func (l *Logger) Sync() error {
	return l.zap.Sync()
}

// With 添加字段（返回新的 logger）
func (l *Logger) With(fields ...zap.Field) *Logger {
	return &Logger{
		zap:    l.zap.With(fields...),
		sugar:  l.sugar.With(fields),
		config: l.config,
	}
}

// Named 创建命名子日志器
func (l *Logger) Named(name string) *Logger {
	return &Logger{
		zap:    l.zap.Named(name),
		sugar:  l.sugar.Named(name),
		config: l.config,
	}
}

// Debug 级别日志
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	l.zap.Debug(msg, fields...)
}

// Info 级别日志
func (l *Logger) Info(msg string, fields ...zap.Field) {
	l.zap.Info(msg, fields...)
}

// Warn 级别日志
func (l *Logger) Warn(msg string, fields ...zap.Field) {
	l.zap.Warn(msg, fields...)
}

// Error 级别日志
func (l *Logger) Error(msg string, fields ...zap.Field) {
	l.zap.Error(msg, fields...)
}

// DPanic 级别日志（开发模式会 panic）
func (l *Logger) DPanic(msg string, fields ...zap.Field) {
	l.zap.DPanic(msg, fields...)
}

// Panic 级别日志（会 panic）
func (l *Logger) Panic(msg string, fields ...zap.Field) {
	l.zap.Panic(msg, fields...)
}

// Fatal 级别日志（会退出程序）
func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	l.zap.Fatal(msg, fields...)
}

// Debugf 格式化 Debug 日志
func (l *Logger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

// Infof 格式化 Info 日志
func (l *Logger) Infof(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

// Warnf 格式化 Warn 日志
func (l *Logger) Warnf(template string, args ...interface{}) {
	l.sugar.Warnf(template, args...)
}

// Errorf 格式化 Error 日志
func (l *Logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

// DPanicf 格式化 DPanic 日志
func (l *Logger) DPanicf(template string, args ...interface{}) {
	l.sugar.DPanicf(template, args...)
}

// Panicf 格式化 Panic 日志
func (l *Logger) Panicf(template string, args ...interface{}) {
	l.sugar.Panicf(template, args...)
}

// Fatalf 格式化 Fatal 日志
func (l *Logger) Fatalf(template string, args ...interface{}) {
	l.sugar.Fatalf(template, args...)
}

// getWriter 获取输出 Writer
func getWriter(path string) zapcore.WriteSyncer {
	switch path {
	case "stdout":
		return zapcore.AddSync(os.Stdout)
	case "stderr":
		return zapcore.AddSync(os.Stderr)
	default:
		// 文件输出（会自动创建目录）
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// 失败时回退到 stdout
			return zapcore.AddSync(os.Stdout)
		}
		return zapcore.AddSync(file)
	}
}

// contains 检查字符串切片是否包含元素
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// 全局便捷函数（使用全局 logger）

// Debug 全局 Debug 日志
func Debug(msg string, fields ...zap.Field) {
	GetLogger().Debug(msg, fields...)
}

// Info 全局 Info 日志
func Info(msg string, fields ...zap.Field) {
	GetLogger().Info(msg, fields...)
}

// Warn 全局 Warn 日志
func Warn(msg string, fields ...zap.Field) {
	GetLogger().Warn(msg, fields...)
}

// Error 全局 Error 日志
func Error(msg string, fields ...zap.Field) {
	GetLogger().Error(msg, fields...)
}

// Fatal 全局 Fatal 日志
func Fatal(msg string, fields ...zap.Field) {
	GetLogger().Fatal(msg, fields...)
}

// Debugf 全局格式化 Debug 日志
func Debugf(template string, args ...interface{}) {
	GetLogger().Debugf(template, args...)
}

// Infof 全局格式化 Info 日志
func Infof(template string, args ...interface{}) {
	GetLogger().Infof(template, args...)
}

// Warnf 全局格式化 Warn 日志
func Warnf(template string, args ...interface{}) {
	GetLogger().Warnf(template, args...)
}

// Errorf 全局格式化 Error 日志
func Errorf(template string, args ...interface{}) {
	GetLogger().Errorf(template, args...)
}

// Fatalf 全局格式化 Fatal 日志
func Fatalf(template string, args ...interface{}) {
	GetLogger().Fatalf(template, args...)
}

// Sync 同步全局日志器
func Sync() error {
	if globalLogger != nil {
		return globalLogger.Sync()
	}
	return nil
}
