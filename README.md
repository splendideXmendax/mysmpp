# mysmpp

`mysmpp` is a compact SMS gateway MVP that accepts SMPP and HTTP submissions, routes messages to upstream providers, persists delivery state, and pushes SMPP delivery receipts back to bound ESME sessions.

The codebase is no longer just a protocol skeleton. It includes SMPP bind/submit/enquire/unbind handling, deliver_sm DLR construction, selected TLV parsing/rendering, HTTP outbound rules, HTTP inbound DLR callbacks, an admin UI, idempotency, basic risk controls, and memory/file/Postgres storage.

## Features

- SMPP 3.4 server for `bind_receiver`, `bind_transmitter`, `bind_transceiver`, `submit_sm`, `enquire_link`, and `unbind`.
- SMPP DLR push via `deliver_sm`, including receipt text, `receipted_message_id`, and `message_state` TLVs.
- Session limits, per-system-id session limits, submit window throttling, bind timeout, and idle session timeout.
- HTTP `/v1/messages` submission and listing with client token auth.
- Prefix/priority routing to named providers.
- Configurable HTTP outbound request rendering for JSON, form, query, and custom headers.
- Inbound HTTP rules for provider callbacks and DLR state updates.
- Mock provider for local SMPP-to-DLR testing.
- Memory, JSON file, and Postgres storage. Postgres outbox uses `FOR UPDATE SKIP LOCKED` for concurrent workers.
- Dispatcher worker pool with configurable per-worker upstream concurrency.
- Admin UI with username/password login, login throttling, config editing, and persisted runtime config.
- Idempotency keys for HTTP submissions and simple in-process risk controls.

## Important Limits

- Long SMS splitting is currently metadata only on the outbound path. `message.Split` records segments, but the dispatcher sends the original full text to HTTP providers. Upstreams must accept long text or implement their own splitting. SMPP upstream submission is not implemented yet.
- Risk counters are process-local. Multiple replicas multiply the effective limits unless you add Redis/Postgres-backed counters.
- Gateway IDs are process-local counters. They are collision-safe within one process, but a full production design should move ID allocation into the storage layer.
- Pending DLR cleanup is lazy, not a dedicated background maintenance job.
- GSM-7 decoding follows the common choice of trimming the ambiguous trailing `@` padding case when byte length is divisible by 7.
- HTTP provider DLRs enter through inbound callback rules. `HTTPProvider.OnDLR` is intentionally a no-op.

## Layout

```text
cmd/mysmpp        gateway entrypoint
cmd/testesme      local SMPP ESME test client
internal/admin    admin UI, sessions, CSRF, config persistence
internal/config   JSON config, defaults, validation, bootstrap secrets
internal/dispatch submit pipeline, outbox workers, pending DLR mapping
internal/httpgw   HTTP API, inbound rules, risk checks
internal/httprule HTTP request rendering helpers
internal/message  message model, encoding detection, splitting, GSM-7/UCS-2
internal/provider upstream provider interface, HTTP/mock providers, rate limit
internal/router   prefix/priority routing
internal/smpp     SMPP PDU/session/submit/DLR/TLV handling
internal/store    memory, file, and Postgres stores
configs           local, Docker, and production examples
docs              extra deployment and configuration notes
```

## Local Run

```powershell
go run ./cmd/mysmpp
```

Default local endpoints from `configs/example.json`:

- HTTP/API/Admin: `http://127.0.0.1:19087`
- SMPP: `127.0.0.1:29175`
- Admin login: `admin` / `mysmpp-admin-19087`
- Example ESME bind: `system_id=dev-esme`, `password=mysmpp-esme-29175`

You can also pass a config explicitly:

```powershell
go run ./cmd/mysmpp -config configs/example.json
```

Admin UI:

```text
http://127.0.0.1:19087/admin/
```

## SMPP DLR Smoke Test

Start the gateway, then run:

```powershell
go run ./cmd/testesme -text ping
```

Expected flow:

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 ... stat:DELIVRD err:000 text:ping
```

This uses the built-in mock provider, so no real SMS vendor is required.

## HTTP API

Submit an MT message:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:19087/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello"}'
```

If `clients` are configured, include:

```text
X-Client-ID: <client_id>
X-Token: <token>
```

List messages:

```powershell
Invoke-RestMethod http://127.0.0.1:19087/v1/messages `
  -Headers @{"X-Client-ID"="demo-client"; "X-Token"="..."}
```

## Config Highlights

- `dispatcher.workers`, `dispatcher.per_worker_concurrency`, `dispatcher.claim_limit`, and `dispatcher.poll_interval_ms` control outbox throughput.
- `providers[].http_timeout_ms` controls the real upstream HTTP client timeout. `providers[].rate_limit.timeout_ms` controls how long a request waits for a rate-limit token.
- `trusted_proxies` controls whether `X-Forwarded-For` / `X-Real-IP` are trusted for client IP allowlists.
- `storage.driver` can be `memory`, `file`, or `postgres`.
- `configs/production.example.json` is tuned toward a single-node 300 TPS deployment and expects Postgres.

For details, see [docs/CONFIGURATION.md](docs/CONFIGURATION.md), [docs/DOCKER.md](docs/DOCKER.md), and [docs/QUICKSTART_DOCKER.md](docs/QUICKSTART_DOCKER.md).

## Docker

```powershell
docker compose up -d --build
```

The Docker image seeds `/app/data/config.json` from `configs/docker.json` on first startup and persists runtime config/data under `/app/data`.

## Postgres

Production should use Postgres rather than memory/file storage:

```json
{
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:CHANGE_ME@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

Run `migrations/001_init.up.sql` before startup.

## Tests

```powershell
go test ./...
```

Current tests cover config loading/validation, admin login/config editing, HTTP submissions and inbound rules, idempotency, provider request rendering and ID extraction, message splitting/codecs, SMPP session flows, DLR PDU/TLV handling, dispatcher routing/outbox behavior, and memory store behavior.

## Roadmap

1. True outbound multipart sending for HTTP providers and future SMPP upstream providers.
2. Storage-backed gateway ID allocation.
3. Distributed risk counters.
4. Metrics and audit logging.
5. Background maintenance for pending/outbox retention and message archival.
6. MO delivery to downstream SMPP/HTTP clients.
