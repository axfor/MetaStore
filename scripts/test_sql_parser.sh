#!/bin/bash

# Test script for SQL Parser integration
# This tests that SQL queries work correctly with both simple parser and advanced parser

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Disable proxy
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY

# MySQL client path
MYSQL_CMD="/usr/local/Cellar/mysql-client@8.0/8.0.44/bin/mysql"
if [ ! -f "$MYSQL_CMD" ]; then
    MYSQL_CMD="mysql"
fi

echo -e "${YELLOW}=== SQL Parser Integration Test ===${NC}"
echo ""

# Clean up old data
rm -rf /tmp/sql_parser_test

# Start single node server with config
echo -e "${YELLOW}Starting MetaStore server with MySQL support...${NC}"
./metastore -config=test_sql_parser_config.yaml > /tmp/sql_parser_test.log 2>&1 &
SERVER_PID=$!

# Wait for server to start
sleep 3

# Test function
test_query() {
    local query=$1
    local desc=$2
    echo -e "${YELLOW}Testing: $desc${NC}"
    echo "Query: $query"

    if env -u http_proxy -u https_proxy -u all_proxy $MYSQL_CMD -h 127.0.0.1 -P 4000 -u root -e "$query" 2>&1; then
        echo -e "${GREEN}✓ PASS${NC}"
        echo ""
        return 0
    else
        echo -e "${RED}✗ FAIL${NC}"
        echo ""
        return 1
    fi
}

# Wait a bit more
sleep 2

# Test 1: Simple SELECT * (should work with both parsers)
test_query "SELECT * FROM kv LIMIT 5" "SELECT * with LIMIT"

# Test 2: SELECT with backticked columns (TiDB parser should handle this)
test_query "SELECT \`key\`, \`value\` FROM kv LIMIT 5" "SELECT with backticked columns"

# Test 3: INSERT with backticked columns
test_query "INSERT INTO kv (\`key\`, \`value\`) VALUES ('test1', 'value1')" "INSERT with backticks"

# Test 4: SELECT specific key (both parsers)
test_query "SELECT * FROM kv WHERE \`key\` = 'test1'" "SELECT with WHERE backticked key"

# Test 5: LIKE query with backticks (both parsers)
test_query "SELECT * FROM kv WHERE \`key\` LIKE 'test%'" "LIKE query with backticks"

# Test 6: SELECT without backticks (fallback to simple parser for reserved words)
test_query "INSERT INTO kv (key, value) VALUES ('test2', 'value2')" "INSERT without backticks (fallback)"
test_query "SELECT * FROM kv WHERE key LIKE 'test%'" "LIKE without backticks (fallback)"

echo -e "${GREEN}=== All Tests Completed ===${NC}"

# Cleanup
kill $SERVER_PID 2>/dev/null || true
rm -rf /tmp/sql_parser_test

echo ""
echo "Server log (last 20 lines):"
tail -20 /tmp/sql_parser_test.log
