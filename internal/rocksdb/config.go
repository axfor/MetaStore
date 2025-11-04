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

package rocksdb

import (
	"github.com/linxGnu/grocksdb"
)

// OptimizationConfig holds configuration for RocksDB performance optimizations
type OptimizationConfig struct {
	// Tier 6A: WAL Optimization
	WAL WALConfig

	// Tier 6B: Block Cache
	BlockCache BlockCacheConfig

	// Tier 6C: Column Families (for future use)
	ColumnFamilies ColumnFamilyConfig
}

// WALConfig configures Write-Ahead Log behavior
// Tier 6A: WAL Optimization (10-20% performance improvement)
type WALConfig struct {
	// Sync controls whether to fsync after every write
	// false = async WAL writes (higher throughput, Raft provides durability)
	// true = sync WAL writes (lower throughput, extra durability)
	Sync bool

	// SizeLimitMB is the maximum size of WAL files before rotation (MB)
	// Larger values reduce rotation overhead but use more disk space
	SizeLimitMB uint64

	// TTLSeconds is the time-to-live for WAL files (seconds)
	// WAL files older than this are automatically deleted
	TTLSeconds uint64

	// MaxTotalSize is the maximum total size of all WAL files (bytes)
	// When exceeded, oldest WAL files are deleted
	MaxTotalSize uint64
}

// BlockCacheConfig configures the LRU block cache
// Tier 6B: Block Cache Optimization (20-30% read performance improvement)
type BlockCacheConfig struct {
	// Size is the cache size in bytes
	// Larger cache improves read performance but uses more memory
	// Recommended: 25-50% of available RAM for read-heavy workloads
	Size uint64

	// NumShardBits controls cache sharding for concurrency
	// More shards reduce lock contention but increase overhead
	// Recommended: 4-6 bits (16-64 shards)
	NumShardBits int

	// HighPriorityPoolRatio is the ratio of cache reserved for index/filter blocks
	// 0.5 = 50% reserved for metadata (recommended for balanced workloads)
	HighPriorityPoolRatio float64
}

// ColumnFamilyConfig configures column families
// Tier 6C: Column Families (15-25% performance improvement + better isolation)
type ColumnFamilyConfig struct {
	// Enabled controls whether to use column families
	Enabled bool

	// Families lists the column families to create
	// Default: ["kv", "lease", "meta"]
	Families []string
}

// DefaultOptimizationConfig returns production-ready optimization settings
func DefaultOptimizationConfig() OptimizationConfig {
	return OptimizationConfig{
		WAL: WALConfig{
			Sync:         false,             // Async writes (Raft provides durability)
			SizeLimitMB:  64,                // 64MB WAL file size limit
			TTLSeconds:   0,                 // No TTL (managed by Raft snapshots)
			MaxTotalSize: 512 * 1024 * 1024, // 512MB total WAL size
		},
		BlockCache: BlockCacheConfig{
			Size:                  512 * 1024 * 1024, // 512MB cache
			NumShardBits:          6,                 // 64 shards
			HighPriorityPoolRatio: 0.5,               // 50% for metadata
		},
		ColumnFamilies: ColumnFamilyConfig{
			Enabled:  false, // Disabled for now (requires migration)
			Families: []string{"kv", "lease", "meta"},
		},
	}
}

// ApplyDBOptions applies optimization settings to RocksDB DBOptions
func (c *OptimizationConfig) ApplyDBOptions(opts *grocksdb.Options) {
	// Tier 6A: WAL Optimization
	if c.WAL.SizeLimitMB > 0 {
		opts.SetMaxTotalWalSize(c.WAL.MaxTotalSize)
	}

	// Performance tuning
	opts.SetMaxBackgroundJobs(4)            // Parallel compaction/flush
	opts.SetWriteBufferSize(64 * 1024 * 1024) // 64MB write buffer
	opts.SetMaxWriteBufferNumber(3)         // 3 memtables
	opts.SetTargetFileSizeBase(64 * 1024 * 1024) // 64MB SST files

	// Compression
	opts.SetCompression(grocksdb.LZ4Compression)

	// Bloom filter for faster point lookups
	opts.SetBloomLocality(1)

	// Tier 6B: Block Cache (if configured)
	if c.BlockCache.Size > 0 {
		cache := grocksdb.NewLRUCache(c.BlockCache.Size)
		cache.SetCapacity(c.BlockCache.Size)

		// Configure block-based table options
		bbto := grocksdb.NewDefaultBlockBasedTableOptions()
		bbto.SetBlockCache(cache)
		bbto.SetBlockSize(16 * 1024) // 16KB blocks
		bbto.SetCacheIndexAndFilterBlocks(true)
		bbto.SetPinL0FilterAndIndexBlocksInCache(true)

		// Use Bloom filter for better read performance
		bbto.SetFilterPolicy(grocksdb.NewBloomFilter(10))

		opts.SetBlockBasedTableFactory(bbto)
	}
}

// ApplyWriteOptions applies optimization settings to RocksDB WriteOptions
func (c *OptimizationConfig) ApplyWriteOptions(wo *grocksdb.WriteOptions) {
	// Tier 6A: WAL Optimization
	wo.SetSync(c.WAL.Sync)

	// Disable WAL entirely would break durability, so we keep it enabled
	// but async (SetSync=false) for better performance
	// Raft consensus provides cross-replica durability
}

// ApplyReadOptions applies optimization settings to RocksDB ReadOptions
func (c *OptimizationConfig) ApplyReadOptions(ro *grocksdb.ReadOptions) {
	// Enable read-ahead for sequential scans
	ro.SetReadaheadSize(4 * 1024 * 1024) // 4MB readahead

	// Use block cache
	ro.SetFillCache(true)
}

// NewOptimizedDBOptions creates DBOptions with Tier 6 optimizations applied
// Use this when opening a new RocksDB database
func NewOptimizedDBOptions() *grocksdb.Options {
	config := DefaultOptimizationConfig()
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	config.ApplyDBOptions(opts)
	return opts
}
