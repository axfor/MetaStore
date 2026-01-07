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

// Logger structuretransformlog
type Logger struct {
	zap    *zap.Logger
	sugar  *zap.SugaredLogger
	config *Config
}

// Config logconfig
type Config struct {
	// Level loglevel: debug, info, warn, error, dpanic, panic, fatal
	Level string

	// OutputPaths logoutputpath(supportedmany )
	// : ["stdout", "/var/log/metastore/app.log"]
	OutputPaths []string

	// ErrorOutputPaths incorrectlogoutputpath
	ErrorOutputPaths []string

	// Encoding encodeformat: json or console
	Encoding string

	// Development isnoopenschema(willfineheapstackinfo)
	Development bool

	// DisableCaller isnodisabledcallerinfo(file、row)
	DisableCaller bool

	// DisableStacktrace isnodisabledheapstacktrace
	DisableStacktrace bool

	// EnableColor isnoenabledoutput( console encode)
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

// ProductionConfig environmentconfig
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

// DevelopmentConfig openenvironmentconfig
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

// NewLogger create newlog
func NewLogger(cfg *Config) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig
	}

	// loglevel
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

	// ifis console encodeenabled
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
				continue // duplicate
			}

			writer := getWriter(path)
			var encoder zapcore.Encoder
			if cfg.Encoding == "json" {
				encoder = zapcore.NewJSONEncoder(encoderConfig)
			} else {
				encoder = zapcore.NewConsoleEncoder(encoderConfig)
			}

			// incorrectlogrecord Error andpreviouslevel
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

// InitGlobalLogger initializegloballog
func InitGlobalLogger(cfg *Config) error {
	var err error
	once.Do(func() {
		globalLogger, err = NewLogger(cfg)
	})
	return err
}

// InitFromConfig fromconfigfileinitializegloballog
// will config.LogConfig convertas log.Config andinitialize
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
		EnableColor:       cfg.Encoding == "console", // console schemaenabled
	}

	return InitGlobalLogger(logCfg)
}

// GetLogger getgloballog
func GetLogger() *Logger {
	if globalLogger == nil {
		// initializeasdefaultconfig
		_ = InitGlobalLogger(DefaultConfig)
	}
	return globalLogger
}

// ReplaceGlobalLogger replacegloballog
func ReplaceGlobalLogger(logger *Logger) {
	globalLogger = logger
}

// Sync synchronouslogbuffer
func (l *Logger) Sync() error {
	return l.zap.Sync()
}

// With addfield(returnnew logger)
func (l *Logger) With(fields ...zap.Field) *Logger {
	return &Logger{
		zap:    l.zap.With(fields...),
		sugar:  l.sugar.With(fields),
		config: l.config,
	}
}

// Named createlog
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

// DPanic levellog(openschemawill panic)
func (l *Logger) DPanic(msg string, fields ...zap.Field) {
	l.zap.DPanic(msg, fields...)
}

// Panic levellog(will panic)
func (l *Logger) Panic(msg string, fields ...zap.Field) {
	l.zap.Panic(msg, fields...)
}

// Fatal levellog(will)
func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	l.zap.Fatal(msg, fields...)
}

// Debugf formattransform Debug log
func (l *Logger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

// Infof formattransform Info log
func (l *Logger) Infof(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

// Warnf formattransform Warn log
func (l *Logger) Warnf(template string, args ...interface{}) {
	l.sugar.Warnf(template, args...)
}

// Errorf formattransform Error log
func (l *Logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

// DPanicf formattransform DPanic log
func (l *Logger) DPanicf(template string, args ...interface{}) {
	l.sugar.DPanicf(template, args...)
}

// Panicf formattransform Panic log
func (l *Logger) Panicf(template string, args ...interface{}) {
	l.sugar.Panicf(template, args...)
}

// Fatalf formattransform Fatal log
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
		// fileoutput(willcreatedirectory)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// failurewhento stdout
			return zapcore.AddSync(os.Stdout)
		}
		return zapcore.AddSync(file)
	}
}

// contains checkisnopackageelement
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// globalfunction(useglobal logger)

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

// Debugf globalformattransform Debug log
func Debugf(template string, args ...interface{}) {
	GetLogger().Debugf(template, args...)
}

// Infof globalformattransform Info log
func Infof(template string, args ...interface{}) {
	GetLogger().Infof(template, args...)
}

// Warnf globalformattransform Warn log
func Warnf(template string, args ...interface{}) {
	GetLogger().Warnf(template, args...)
}

// Errorf globalformattransform Error log
func Errorf(template string, args ...interface{}) {
	GetLogger().Errorf(template, args...)
}

// Fatalf globalformattransform Fatal log
func Fatalf(template string, args ...interface{}) {
	GetLogger().Fatalf(template, args...)
}

// Sync synchronousgloballog
func Sync() error {
	if globalLogger != nil {
		return globalLogger.Sync()
	}
	return nil
}
