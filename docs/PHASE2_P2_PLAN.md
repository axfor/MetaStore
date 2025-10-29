# Phase 2 - P2 (Nice-to-Have) - Implementation Plan

**Status**: 📋 Planning
**Priority**: P2 (Nice-to-Have)
**Estimated Time**: 8-12 hours
**Goal**: Production polish and operational convenience

---

## Overview

Phase 2 - P2 focuses on **production polish and operational convenience** - features that make the system easier to operate and maintain, but are not critical for core functionality.

**Guiding Principle**: High value-to-effort ratio. Only implement features that significantly improve operational experience with minimal complexity.

---

## Proposed Tasks

### 1. Auto-Compaction Worker (High Value) ⭐
**Priority**: High | **Time**: 2-3 hours | **Value**: Eliminates manual maintenance

**What**:
- Background goroutine for periodic compaction
- Configurable retention policy (keep N revisions)
- Graceful start/stop
- Prometheus metrics for compaction events

**Implementation**:
```go
type AutoCompactor struct {
    store          *RocksDB
    keepRevisions  int64
    checkInterval  time.Duration
    stopCh         chan struct{}
}

func (ac *AutoCompactor) Start() {
    ticker := time.NewTicker(ac.checkInterval)
    for {
        select {
        case <-ticker.C:
            ac.maybeCompact()
        case <-ac.stopCh:
            return
        }
    }
}
```

**Benefits**:
- ✅ Zero manual intervention
- ✅ Consistent space management
- ✅ Configurable policies

**Config**:
```yaml
server:
  compaction:
    auto_compact: true
    keep_revisions: 100000
    check_interval: 1h
```

---

### 2. Health Check Endpoint (High Value) ⭐
**Priority**: High | **Time**: 1-2 hours | **Value**: Essential for load balancers

**What**:
- `/health` endpoint with detailed status
- `/readiness` for Kubernetes readiness probes
- `/liveness` for Kubernetes liveness probes
- Check store availability, Raft status, disk space

**Implementation**:
```go
GET /health
{
  "status": "healthy",
  "timestamp": "2025-01-XX 10:30:00",
  "checks": {
    "store": "ok",
    "raft": "ok (leader)",
    "disk": "ok (75% used)"
  }
}

GET /readiness  → 200 OK (ready) or 503 Service Unavailable
GET /liveness   → 200 OK (alive) or 503 Service Unavailable
```

**Benefits**:
- ✅ Kubernetes integration
- ✅ Load balancer health checks
- ✅ Monitoring integration

---

### 3. Per-Method Rate Limiting (Medium Value)
**Priority**: Medium | **Time**: 2-3 hours | **Value**: Better DoS protection

**What**:
- Different rate limits for different methods
- Separate limits for read vs write operations
- Per-client rate limiting (by IP or auth token)

**Implementation**:
```yaml
server:
  grpc:
    rate_limits:
      - method: "/etcdserverpb.KV/Put"
        qps: 5000
        burst: 10000
      - method: "/etcdserverpb.KV/Range"
        qps: 20000
        burst: 40000
```

**Benefits**:
- ✅ Better resource protection
- ✅ Fair allocation across operations
- ✅ Prevent single operation type from monopolizing

---

### 4. Configuration Validation Enhancement (Medium Value)
**Priority**: Medium | **Time**: 1-2 hours | **Value**: Prevents misconfigurations

**What**:
- Comprehensive validation at startup
- Check for common misconfigurations
- Warn about non-optimal settings
- Validate file permissions, directory existence

**Implementation**:
```go
func (cfg *Config) Validate() error {
    // Check required fields
    if cfg.Server.ClusterID == 0 {
        return errors.New("cluster_id is required")
    }

    // Check logical consistency
    if cfg.Server.GRPC.RateLimitQPS > cfg.Server.GRPC.RateLimitBurst {
        return errors.New("rate_limit_qps cannot exceed burst")
    }

    // Check file permissions
    if cfg.Server.DataDir != "" {
        if !isWritable(cfg.Server.DataDir) {
            return errors.New("data_dir is not writable")
        }
    }

    // Warn about non-optimal settings
    if cfg.Server.GRPC.MaxConnections > 50000 {
        log.Warn("max_connections is very high, may impact memory usage")
    }

    return nil
}
```

**Benefits**:
- ✅ Fail fast on misconfiguration
- ✅ Clear error messages
- ✅ Prevents production issues

---

### 5. Request Tracing (Low-Medium Value)
**Priority**: Low-Medium | **Time**: 3-4 hours | **Value**: Advanced debugging

**What**:
- OpenTelemetry integration
- Distributed trace IDs
- Span propagation across Raft nodes
- Trace sampling configuration

**Implementation**:
```yaml
server:
  tracing:
    enabled: true
    sampling_rate: 0.01  # Sample 1% of requests
    exporter: "jaeger"
    endpoint: "jaeger-collector:14268"
```

**Benefits**:
- ✅ Debug complex request flows
- ✅ Identify performance bottlenecks
- ✅ Correlate logs across nodes

**Cost**:
- ⚠️ Additional dependencies (OpenTelemetry)
- ⚠️ Slight performance overhead
- ⚠️ Requires external tracing backend

---

### 6. Batch Operations Optimization (Low Value)
**Priority**: Low | **Time**: 4-5 hours | **Value**: Niche use case

**What**:
- Optimized batch Put/Delete
- Single Raft proposal for multiple operations
- Reduced round-trips

**Implementation**:
```go
func (r *RocksDB) BatchPut(kvs []KeyValue) error {
    // Create single Raft operation with multiple KVs
    op := RaftOperation{
        Type: "BATCH_PUT",
        Batch: kvs,
    }
    // Submit as single proposal
}
```

**Benefits**:
- ✅ Better throughput for batch writes
- ✅ Lower Raft overhead

**Cost**:
- ⚠️ Complex implementation
- ⚠️ Most clients don't need this

---

## Recommended Implementation Order

### Phase 2 - P2 (Recommended)
1. **Auto-Compaction Worker** (2-3h) ⭐ - High value, low complexity
2. **Health Check Endpoint** (1-2h) ⭐ - Essential for production
3. **Configuration Validation** (1-2h) ⭐ - Prevents issues

**Total**: 4-7 hours for core P2 features

### Optional (If Time Permits)
4. **Per-Method Rate Limiting** (2-3h) - Good but not critical
5. **Request Tracing** (3-4h) - Advanced, requires infrastructure

### Not Recommended
6. **Batch Operations** - Low value, high complexity

---

## Decision: Implement Top 3

For maximum value with minimal complexity, I recommend implementing only the **top 3 tasks**:

1. ✅ Auto-Compaction Worker
2. ✅ Health Check Endpoint
3. ✅ Configuration Validation Enhancement

**Rationale**:
- These 3 provide the most operational value
- Low implementation complexity (4-7 hours total)
- No external dependencies
- Immediate production benefit

**Skip for now**:
- Per-method rate limiting (nice-to-have, current global limit sufficient)
- Request tracing (requires infrastructure, can add later if needed)
- Batch operations (niche use case, premature optimization)

---

## Implementation Plan

### Task 1: Auto-Compaction Worker
**Files**:
- `internal/rocksdb/autocompact.go` (new, ~150 lines)
- `internal/rocksdb/autocompact_test.go` (new, ~100 lines)
- `pkg/config/config.go` (modify, +15 lines)

**Tests**: Start/stop, periodic trigger, metrics

---

### Task 2: Health Check Endpoint
**Files**:
- `pkg/health/health.go` (new, ~200 lines)
- `pkg/health/health_test.go` (new, ~80 lines)
- `cmd/metastore/main.go` (modify, +20 lines)

**Endpoints**: `/health`, `/readiness`, `/liveness`

---

### Task 3: Configuration Validation
**Files**:
- `pkg/config/validation.go` (new, ~250 lines)
- `pkg/config/validation_test.go` (new, ~150 lines)

**Validations**: Required fields, logical consistency, file permissions, warnings

---

## Success Criteria

After Phase 2 - P2 completion:
- ✅ Zero-config auto-compaction working
- ✅ Health checks pass in Kubernetes
- ✅ Misconfigurations caught at startup
- ✅ All tests passing
- ✅ Production readiness: **99.5/100**

---

## Appendix: Deferred Features

Features considered but deferred (can implement later if needed):

1. **Configuration Hot Reload** - Requires complex state management
2. **Memory Usage Tracking** - Prometheus metrics already cover this
3. **Advanced Logging** - Current structured logging sufficient
4. **Client Connection Pooling** - Client responsibility
5. **Query Result Caching** - Premature optimization

---

*Plan Created: 2025-01-XX*
*Decision: Implement Top 3 Tasks*
*Estimated Time: 4-7 hours*
