#!/bin/bash

# MetaStore Performance Test Runner
# This script runs comprehensive performance tests and generates a report

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
NC='\033[0m' # No Color

# Create report directory
mkdir -p "$REPORT_DIR"

echo -e "${BLUE}================================${NC}"
echo -e "${BLUE}MetaStore Performance Test Suite${NC}"
echo -e "${BLUE}================================${NC}"
echo ""
echo "Project: $PROJECT_ROOT"
echo "Report Directory: $REPORT_DIR"
echo "Timestamp: $TIMESTAMP"
echo ""

cd "$PROJECT_ROOT"

# Run performance tests
echo -e "${YELLOW}Running Performance Tests...${NC}"
echo ""

PERF_REPORT="$REPORT_DIR/performance_${TIMESTAMP}.txt"

go test -v -timeout 30m ./test \
    -run "^TestPerformance_" \
    2>&1 | tee "$PERF_REPORT"

PERF_EXIT_CODE=${PIPESTATUS[0]}

echo ""
if [ $PERF_EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ Performance tests passed${NC}"
else
    echo -e "${RED}✗ Performance tests failed${NC}"
fi
echo ""

# Run benchmarks
echo -e "${YELLOW}Running Benchmark Tests...${NC}"
echo ""

BENCH_REPORT="$REPORT_DIR/benchmark_${TIMESTAMP}.txt"

go test -bench=. -benchmem -benchtime=5s ./test \
    -run=^$ \
    2>&1 | tee "$BENCH_REPORT"

BENCH_EXIT_CODE=${PIPESTATUS[0]}

echo ""
if [ $BENCH_EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ Benchmark tests completed${NC}"
else
    echo -e "${RED}✗ Benchmark tests failed${NC}"
fi
echo ""

# Generate summary
SUMMARY_FILE="$REPORT_DIR/summary_${TIMESTAMP}.md"

cat > "$SUMMARY_FILE" << EOF
# MetaStore Performance Test Summary

**Date**: $(date)
**Commit**: $(git rev-parse --short HEAD 2>/dev/null || echo "N/A")

## Performance Test Results

EOF

# Extract key metrics from performance tests
if grep -q "PASS" "$PERF_REPORT"; then
    echo "### Test Status: ✅ PASSED" >> "$SUMMARY_FILE"
else
    echo "### Test Status: ❌ FAILED" >> "$SUMMARY_FILE"
fi

echo "" >> "$SUMMARY_FILE"
echo "### Key Metrics" >> "$SUMMARY_FILE"
echo "" >> "$SUMMARY_FILE"

# Extract throughput numbers
grep -E "Throughput:|throughput:" "$PERF_REPORT" | while read -r line; do
    echo "- $line" >> "$SUMMARY_FILE"
done

# Extract latency numbers
grep -E "Average latency:|latency:" "$PERF_REPORT" | while read -r line; do
    echo "- $line" >> "$SUMMARY_FILE"
done

echo "" >> "$SUMMARY_FILE"
echo "## Benchmark Results" >> "$SUMMARY_FILE"
echo "" >> "$SUMMARY_FILE"
echo "\`\`\`" >> "$SUMMARY_FILE"
grep "^Benchmark" "$BENCH_REPORT" >> "$SUMMARY_FILE" || true
echo "\`\`\`" >> "$SUMMARY_FILE"

echo "" >> "$SUMMARY_FILE"
echo "## Files" >> "$SUMMARY_FILE"
echo "" >> "$SUMMARY_FILE"
echo "- Performance Test Report: [\`$(basename "$PERF_REPORT")\`]($PERF_REPORT)" >> "$SUMMARY_FILE"
echo "- Benchmark Report: [\`$(basename "$BENCH_REPORT")\`]($BENCH_REPORT)" >> "$SUMMARY_FILE"

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Test Summary Generated${NC}"
echo -e "${GREEN}================================${NC}"
echo ""
echo "Summary: $SUMMARY_FILE"
echo ""
cat "$SUMMARY_FILE"

# Exit with appropriate code
if [ $PERF_EXIT_CODE -ne 0 ] || [ $BENCH_EXIT_CODE -ne 0 ]; then
    exit 1
fi

exit 0
