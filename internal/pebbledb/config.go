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

package pebbledb

import (
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// OptimizationConfig holds configuration for Pebble performance optimizations
type OptimizationConfig struct {
	// Tier 6A: WAL Optimization
	WAL WALConfig

	// Tier 6B: Block Cache
	BlockCache BlockCacheConfig

	// Performance tuning (configurable via config.yaml)
	MaxBackgroundJobs    int    // Default 2 (lightweight)
	WriteBufferSize      uint64 // Default 4MB (lightweight)
	MaxWriteBufferNumber int    // Default 2 (lightweight)
	TargetFileSizeBase   uint64 // Default 16MB (lightweight)

	// Compression: "none", "snappy", "lz4", "zstd" (default: "snappy")
	Compression string
}

// WALConfig configures Write-Ahead Log behavior
type WALConfig struct {
	// Sync controls whether to fsync after every write
	// false = async WAL writes (higher throughput, Raft provides durability)
	// true = sync WAL writes (lower throughput, extra durability)
	Sync bool
}

// BlockCacheConfig configures the block cache
type BlockCacheConfig struct {
	// Size is the cache size in bytes
	Size uint64
}

// DefaultOptimizationConfig returns lightweight optimization settings
func DefaultOptimizationConfig() OptimizationConfig {
	return OptimizationConfig{
		WAL: WALConfig{
			Sync: false, // Async writes (Raft provides durability)
		},
		BlockCache: BlockCacheConfig{
			Size: 8 * 1024 * 1024, // 8MB cache
		},
		MaxBackgroundJobs:    2,
		WriteBufferSize:      4 * 1024 * 1024,  // 4MB
		MaxWriteBufferNumber: 2,
		TargetFileSizeBase:   16 * 1024 * 1024,  // 16MB SST files
		Compression:          "snappy",
	}
}

// WriteOptions returns the appropriate pebble.WriteOptions based on config
func (c *OptimizationConfig) WriteOptions() *pebble.WriteOptions {
	if c.WAL.Sync {
		return pebble.Sync
	}
	return pebble.NoSync
}

// NewPebbleOptions creates Pebble options with optimizations applied
func (c *OptimizationConfig) NewPebbleOptions() *pebble.Options {
	opts := &pebble.Options{}

	if c.WriteBufferSize > 0 {
		opts.MemTableSize = c.WriteBufferSize
	}
	if c.MaxWriteBufferNumber > 0 {
		opts.MemTableStopWritesThreshold = c.MaxWriteBufferNumber + 1
	}
	if c.MaxBackgroundJobs > 0 {
		opts.MaxConcurrentCompactions = func() int { return c.MaxBackgroundJobs }
	}

	// Block cache
	if c.BlockCache.Size > 0 {
		opts.Cache = pebble.NewCache(int64(c.BlockCache.Size))
	}

	// Configure levels with compression and bloom filter
	compression := parseCompression(c.Compression)
	opts.Levels = make([]pebble.LevelOptions, 7)
	for i := range opts.Levels {
		opts.Levels[i].BlockSize = 16 * 1024 // 16KB blocks
		opts.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		if i < 2 {
			opts.Levels[i].Compression = pebble.SnappyCompression
		} else {
			opts.Levels[i].Compression = compression
		}
		if c.TargetFileSizeBase > 0 {
			opts.Levels[i].TargetFileSize = int64(c.TargetFileSizeBase)
		}
	}

	return opts
}

// parseCompression converts compression string to pebble.Compression
func parseCompression(compression string) pebble.Compression {
	switch compression {
	case "none":
		return pebble.NoCompression
	case "snappy":
		return pebble.SnappyCompression
	case "zstd":
		return pebble.ZstdCompression
	default:
		return pebble.SnappyCompression
	}
}
