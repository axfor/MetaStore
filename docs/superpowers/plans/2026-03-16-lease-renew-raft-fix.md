# LEASE_RENEW Raft Fix Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix Patroni session lease loss in MetaStore by making `LeaseRenew` a replicated Raft operation while preserving etcd-compatible request handling, responses, and error semantics.

**Architecture:** Today `LeaseGrant` and `LeaseRevoke` are replicated through Raft, but `LeaseRenew` updates only the local PebbleDB copy of the lease. The fix is to introduce a `LEASE_RENEW` Raft operation that carries the authoritative `GrantTime`, apply it on every node through the existing Raft commit path, and keep the public etcd lease APIs unchanged so Patroni and other etcd clients remain compatible.

**Tech Stack:** Go, PebbleDB, custom Raft layer, etcd v3 gRPC/HTTP compatibility layer, Patroni integration tests

---

## Problem Summary

Patroni initially works against MetaStore because lease creation is replicated cluster-wide. After some time, Patroni starts receiving `lease not found` from MetaStore and all Patroni nodes eventually report `cluster_unlocked=true`.

The root cause is in the current Pebble-backed lease implementation:

- `LeaseGrant` proposes `LEASE_GRANT` through Raft and waits for commit (`internal/pebbledb/kvstore.go:1290-1342`).
- `LeaseRevoke` proposes `LEASE_REVOKE` through Raft and waits for commit (`internal/pebbledb/kvstore.go:1375-1432`).
- `LeaseRenew` does **not** propose through Raft; it reads the lease and writes a new `GrantTime` directly to the local Pebble instance (`internal/pebbledb/kvstore.go:1719-1745`).
- `applyOperation()` has no `LEASE_RENEW` branch, so renewal never becomes cluster-wide committed state (`internal/pebbledb/kvstore.go:232-305`).
- Lease expiry is based on `GrantTime + TTL` (`internal/kvstore/types.go:198-224`), and leader-driven expiry cleanup operates from the leader node’s store view after syncing leases from storage (`api/etcd/lease_manager.go:225-304`).

This means one node can accept keepalive and update its local `GrantTime`, while the Raft leader still sees an older `GrantTime` and later revokes the lease cluster-wide as expired.

## Constraints

- Preserve etcd-compatible request and response behavior for `LeaseKeepAlive`, `LeaseTimeToLive`, and related errors.
- Do not introduce client-visible API changes.
- Keep the change narrow: fix correctness first, accept the extra Raft write cost for the short-term solution.
- Keep `GrantTime` deterministic across nodes by carrying it inside the Raft operation payload instead of recomputing it at apply time.

## File Structure

- Modify: `internal/pebbledb/kvstore.go`
  - Extend `RaftOperation` to cover `LEASE_RENEW`
  - Convert `LeaseRenew` to the same propose/wait/apply model used by `LeaseGrant` and `LeaseRevoke`
  - Add the internal apply helper that updates `GrantTime` using the committed timestamp
- Modify: `test/lease_cross_node_test.go`
  - Strengthen the existing cross-node lease test so it fails if renewals are not replicated
- Create: `test/lease_renew_replication_test.go`
  - Add focused regression coverage for cross-node renewal, expiry safety, and revoke-vs-renew ordering
- Modify: `internal/pebbledb/batch_lease_test.go`
  - Add deterministic batch-ordering coverage for `LEASE_RENEW`
- Modify: `api/etcdgateway/gateway_test.go`
  - Add etcd-visible lease compatibility checks for missing leases
- Verify only: `api/etcd/lease.go`
  - Confirm `LeaseKeepAlive` and `LeaseTimeToLive` continue to expose etcd-compatible behavior without public API changes

## Chunk 1: Replicated lease renewal

### Task 1: Make the existing cross-node lease test expose the bug

**Files:**
- Modify: `test/lease_cross_node_test.go:28-62`
- Test: `test/lease_cross_node_test.go`

- [ ] **Step 1: Write the failing renewal-persistence assertions**

Shorten the granted TTL and add assertions after `KeepAliveOnce` so the test crosses the original pre-renew expiry boundary:

```go
resp, err := clus.clients[leaderIdx].Grant(ctx, 5)
require.NoError(t, err)
leaseID := resp.ID

_, err = clus.clients[leaderIdx].Put(ctx, "cross-node/lease", "value", clientv3.WithLease(leaseID))
require.NoError(t, err)

time.Sleep(2 * time.Second)
keepAliveResp, err := clus.clients[followerIdx].KeepAliveOnce(ctx, leaseID)
require.NoError(t, err)
assert.Equal(t, leaseID, keepAliveResp.ID)
assert.Greater(t, keepAliveResp.TTL, int64(0))

time.Sleep(4 * time.Second)

getResp, err := clus.clients[leaderIdx].Get(ctx, "cross-node/lease")
require.NoError(t, err)
assert.Len(t, getResp.Kvs, 1)

keepAliveResp, err = clus.clients[leaderIdx].KeepAliveOnce(ctx, leaseID)
require.NoError(t, err)
assert.Equal(t, leaseID, keepAliveResp.ID)
```

- [ ] **Step 2: Run the targeted test to verify it fails**

Run:

```bash
go test -v -timeout=20m -run TestLeaseOperationsAcrossNodes_PebbleCluster ./test
```

Expected: FAIL because the key is deleted or the second `KeepAliveOnce` returns `lease not found` once the original 5-second `GrantTime` window expires on the leader-side view.

- [ ] **Step 3: Commit the failing test**

```bash
git add test/lease_cross_node_test.go
git commit -m "test: expose unreplicated lease renewal bug"
```

### Task 2: Add `LEASE_RENEW` to the Raft write path

**Files:**
- Modify: `internal/pebbledb/kvstore.go:99-116`
- Modify: `internal/pebbledb/kvstore.go:232-390`
- Modify: `internal/pebbledb/kvstore.go:913-980`
- Modify: `internal/pebbledb/kvstore.go:1719-1768`

- [ ] **Step 1: Add a new Raft operation type and apply branch**

Update the `RaftOperation` comment, `applyOperation()`, and `applyOperationsBatch()` so `LEASE_RENEW` is handled on both the single-entry and batched commit paths:

```go
type RaftOperation struct {
    Type string `json:"type"` // "PUT", "DELETE", "LEASE_GRANT", "LEASE_REVOKE", "LEASE_RENEW", "TXN"
    // ...
}
```

```go
case "LEASE_RENEW":
    if err := r.leaseRenewUnlockedWithTime(op.LeaseID, op.GrantTime); err != nil {
        log.Error("Failed to apply LEASE_RENEW operation",
            zap.Error(err),
            zap.Int64("leaseID", op.LeaseID),
            zap.String("component", "storage-pebble"))
    }
```

```go
case "LEASE_RENEW":
    if err := r.prepareLeaseRenewBatchWithTime(batch, op.LeaseID, op.GrantTime); err != nil {
        log.Error("Failed to prepare LEASE_RENEW in batch",
            zap.Error(err),
            zap.Int64("leaseID", op.LeaseID),
            zap.String("component", "storage-pebble"))
        continue
    }
    batchDirty = true
```

- [ ] **Step 2: Replace local-write `LeaseRenew` with propose/wait/apply**

Refactor `LeaseRenew` to match the `LeaseGrant` and `LeaseRevoke` flow:

```go
func (r *PebbleDB) LeaseRenew(ctx context.Context, id int64) (*kvstore.Lease, error) {
    seq := r.seqNum.Add(1)
    seqNum := fmt.Sprintf("seq-%d", seq)

    waitCh := make(chan struct{})
    r.pendingMu.Lock()
    r.pendingOps[seqNum] = waitCh
    r.pendingMu.Unlock()

    cleanup := func() {
        r.pendingMu.Lock()
        delete(r.pendingOps, seqNum)
        r.pendingMu.Unlock()
    }

    op := RaftOperation{
        Type:      "LEASE_RENEW",
        LeaseID:   id,
        GrantTime: timeNow().UnixNano(),
        SeqNum:    seqNum,
    }

    data, err := marshalRaftOperation(&op)
    if err != nil {
        cleanup()
        return nil, err
    }
    if err := r.propose(ctx, data); err != nil {
        cleanup()
        return nil, err
    }

    select {
    case <-waitCh:
        return r.getLease(id)
    case <-ctx.Done():
        cleanup()
        return nil, ctx.Err()
    case <-time.After(30 * time.Second):
        cleanup()
        return nil, fmt.Errorf("timeout waiting for Raft commit")
    }
}
```

- [ ] **Step 3: Add the apply helper that preserves committed `GrantTime`**

Implement helpers that update only an existing lease and use the Raft-supplied timestamp for both direct apply and batched apply:

```go
func (r *PebbleDB) leaseRenewUnlockedWithTime(id int64, grantTimeNano int64) error {
    lease, err := r.getLease(id)
    if err != nil {
        return err
    }
    if lease == nil {
        return fmt.Errorf("lease not found: %d", id)
    }

    if grantTimeNano > 0 {
        lease.GrantTime = time.Unix(0, grantTimeNano)
    } else {
        lease.GrantTime = timeNow()
    }

    data, err := common.SerializeLease(lease)
    if err != nil {
        return err
    }

    dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, id))
    return r.db.Set(dbKey, data, r.wo)
}
```

```go
func (r *PebbleDB) prepareLeaseRenewBatchWithTime(batch *pebble.Batch, leaseID int64, grantTimeNano int64) error {
    lease, err := r.getLeaseFromReader(batch, leaseID)
    if err != nil {
        return err
    }
    if lease == nil {
        return fmt.Errorf("lease not found: %d", leaseID)
    }

    if grantTimeNano > 0 {
        lease.GrantTime = time.Unix(0, grantTimeNano)
    } else {
        lease.GrantTime = timeNow()
    }

    data, err := common.SerializeLease(lease)
    if err != nil {
        return err
    }

    dbKey := []byte(fmt.Sprintf("%s%d", leasePrefix, leaseID))
    return batch.Set(dbKey, data, nil)
}
```

- [ ] **Step 4: Run the targeted cross-node test again**

Run:

```bash
go test -v -timeout=20m -run TestLeaseOperationsAcrossNodes_PebbleCluster ./test
```

Expected: PASS; the renewed lease remains alive and the key is still attached when checked from the leader side.

- [ ] **Step 5: Commit the renewal implementation**

```bash
git add internal/pebbledb/kvstore.go test/lease_cross_node_test.go
git commit -m "fix: replicate lease renewals through raft"
```

### Task 3: Add TTL, batch-ordering, and etcd-compatibility regression coverage

**Files:**
- Create: `test/lease_renew_replication_test.go`
- Modify: `internal/pebbledb/batch_lease_test.go`
- Modify: `api/etcdgateway/gateway_test.go`
- Verify: `api/etcd/lease.go:68-135`

- [ ] **Step 1: Add a regression test proving leader expiry cleanup no longer revokes a renewed lease**

Create a focused integration test:

```go
func TestLeaseRenewReplicationPreventsLeaderExpiryCleanup(t *testing.T) {
    clus := newEtcdPebbleCluster(t, 3)
    defer clus.Close(t)

    ctx := context.Background()
    leaderIdx, followerIdx := findLeaderFollower(t, clus)

    resp, err := clus.clients[leaderIdx].Grant(ctx, 5)
    require.NoError(t, err)
    leaseID := resp.ID

    _, err = clus.clients[leaderIdx].Put(ctx, "renew/guard", "value", clientv3.WithLease(leaseID))
    require.NoError(t, err)

    time.Sleep(2 * time.Second)
    _, err = clus.clients[followerIdx].KeepAliveOnce(ctx, leaseID)
    require.NoError(t, err)

    time.Sleep(4 * time.Second)
    getResp, err := clus.clients[leaderIdx].Get(ctx, "renew/guard")
    require.NoError(t, err)
    assert.Len(t, getResp.Kvs, 1)

    _, err = clus.clients[leaderIdx].KeepAliveOnce(ctx, leaseID)
    require.NoError(t, err)
}
```

- [ ] **Step 2: Add deterministic batch-ordering tests for `LEASE_RENEW`**

Extend `internal/pebbledb/batch_lease_test.go` with deterministic ordering coverage so later operations in the same batch see earlier lease mutations:

```go
func TestPebbleDB_ApplyOperationsBatch_LeaseGrantRenewThenPut(t *testing.T) {
    store, cleanup := createTestStore(t, "test-batch-lease-grant-renew-put")
    defer cleanup()

    renewedAt := time.Now().Add(10 * time.Second).UnixNano()
    store.applyOperationsBatch([]*RaftOperation{
        {Type: "LEASE_GRANT", LeaseID: 404, TTL: 30},
        {Type: "LEASE_RENEW", LeaseID: 404, GrantTime: renewedAt},
        {Type: "PUT", Key: "batch/renew", Value: "value", LeaseID: 404},
    })

    lease, err := store.getLease(404)
    require.NoError(t, err)
    require.NotNil(t, lease)
    assert.Equal(t, time.Unix(0, renewedAt), lease.GrantTime)
    assert.True(t, lease.Keys["batch/renew"])
}

func TestPebbleDB_ApplyOperationsBatch_LeaseGrantRevokeThenRenewKeepsLeaseDeleted(t *testing.T) {
    store, cleanup := createTestStore(t, "test-batch-lease-grant-revoke-renew")
    defer cleanup()

    store.applyOperationsBatch([]*RaftOperation{
        {Type: "LEASE_GRANT", LeaseID: 405, TTL: 30},
        {Type: "LEASE_REVOKE", LeaseID: 405},
        {Type: "LEASE_RENEW", LeaseID: 405, GrantTime: time.Now().UnixNano()},
    })

    lease, err := store.getLease(405)
    assert.Error(t, err)
    assert.Nil(t, lease)
}
```

- [ ] **Step 3: Add explicit etcd-visible compatibility tests for missing leases**

Extend both the Pebble/Raft integration tests and `api/etcdgateway/gateway_test.go` so the documented etcd-compatible semantics are guarded on the actual implementation path and through the HTTP gateway:

```go
func TestLeaseMissingLeaseSemantics_PebbleCluster(t *testing.T) {
    clus := newEtcdPebbleCluster(t, 3)
    defer clus.Close(t)

    ctx := context.Background()
    leaderIdx, followerIdx := findLeaderFollower(t, clus)
    missingLeaseID := clientv3.LeaseID(999999)

    _, err := clus.clients[followerIdx].KeepAliveOnce(ctx, missingLeaseID)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "lease not found")

    ttlResp, err := clus.clients[leaderIdx].TimeToLive(ctx, missingLeaseID)
    require.NoError(t, err)
    assert.Equal(t, int64(-1), ttlResp.TTL)
    assert.Equal(t, int64(0), ttlResp.GrantedTTL)
}
```

```go
func TestHTTPLeaseTimeToLiveMissingLeaseCompatibleResponse(t *testing.T) {
    _, httpSrv := newGatewayTestServer(t)

    body := postJSON(t, httpSrv.URL+"/v3/lease/timetolive", `{"ID":"999999"}`)

    var resp map[string]any
    require.NoError(t, json.Unmarshal(body, &resp), string(body))

    result := resp["result"].(map[string]any)
    require.Equal(t, float64(-1), result["TTL"])
    require.Equal(t, float64(0), result["grantedTTL"])
}
```

Add a companion missing-lease keepalive test that verifies the gateway still returns the existing etcd-compatible error document instead of a success payload:

```go
func TestHTTPLeaseKeepAliveMissingLeaseReturnsCompatibleError(t *testing.T) {
    _, httpSrv := newGatewayTestServer(t)

    req, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/v3/lease/keepalive", bytes.NewBufferString(`{"ID":"999999"}`))
    require.NoError(t, err)
    req.Header.Set("Content-Type", "application/json")

    resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()
    require.Equal(t, http.StatusOK, resp.StatusCode)

    payload, err := io.ReadAll(resp.Body)
    require.NoError(t, err)

    var errResp map[string]any
    require.NoError(t, json.Unmarshal(payload, &errResp), string(payload))

    result := errResp["result"].(map[string]any)
    require.Equal(t, float64(5), result["code"])
    require.Equal(t, "lease not found", result["message"])
}
```

- [ ] **Step 4: Run the focused lease regression suite**

Run:

```bash
go test -v -timeout=20m -run 'TestLeaseOperationsAcrossNodes_PebbleCluster|TestLeaseRenew|TestLeaseMissingLeaseSemantics_PebbleCluster' ./test
go test -v -timeout=10m ./internal/pebbledb/ -run 'TestPebbleDB_ApplyOperationsBatch_LeaseGrantRenewThenPut|TestPebbleDB_ApplyOperationsBatch_LeaseGrantRevokeThenRenewKeepsLeaseDeleted'
go test -v -timeout=10m ./api/etcdgateway/ -run 'TestHTTPLeaseKeepAliveReturnsSingleJSONDocument|TestHTTPLeaseTimeToLiveMissingLeaseCompatibleResponse|TestHTTPLeaseKeepAliveMissingLeaseReturnsCompatibleError'
```

Expected: PASS for the strengthened cross-node test, the new renewal regression tests (including Pebble missing-lease semantics), the batch-ordering tests, and the gateway compatibility tests.

- [ ] **Step 5: Verify etcd-compatible API behavior remains unchanged**

Run the broader integration suite after the focused checks:

```bash
make test
```

Expected:

- lease API/gateway tests still pass without response shape changes
- full repository test suite passes

- [ ] **Step 6: Commit the regression coverage**

```bash
git add test/lease_renew_replication_test.go internal/pebbledb/batch_lease_test.go api/etcdgateway/gateway_test.go
git commit -m "test: cover replicated lease renew regressions"
```

## Appendix: Manual Patroni smoke test (non-gating)

If the implementer has access to the same local environment used during investigation, they can additionally rerun the Patroni smoke test after the code and automated tests are complete. This appendix is intentionally non-gating because it depends on machine-local paths and a pre-existing MetaStore cluster outside the repository automation.
