# Test Report - 2026-06-21

## Summary

Testing covered local unit tests and live server verification after updating the code, migrating to Postgres, and fixing the DLR pending lookup race.

Final status:

- Local unit tests: pass
- Server unit tests: pass
- Live health check: pass
- SMPP bind and submit: pass
- SMPP downstream DLR delivery: pass after fix
- Invalid E.164 country code rejection: pass
- Postgres restart persistence: pass
- Pre-bind heartbeat behavior: pass

## Local Test Results

Command:

```bash
go test ./...
```

Result:

```text
ok github.com/splendideXmendax/mysmpp/internal/admin
ok github.com/splendideXmendax/mysmpp/internal/config
ok github.com/splendideXmendax/mysmpp/internal/dispatch
ok github.com/splendideXmendax/mysmpp/internal/httpgw
ok github.com/splendideXmendax/mysmpp/internal/message
ok github.com/splendideXmendax/mysmpp/internal/netutil
ok github.com/splendideXmendax/mysmpp/internal/provider
ok github.com/splendideXmendax/mysmpp/internal/router
ok github.com/splendideXmendax/mysmpp/internal/smpp
ok github.com/splendideXmendax/mysmpp/internal/smppclient
ok github.com/splendideXmendax/mysmpp/internal/store
```

## Server Health

Health check after deployment:

```json
{
  "status": "ok",
  "checks": {
    "storage": "ok",
    "pending_size": 0,
    "outbox_depth": 0,
    "smpp_listener": "ok"
  }
}
```

Containers verified:

```text
mysmpp            Up
mysmpp-postgres   Up (healthy)
smpp-app          Up
redis             Up
```

## Bug Verification Matrix

| Item | Severity | Result | Notes |
|---|---:|---|---|
| Downstream SMS status report not pushed to client | High | Fixed and verified | SMPP `deliver_sm` DLR was received by the test ESME after the race fix. |
| Invalid country code not rejected, example `285032768252` | Low | Already fixed | Submit is rejected with SMPP status `0x0000000b` (`ESME_RINVDSTADR`). |
| `860015013628000` country code plus trunk zero behavior | Low | Configurable | Current production config passes it through. Code supports rewriting to `8615013628000` with `strip_trunk_zero_after_cc=true`. |
| Service restart or disconnect loses data/messages | High | Fixed by deployment | Postgres mode is active. Restart test confirmed data remains available. |
| Heartbeat sent before bind | Medium | Not reproduced | A raw TCP client received no data for 6 seconds before bind. |

## Live SMPP DLR Test

Command pattern:

```bash
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u <configured-esme> \
  -p <redacted> \
  -src 10690000 \
  -dst 13800138000 \
  -text afterfix-dlr-20260621210805 \
  -wait 20s
```

Result:

```text
bound. sending...
submitted msg_id=g000000001329
[DLR] 13800138000 -> 10690000 : id:g000000001329 ... stat:DELIVRD err:000 text:afterfix-dlr-2026062
dlr_push_to_smpp_client_afterfix result=PASS
```

Database confirmation:

```text
g000000001329|13800138000|DELIVRD|ap2-upstream|2998162935|afterfix-dlr-20260621210805
```

## Invalid Country Code Test

Destination:

```text
285032768252
```

Result:

```text
bound. sending...
submit failed status=0x0000000b
invalid_country_code_285_afterfix result=PASS
```

Interpretation:

- `285` is not an assigned E.164 country calling code in the gateway's coarse country-code table.
- The message is rejected before entering outbox.

## Trunk Zero Test

Destination:

```text
860015013628000
```

Current production behavior:

```text
submitted msg_id=g000000001330
[DLR] 860015013628000 -> 10690000 : id:g000000001330 ... stat:DELIVRD err:000
trunk_zero_8600_afterfix result=PASS
```

Database confirmation:

```text
g000000001330|860015013628000|DELIVRD|ap2-upstream|595367790|afterfix-zero-20260621210805
```

Interpretation:

- The current route configuration passes the number through unchanged.
- To rewrite `860015013628000` to `8615013628000`, configure the route:

```json
{
  "addr_rewrite": {
    "strip_trunk_zero_after_cc": true,
    "country_code": "86",
    "enforce_e164_len": true
  }
}
```

## Restart Persistence Test

Action:

```bash
docker compose restart mysmpp
```

Verification:

```text
g000000001329|DELIVRD|afterfix-dlr-20260621210805
```

Result:

- The message remained in Postgres after service restart.
- Health check returned `status=ok`.

## Pre-Bind Heartbeat Test

Method:

- Opened a raw TCP connection to `127.0.0.1:29175`.
- Did not send a bind PDU.
- Waited 6 seconds for inbound data.

Result:

```text
prebind result=PASS no_data_before_bind_for_6s
```

Interpretation:

- The server did not send `enquire_link` before bind during the observed window.

## Notes And Remaining Boundaries

- SMPP downstream DLR delivery is fixed and verified.
- HTTP downstream `callback_url` active callback is still not implemented. HTTP-originated messages can be queried for final state, but the gateway does not yet push callback requests to HTTP clients.
- The DLR race fix addresses fast upstream DLR delivery after `submit_sm_resp`; truly unknown provider IDs still return `not found`.

