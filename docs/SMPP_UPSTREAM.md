# SMPP 上游 Provider 与 SMPP-to-SMPP 中继

本文说明如何把 mysmpp 配成 SMPP 客户端，主动 bind 上游 SMSC，实现：

```text
下游 HTTP/SMPP -> mysmpp -> 上游 SMPP SMSC -> DLR -> mysmpp -> 下游 SMPP DLR
```

## 1. 需要上游提供的信息

| 信息 | 示例 | 含义 |
|---|---|---|
| 上游 Host | `smsc.example.com` | 上游 SMSC 地址 |
| 上游 Port | `2775` | 上游 SMPP 端口 |
| System ID | `acct` | mysmpp bind 上游时使用的账号，最多 15 字节 |
| Password | `secret88` | mysmpp bind 上游时使用的密码，最多 8 字节 |
| Bind mode | `transceiver` | 当前支持 `transceiver` |
| System Type | 空或 `VMA` | bind PDU 的 system_type，最多 12 字节 |
| TON/NPI | source 自动，dest `1/1` | 地址类型配置 |
| Window | `16` 或 `32` | 单连接在途 submit_sm 数量 |
| Enquire Link | `30s` | 心跳周期 |
| message_id 格式 | `dec` / `hex` / `auto` | submit_sm_resp 和 DLR ID 的进制 |

## 2. 最小配置

把 `providers` 和 `routes` 改成如下结构：

```json
{
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "48h",
    "max_attempts": 5
  },
  "providers": [
    {
      "name": "smsc-a",
      "protocol": "smpp",
      "endpoint": "smsc.example.com:2775",
      "system_id": "acct",
      "password": "secret88",
      "enabled": true,
      "rate_limit": {
        "tps": 100,
        "burst": 200,
        "timeout_ms": 2000
      },
      "smpp": {
        "bind_mode": "transceiver",
        "system_type": "",
        "binds": 1,
        "window_size": 16,
        "enquire_period": "30s",
        "response_timeout_ms": 5000,
        "reconnect_min": "1s",
        "reconnect_max": "60s",
        "source_ton": -1,
        "source_npi": -1,
        "dest_ton": 1,
        "dest_npi": 1,
        "service_type": "",
        "validity_period": "",
        "registered_delivery": -1,
        "gsm7_packing": "unpacked",
        "long_message": "udh",
        "message_id_resp_format": "auto",
        "message_id_dlr_format": "auto",
        "dlr_id_source": "auto",
        "retry_on_timeout": false,
        "tls": false
      }
    }
  ],
  "routes": [
    {
      "name": "default",
      "prefix": [],
      "provider": "smsc-a",
      "priority": 1
    }
  ]
}
```

注意：`protocol=smpp` 时 `rule` 必须为空，不需要配置 `outbound`。上游 DLR 由 SMPP `deliver_sm` 收到，不需要配置 HTTP `inbound` 回调。

## 3. 字段说明

| 字段 | 默认 | 含义 |
|---|---|---|
| `endpoint` | 必填 | 上游 SMSC `host:port` |
| `system_id` | 必填 | 上游分配的 bind 账号，最多 15 字节 |
| `password` | 必填 | 上游 bind 密码，最多 8 字节 |
| `bind_mode` | `transceiver` | 当前仅支持 `transceiver` |
| `system_type` | 空 | bind PDU 的 system_type |
| `binds` | `1` | 同账号建立几条上游连接 |
| `window_size` | `16` | 单连接未收到 resp 的 submit_sm 上限 |
| `enquire_period` | `30s` | 主动心跳周期 |
| `response_timeout_ms` | `5000` | 等待 submit_sm_resp 的超时 |
| `reconnect_min` / `reconnect_max` | `1s` / `60s` | 断线重连退避 |
| `source_ton` / `source_npi` | `-1` / `-1` | `-1` 自动判断源地址；字母 sender 会用 TON=5 |
| `dest_ton` / `dest_npi` | `1` / `1` | 目的地址 TON/NPI |
| `registered_delivery` | `-1` | `-1` 透传下游；`1` 强制请求 DLR；`0` 强制关闭 |
| `gsm7_packing` | `unpacked` | 大多数 SMSC 要 `unpacked`；老系统才可能要 `packed` |
| `long_message` | `udh` | 可选 `udh`、`payload`、`sar` |
| `message_id_resp_format` | `auto` | submit_sm_resp 的 message_id 格式 |
| `message_id_dlr_format` | `auto` | DLR receipt 中 message_id 格式 |
| `dlr_id_source` | `auto` | 优先从 TLV 还是 receipt text 取 ID |
| `retry_on_timeout` | `false` | submit_sm_resp 超时是否重试；开启可能重复扣费 |
| `tls` | `false` | 当前未实现，设为 `true` 会被拒绝 |

## 4. 联调测试

### 4.1 下游 HTTP -> 上游 SMPP

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -u admin:'<admin.password>' \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"hello smpp upstream"}'
```

预期返回：

```json
{"gateway_id":"g0000000001","provider":"smsc-a","route":"default","state":"queued"}
```

再查询：

```bash
curl -sS -u admin:'<admin.password>' \
  'http://127.0.0.1:19087/v1/messages?limit=10&offset=0'
```

如果上游返回 DLR，消息最终应变为 `DELIVRD`、`UNDELIV`、`EXPIRED` 等终态。

### 4.2 下游 SMPP -> 上游 SMPP -> 下游 SMPP DLR

```bash
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u dev-esme \
  -p esmepw1 \
  -src 10690000 \
  -dst 13800138000 \
  -text 'hello smpp to smpp' \
  -wait 30s
```

预期：

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] ... stat:DELIVRD ...
```

## 5. message_id 对账

上游常见问题是 submit_sm_resp 返回十六进制 ID，而 DLR 文本里返回十进制 ID。例如：

```text
submit_sm_resp: 0000004F
DLR text:       id:79 stat:DELIVRD
```

如果 pending 查不到 DLR，把格式显式改成：

```json
"message_id_resp_format": "hex",
"message_id_dlr_format": "dec"
```

可选值：

| 值 | 行为 |
|---|---|
| `auto` | 有十六进制字母时按 hex 转十进制，否则保留原样 |
| `dec` | 去掉十进制前导零 |
| `hex` | 按十六进制转十进制 |

## 6. 故障排查

| 现象 | 重点检查 |
|---|---|
| outbox 持续增长 | 上游 IP/端口、防火墙、账号密码、bind 状态、window 是否满 |
| submit_sm_resp timeout | 上游 RTT、`response_timeout_ms`、window、binds、SMSC 是否回 resp |
| pending 持续增长 | 上游是否发 DLR、message_id 进制是否一致、`pending_ttl` 是否过短 |
| bind 失败 | `system_id`、`password`、`system_type` 长度和上游账号是否正确 |
| DLR 不回下游 SMPP | 下游是否用 transceiver/receiver bind，提交时 `registered_delivery` 是否非 0 |

## 7. 当前边界

| 能力 | 状态 |
|---|---|
| transceiver bind | 已支持 |
| 多 bind 连接池 | 已支持 |
| submit 窗口 | 已支持 |
| deliver_sm DLR 解析 | 已支持 |
| `tx_rx` 分离 bind | 未实现 |
| SMPP over TLS | 未实现 |
| MO 路由到下游 | 未实现 |
| 分段 DLR 聚合落库 | 未实现 |
