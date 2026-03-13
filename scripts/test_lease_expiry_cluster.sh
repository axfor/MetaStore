#!/bin/bash

# Lease 过期测试脚本：3节点集群，测试 lease 过期、重启后不复活、剩余时间过期等场景

set -e

echo "===== Lease 过期测试：3节点集群 ====="
echo ""

# Disable proxy
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY

pre_dir=$(pwd)
ETCDCTL=$(which etcdctl 2>/dev/null || echo "$pre_dir/../tools/etcdctl")

# 清理
rm -rf raft-lease-test
mkdir -p raft-lease-test/{node1,node2,node3}

pkill -9 metastore >/dev/null 2>&1 || true
sleep 2

# 编译 (如果二进制不存在或传入 --build 参数)
cd "$pre_dir/../"
if [ ! -f metaStore ] || [ "${1}" = "--build" ]; then
    echo "1. 编译程序..."
    make build
    echo "   编译成功"
else
    echo "1. 使用已编译的二进制..."
fi
echo ""
cd "$pre_dir"

cp "$pre_dir/../metaStore" raft-lease-test/
cd raft-lease-test
mkdir -p data/pebble/{1,2,3}

CLUSTER="http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023"

start_cluster() {
    echo "   启动3节点集群..."
    ./metastore --member-id=1 --cluster=$CLUSTER --grpc-addr=:12379 --storage=pebble > node1/log.txt 2>&1 &
    PID1=$!
    sleep 3

    ./metastore --member-id=2 --cluster=$CLUSTER --grpc-addr=:12380 --storage=pebble > node2/log.txt 2>&1 &
    PID2=$!
    sleep 3

    ./metastore --member-id=3 --cluster=$CLUSTER --grpc-addr=:12381 --storage=pebble > node3/log.txt 2>&1 &
    PID3=$!
    sleep 8

    # 检查进程
    for i in 1 2 3; do
        pid_var="PID$i"
        if ! ps -p ${!pid_var} > /dev/null 2>&1; then
            echo "   [FAIL] 节点 $i 启动失败"
            cat "node$i/log.txt"
            stop_cluster
            exit 1
        fi
    done
    echo "   所有节点运行中"
}

stop_cluster() {
    echo "   停止集群..."
    kill $PID1 $PID2 $PID3 2>/dev/null || true
    wait $PID1 $PID2 $PID3 2>/dev/null || true
    sleep 2
    echo "   集群已停止"
}

export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS="localhost:12379,localhost:12380,localhost:12381"
chmod a+x "$ETCDCTL" 2>/dev/null || true

PASS=0
FAIL=0
TOTAL=0

run_test() {
    TOTAL=$((TOTAL + 1))
    echo ""
    echo "------------------------------------------------------------"
    echo "TEST $TOTAL: $1"
    echo "------------------------------------------------------------"
}

pass() {
    PASS=$((PASS + 1))
    echo "   [PASS] $1"
}

fail() {
    FAIL=$((FAIL + 1))
    echo "   [FAIL] $1"
}

# ============================================================
# 启动集群
# ============================================================
echo "2. 启动集群..."
start_cluster

# ============================================================
# TEST 1: Lease 正常过期
# ============================================================
run_test "Lease 正常过期 (TTL=5s)"

echo "   创建 lease (TTL=5s)..."
LEASE_OUT=$($ETCDCTL lease grant 5)
LEASE_ID=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   Lease ID: $LEASE_ID"
echo "   Lease grant output: $LEASE_OUT"

echo "   绑定 key 到 lease..."
$ETCDCTL put lease-test-1 "value1" --lease=$LEASE_ID

echo "   验证 key 存在..."
GET_OUT=$($ETCDCTL get lease-test-1 --print-value-only)
if [ "$GET_OUT" = "value1" ]; then
    echo "   key 存在: $GET_OUT"
else
    fail "key 不存在或值不正确"
fi

echo "   查看 lease TTL..."
$ETCDCTL lease timetolive $LEASE_ID

echo "   等待 lease 过期 (7s)..."
sleep 7

echo "   验证 key 已被删除..."
GET_OUT=$($ETCDCTL get lease-test-1 --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "Lease 过期后 key 已自动删除"
else
    fail "Lease 过期后 key 仍存在: $GET_OUT"
fi

# 验证 lease 已不存在
LEASE_TTL_OUT=$($ETCDCTL lease timetolive $LEASE_ID 2>&1 || true)
echo "   Lease TTL 查询: $LEASE_TTL_OUT"

# ============================================================
# TEST 2: Lease 过期后重启集群，验证不复活
# ============================================================
run_test "Lease 过期后重启集群，验证不复活 (TTL=5s)"

echo "   创建 lease (TTL=5s)..."
LEASE_OUT=$($ETCDCTL lease grant 5)
LEASE_ID2=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   Lease ID: $LEASE_ID2"

$ETCDCTL put lease-test-2 "value2" --lease=$LEASE_ID2

echo "   等待 lease 过期 (7s)..."
sleep 7

echo "   验证 lease 已过期..."
GET_OUT=$($ETCDCTL get lease-test-2 --print-value-only)
if [ -z "$GET_OUT" ]; then
    echo "   lease 已过期，key 已删除"
else
    fail "lease 未过期"
fi

echo "   重启集群..."
stop_cluster
sleep 3
start_cluster

echo "   验证过期 lease 的 key 仍不存在..."
GET_OUT=$($ETCDCTL get lease-test-2 --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "重启后过期 lease 的 key 未复活"
else
    fail "重启后过期 lease 的 key 复活了: $GET_OUT"
fi

# ============================================================
# TEST 3: 重启期间 Lease TTL 耗尽
# ============================================================
run_test "重启期间 Lease TTL 耗尽 (TTL=10s, 停机>10s)"

echo "   创建 lease (TTL=10s)..."
LEASE_OUT=$($ETCDCTL lease grant 10)
LEASE_ID3=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   Lease ID: $LEASE_ID3"

$ETCDCTL put lease-test-3 "value3" --lease=$LEASE_ID3

echo "   验证 key 存在..."
GET_OUT=$($ETCDCTL get lease-test-3 --print-value-only)
echo "   key 值: $GET_OUT"

echo "   查看剩余 TTL..."
$ETCDCTL lease timetolive $LEASE_ID3

echo "   立即停止集群 (lease 尚未过期)..."
stop_cluster

echo "   等待超过 TTL (15s)，让 lease 在停机期间过期..."
sleep 15

echo "   重启集群..."
start_cluster

echo "   验证 lease 在停机期间已过期，key 不存在..."
GET_OUT=$($ETCDCTL get lease-test-3 --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "停机期间 lease 过期后，重启后 key 未复活"
else
    fail "停机期间 lease 过期后，重启后 key 复活了: $GET_OUT"
fi

# 验证 lease 本身不存在
LEASE_TTL_OUT=$($ETCDCTL lease timetolive $LEASE_ID3 2>&1 || true)
echo "   Lease TTL 查询: $LEASE_TTL_OUT"

# ============================================================
# TEST 4: 重启期间 Lease 部分过期 (短TTL过期，长TTL存活)
# ============================================================
run_test "混合 TTL: 短TTL过期，长TTL存活 (TTL=5s vs TTL=300s)"

echo "   创建短 lease (TTL=5s)..."
LEASE_OUT=$($ETCDCTL lease grant 5)
LEASE_SHORT=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   短 Lease ID: $LEASE_SHORT"

echo "   创建长 lease (TTL=300s)..."
LEASE_OUT=$($ETCDCTL lease grant 300)
LEASE_LONG=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   长 Lease ID: $LEASE_LONG"

$ETCDCTL put short-ttl-key "short" --lease=$LEASE_SHORT
$ETCDCTL put long-ttl-key "long" --lease=$LEASE_LONG

echo "   立即停止集群..."
stop_cluster

echo "   等待 10s (短TTL过期，长TTL仍有效)..."
sleep 10

echo "   重启集群..."
start_cluster

echo "   验证短 TTL key 不存在..."
GET_SHORT=$($ETCDCTL get short-ttl-key --print-value-only)
if [ -z "$GET_SHORT" ]; then
    echo "   [OK] 短TTL key 已过期"
else
    fail "短TTL key 在停机期间应该过期但未过期: $GET_SHORT"
fi

echo "   验证长 TTL key 仍存在..."
GET_LONG=$($ETCDCTL get long-ttl-key --print-value-only)
if [ "$GET_LONG" = "long" ]; then
    echo "   [OK] 长TTL key 仍存在"
else
    fail "长TTL key 不应该消失"
fi

echo "   查看长 lease 剩余 TTL..."
$ETCDCTL lease timetolive $LEASE_LONG

if [ -z "$GET_SHORT" ] && [ "$GET_LONG" = "long" ]; then
    pass "混合 TTL 场景正确: 短TTL过期，长TTL存活"
fi

# 清理长 lease
$ETCDCTL lease revoke $LEASE_LONG 2>/dev/null || true

# ============================================================
# TEST 5: Lease KeepAlive 续期后过期
# ============================================================
run_test "Lease KeepAlive 续期后重启，验证续期时间生效"

echo "   创建 lease (TTL=5s)..."
LEASE_OUT=$($ETCDCTL lease grant 5)
LEASE_ID5=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   Lease ID: $LEASE_ID5"

$ETCDCTL put keepalive-key "alive" --lease=$LEASE_ID5

echo "   等待 3s 后续期..."
sleep 3
echo "   续期..."
$ETCDCTL lease keep-alive $LEASE_ID5 --once
$ETCDCTL lease timetolive $LEASE_ID5

echo "   等待 3s 后再续期..."
sleep 3
echo "   续期..."
$ETCDCTL lease keep-alive $LEASE_ID5 --once
$ETCDCTL lease timetolive $LEASE_ID5

echo "   验证 key 仍存在 (因为续期了)..."
GET_OUT=$($ETCDCTL get keepalive-key --print-value-only)
if [ "$GET_OUT" = "alive" ]; then
    echo "   [OK] 续期后 key 仍存在"
else
    fail "续期后 key 不应该消失"
fi

echo "   停止续期，等待过期 (7s)..."
sleep 7

GET_OUT=$($ETCDCTL get keepalive-key --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "停止续期后 lease 正常过期"
else
    fail "停止续期后 lease 未过期: $GET_OUT"
fi

# ============================================================
# TEST 6: 通过 follower 节点创建 lease，重启后验证
# ============================================================
run_test "Follower 节点创建 lease，停机过期后重启验证"

echo "   通过 follower (12380) 创建 lease (TTL=8s)..."
LEASE_OUT=$($ETCDCTL --endpoints=localhost:12380 lease grant 8)
LEASE_ID6=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
echo "   Lease ID: $LEASE_ID6"

$ETCDCTL --endpoints=localhost:12380 put follower-lease-key "follower-value" --lease=$LEASE_ID6

echo "   通过另一个 follower (12381) 验证 key 存在..."
GET_OUT=$($ETCDCTL --endpoints=localhost:12381 get follower-lease-key --print-value-only)
if [ "$GET_OUT" = "follower-value" ]; then
    echo "   [OK] key 在集群中已复制"
else
    fail "key 未在集群中复制"
fi

echo "   立即停止集群..."
stop_cluster

echo "   等待 15s (lease 在停机期间过期)..."
sleep 15

echo "   重启集群..."
start_cluster

echo "   验证 follower 创建的过期 lease 的 key 不存在..."
GET_OUT=$($ETCDCTL get follower-lease-key --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "Follower 创建的 lease 在停机期间过期，重启后不复活"
else
    fail "Follower 创建的 lease 重启后复活了: $GET_OUT"
fi

# ============================================================
# TEST 7: 多次重启验证
# ============================================================
run_test "多次重启后验证过期 lease 不复活"

echo "   创建 lease (TTL=5s) 并绑定 key..."
LEASE_OUT=$($ETCDCTL lease grant 5)
LEASE_ID7=$(echo "$LEASE_OUT" | grep -oE '[0-9a-f]{16}' || echo "$LEASE_OUT" | grep -oE '[0-9]+' | head -1)
$ETCDCTL put multi-restart-key "value" --lease=$LEASE_ID7

echo "   等待过期 (7s)..."
sleep 7

echo "   第1次重启..."
stop_cluster
sleep 2
start_cluster

GET_OUT=$($ETCDCTL get multi-restart-key --print-value-only)
if [ -z "$GET_OUT" ]; then
    echo "   [OK] 第1次重启后 key 不存在"
else
    fail "第1次重启后 key 复活了"
fi

echo "   第2次重启..."
stop_cluster
sleep 2
start_cluster

GET_OUT=$($ETCDCTL get multi-restart-key --print-value-only)
if [ -z "$GET_OUT" ]; then
    echo "   [OK] 第2次重启后 key 仍不存在"
else
    fail "第2次重启后 key 复活了"
fi

echo "   第3次重启..."
stop_cluster
sleep 2
start_cluster

GET_OUT=$($ETCDCTL get multi-restart-key --print-value-only)
if [ -z "$GET_OUT" ]; then
    pass "多次重启后过期 lease 始终不复活"
else
    fail "第3次重启后 key 复活了"
fi

# ============================================================
# 清理
# ============================================================
echo ""
echo "============================================================"
echo "   测试结果"
echo "============================================================"
echo "   总计: $TOTAL"
echo "   通过: $PASS"
echo "   失败: $FAIL"
echo "============================================================"

stop_cluster

cd "$pre_dir"
rm -rf raft-lease-test

if [ $FAIL -gt 0 ]; then
    echo ""
    echo "   有 $FAIL 个测试失败!"
    exit 1
else
    echo ""
    echo "   所有测试通过!"
    exit 0
fi
