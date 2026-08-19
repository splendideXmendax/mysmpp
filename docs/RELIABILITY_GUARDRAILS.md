# Reliability Guardrails

This note documents the safeguards added for the issues reviewed in `PENDING_ISSUES_REVIEW.md`.

## Destination Address Validation

`dispatcher.validate_dest_addr` defaults to `true`.

Before route matching and before writing to `messages` or `outbox`, mysmpp validates the destination address:

- optional leading `+`
- digits only after `+` removal
- total E.164 length from 4 to 15 digits
- assigned 1-3 digit E.164 country calling code

Invalid destinations are rejected immediately. SMPP submitters receive `ESME_RINVDSTADR (0x0B)`, and the message is not queued or retried.

This is intentionally a coarse E.164 guard. It does not validate every country's national numbering plan.

## Route Address Rewrite

Route-level `addr_rewrite` is optional and defaults to pass-through.

```json
{
  "name": "cn",
  "prefix": ["86"],
  "provider": "provider-a",
  "priority": 100,
  "addr_rewrite": {
    "strip_trunk_zero_after_cc": true,
    "country_code": "86",
    "add_prefix": "",
    "enforce_e164_len": true
  }
}
```

`strip_trunk_zero_after_cc=true` rewrites `860015013628000` to `8615013628000`.

Keep this disabled unless the upstream contract explicitly says national trunk zeroes must be removed. Some providers expect the original number, so the default remains pass-through.

## Stale Outbox Claim Recovery

`dispatcher.claim_timeout` defaults to `60s`.

If a worker crashes before starting the provider call, the row remains `claimed`. The dispatcher periodically requeues only these stale `claimed` rows after `claim_timeout`.

This applies to Postgres, file, and memory stores. For production use, prefer Postgres so recovery survives process and host restarts.

Before invoking a provider, the worker persists `sending`. A successful provider result is completed through one Store operation that writes every pending DLR row, updates the message, and marks the outbox `done`. Ambiguous provider errors and exhausted completion writes become `uncertain`/`UNKNOWN`; `sending` and `uncertain` are never automatically requeued. This is intentionally fail-closed to avoid duplicate upstream delivery.

To reduce harmless claim churn while workers are saturated, a practical sizing guideline is:

```text
ceil(dispatcher.claim_limit / dispatcher.per_worker_concurrency) * max_provider_response_timeout
```

This is an operational recommendation, not a correctness requirement. Store ownership checks and the persisted `sending` transition prevent duplicate provider entry. Upstream-side idempotency is still recommended as defense in depth. Operators should investigate `sending` and `uncertain` rows manually instead of changing them back to `pending` without upstream evidence.

Expired pending DLR mappings are swept by a background loop controlled by `dispatcher.pending_sweep_interval` instead of being deleted on every DLR lookup.

## Idempotent Submit

HTTP submits with the same `(client_id, client_msg_id)` are guarded inside the store transaction.

The idempotency row is inserted first with `ON CONFLICT DO NOTHING`. If the key already exists, the transaction returns the original `gateway_id` and does not create another message or outbox row. This protects concurrent client retries from duplicate upstream sends and duplicate billing.

## Memory Storage Warning

`storage.driver=memory` is volatile. Messages, outbox rows, pending DLR mappings, idempotency keys, daily quota usage, and allocated IDs are lost on restart.

On startup mysmpp logs a warning when memory storage is active. Use memory only for local development; use `postgres` for production and restart recovery.
