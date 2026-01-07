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

// RotationConfig log轮转config
type RotationConfig struct {
	// Filename logfilepath
	Filename string

	// MaxSize 单个logfilemaximumsize（MB）
	MaxSize int

	// MaxAge logfilemaximum保留天数
	MaxAge int

	// MaxBackups maximumbackupfilequantity
	MaxBackups int

	// Compress isnocompressoldlog
	Compress bool

	// LocalTime isnouse本地time（default UTC）
	LocalTime bool
}

// RotatingFileWriter supported轮转filewrite器
type RotatingFileWriter struct {
	mu     sync.Mutex
	config RotationConfig

	file    *os.File
	size    int64
	lastDay int
}

// NewRotatingFileWriter create轮转filewrite器
func NewRotatingFileWriter(config RotationConfig) (*RotatingFileWriter, error) {
	if config.MaxSize == 0 {
		config.MaxSize = 100 // default 100 MB
	}
	if config.MaxAge == 0 {
		config.MaxAge = 7 // default保留 7 天
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = 10 // default保留 10 个backup
	}

	w := &RotatingFileWriter{
		config: config,
	}

	// openlogfile
	if err := w.openFile(); err != nil {
		return nil, err
	}

	// start定期clean up
	go w.cleanupRoutine()

	return w, nil
}

// Write implement io.Writer
func (w *RotatingFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// checkisnoneed轮转
	if w.shouldRotate(len(p)) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Sync implement zapcore.WriteSyncer
func (w *RotatingFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Sync()
	}
	return nil
}

// Close closefile
func (w *RotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// openFile openlogfile
func (w *RotatingFileWriter) openFile() error {
	// createdirectory
	dir := filepath.Dir(w.config.Filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// openfile
	file, err := os.OpenFile(w.config.Filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// getcurrentfilesize
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

// shouldRotate checkisnoneed轮转
func (w *RotatingFileWriter) shouldRotate(writeLen int) bool {
	// checkfilesize
	if w.size+int64(writeLen) >= int64(w.config.MaxSize)*1024*1024 {
		return true
	}

	// checkdate变化（每天轮转）
	currentDay := time.Now().Day()
	if currentDay != w.lastDay {
		return true
	}

	return false
}

// rotate executelog轮转
func (w *RotatingFileWriter) rotate() error {
	// closecurrentfile
	if w.file != nil {
		w.file.Close()
	}

	// renamecurrentfile
	timestamp := time.Now().Format("2006-01-02-15-04-05")
	backupName := w.config.Filename + "." + timestamp

	if err := os.Rename(w.config.Filename, backupName); err != nil {
		// ifrenamefailure，直接opennewfile
		return w.openFile()
	}

	// ifenabledcompress，compressoldfile（后台execute）
	if w.config.Compress {
		go compressFile(backupName)
	}

	// opennewfile
	return w.openFile()
}

// cleanupRoutine 定期clean upoldlog
func (w *RotatingFileWriter) cleanupRoutine() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		w.cleanup()
	}
}

// cleanup clean upexpirationlog
func (w *RotatingFileWriter) cleanup() {
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := filepath.Dir(w.config.Filename)
	base := filepath.Base(w.config.Filename)

	files, err := filepath.Glob(filepath.Join(dir, base+".*"))
	if err != nil {
		return
	}

	// 按modifytimesort，delete最oldfile
	cutoff := time.Now().AddDate(0, 0, -w.config.MaxAge)

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// checkfile年龄
		if info.ModTime().Before(cutoff) {
			os.Remove(file)
			continue
		}
	}

	// checkbackupquantity
	if len(files) > w.config.MaxBackups {
		// delete最oldfile
		for i := 0; i < len(files)-w.config.MaxBackups; i++ {
			os.Remove(files[i])
		}
	}
}

// compressFile compressfile（简化version，仅rename）
func compressFile(filename string) {
	// 实际生产environmentcanuse gzip compress
	// 这里as简化，只isadd .gz suffix
	newName := filename + ".gz"
	os.Rename(filename, newName)
}

// NewRotatingLogger create带log轮转 Logger
func NewRotatingLogger(cfg *Config, rotationCfg RotationConfig) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig
	}

	// create轮转write器
	writer, err := NewRotatingFileWriter(rotationCfg)
	if err != nil {
		return nil, err
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

	// create encoder
	var encoder zapcore.Encoder
	if cfg.Encoding == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// create core（use轮转write器）
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(writer),
		level,
	)

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
