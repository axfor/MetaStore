# Performance Test Reorganization Report

## Summary

Successfully reorganized performance tests to separate Memory and Pebble storage backends with clear, consistent naming conventions.

---

## Changes Made

### 1. File Reorganization

**Before:**
- `test/performance_test.go` - Mixed Memory tests with one Pebble test
- `test/performance_pebble_test.go` - Pebble tests

**After:**
- `test/performance_memory_test.go` - **Memory storage tests only**
- `test/performance_pebble_test.go` - **Pebble storage tests only**

### 2. Test Function Naming

All performance tests now follow a consistent naming pattern: `Test<StorageBackend>Performance_<TestName>`

#### Memory Performance Tests (performance_memory_test.go)

| Old Name | New Name |
|----------|----------|
| `TestPerformance_LargeScaleLoad` | `TestMemoryPerformance_LargeScaleLoad` |
| `TestPerformance_SustainedLoad` | `TestMemoryPerformance_SustainedLoad` |
| `TestPerformance_MixedWorkload` | `TestMemoryPerformance_MixedWorkload` |
| `TestPerformance_TransactionThroughput` | `TestMemoryPerformance_TransactionThroughput` |
| ~~`TestPerformance_WatchScalability`~~ | **Moved to Pebble file** |

#### Pebble Performance Tests (performance_pebble_test.go)

| Old Name | New Name |
|----------|----------|
| `TestPerformancePebble_LargeScaleLoad` | `TestPebblePerformance_LargeScaleLoad` |
| `TestPerformancePebble_SustainedLoad` | `TestPebblePerformance_SustainedLoad` |
| `TestPerformancePebble_MixedWorkload` | `TestPebblePerformance_MixedWorkload` |
| `TestPerformancePebble_Compaction` | `TestPebblePerformance_Compaction` |
| N/A (moved from Memory) | `TestPebblePerformance_WatchScalability` |

---

## Benefits

### 1. **Clear Separation**
- Memory tests isolated in `performance_memory_test.go`
- Pebble tests isolated in `performance_pebble_test.go`
- No mixing of storage backends in the same file

### 2. **Consistent Naming**
- All tests use pattern: `Test<Backend>Performance_<Name>`
- Easy to identify which storage backend is being tested
- Alphabetically grouped by backend when listing tests

### 3. **Better Organization**
- Run Memory tests only: `go test ./test -run "TestMemoryPerformance.*"`
- Run Pebble tests only: `go test ./test -run "TestPebblePerformance.*"`
- Run all performance tests: `go test ./test -run "Test(Memory|Pebble)Performance.*"`

### 4. **Fixed Misplaced Test**
- `TestPerformance_WatchScalability` was using `startTestServerPebble()`
- Correctly moved to Pebble file as `TestPebblePerformance_WatchScalability`

---

## Verification

All tests are properly discovered and can be listed:

```bash
$ CGO_ENABLED=1 CGO_LDFLAGS="-lpebble -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -list "TestMemoryPerformance.*|TestPebblePerformance.*"

TestMemoryPerformance_LargeScaleLoad
TestMemoryPerformance_SustainedLoad
TestMemoryPerformance_MixedWorkload
TestMemoryPerformance_TransactionThroughput
TestPebblePerformance_LargeScaleLoad
TestPebblePerformance_SustainedLoad
TestPebblePerformance_MixedWorkload
TestPebblePerformance_Compaction
TestPebblePerformance_WatchScalability
ok      metaStore/test  0.847s
```

---

## Usage Examples

### Run Only Memory Performance Tests

```bash
go test ./test -run "TestMemoryPerformance.*" -v
```

### Run Only Pebble Performance Tests

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lpebble -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "TestPebblePerformance.*" -v
```

### Run Specific Test

```bash
# Memory MixedWorkload test
go test ./test -run "TestMemoryPerformance_MixedWorkload" -v

# Pebble Compaction test
CGO_ENABLED=1 CGO_LDFLAGS="-lpebble -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "TestPebblePerformance_Compaction" -v
```

### Run All Performance Tests

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lpebble -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
  go test ./test -run "Test(Memory|Pebble)Performance.*" -v
```

---

## Files Modified

1. **Renamed:**
   - `test/performance_test.go` → `test/performance_memory_test.go`

2. **Modified:**
   - `test/performance_memory_test.go` - All test function names updated, WatchScalability test removed
   - `test/performance_pebble_test.go` - All test function names updated, WatchScalability test added

---

## Test Coverage

### Memory Storage (4 tests)
- ✅ Large-scale concurrent load (50 clients, 1000 ops each)
- ✅ Sustained load over time (20 clients, 30s duration)
- ✅ Mixed workload (PUT/GET/DELETE/RANGE operations)
- ✅ Transaction throughput (10K transactions, 10 clients)

### Pebble Storage (5 tests)
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

# New (Pebble):
CGO_ENABLED=1 CGO_LDFLAGS="..." go test ./test -run "TestPebblePerformance_MixedWorkload"
```

---

**Date:** 2025-11-01
**Status:** ✅ Complete
**Breaking Changes:** Yes - Test function names changed
