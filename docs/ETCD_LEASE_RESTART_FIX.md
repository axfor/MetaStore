# Fix: etcd Lease Recovery After Server Restart

## Problem

When MetaStore server restarts, etcd leases become unusable even though they are persisted in Pebble.

### Root Cause

The `LeaseManager` (`api/etcd/lease_manager.go`) creates a fresh empty `leases` map on startup via `NewLeaseManagerWithNodeID()`. Although leases are persisted in Pebble (with `lease:` prefix) and recovered via Raft snapshots, the `LeaseManager` never loads them into its in-memory map on startup.

This causes the following issues:

1. **KeepAlive fails** — `Renew()` checks `lm.leases` map first, returns `ErrLeaseNotFound` immediately without reaching the store
2. **TimeToLive fails** — same check pattern, returns `ErrLeaseNotFound`
3. **Expiry checker useless** — iterates empty `lm.leases` map, never finds expired leases to clean up
4. **Lease ID collision** — `leaseIDCounter` starts at 0, could collide with existing lease IDs

### Impact

- Clients that created leases before the restart cannot renew them (KeepAlive breaks)
- Leases that should expire during downtime are never cleaned up
- Keys attached to orphaned leases remain indefinitely
- New lease IDs may collide with existing ones

## Solution

### Added `LoadLeases()` method

A new `LoadLeases()` method on `LeaseManager` that:

1. Calls `store.Leases(ctx)` to read all persisted leases from Pebble
2. Populates the in-memory `lm.leases` map with each recovered lease
3. Extracts the lower 48 bits of each lease ID to find the max counter value
4. Sets `leaseIDCounter` to the max counter value to prevent ID collisions

Already-expired leases are loaded into the map and handled naturally by the `expiryChecker` on its first tick, avoiding Raft operations during startup.

### Modified `Start()` method

`Start()` now calls `LoadLeases()` before starting the expiry checker goroutine. If `LoadLeases()` fails, it logs the error and continues (degraded mode).

## Files Changed

- `api/etcd/lease_manager.go` — added `LoadLeases()` method, modified `Start()`
- `api/etcd/lease_manager_test.go` — new test file with 4 test cases

## Test Cases

| Test | Description |
|------|-------------|
| `TestLeaseManager_LoadLeases_Empty` | LoadLeases succeeds with no persisted leases |
| `TestLeaseManager_LoadLeases_Recovery` | Leases created in store are recovered; Renew/TimeToLive work after LoadLeases |
| `TestLeaseManager_LoadLeases_CounterInit` | GenerateLeaseID produces IDs with counters higher than recovered max |
| `TestLeaseManager_LoadLeases_ExpiredHandling` | Expired leases are loaded then cleaned up by checkExpiredLeases |

## Recovery Flow After Fix

```
Server Restart
    │
    ├── Pebble opens, loads snapshot (leases persisted as "lease:ID")
    │
    ├── LeaseManager.Start() called
    │   ├── LoadLeases()
    │   │   ├── store.Leases() → reads all "lease:" entries from Pebble
    │   │   ├── Populates lm.leases map
    │   │   └── Sets leaseIDCounter = max(existing counters)
    │   │
    │   └── go expiryChecker()
    │       └── First tick: revokes any leases expired during downtime
    │
    └── Clients can now KeepAlive their existing leases
```
