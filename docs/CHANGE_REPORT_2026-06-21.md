# Change Report - 2026-06-21

## Scope

This report records the code, deployment, and operational changes made for the `mysmpp` deployment on 2026-06-21. Sensitive credentials are intentionally omitted.

## Repository Baseline

- Branch: `main`
- Upstream repository: `https://github.com/splendideXmendax/mysmpp.git`
- Updated server source from `eda9439` to `cbf25af`
- Added one local code fix after production verification:
  - `internal/dispatch/dispatcher.go`
  - `internal/dispatch/dispatcher_test.go`

## Deployment Changes

### Postgres Storage

The production deployment was migrated from file storage to Postgres.

Before:

```json
{
  "storage": {
    "driver": "file",
    "dsn": "/app/data/store.json"
  }
}
```

After:

```json
{
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:<redacted>@postgres:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

The server now runs a `postgres:16-alpine` container named `mysmpp-postgres` with a Docker named volume for database persistence.

### Data Migration

The previous file store snapshot was imported into Postgres before switching the runtime configuration:

- Messages imported: `328`
- Pending mappings imported: `296`
- Outbox rows imported: `358`
- Gateway ID high-water mark imported: `328`

The old live `store.json` file was removed from active use after the Postgres cutover. Backup copies remain available on the server.

### Backups Created

The following backup directories were created on the server:

- `/root/mysmpp-backup-20260621-204931`
- `/root/mysmpp-pg-migration-20260621-205352`
- `/root/mysmpp-update-20260621-205816`
- `/root/mysmpp-fix-dlr-race-20260621210645`

These backups include runtime config snapshots, old file-store data where available, and deployment files.

## Code Changes

### DLR Pending Lookup Race Fix

Issue found during live SMPP testing:

1. The upstream SMPP provider returned a DLR immediately after `submit_sm_resp`.
2. The DLR was handled before the dispatcher had persisted the `pending` mapping.
3. `HandleDLR` failed with `dlr mapping not found`.
4. The downstream SMPP client did not receive `deliver_sm`.

Fix:

- Added a short DLR lookup wait in `Dispatcher`.
- `HandleDLR` now calls `getPendingForDLR`.
- If the mapping is not found immediately, the dispatcher polls briefly for up to 2 seconds before rejecting the DLR.
- This preserves existing behavior for truly missing mappings while handling fast upstream DLR delivery.

Files changed:

- `internal/dispatch/dispatcher.go`
- `internal/dispatch/dispatcher_test.go`

### Regression Test

Added `TestDispatcherWaitsForPendingWhenDLRArrivesEarly`.

The test simulates a DLR arriving before the pending mapping is saved, then verifies that:

- `HandleDLR` waits for the pending mapping.
- The message state is updated to `DELIVRD`.
- The pending mapping is deleted after handling.

## Operational Notes

- The running server intentionally keeps a local Docker Compose deployment configuration for Postgres.
- Runtime credentials and `.env` files are not committed.
- The `callback_url` feature for HTTP-originated downstream clients is still not implemented. SMPP downstream DLR delivery is implemented and verified.

