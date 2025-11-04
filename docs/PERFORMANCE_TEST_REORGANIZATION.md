# Performance Test Reorganization Report

## Summary

Successfully reorganized performance tests to separate Memory and RocksDB storage backends with clear, consistent naming conventions.

---

## Changes Made

### 1. File Reorganization

**Before:**
- `test/performance_test.go` - Mixed Memory tests with one RocksDB test
- `test/performance_rocksdb_test.go` - RocksDB tests

**After:**
- `test/performance_memory_test.go` - **Memory storage tests only**
- `test/performance_rocksdb_test.go` - **RocksDB storage tests only**

### 2. Test Function Naming

All performance tests now follow a consistent naming pattern: `Test<StorageBackend>Performance_<TestName>`

#### Memory Performance Tests (performance_memory_test.go)

| Old Name | New Name |
|----------|----------|
| `TestPerformance_LargeScaleLoad` | `TestMemoryPerformance_LargeScaleLoad` |
| `TestPerformance_SustainedLoad` | `TestMemoryPerformance_SustainedLoad` |
| `TestPerformance_MixedWorkload` | `TestMemoryPerformance_MixedWorkload` |
| `TestPerformance_TransactionThroughput` | `TestMemoryPerformance_TransactionThroughput` |
| ~~`TestPerformance_WatchScalability`~~ | **Moved to RocksDB file** |

#### RocksDB Performance Tests (performance_rocksdb_test.go)

| Old Name | New Name |
|----------|----------|
| `TestPerformanceRocksDB_LargeScaleLoad` | `TestRocksDBPerformance_LargeScaleLoad` |
| `TestPerformanceRocksDB_SustainedLoad` | `TestRocksDBPerformance_SustainedLoad` |
| `TestPerformanceRocksDB_MixedWorkload` | `TestRocksDBPerformance_MixedWorkload` |
| `TestPerformanceRocksDB_Compaction` | `TestRocksDBPerformance_Compaction` |
| N/A (moved from Memory) | `TestRocksDBPerformance_WatchScalability` |

---

## Benefits

### 1. **Clear Separation**
- Memory tests isolated in `performance_memory_test.go`
- RocksDB tests isolated in `performance_rocksdb_test.go`
- No mixing of storage backends in the same file

### 2. **Consistent Naming**
- All tests use pattern: `Test<Backend>Performance_<Name>`
- Easy to identify which storage backend is being tested
- Alphabetically grouped by backend when listing tests

### 3. **Better Organization**
- Run Memory tests only: `go test ./test -run "TestMemoryPerformance.*"`
- Run RocksDB tests only: `go test ./test -run "TestRocksDBPerformance.*"`
- Run all performance tests: `go test ./test -run "Test(Memory|RocksDB)Performance.*"`

### 4. **Fixed Misplaced Test**
- `TestPerformance_WatchScalability` was using `startTestServerRocksDB()`
- Correctly moved to RocksDB file as `TestRocksDBPerformance_WatchScalability`

---

## Verification

All tests are properly discovered and can be listed:

```bash
$ CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -list "TestMemoryPerformance.*|TestRocksDBPerformance.*"

TestMemoryPerformance_LargeScaleLoad
TestMemoryPerformance_SustainedLoad
TestMemoryPerformance_MixedWorkload
TestMemoryPerformance_TransactionThroughput
TestRocksDBPerformance_LargeScaleLoad
TestRocksDBPerformance_SustainedLoad
TestRocksDBPerformance_MixedWorkload
TestRocksDBPerformance_Compaction
TestRocksDBPerformance_WatchScalability
ok      metaStore/test  0.847s
```

---

## Usage Examples

### Run Only Memory Performance Tests

```bash
go test ./test -run "TestMemoryPerformance.*" -v
```

### Run Only RocksDB Performance Tests

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "TestRocksDBPerformance.*" -v
```

### Run Specific Test

```bash
# Memory MixedWorkload test
go test ./test -run "TestMemoryPerformance_MixedWorkload" -v

# RocksDB Compaction test
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "TestRocksDBPerformance_Compaction" -v
```

### Run All Performance Tests

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "Test(Memory|RocksDB)Performance.*" -v
```

---

## Files Modified

1. **Renamed:**
   - `test/performance_test.go` → `test/performance_memory_test.go`

2. **Modified:**
   - `test/performance_memory_test.go` - All test function names updated, WatchScalability test removed
   - `test/performance_rocksdb_test.go` - All test function names updated, WatchScalability test added

---

## Test Coverage

### Memory Storage (4 tests)
- ✅ Large-scale concurrent load (50 clients, 1000 ops each)
- ✅ Sustained load over time (20 clients, 30s duration)
- ✅ Mixed workload (PUT/GET/DELETE/RANGE operations)
- ✅ Transaction throughput (10K transactions, 10 clients)

### RocksDB Storage (5 tests)
- ✅ Large-scale concurrent load (50 clients, 1000 ops each)
- ✅ Sustained load over time (20 clients, 30s duration)
- ✅ Mixed workload (PUT/GET/DELETE/RANGE operations)
- ✅ Compaction performance (2K keys with updates)
- ✅ Watch scalability (10 watchers, 10 events)

---

## Migration Notes

If you have any scripts or CI/CD pipelines that reference the old test names, update them as follows:

```bash
# Old (no longer works):
go test ./test -run "TestPerformance_MixedWorkload"

# New (Memory):
go test ./test -run "TestMemoryPerformance_MixedWorkload"

# New (RocksDB):
CGO_ENABLED=1 CGO_LDFLAGS="..." go test ./test -run "TestRocksDBPerformance_MixedWorkload"
```

---

**Date:** 2025-11-01
**Status:** ✅ Complete
**Breaking Changes:** Yes - Test function names changed
