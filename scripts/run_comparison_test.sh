#!/bin/bash

# MetaStore Performance Comparison Test Runner
# Compares Memory vs RocksDB storage engine performance

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPORT_DIR="$PROJECT_ROOT/test-reports"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Create report directory
mkdir -p "$REPORT_DIR"

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}MetaStore Performance Comparison Suite${NC}"
echo -e "${BLUE}Memory vs RocksDB Storage Engines${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo "Project: $PROJECT_ROOT"
echo "Report Directory: $REPORT_DIR"
echo "Timestamp: $TIMESTAMP"
echo ""

cd "$PROJECT_ROOT"

# CGO flags for RocksDB
export CGO_ENABLED=1
export CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain"

# Clean up old test data
echo -e "${YELLOW}Cleaning up old test data...${NC}"
rm -rf data/perf-test* 2>/dev/null || true
echo ""

# Run Memory engine performance tests
echo -e "${CYAN}================================${NC}"
echo -e "${CYAN}Testing: Memory Storage Engine${NC}"
echo -e "${CYAN}================================${NC}"
echo ""

MEMORY_REPORT="$REPORT_DIR/memory_performance_${TIMESTAMP}.txt"

echo -e "${YELLOW}Running Memory performance tests...${NC}"
go test -v -timeout 30m ./test \
    -run "^TestPerformance_" \
    2>&1 | tee "$MEMORY_REPORT"

MEMORY_EXIT_CODE=${PIPESTATUS[0]}

echo ""
if [ $MEMORY_EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ Memory performance tests completed${NC}"
else
    echo -e "${RED}✗ Memory performance tests failed${NC}"
fi
echo ""

# Clean up between tests
echo -e "${YELLOW}Cleaning up test data...${NC}"
rm -rf data/perf-test* 2>/dev/null || true
sleep 2
echo ""

# Run RocksDB engine performance tests
echo -e "${CYAN}================================${NC}"
echo -e "${CYAN}Testing: RocksDB Storage Engine${NC}"
echo -e "${CYAN}================================${NC}"
echo ""

ROCKSDB_REPORT="$REPORT_DIR/rocksdb_performance_${TIMESTAMP}.txt"

echo -e "${YELLOW}Running RocksDB performance tests...${NC}"
go test -v -timeout 30m ./test \
    -run "^TestPerformanceRocksDB_" \
    2>&1 | tee "$ROCKSDB_REPORT"

ROCKSDB_EXIT_CODE=${PIPESTATUS[0]}

echo ""
if [ $ROCKSDB_EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ RocksDB performance tests completed${NC}"
else
    echo -e "${RED}✗ RocksDB performance tests failed${NC}"
fi
echo ""

# Generate comparison report
COMPARISON_FILE="$REPORT_DIR/comparison_${TIMESTAMP}.md"

cat > "$COMPARISON_FILE" << 'EOF'
# MetaStore Performance Comparison Report
## Memory vs RocksDB Storage Engines

**Date**: $(date)
**Test Duration**: ~1 hour

---

## Test Summary

EOF

# Add Memory results
echo "### Memory Storage Engine" >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"
if [ $MEMORY_EXIT_CODE -eq 0 ]; then
    echo "**Status**: ✅ PASSED" >> "$COMPARISON_FILE"
else
    echo "**Status**: ❌ FAILED" >> "$COMPARISON_FILE"
fi
echo "" >> "$COMPARISON_FILE"

# Extract Memory metrics
echo "#### Performance Metrics" >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"
echo '```' >> "$COMPARISON_FILE"
grep -E "Throughput:|Average latency:|Total operations:|Successful operations:" "$MEMORY_REPORT" | head -20 >> "$COMPARISON_FILE" || true
echo '```' >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"

# Add RocksDB results
echo "### RocksDB Storage Engine" >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"
if [ $ROCKSDB_EXIT_CODE -eq 0 ]; then
    echo "**Status**: ✅ PASSED" >> "$COMPARISON_FILE"
else
    echo "**Status**: ❌ FAILED" >> "$COMPARISON_FILE"
fi
echo "" >> "$COMPARISON_FILE"

# Extract RocksDB metrics
echo "#### Performance Metrics" >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"
echo '```' >> "$COMPARISON_FILE"
grep -E "Throughput:|Average latency:|Total operations:|Successful operations:" "$ROCKSDB_REPORT" | head -20 >> "$COMPARISON_FILE" || true
echo '```' >> "$COMPARISON_FILE"
echo "" >> "$COMPARISON_FILE"

# Add comparison section
cat >> "$COMPARISON_FILE" << 'EOF'

---

## Performance Comparison

| Metric | Memory | RocksDB | Difference |
|--------|--------|---------|------------|
| Large-Scale Load Throughput | [See above] | [See above] | - |
| Large-Scale Load Latency | [See above] | [See above] | - |
| Sustained Load Throughput | [See above] | [See above] | - |
| Mixed Workload Throughput | [See above] | [See above] | - |

---

## Analysis

### Memory Storage Engine
**Advantages**:
- Faster read/write operations
- Lower latency
- No disk I/O overhead

**Disadvantages**:
- Limited by available RAM
- Data loss on crash (relies on WAL for recovery)
- Not suitable for large datasets

### RocksDB Storage Engine
**Advantages**:
- Persistent storage
- Handles large datasets
- Production-ready durability
- Efficient compaction

**Disadvantages**:
- Disk I/O overhead
- Slightly higher latency
- Compaction can impact performance

---

## Recommendations

### Use Memory Engine When:
1. Dataset fits in memory (< 10GB)
2. High throughput/low latency is critical
3. Data can be recovered from WAL
4. Testing/development environments

### Use RocksDB Engine When:
1. Dataset is large (> 10GB)
2. Durability is critical
3. Production environments
4. Long-term data retention required

---

## Detailed Reports

- **Memory Performance**: `$(basename "$MEMORY_REPORT")`
- **RocksDB Performance**: `$(basename "$ROCKSDB_REPORT")`

EOF

# Display summary
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Performance Comparison Complete${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo -e "${CYAN}Test Results:${NC}"
echo -e "  Memory Engine:  $([ $MEMORY_EXIT_CODE -eq 0 ] && echo -e "${GREEN}PASSED${NC}" || echo -e "${RED}FAILED${NC}")"
echo -e "  RocksDB Engine: $([ $ROCKSDB_EXIT_CODE -eq 0 ] && echo -e "${GREEN}PASSED${NC}" || echo -e "${RED}FAILED${NC}")"
echo ""
echo -e "${CYAN}Reports Generated:${NC}"
echo "  Memory:     $MEMORY_REPORT"
echo "  RocksDB:    $ROCKSDB_REPORT"
echo "  Comparison: $COMPARISON_FILE"
echo ""

# Show comparison summary
echo -e "${CYAN}Comparison Summary:${NC}"
cat "$COMPARISON_FILE"

# Exit with appropriate code
if [ $MEMORY_EXIT_CODE -ne 0 ] || [ $ROCKSDB_EXIT_CODE -ne 0 ]; then
    exit 1
fi

exit 0
