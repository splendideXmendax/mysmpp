# mysmpp Relay Upgrade Notes

This document records the parts of `mysmpp改造方案.md` implemented in this pass and the deployment notes for the new relay path.

## Implemented

- SMPP bind authentication now reads the current runtime config, so `/v1/config` changes take effect without restarting.
- Admin Basic Auth no longer allows loopback bypass. Admin username and password are always required.
- Secret placeholders named `CHANGE_ME_BEFORE_DEPLOY` are rejected during config validation.
- SMPP `readLoop` no longer polls with a one-second read deadline.
- SMPP `window_size` now throttles concurrent `submit_sm` requests with `ESME_RTHROTTLED`.
- HTTP inbound rule tokens and admin credentials use constant-time string comparison.
- Inbound HTTP rules now require `auth_header` and `auth_token`; unauthenticated callback rules are rejected by config validation and at runtime.
- Inbound DLR rules must declare `provider`, and dispatcher DLR handling rejects callbacks whose provider does not match the pending correlation record.
- HTTP provider response parsing now uses `outbound[].response.id_path` and `id_regex`, while still accepting the old `__response_*` fields for compatibility.
- Messages are queued first and sent by dispatcher workers from an outbox.
- DLR correlation is stored in the shared Store pending map and updates message state on callback.
- Memory Store now implements messages, pending, outbox, idempotency, and queue depth APIs, with opportunistic cleanup for expired pending/idempotency records and a message retention cap.
- HTTP `/v1/messages` validates payloads, supports `client_msg_id` idempotency, optional `clients` token auth, IP allow lists, simple block lists, and simple rate limits.
- HTTP request body sizes are capped for message submission, config updates, and dynamic inbound callbacks.
- SMPP `submit_sm` decoding handles GSM-7 septet padding and accepts common unpacked ASCII payloads when `data_coding=0`.
- Providers can be wrapped with per-provider `rate_limit` settings.
- `/healthz` returns queue and pending checks.
- PostgreSQL schema migrations are provided under `migrations/`.

## Queue Flow

`/v1/messages` and SMPP `submit_sm` both call `Dispatcher.Submit`.

1. Dispatcher matches the route.
2. Dispatcher writes a `queued` message to Store.
3. Dispatcher enqueues an outbox item.
4. Worker goroutines claim pending outbox rows/items.
5. Worker sends to the selected provider.
6. Worker updates the message to `sent`, stores pending DLR correlation, and acks the outbox item.
7. Provider DLR callback updates the message state, deletes pending correlation, and pushes `deliver_sm` for SMPP sources when required.

The current production-ready persistent schema is in SQL. The runtime implementation in this pass keeps Memory Store as the active driver, so the next deployment step is implementing the PostgreSQL Store against this Store interface.

## Config Additions

HTTP clients:

```json
"clients": [
  {
    "client_id": "demo-client",
    "token": "CHANGE_ME_BEFORE_DEPLOY",
    "enabled": true,
    "allowed_ips": ["127.0.0.1/32", "::1/128"]
  }
]
```

Risk controls:

```json
"risk": {
  "blocked_to_prefix": [],
  "blocked_keywords": [],
  "per_number_per_minute": 5,
  "per_number_per_day": 20,
  "per_client_per_second": 100
}
```

Provider rate limit:

```json
"rate_limit": {
  "tps": 200,
  "burst": 400,
  "timeout_ms": 2000
}
```

HTTP response parsing:

```json
"response": {
  "id_path": "data.message_id",
  "id_regex": "MsgID:\\s+([A-Za-z0-9_-]+)"
}
```

Inbound DLR callback rules:

```json
{
  "name": "provider-a-dlr",
  "method": "POST",
  "path": "/callback/provider-a/dlr",
  "provider": "provider-a",
  "auth_header": "X-Token",
  "auth_token": "CHANGE_ME_BEFORE_DEPLOY",
  "fields": {
    "provider_id": "message_id",
    "status": "status",
    "error_code": "error"
  }
}
```

## Required Before Production

- Replace every `CHANGE_ME_BEFORE_DEPLOY` value before starting the service.
- Keep each provider DLR callback on a distinct authenticated inbound rule, and set `provider` to the exact configured provider name.
- Move `storage.driver` to `postgres` after implementing the PostgreSQL Store and applying `migrations/001_init.up.sql`.
- Put the service behind TLS termination if exposed outside a trusted network.
- Add alerts on `/healthz` when `outbox_depth` grows beyond the operational threshold.

## Verification

Run:

```powershell
go test ./...
```

Current focused coverage includes config validation, dispatcher queueing, worker dispatch, pending DLR completion and provider mismatch rejection, HTTP request rendering, SMPP session behavior, SMPP throttling primitives, GSM-7/default-coding decoding edge cases, Memory Store cleanup, and HTTP gateway submission behavior.
