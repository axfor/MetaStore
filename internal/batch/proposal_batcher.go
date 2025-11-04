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

package batch

import (
	"context"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProposalBatcher 动态批量提案系统
// 根据负载动态调整批量大小和超时时间，在低负载和高负载场景下取得平衡
// 参考 TiKV、etcd 的批量优化策略
type ProposalBatcher struct {
	// 配置参数
	minBatchSize  int           // 最小批量大小（低负载场景）
	maxBatchSize  int           // 最大批量大小（高负载场景）
	minTimeout    time.Duration // 最小超时时间（低负载场景，优化延迟）
	maxTimeout    time.Duration // 最大超时时间（高负载场景，优化吞吐）
	loadThreshold float64       // 负载阈值，用于判断高低负载切换

	// 状态
	mu            sync.Mutex
	buffer        []string      // 缓冲区
	currentLoad   float64       // 当前负载（0.0-1.0），使用指数移动平均计算
	proposalCount int64         // 总提案数
	batchCount    int64         // 总批次数

	// 通道
	proposeC chan []byte   // Raft propose 通道（batcher 拥有并负责关闭）
	inputC   <-chan string // 输入提案通道
	stopC    chan struct{} // 停止信号

	// 动态参数（根据负载自适应）
	currentBatchSize int           // 当前批量大小
	currentTimeout   time.Duration // 当前超时时间

	logger *zap.Logger
}

// BatchConfig 批量提案配置
type BatchConfig struct {
	MinBatchSize  int           // 最小批量大小（默认 1）
	MaxBatchSize  int           // 最大批量大小（默认 256）
	MinTimeout    time.Duration // 最小超时时间（默认 5ms）
	MaxTimeout    time.Duration // 最大超时时间（默认 20ms）
	LoadThreshold float64       // 负载阈值（默认 0.7）
}

// DefaultBatchConfig 返回默认批量配置
// 基于 TiKV 和 etcd 的经验值
func DefaultBatchConfig() BatchConfig {
	return BatchConfig{
		MinBatchSize:  1,            // 低负载：单个提案，最低延迟
		MaxBatchSize:  256,          // 高负载：大批量，最高吞吐（TiKV 使用 256）
		MinTimeout:    5 * time.Millisecond,  // 低负载：5ms 超时
		MaxTimeout:    20 * time.Millisecond, // 高负载：20ms 超时
		LoadThreshold: 0.7,          // 70% 负载阈值
	}
}

// NewProposalBatcher 创建新的动态批量提案器
// batcher 拥有并管理输出通道的生命周期，调用者通过 ProposeC() 获取只读通道
func NewProposalBatcher(
	config BatchConfig,
	inputC <-chan string,
	logger *zap.Logger,
) *ProposalBatcher {
	if logger == nil {
		logger = zap.NewNop()
	}

	batcher := &ProposalBatcher{
		minBatchSize:     config.MinBatchSize,
		maxBatchSize:     config.MaxBatchSize,
		minTimeout:       config.MinTimeout,
		maxTimeout:       config.MaxTimeout,
		loadThreshold:    config.LoadThreshold,
		proposeC:         make(chan []byte, 256), // batcher 创建并拥有此通道
		inputC:           inputC,
		stopC:            make(chan struct{}),
		buffer:           make([]string, 0, config.MaxBatchSize),
		currentLoad:      0.0,
		currentBatchSize: config.MinBatchSize,
		currentTimeout:   config.MinTimeout,
		logger:           logger,
	}

	return batcher
}

// ProposeC 返回输出通道（只读），用于接收批量提案数据
func (b *ProposalBatcher) ProposeC() <-chan []byte {
	return b.proposeC
}

// Start 启动批量提案器
func (b *ProposalBatcher) Start(ctx context.Context) {
	go b.run(ctx)
}

// Stop 停止批量提案器
func (b *ProposalBatcher) Stop() {
	close(b.stopC)
}

// run 批量提案器主循环
func (b *ProposalBatcher) run(ctx context.Context) {
	ticker := time.NewTicker(b.currentTimeout)
	defer ticker.Stop()

	// 确保在退出时刷新剩余提案并关闭输出通道
	defer func() {
		b.flush()
		close(b.proposeC) // batcher 拥有此通道，负责关闭
	}()

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("proposal batcher stopped due to context cancellation")
			return
		case <-b.stopC:
			b.logger.Info("proposal batcher stopped")
			return

		case proposal, ok := <-b.inputC:
			if !ok {
				// 输入通道已关闭，刷新剩余的提案后返回
				return
			}

			b.mu.Lock()
			b.buffer = append(b.buffer, proposal)
			bufferLen := len(b.buffer)
			b.mu.Unlock()

			// 如果达到当前批量大小，立即刷新
			if bufferLen >= b.currentBatchSize {
				b.flush()
				// 重置定时器
				ticker.Reset(b.currentTimeout)
			}

		case <-ticker.C:
			// 超时，刷新缓冲区
			b.flush()
			// 调整动态参数
			b.adjustParameters()
			// 重置定时器为新的超时时间
			ticker.Reset(b.currentTimeout)
		}
	}
}

// flush 刷新缓冲区，将批量提案发送到 Raft
func (b *ProposalBatcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}

	// 复制缓冲区并清空
	batch := make([]string, len(b.buffer))
	copy(batch, b.buffer)
	b.buffer = b.buffer[:0]

	// 更新统计
	b.proposalCount += int64(len(batch))
	b.batchCount++
	batchCount := b.batchCount
	b.mu.Unlock()

	// 编码批量提案
	batchData, err := EncodeBatch(batch)
	if err != nil {
		b.logger.Error("failed to encode batch proposals",
			zap.Error(err),
			zap.Int("batch_size", len(batch)))
		return
	}

	// 发送到 Raft
	select {
	case b.proposeC <- batchData:
		b.logger.Debug("batch proposal sent",
			zap.Int("batch_size", len(batch)),
			zap.Int64("batch_count", batchCount),
			zap.Float64("current_load", b.currentLoad),
			zap.Int("current_batch_size", b.currentBatchSize),
			zap.Duration("current_timeout", b.currentTimeout))
	case <-b.stopC:
		return
	}
}

// adjustParameters 动态调整批量参数
// 使用自适应 EMA 计算负载，根据负载调整批量大小和超时时间
// 优化点：
// 1. 自适应 alpha：负载剧烈变化时使用更激进的 alpha，快速响应
// 2. 缓冲区阈值快速响应：缓冲区接近满时立即切换高负载模式
func (b *ProposalBatcher) adjustParameters() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 计算瞬时负载：缓冲区使用率
	bufferUsage := float64(len(b.buffer)) / float64(b.maxBatchSize)

	// 【优化 1】自适应 alpha：根据负载变化幅度动态调整
	// 负载剧烈变化时（流量突增/突降），使用更大的 alpha 快速响应
	// 负载平稳时，使用较小的 alpha 平滑波动
	loadDelta := math.Abs(bufferUsage - b.currentLoad)
	alpha := 0.3 // 默认：平稳时的 alpha（历史权重 70%）
	if loadDelta > 0.3 {
		// 负载剧烈变化（变化超过 30%），快速响应
		alpha = 0.7 // 激进 alpha（当前权重 70%），1-2 个周期即可切换模式
	} else if loadDelta > 0.15 {
		// 负载中等变化（变化 15%-30%），适中响应
		alpha = 0.5 // 中等 alpha（当前和历史各占一半）
	}

	// 使用自适应 EMA 更新负载
	b.currentLoad = alpha*bufferUsage + (1-alpha)*b.currentLoad

	// 【优化 2】缓冲区阈值快速响应：避免缓冲区溢出
	// 如果缓冲区使用率超过 80%，立即使用高负载模式参数
	// 这样可以在流量激增时立即提升批量大小，避免缓冲区溢出
	effectiveLoad := b.currentLoad
	if bufferUsage > 0.8 {
		// 缓冲区接近满，强制使用高负载模式
		effectiveLoad = math.Max(effectiveLoad, b.loadThreshold+0.1)
	}

	// 根据有效负载调整参数
	if effectiveLoad > b.loadThreshold {
		// 高负载：增大批量大小，延长超时时间，优化吞吐量
		b.currentBatchSize = interpolate(
			b.currentLoad,
			b.loadThreshold, 1.0,
			float64(b.maxBatchSize)/2, float64(b.maxBatchSize),
		)
		b.currentTimeout = time.Duration(interpolate(
			b.currentLoad,
			b.loadThreshold, 1.0,
			float64(b.maxTimeout)/2, float64(b.maxTimeout),
		))
	} else {
		// 低负载：减小批量大小，缩短超时时间，优化延迟
		b.currentBatchSize = interpolate(
			b.currentLoad,
			0.0, b.loadThreshold,
			float64(b.minBatchSize), float64(b.maxBatchSize)/2,
		)
		b.currentTimeout = time.Duration(interpolate(
			b.currentLoad,
			0.0, b.loadThreshold,
			float64(b.minTimeout), float64(b.maxTimeout)/2,
		))
	}

	b.logger.Debug("adjusted batch parameters",
		zap.Float64("buffer_usage", bufferUsage),
		zap.Float64("alpha", alpha),
		zap.Float64("current_load", b.currentLoad),
		zap.Float64("effective_load", effectiveLoad),
		zap.Int("current_batch_size", b.currentBatchSize),
		zap.Duration("current_timeout", b.currentTimeout),
		zap.Int("buffer_len", len(b.buffer)))
}

// interpolate 线性插值函数
// 将 value 从 [min, max] 范围映射到 [targetMin, targetMax] 范围
func interpolate(value, min, max, targetMin, targetMax float64) int {
	if value <= min {
		return int(targetMin)
	}
	if value >= max {
		return int(targetMax)
	}
	ratio := (value - min) / (max - min)
	return int(targetMin + ratio*(targetMax-targetMin))
}

// Stats 返回批量提案器统计信息
func (b *ProposalBatcher) Stats() BatchStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	var avgBatchSize float64
	if b.batchCount > 0 {
		avgBatchSize = float64(b.proposalCount) / float64(b.batchCount)
	}

	return BatchStats{
		TotalProposals:   b.proposalCount,
		TotalBatches:     b.batchCount,
		AvgBatchSize:     avgBatchSize,
		CurrentLoad:      b.currentLoad,
		CurrentBatchSize: b.currentBatchSize,
		CurrentTimeout:   b.currentTimeout,
		BufferLen:        len(b.buffer),
	}
}

// BatchStats 批量提案器统计信息
type BatchStats struct {
	TotalProposals   int64         // 总提案数
	TotalBatches     int64         // 总批次数
	AvgBatchSize     float64       // 平均批量大小
	CurrentLoad      float64       // 当前负载
	CurrentBatchSize int           // 当前批量大小
	CurrentTimeout   time.Duration // 当前超时时间
	BufferLen        int           // 当前缓冲区长度
}
