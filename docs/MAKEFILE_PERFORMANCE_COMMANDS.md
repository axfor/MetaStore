# Makefile Performance Test Commands

## Overview

Added new Makefile commands for running performance tests separately for Memory and RocksDB storage backends.

---

## New Commands

### 1. `make test-perf-memory`

Run only Memory storage performance tests.

**Command:**
```bash
make test-perf-memory
```

**Tests Executed:**
- `TestMemoryPerformance_LargeScaleLoad` - 50 clients, 1000 ops each
- `TestMemoryPerformance_SustainedLoad` - 20 clients, 30s duration
- `TestMemoryPerformance_MixedWorkload` - Mixed PUT/GET/DELETE/RANGE operations
- `TestMemoryPerformance_TransactionThroughput` - 10K transactions

**Timeout:** 10 minutes

---

### 2. `make test-perf-rocksdb`

Run only RocksDB storage performance tests.

**Command:**
```bash
make test-perf-rocksdb
```

**Tests Executed:**
- `TestRocksDBPerformance_LargeScaleLoad` - 50 clients, 1000 ops each
- `TestRocksDBPerformance_SustainedLoad` - 20 clients, 30s duration
- `TestRocksDBPerformance_MixedWorkload` - Mixed PUT/GET/DELETE/RANGE operations
- `TestRocksDBPerformance_Compaction` - 2K keys with updates and compaction
- `TestRocksDBPerformance_WatchScalability` - 10 watchers, 10 events

**Timeout:** 10 minutes

---

### 3. `make test-perf`

Run all performance tests (both Memory and RocksDB).

**Command:**
```bash
make test-perf
```

**Tests Executed:**
- All Memory performance tests (4 tests)
- All RocksDB performance tests (5 tests)

**Total:** 9 performance tests

**Timeout:** 20 minutes (10 minutes per backend)

---

## Usage Examples

### Run only Memory performance tests
```bash
make test-perf-memory
```

**Expected Output:**
```
Running Memory storage performance tests...
=== RUN   TestMemoryPerformance_LargeScaleLoad
=== RUN   TestMemoryPerformance_SustainedLoad
=== RUN   TestMemoryPerformance_MixedWorkload
=== RUN   TestMemoryPerformance_TransactionThroughput
PASS
Memory performance tests completed!
```

---

### Run only RocksDB performance tests
```bash
make test-perf-rocksdb
```

**Expected Output:**
```
Running RocksDB storage performance tests...
=== RUN   TestRocksDBPerformance_LargeScaleLoad
=== RUN   TestRocksDBPerformance_SustainedLoad
=== RUN   TestRocksDBPerformance_MixedWorkload
=== RUN   TestRocksDBPerformance_Compaction
=== RUN   TestRocksDBPerformance_WatchScalability
PASS
RocksDB performance tests completed!
```

---

### Run all performance tests
```bash
make test-perf
```

**Expected Output:**
```
Running all performance tests...
Testing Memory storage performance...
=== RUN   TestMemoryPerformance_LargeScaleLoad
=== RUN   TestMemoryPerformance_SustainedLoad
=== RUN   TestMemoryPerformance_MixedWorkload
=== RUN   TestMemoryPerformance_TransactionThroughput
PASS
Testing RocksDB storage performance...
=== RUN   TestRocksDBPerformance_LargeScaleLoad
=== RUN   TestRocksDBPerformance_SustainedLoad
=== RUN   TestRocksDBPerformance_MixedWorkload
=== RUN   TestRocksDBPerformance_Compaction
=== RUN   TestRocksDBPerformance_WatchScalability
PASS
All performance tests completed!
```

---

## Integration with Existing Commands

### All Test Commands

| Command | Description | Timeout |
|---------|-------------|---------|
| `make test` | Run all tests (unit + integration) | 45m |
| `make test-unit` | Run only unit tests | 10m |
| `make test-integration` | Run only integration tests | 20m |
| `make test-storage` | Run only RocksDB storage tests | - |
| `make test-coverage` | Run tests with coverage report | 20m |
| `make test-maintenance` | Run only Maintenance Service tests | 10m |
| `make test-quick` | Run quick tests (Status, Hash, Alarm) | 5m |
| **`make test-perf-memory`** | **Run Memory performance tests** | **10m** |
| **`make test-perf-rocksdb`** | **Run RocksDB performance tests** | **10m** |
| **`make test-perf`** | **Run all performance tests** | **20m** |

---

## CI/CD Integration

### Recommended CI Pipeline

```yaml
# .github/workflows/ci.yml (example)
jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Run unit tests
        run: make test-unit

  integration-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Run integration tests
        run: make test-integration

  performance-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Run Memory performance tests
        run: make test-perf-memory
      - name: Run RocksDB performance tests
        run: make test-perf-rocksdb
```

---

## Benefits

### 1. **Faster Iteration**
Run only the performance tests you need during development:
- Testing Memory optimizations? Use `make test-perf-memory`
- Testing RocksDB optimizations? Use `make test-perf-rocksdb`

### 2. **CI/CD Efficiency**
Split performance tests into separate jobs for parallel execution:
- Memory tests can run in parallel with RocksDB tests
- Faster feedback on performance regressions

### 3. **Clear Organization**
- Separate commands for separate concerns
- Easy to understand what each command does
- Follows naming pattern: `test-perf-<backend>`

### 4. **Flexible Testing**
```bash
# Quick check during development
make test-quick

# Verify Memory performance after optimization
make test-perf-memory

# Full performance validation before release
make test-perf

# Complete test suite
make test
```

---

## Implementation Details

### Makefile Configuration

```makefile
## test-perf-memory: Run Memory storage performance tests
test-perf-memory:
	@echo "$(CYAN)Running Memory storage performance tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestMemoryPerformance_" ./test/
	@echo "$(GREEN)Memory performance tests completed!$(NO_COLOR)"

## test-perf-rocksdb: Run RocksDB storage performance tests
test-perf-rocksdb:
	@echo "$(CYAN)Running RocksDB storage performance tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestRocksDBPerformance_" ./test/
	@echo "$(GREEN)RocksDB performance tests completed!$(NO_COLOR)"

## test-perf: Run all performance tests (Memory + RocksDB)
test-perf:
	@echo "$(CYAN)Running all performance tests...$(NO_COLOR)"
	@echo "$(YELLOW)Testing Memory storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestMemoryPerformance_" ./test/
	@echo "$(YELLOW)Testing RocksDB storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestRocksDBPerformance_" ./test/
	@echo "$(GREEN)All performance tests completed!$(NO_COLOR)"
```

### Test Name Patterns

The commands use Go test name patterns to filter tests:
- `-run="TestMemoryPerformance_"` - Matches all Memory performance tests
- `-run="TestRocksDBPerformance_"` - Matches all RocksDB performance tests

This leverages the test naming convention established in the test file reorganization.

---

## See Also

- [Performance Test Reorganization](PERFORMANCE_TEST_REORGANIZATION.md) - Details on test file structure
- [Sharded Map Optimization Report](SHARDED_MAP_OPTIMIZATION_REPORT.md) - Memory storage optimization results

---

**Date:** 2025-11-01
**Status:** ✅ Complete
