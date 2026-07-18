# Change Report - 2026-07-02

## Scope

This report records the DLR reliability fixes made from `mysmpp-修复文档.md`, the verification performed locally, and the deployment to the test server `REDACTED`.

## Code Changes

- Added multi-provider-ID support for SMPP upstream sends so long-message segments each get a pending DLR mapping.
- Made `SavePending` failure requeue the outbox item instead of acking it and silently losing the DLR mapping.
- Persisted HTTP `callback_url` / `callback_rule` in outbox and pending records, and added active JSON callback delivery for HTTP-originated final DLRs.
- Kept pending mappings for intermediate DLR states such as `ENROUTE` and `ACCEPTD`; mappings are deleted only for final states.
- Decoupled upstream DLR handling from the SMPP upstream read loop with a bounded dispatcher DLR worker queue.
- Protected ready/offline DLR records from pending TTL cleanup and made `FlushDLR` drain ready DLRs in batches.
- Fixed route matching to choose the actual matched longest prefix within priority semantics.
- Replaced HTTP/MO fallback process-local IDs with time-plus-random IDs to avoid cross-restart overwrites.
- Moved SMPP downstream submit handling to a bounded async worker pool and added a short idempotency key for SMPP submit retries.
- Changed SMPP session send to report closed-session failures so pending DLRs are not deleted after a failed enqueue.

## Database Migration

Added migration `003_dlr_reliability`:

- `pending.callback_url`
- `pending.callback_rule`
- `idx_pending_ready_received`

The migration was applied on the test deployment before replacing the application binary.

## Tests

Local verification:

```bash
go test ./...
```

Added regression coverage for:

- HTTP DLR callback dispatch.
- Intermediate DLR state retaining pending mappings.
- Multiple provider IDs creating multiple pending records.
- `SavePending` failures requeueing outbox items.
- Ready DLR records surviving expiry sweeps.
- Actual longest-prefix route matching.

## Test Deployment

Deployment target:

- Host: `REDACTED`
- Runtime: Docker container `mysmpp`
- Database: Docker container `mysmpp-postgres`

Backups created on the server:

- `/root/mysmpp-deploy-backup-20260702-173621`
- `/root/mysmpp-deploy-backup-20260702-173919`

Smoke test:

- Bound as the configured SMPP ESME (`REDACTED`).
- Submitted message `g000000006329`.
- Received downstream DLR `DELIVRD`.

Runtime logs showed the updated service listening on HTTP `19087` and SMPP `29175` after restart.
