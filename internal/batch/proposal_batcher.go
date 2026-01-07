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

// ProposalBatcher dynamicsystem
// rootloaddynamiccompletesizeandtimeouttime，inlowloadandhighloadscenarioscenenextgetget
// reference TiKV、etcd optimizepolicy
type ProposalBatcher struct {
	// configargument
	minBatchSize  int           // minimumsize(lowloadscenarioscene)
	maxBatchSize  int           // maximumsize(highloadscenarioscene)
	minTimeout    time.Duration // minimumtimeouttime(lowloadscenarioscene，optimizelatency)
	maxTimeout    time.Duration // maximumtimeouttime(highloadscenarioscene，optimize)
	loadThreshold float64       // loadthreshold，for highlowload

	// status
	mu            sync.Mutex
	buffer        []string      // buffer
	currentLoad   float64       // currentload(0.0-1.0)，useexponentmoveaveragecalculate
	proposalCount int64         // 
	batchCount    int64         // times

	// channel
	proposeC chan []byte   // Raft propose channel(batcher ownandnegativeclose)
	inputC   <-chan string // inputchannel
	stopC    chan struct{} // stopped

	// dynamicargument(rootloadshould)
	currentBatchSize int           // currentsize
	currentTimeout   time.Duration // currenttimeouttime

	logger *zap.Logger
}

// BatchConfig config
type BatchConfig struct {
	MinBatchSize  int           // minimumsize(default 1)
	MaxBatchSize  int           // maximumsize(default 256)
	MinTimeout    time.Duration // minimumtimeouttime(default 5ms)
	MaxTimeout    time.Duration // maximumtimeouttime(default 20ms)
	LoadThreshold float64       // loadthreshold(default 0.7)
}

// DefaultBatchConfig returndefaultconfig
// at TiKV and etcd verifyvalue
func DefaultBatchConfig() BatchConfig {
	return BatchConfig{
		MinBatchSize:  1,            // lowload：single ，lowlatency
		MaxBatchSize:  256,          // highload：large，high(TiKV use 256)
		MinTimeout:    5 * time.Millisecond,  // lowload：5ms timeout
		MaxTimeout:    20 * time.Millisecond, // highload：20ms timeout
		LoadThreshold: 0.7,          // 70% loadthreshold
	}
}

// NewProposalBatcher create newdynamic
// batcher ownandmanagementoutputchannelperiod，callervia ProposeC() getread-onlychannel
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
		proposeC:         make(chan []byte, 256), // batcher createandownchannel
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

// ProposeC returnoutputchannel(read-only)，for receivedata
func (b *ProposalBatcher) ProposeC() <-chan []byte {
	return b.proposeC
}

// Start start
func (b *ProposalBatcher) Start(ctx context.Context) {
	go b.run(ctx)
}

// Stop stopped
func (b *ProposalBatcher) Stop() {
	close(b.stopC)
}

// run 
func (b *ProposalBatcher) run(ctx context.Context) {
	ticker := time.NewTicker(b.currentTimeout)
	defer ticker.Stop()

	// inwhenrefreshandcloseoutputchannel
	defer func() {
		b.flush()
		close(b.proposeC) // batcher ownchannel，negativeclose
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
				// inputchannelalready close，refreshafterreturn
				return
			}

			b.mu.Lock()
			b.buffer = append(b.buffer, proposal)
			bufferLen := len(b.buffer)
			b.mu.Unlock()

			// iftocurrentsize，refresh
			if bufferLen >= b.currentBatchSize {
				b.flush()
				// resetschedule
				ticker.Reset(b.currentTimeout)
			}

		case <-ticker.C:
			// timeout，refreshbuffer
			b.flush()
			// completedynamicargument
			b.adjustParameters()
			// resetscheduleasnewtimeouttime
			ticker.Reset(b.currentTimeout)
		}
	}
}

// flush refreshbuffer，willsendto Raft
func (b *ProposalBatcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}

	// copybufferandclear
	batch := make([]string, len(b.buffer))
	copy(batch, b.buffer)
	b.buffer = b.buffer[:0]

	// updatestatistics
	b.proposalCount += int64(len(batch))
	b.batchCount++
	batchCount := b.batchCount
	b.mu.Unlock()

	// encode
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

// adjustParameters dynamiccompleteargument
// useshould EMA calculateload，rootloadcompletesizeandtimeouttime
// optimize：
// 1. should alpha：loadchangetransformwhen using alpha，fastresponse
// 2. bufferthresholdfastresponse：bufferfullwhenhighloadschema
func (b *ProposalBatcher) adjustParameters() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// calculatewhenload：bufferuse
	bufferUsage := float64(len(b.buffer)) / float64(b.maxBatchSize)

	// 【optimize 1】should alpha：rootloadchangetransformdynamiccomplete
	// loadchangetransformwhen(trafficincrease/)，uselarge alpha fastresponse
	// loadwhen，usesmall alpha 
	loadDelta := math.Abs(bufferUsage - b.currentLoad)
	alpha := 0.3 // default：when alpha(weight 70%)
	if loadDelta > 0.3 {
		// loadchangetransform(changetransformed 30%)，fastresponse
		alpha = 0.7 //  alpha(currentweight 70%)，1-2  periodcanschema
	} else if loadDelta > 0.15 {
		// loadinwaitchangetransform(changetransform 15%-30%)，inresponse
		alpha = 0.5 // inwait alpha(currentandfirsthalf)
	}

	// useshould EMA updateload
	b.currentLoad = alpha*bufferUsage + (1-alpha)*b.currentLoad

	// 【optimize 2】bufferthresholdfastresponse：buffer
	// ifbufferuseed 80%，usehighloadschemaargument
	// canintrafficincreasewhensize，buffer
	effectiveLoad := b.currentLoad
	if bufferUsage > 0.8 {
		// bufferfull，mandatoryusehighloadschema
		effectiveLoad = math.Max(effectiveLoad, b.loadThreshold+0.1)
	}

	// rootvalidloadcompleteargument
	if effectiveLoad > b.loadThreshold {
		// highload：increaselargesize，longtimeouttime，optimizethroughput
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
		// lowload：decreasesmallsize，shorttimeouttime，optimizelatency
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

// interpolate valuefunction
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

// Stats returnstatisticsinfo
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

// BatchStats statisticsinfo
type BatchStats struct {
	TotalProposals   int64         // 
	TotalBatches     int64         // times
	AvgBatchSize     float64       // averagesize
	CurrentLoad      float64       // currentload
	CurrentBatchSize int           // currentsize
	CurrentTimeout   time.Duration // currenttimeouttime
	BufferLen        int           // currentbufferlength
}
