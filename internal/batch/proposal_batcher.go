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

// ProposalBatcher dynamic批量提案系统
// root据loaddynamic调整批量sizeandtimeouttime，inlowloadandhighload场景下取得平衡
// reference TiKV、etcd 批量optimizepolicy
type ProposalBatcher struct {
	// configargument
	minBatchSize  int           // minimum批量size（lowload场景）
	maxBatchSize  int           // maximum批量size（highload场景）
	minTimeout    time.Duration // minimumtimeouttime（lowload场景，optimizelatency）
	maxTimeout    time.Duration // maximumtimeouttime（highload场景，optimize吞吐）
	loadThreshold float64       // loadthreshold，用at判断highlowload切换

	// status
	mu            sync.Mutex
	buffer        []string      // buffer
	currentLoad   float64       // currentload（0.0-1.0），useexponentmoveaveragecalculate
	proposalCount int64         // 总提案数
	batchCount    int64         // 总批times

	// channel
	proposeC chan []byte   // Raft propose channel（batcher 拥有并负责close）
	inputC   <-chan string // input提案channel
	stopC    chan struct{} // stopped信号

	// dynamicargument（root据load自适应）
	currentBatchSize int           // current批量size
	currentTimeout   time.Duration // currenttimeouttime

	logger *zap.Logger
}

// BatchConfig 批量提案config
type BatchConfig struct {
	MinBatchSize  int           // minimum批量size（default 1）
	MaxBatchSize  int           // maximum批量size（default 256）
	MinTimeout    time.Duration // minimumtimeouttime（default 5ms）
	MaxTimeout    time.Duration // maximumtimeouttime（default 20ms）
	LoadThreshold float64       // loadthreshold（default 0.7）
}

// DefaultBatchConfig returndefault批量config
// 基at TiKV and etcd 经验value
func DefaultBatchConfig() BatchConfig {
	return BatchConfig{
		MinBatchSize:  1,            // lowload：单个提案，最lowlatency
		MaxBatchSize:  256,          // highload：大批量，最high吞吐（TiKV use 256）
		MinTimeout:    5 * time.Millisecond,  // lowload：5ms timeout
		MaxTimeout:    20 * time.Millisecond, // highload：20ms timeout
		LoadThreshold: 0.7,          // 70% loadthreshold
	}
}

// NewProposalBatcher createnewdynamic批量提案器
// batcher 拥有并managementoutputchannel生命period，call者via ProposeC() getread-onlychannel
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
		proposeC:         make(chan []byte, 256), // batcher create并拥有此channel
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

// ProposeC returnoutputchannel（read-only），用atreceive批量提案data
func (b *ProposalBatcher) ProposeC() <-chan []byte {
	return b.proposeC
}

// Start start批量提案器
func (b *ProposalBatcher) Start(ctx context.Context) {
	go b.run(ctx)
}

// Stop stopped批量提案器
func (b *ProposalBatcher) Stop() {
	close(b.stopC)
}

// run 批量提案器主循环
func (b *ProposalBatcher) run(ctx context.Context) {
	ticker := time.NewTicker(b.currentTimeout)
	defer ticker.Stop()

	// 确保in退出时refresh剩余提案并closeoutputchannel
	defer func() {
		b.flush()
		close(b.proposeC) // batcher 拥有此channel，负责close
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
				// inputchannel已close，refresh剩余提案后return
				return
			}

			b.mu.Lock()
			b.buffer = append(b.buffer, proposal)
			bufferLen := len(b.buffer)
			b.mu.Unlock()

			// if达tocurrent批量size，立即refresh
			if bufferLen >= b.currentBatchSize {
				b.flush()
				// resetschedule器
				ticker.Reset(b.currentTimeout)
			}

		case <-ticker.C:
			// timeout，refreshbuffer
			b.flush()
			// 调整dynamicargument
			b.adjustParameters()
			// resetschedule器asnewtimeouttime
			ticker.Reset(b.currentTimeout)
		}
	}
}

// flush refreshbuffer，will批量提案sendto Raft
func (b *ProposalBatcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}

	// copybuffer并clear
	batch := make([]string, len(b.buffer))
	copy(batch, b.buffer)
	b.buffer = b.buffer[:0]

	// updatestatistics
	b.proposalCount += int64(len(batch))
	b.batchCount++
	batchCount := b.batchCount
	b.mu.Unlock()

	// encode批量提案
	batchData, err := EncodeBatch(batch)
	if err != nil {
		b.logger.Error("failed to encode batch proposals",
			zap.Error(err),
			zap.Int("batch_size", len(batch)))
		return
	}

	// sendto Raft
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

// adjustParameters dynamic调整批量argument
// use自适应 EMA calculateload，root据load调整批量sizeandtimeouttime
// optimize点：
// 1. 自适应 alpha：load剧烈变化时use更激进 alpha，fastresponse
// 2. bufferthresholdfastresponse：buffer接近full时立即切换highloadschema
func (b *ProposalBatcher) adjustParameters() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// calculate瞬时load：bufferuse率
	bufferUsage := float64(len(b.buffer)) / float64(b.maxBatchSize)

	// 【optimize 1】自适应 alpha：root据load变化幅度dynamic调整
	// load剧烈变化时（traffic突增/突降），use更大 alpha fastresponse
	// load平稳时，use较小 alpha 平滑波动
	loadDelta := math.Abs(bufferUsage - b.currentLoad)
	alpha := 0.3 // default：平稳时 alpha（历史weight 70%）
	if loadDelta > 0.3 {
		// load剧烈变化（变化超过 30%），fastresponse
		alpha = 0.7 // 激进 alpha（currentweight 70%），1-2 个period即可切换schema
	} else if loadDelta > 0.15 {
		// load中等变化（变化 15%-30%），适中response
		alpha = 0.5 // 中等 alpha（currentand历史各占一半）
	}

	// use自适应 EMA updateload
	b.currentLoad = alpha*bufferUsage + (1-alpha)*b.currentLoad

	// 【optimize 2】bufferthresholdfastresponse：避免buffer溢出
	// ifbufferuse率超过 80%，立即usehighloadschemaargument
	// 这样canintraffic激增时立即提升批量size，避免buffer溢出
	effectiveLoad := b.currentLoad
	if bufferUsage > 0.8 {
		// buffer接近full，mandatoryusehighloadschema
		effectiveLoad = math.Max(effectiveLoad, b.loadThreshold+0.1)
	}

	// root据validload调整argument
	if effectiveLoad > b.loadThreshold {
		// highload：增大批量size，延长timeouttime，optimizethroughput
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
		// lowload：减小批量size，缩短timeouttime，optimizelatency
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

// interpolate 线性插valuefunction
// will value from [min, max] rangemapto [targetMin, targetMax] range
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

// Stats return批量提案器statisticsinfo
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

// BatchStats 批量提案器statisticsinfo
type BatchStats struct {
	TotalProposals   int64         // 总提案数
	TotalBatches     int64         // 总批times
	AvgBatchSize     float64       // average批量size
	CurrentLoad      float64       // currentload
	CurrentBatchSize int           // current批量size
	CurrentTimeout   time.Duration // currenttimeouttime
	BufferLen        int           // currentbufferlength
}
