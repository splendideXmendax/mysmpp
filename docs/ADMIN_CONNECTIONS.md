# Admin Connections Page

`/admin/connections` is the runtime page for SMPP upstream providers. It is useful after configuring a provider with `protocol: "smpp"` because it shows the live bind state and provides a real test-send path.

Open it from:

```text
http://<server-ip>:19087/admin/connections
```

You can also enter it from `/admin/providers`, where the providers page shows a compact SMPP upstream status block above the provider JSON editor.

## Status Fields

| Field | Meaning | Healthy signal |
|---|---|---|
| `provider` | The upstream provider name, from `providers[].name`. Routes reference this value through `routes[].provider`. | Matches the expected route target. |
| `endpoint` | The upstream SMPP SMSC address, from `providers[].endpoint`. | Correct `host:port` supplied by the upstream. |
| `conn` | The connection number inside the provider pool. The total count comes from `providers[].smpp.binds`. | Expected IDs such as `#1`, `#2`. |
| `state` | Current connection state. | `bound` means the bind succeeded and the connection can submit. |
| `window` | `in-flight / window_size`. `in-flight` is the number of submitted PDUs waiting for `submit_sm_resp`; `window_size` is the per-connection limit. | `in-flight` should normally stay below `window_size`. |
| `last inbound` | Last time mysmpp received a PDU from the upstream connection. This can be a response, DLR, enquire_link, or enquire_link_resp. | Updates when the upstream is active. `never` means no inbound PDU has been seen. |
| `submit` | Submit counters. `ok` counts successful `submit_sm_resp`; `fail` counts failed submits. | `ok` increases during traffic, `fail` stays low. |
| `DLR` | Count of upstream `deliver_sm` PDUs received on the connection. | Increases when upstream delivery receipts arrive. |
| `last error` | Most recent connect, bind, read/write, submit, or protocol error. | `none` during normal operation. |

## Test Send

The **Test send** form at the bottom of `/admin/connections` uses the normal production path:

```text
admin form -> dispatcher -> route match -> upstream provider -> submit_sm -> upstream SMSC
```

It is not a fake health check. It creates a real message, uses normal route selection, consumes real SMPP window capacity, waits for the upstream `submit_sm_resp`, and then lets normal pending/DLR tracking handle receipts.

| Field | Meaning | Validation |
|---|---|---|
| `from` | Sender address, written to `submit_sm.source_addr`. | 1-32 characters. |
| `to` | Destination address, written to `submit_sm.destination_addr`. | 11 digits, or E.164 style with a leading `+`. |
| `text` | SMS text body. | 1-1000 characters. Encoding is auto-detected as GSM-7 or UCS-2. |

Use a permitted test number in production, because the upstream may charge or deliver the message.

## Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| `state` is not `bound` | TCP cannot connect, bind failed, credentials wrong, or upstream is restarting. | Check `endpoint`, firewall, `system_id`, `password`, and `system_type`. |
| `window` stays full | Upstream is not returning `submit_sm_resp`, or RTT is higher than expected. | Check upstream health, increase `response_timeout_ms`, or tune `binds` and `window_size`. |
| `last inbound` is `never` | No response or heartbeat was received. | Check network path, NAT idle timeout, and upstream enquire/respond behavior. |
| `submit fail` grows | Submit is being rejected or timing out. | Check `last error`, application logs, and upstream throttling/error codes. |
| `DLR` stays zero | Upstream is not sending receipts, or receipts are not recognized. | Check `registered_delivery`, upstream DLR enablement, `message_id_resp_format`, `message_id_dlr_format`, and `dlr_id_source`. |
