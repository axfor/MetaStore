# Makefile Performance Test Commands

## Overview

Added new Makefile commands for running performance tests separately for Memory and Pebble storage backends.

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

### 2. `make test-perf-pebble`

Run only Pebble storage performance tests.

**Command:**
```bash
make test-perf-pebble
```

**Tests Executed:**
- `TestPebblePerformance_LargeScaleLoad` - 50 clients, 1000 ops each
- `TestPebblePerformance_SustainedLoad` - 20 clients, 30s duration
- `TestPebblePerformance_MixedWorkload` - Mixed PUT/GET/DELETE/RANGE operations
- `TestPebblePerformance_Compaction` - 2K keys with updates and compaction
- `TestPebblePerformance_WatchScalability` - 10 watchers, 10 events

**Timeout:** 10 minutes

---

### 3. `make test-perf`

Run all performance tests (both Memory and Pebble).

**Command:**
```bash
make test-perf
```

**Tests Executed:**
- All Memory performance tests (4 tests)
- All Pebble performance tests (5 tests)

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

### Run only Pebble performance tests
```bash
make test-perf-pebble
```

**Expected Output:**
```
Running Pebble storage performance tests...
=== RUN   TestPebblePerformance_LargeScaleLoad
=== RUN   TestPebblePerformance_SustainedLoad
=== RUN   TestPebblePerformance_MixedWorkload
=== RUN   TestPebblePerformance_Compaction
=== RUN   TestPebblePerformance_WatchScalability
PASS
Pebble performance tests completed!
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
Testing Pebble storage performance...
=== RUN   TestPebblePerformance_LargeScaleLoad
=== RUN   TestPebblePerformance_SustainedLoad
=== RUN   TestPebblePerformance_MixedWorkload
=== RUN   TestPebblePerformance_Compaction
=== RUN   TestPebblePerformance_WatchScalability
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
| `make test-storage` | Run only Pebble storage tests | - |
| `make test-coverage` | Run tests with coverage report | 20m |
| `make test-maintenance` | Run only Maintenance Service tests | 10m |
| `make test-quick` | Run quick tests (Status, Hash, Alarm) | 5m |
| **`make test-perf-memory`** | **Run Memory performance tests** | **10m** |
| **`make test-perf-pebble`** | **Run Pebble performance tests** | **10m** |
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
      - name: Run Pebble performance tests
        run: make test-perf-pebble
```

---

## Benefits

### 1. **Faster Iteration**
Run only the performance tests you need during development:
- Testing Memory optimizations? Use `make test-perf-memory`
- Testing Pebble optimizations? Use `make test-perf-pebble`

### 2. **CI/CD Efficiency**
Split performance tests into separate jobs for parallel execution:
- Memory tests can run in parallel with Pebble tests
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

## test-perf-pebble: Run Pebble storage performance tests
test-perf-pebble:
	@echo "$(CYAN)Running Pebble storage performance tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestPebblePerformance_" ./test/
	@echo "$(GREEN)Pebble performance tests completed!$(NO_COLOR)"

## test-perf: Run all performance tests (Memory + Pebble)
test-perf:
	@echo "$(CYAN)Running all performance tests...$(NO_COLOR)"
	@echo "$(YELLOW)Testing Memory storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestMemoryPerformance_" ./test/
	@echo "$(YELLOW)Testing Pebble storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestPebblePerformance_" ./test/
	@echo "$(GREEN)All performance tests completed!$(NO_COLOR)"
```

### Test Name Patterns

The commands use Go test name patterns to filter tests:
- `-run="TestMemoryPerformance_"` - Matches all Memory performance tests
- `-run="TestPebblePerformance_"` - Matches all Pebble performance tests

This leverages the test naming convention established in the test file reorganization.

---

## See Also

- [Performance Test Reorganization](PERFORMANCE_TEST_REORGANIZATION.md) - Details on test file structure
- [Sharded Map Optimization Report](SHARDED_MAP_OPTIMIZATION_REPORT.md) - Memory storage optimization results

---

**Date:** 2025-11-01
**Status:** ✅ Complete
