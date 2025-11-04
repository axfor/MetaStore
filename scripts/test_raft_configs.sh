#!/bin/bash
# Raft 配置性能对比测试脚本

set -e

echo "=========================================="
echo "Raft 配置性能对比测试"
echo "=========================================="
echo ""

pre_dir=$(pwd)

cd $pre_dir/../

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;36m'
NC='\033[0m' # No Color

# 测试结果文件
RESULT_FILE="raft_config_test_results.txt"
rm -f "$RESULT_FILE"

# 配置备份
CONFIG_FILE="configs/config.yaml"
BACKUP_FILE="configs/config.yaml.backup"
cp "$CONFIG_FILE" "$BACKUP_FILE"

echo "配置文件已备份到: $BACKUP_FILE"
echo ""

# 测试函数
run_test() {
    local config_name=$1
    local tick_interval=$2
    local max_inflight=$3

    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}测试配置: $config_name${NC}"
    echo -e "${BLUE}  tick_interval: $tick_interval${NC}"
    echo -e "${BLUE}  max_inflight_msgs: $max_inflight${NC}"
    echo -e "${BLUE}========================================${NC}"

    # 更新配置文件
    sed -i '' "s/tick_interval:.*ms.*/tick_interval: $tick_interval/" "$CONFIG_FILE"
    sed -i '' "s/max_inflight_msgs:.*/max_inflight_msgs: $max_inflight/" "$CONFIG_FILE"

    echo "配置已更新，等待 2 秒..."
    sleep 2

    # 运行性能测试
    echo -e "${YELLOW}运行 Memory 性能测试...${NC}"
    echo "" >> "$RESULT_FILE"
    echo "========================================" >> "$RESULT_FILE"
    echo "配置: $config_name" >> "$RESULT_FILE"
    echo "  tick_interval: $tick_interval" >> "$RESULT_FILE"
    echo "  max_inflight_msgs: $max_inflight" >> "$RESULT_FILE"
    echo "========================================" >> "$RESULT_FILE"

    # 运行测试并捕获结果
    if CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2 -Wl,-U,_SecTrustCopyCertificateChain" \
       go test -v -timeout=15m ./test -run="TestMemoryPerformance" 2>&1 | tee -a "$RESULT_FILE" | grep -E "(PASS|FAIL|吞吐量|ops/sec|延迟|Latency)"; then
        echo -e "${GREEN}✓ 测试完成${NC}"
    else
        echo -e "${RED}✗ 测试失败或超时${NC}"
    fi

    echo ""
    sleep 3
}

# 清理之前的测试数据
echo "清理测试数据..."
rm -rf data/perf-test

# 配置 1: 基准配置（etcd 默认）
run_test "基准配置 (etcd默认)" "100ms" "512"

# 配置 2: 快速响应配置
run_test "快速响应配置" "50ms" "1024"

# 配置 3: 极速响应配置
run_test "极速响应配置" "30ms" "2048"

# 恢复原始配置
echo -e "${YELLOW}恢复原始配置...${NC}"
mv "$BACKUP_FILE" "$CONFIG_FILE"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}测试完成！${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "测试结果已保存到: $RESULT_FILE"
echo ""
echo "快速查看结果摘要："
echo ""
grep -A 5 "配置:" "$RESULT_FILE" | grep -E "(配置:|吞吐量|ops/sec|延迟|Latency)" || echo "未找到性能数据"


cd $pre_dir