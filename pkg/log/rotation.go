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
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RotationConfig 日志轮转配置
type RotationConfig struct {
	// Filename 日志文件路径
	Filename string

	// MaxSize 单个日志文件最大大小（MB）
	MaxSize int

	// MaxAge 日志文件最大保留天数
	MaxAge int

	// MaxBackups 最大备份文件数量
	MaxBackups int

	// Compress 是否压缩旧日志
	Compress bool

	// LocalTime 是否使用本地时间（默认 UTC）
	LocalTime bool
}

// RotatingFileWriter 支持轮转的文件写入器
type RotatingFileWriter struct {
	mu     sync.Mutex
	config RotationConfig

	file    *os.File
	size    int64
	lastDay int
}

// NewRotatingFileWriter 创建轮转文件写入器
func NewRotatingFileWriter(config RotationConfig) (*RotatingFileWriter, error) {
	if config.MaxSize == 0 {
		config.MaxSize = 100 // 默认 100 MB
	}
	if config.MaxAge == 0 {
		config.MaxAge = 7 // 默认保留 7 天
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = 10 // 默认保留 10 个备份
	}

	w := &RotatingFileWriter{
		config: config,
	}

	// 打开日志文件
	if err := w.openFile(); err != nil {
		return nil, err
	}

	// 启动定期清理
	go w.cleanupRoutine()

	return w, nil
}

// Write 实现 io.Writer
func (w *RotatingFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 检查是否需要轮转
	if w.shouldRotate(len(p)) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Sync 实现 zapcore.WriteSyncer
func (w *RotatingFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Sync()
	}
	return nil
}

// Close 关闭文件
func (w *RotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// openFile 打开日志文件
func (w *RotatingFileWriter) openFile() error {
	// 创建目录
	dir := filepath.Dir(w.config.Filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// 打开文件
	file, err := os.OpenFile(w.config.Filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// 获取当前文件大小
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}

	w.file = file
	w.size = info.Size()
	w.lastDay = time.Now().Day()

	return nil
}

// shouldRotate 检查是否需要轮转
func (w *RotatingFileWriter) shouldRotate(writeLen int) bool {
	// 检查文件大小
	if w.size+int64(writeLen) >= int64(w.config.MaxSize)*1024*1024 {
		return true
	}

	// 检查日期变化（每天轮转）
	currentDay := time.Now().Day()
	if currentDay != w.lastDay {
		return true
	}

	return false
}

// rotate 执行日志轮转
func (w *RotatingFileWriter) rotate() error {
	// 关闭当前文件
	if w.file != nil {
		w.file.Close()
	}

	// 重命名当前文件
	timestamp := time.Now().Format("2006-01-02-15-04-05")
	backupName := w.config.Filename + "." + timestamp

	if err := os.Rename(w.config.Filename, backupName); err != nil {
		// 如果重命名失败，直接打开新文件
		return w.openFile()
	}

	// 如果启用压缩，压缩旧文件（后台执行）
	if w.config.Compress {
		go compressFile(backupName)
	}

	// 打开新文件
	return w.openFile()
}

// cleanupRoutine 定期清理旧日志
func (w *RotatingFileWriter) cleanupRoutine() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		w.cleanup()
	}
}

// cleanup 清理过期日志
func (w *RotatingFileWriter) cleanup() {
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := filepath.Dir(w.config.Filename)
	base := filepath.Base(w.config.Filename)

	files, err := filepath.Glob(filepath.Join(dir, base+".*"))
	if err != nil {
		return
	}

	// 按修改时间排序，删除最旧的文件
	cutoff := time.Now().AddDate(0, 0, -w.config.MaxAge)

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// 检查文件年龄
		if info.ModTime().Before(cutoff) {
			os.Remove(file)
			continue
		}
	}

	// 检查备份数量
	if len(files) > w.config.MaxBackups {
		// 删除最旧的文件
		for i := 0; i < len(files)-w.config.MaxBackups; i++ {
			os.Remove(files[i])
		}
	}
}

// compressFile 压缩文件（简化版本，仅重命名）
func compressFile(filename string) {
	// 实际生产环境可以使用 gzip 压缩
	// 这里为了简化，只是添加 .gz 后缀
	newName := filename + ".gz"
	os.Rename(filename, newName)
}

// NewRotatingLogger 创建带日志轮转的 Logger
func NewRotatingLogger(cfg *Config, rotationCfg RotationConfig) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig
	}

	// 创建轮转写入器
	writer, err := NewRotatingFileWriter(rotationCfg)
	if err != nil {
		return nil, err
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

	// 创建 encoder
	var encoder zapcore.Encoder
	if cfg.Encoding == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// 创建 core（使用轮转写入器）
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(writer),
		level,
	)

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
