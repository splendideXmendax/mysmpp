# mysmpp

`mysmpp` is a Go SMS gateway skeleton for SMPP and configurable HTTP integrations. The first version focuses on a clean service boundary: SMPP sessions, message segmentation, HTTP inbound callbacks, HTTP outbound rule rendering, routing, and pluggable storage.

## Goals

- Accept SMPP client connections and expose an HTTP API for message submission.
- Handle short SMS and long SMS segmentation for GSM-7 and UCS-2 text.
- Support different HTTP provider rules for inbound callbacks and outbound delivery.
- Keep routing and provider definitions config-driven.
- Leave clear extension points for DLR, retry queues, persistent storage, and full SMPP TLV support.

## Current Shape

```text
cmd/mysmpp          process entrypoint
internal/config     JSON config model and loader
internal/httpgw     HTTP API and configurable inbound/outbound rule helpers
internal/message    message model, encoding detection, split/join logic
internal/router     prefix-based provider routing
internal/smpp       minimal SMPP TCP server and PDU helpers
internal/store      storage interface plus in-memory implementation
configs             sample gateway config
```

## Run

```powershell
go run ./cmd/mysmpp -config configs/example.json
```

Defaults:

- HTTP API: `:8080`
- SMPP listener: `:2775`
- SMPP bind: `system_id=mysmpp`, `password=secret`

Open the simple configuration page:

```text
http://127.0.0.1:8080/ui/config
```

## HTTP API

Submit an MT message:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:8080/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello"}'
```

List stored messages:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/messages
```

Provider callbacks can be configured in `inbound`. For example, `configs/example.json` maps:

- `msg_id` to internal `id`
- `src` to `from`
- `dst` to `to`
- `content` to `text`

## HTTP Rule Model

Outbound HTTP rules map provider parameter names to internal message fields:

```json
{
  "name": "http-json-b",
  "method": "POST",
  "content_type": "application/json",
  "fields": {
    "messageId": "id",
    "sender": "from",
    "receiver": "to",
    "body": "text",
    "coding": "encoding"
  }
}
```

This makes it possible to support providers that expect form posts, JSON payloads, custom headers, or query strings without changing gateway code.

For detailed route, upstream, downstream, and HTTP rule design, see [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

## SMPP Support

The current SMPP package is a protocol foundation. It supports:

- TCP listener
- PDU header parsing and writing
- `bind_receiver`, `bind_transmitter`, `bind_transceiver`
- `enquire_link`
- `unbind`
- basic `submit_sm` parsing and `submit_sm_resp`

Next SMPP milestones:

- `deliver_sm` for MO and delivery receipts back to SMPP clients
- SMPP TLV parsing and rendering
- UCS-2 binary decoding and data coding handling
- registered delivery and DLR state machine
- windowing, throttling, and bind/session policy

## Long SMS

`internal/message` detects GSM-7-compatible text vs UCS-2 text and applies common segment sizes:

- GSM-7 single SMS: 160 chars
- GSM-7 concatenated segment: 153 chars
- UCS-2 single SMS: 70 chars
- UCS-2 concatenated segment: 67 chars

Concatenated segments include a UDH placeholder so provider adapters can render the final protocol-specific payload.

## Roadmap

1. Add durable storage: SQLite/PostgreSQL/MySQL implementations behind `store.Store`.
2. Add outbound dispatcher with retry, rate limiting, and provider failover.
3. Complete SMPP submit/deliver flows, including TLV and DLR.
4. Add admin API for routes, providers, queue status, and message tracing.
5. Add metrics and structured audit logs.
