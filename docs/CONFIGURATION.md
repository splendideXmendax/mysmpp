# mysmpp 配置详解

本文说明 `mysmpp` 的 JSON 配置结构、默认值、生产建议和常见规则写法。

## 配置文件选择

| 文件 | 场景 | HTTP | SMPP | 存储 |
|---|---|---:|---:|---|
| `configs/example.json` | 本机开发 | `127.0.0.1:19087` | `127.0.0.1:29175` | memory |
| `configs/dev.json` | 本机开发备用 | `127.0.0.1:19087` | `127.0.0.1:29175` | memory |
| `configs/docker.json` | Docker 首次启动种子 | `0.0.0.0:19087` | `0.0.0.0:29175` | file |
| `configs/production.example.json` | 生产模板 | `:19087` | `:29175` | postgres |

启动时指定配置:

```bash
./mysmpp -config configs/example.json
```

不指定时，默认读取:

```text
configs/example.json
```

## 完整结构

```json
{
  "server": {},
  "smpp": {},
  "dispatcher": {},
  "esmes": [],
  "routes": [],
  "providers": [],
  "inbound": [],
  "outbound": [],
  "clients": [],
  "trusted_proxies": [],
  "risk": {},
  "storage": {},
  "admin": {}
}
```

## server

```json
{
  "http_addr": "127.0.0.1:19087",
  "shutdown_timeout": "10s"
}
```

字段:

| 字段 | 说明 | 默认值 |
|---|---|---|
| `http_addr` | HTTP API 和管理后台监听地址 | `127.0.0.1:19087` |
| `shutdown_timeout` | 收到退出信号后的优雅关闭等待时间 | `10s` |

Docker 中必须监听 `0.0.0.0:19087`，否则宿主机端口映射访问不到容器内服务。

## smpp

```json
{
  "addr": "127.0.0.1:29175",
  "system_id": "mysmpp-dev",
  "password": "smpppw1",
  "system_type": "gateway",
  "max_sessions": 128,
  "max_sessions_per_system_id": 4,
  "window_size": 16,
  "enquire_period": "30s"
}
```

字段:

| 字段 | 说明 | 默认值 |
|---|---|---|
| `addr` | SMPP TCP 监听地址 | `127.0.0.1:29175` |
| `system_id` | 网关在 bind response 中返回的 system_id | `mysmpp` |
| `password` | 兼容旧配置的 SMPP 密码字段，建议使用 `esmes` | 空 |
| `system_type` | SMPP system_type | `gateway` |
| `max_sessions` | 最大 SMPP 会话数 | `128` |
| `max_sessions_per_system_id` | 同一个 ESME system_id 最大会话数 | `4` |
| `window_size` | 单会话并发 submit 窗口，满时返回 throttled | `16` |
| `enquire_period` | enquire_link 周期；空闲超过 2 倍周期会关闭会话 | `30s` |

未完成 bind 的连接会在 30 秒后关闭，避免半开连接占用会话。

## dispatcher

```json
{
  "workers": 10,
  "per_worker_concurrency": 10,
  "claim_limit": 20,
  "poll_interval_ms": 20,
  "pending_ttl": "30m",
  "max_attempts": 5,
  "claim_timeout": "60s",
  "pending_sweep_interval": "1m",
  "validate_dest_addr": true
}
```

字段:

| 字段 | 说明 | 默认值 |
|---|---|---:|
| `workers` | outbox claim worker 数量 | `10` |
| `per_worker_concurrency` | 每个 worker 同时调用上游的并发数 | `10` |
| `claim_limit` | 每次最多 claim 的 outbox 数量 | `20` |
| `poll_interval_ms` | worker 轮询间隔 | `20` |
| `pending_ttl` | provider_id 到 gateway_id 的 DLR 映射保留时间 | `30m` |
| `max_attempts` | 进入 `sending` 前可重试失败的最大尝试次数 | `5` |

总上游并发:

```text
workers * per_worker_concurrency
```

估算公式:

```text
目标 TPS * 上游平均 RTT 秒数 * 安全系数 1.5-2
```

例子:

| 上游平均 RTT | 推荐并发 | 示例 |
|---:|---:|---|
| 100ms | 50 | `workers=10`, `per_worker_concurrency=5` |
| 200ms | 100 | `workers=10`, `per_worker_concurrency=10` |
| 500ms | 250 | `workers=10`, `per_worker_concurrency=25` |

Additional dispatcher fields:

| Field | Default | Description |
|---|---:|---|
| `claim_timeout` | `60s` | Stale `claimed` outbox reclaim threshold. Only work that has not entered `sending` is moved back to `pending`. |
| `pending_sweep_interval` | `1m` | Background interval for deleting expired pending DLR mappings. |
| `validate_dest_addr` | `true` | Validate destination address before route match. Invalid E.164 or unassigned country code returns SMPP `ESME_RINVDSTADR` and does not enter outbox. |

Destination validation is intentionally minimal: optional leading `+`, digits only, total E.164 length `4..15`, and an assigned 1-3 digit country calling code. It does not validate each country's full national numbering plan.

`claim_timeout` only controls work that has not entered `sending`. A short value may cause harmless claim churn under a saturated worker pool, but Store ownership checks prevent two workers from entering `sending` for the same row.

## esmes

`esmes` 是允许 bind 到网关的 SMPP 客户端账号。

```json
[
  {
    "system_id": "dev-esme",
    "password": "esmepw1",
    "tenant_id": "customer-a",
    "enabled": true
  }
]
```

字段:

| 字段 | 说明 |
|---|---|
| `system_id` | ESME bind 用户名，必须唯一 |
| `password` | ESME bind 密码 |
| `tenant_id` | 可选，关联 `tenants[].tenant_id`；省略时该 ESME 是独立兼容租户 |
| `enabled` | 可选；省略表示启用，显式 `false` 禁止 bind |

生产环境不要使用示例密码。配置校验会拒绝 `CHANGE_ME_BEFORE_DEPLOY` 占位符。

## tenants

`tenants` 定义主租户。一个主租户可同时关联多个 HTTP client 和 SMPP ESME，两种协议共同使用 TPS 和单日短信分片额度。

```json
[
  {
    "tenant_id": "customer-a",
    "enabled": true,
    "limits": {
      "tps": 50,
      "burst": 100,
      "daily_segments": 100000,
      "timezone": "Asia/Shanghai"
    }
  }
]
```

| 字段 | 说明 |
|---|---|
| `tenant_id` | 主租户唯一 ID，最长 64 字节 |
| `enabled` | 可选；省略表示启用，`false` 时拒绝该租户的新提交 |
| `limits.tps` | 每实例令牌桶速率；`0` 表示不限制 |
| `limits.burst` | 瞬时突发容量；`tps>0` 且省略时等于 `tps` |
| `limits.daily_segments` | 按实际短信分片计数的自然日硬上限；`0` 表示不限制 |
| `limits.timezone` | 自然日时区，IANA 名称；省略时使用 `UTC` |

日额度在 `SubmitAtomic` 内与幂等键、消息、outbox 原子写入。重复 `client_msg_id` 不重复扣减。消息一旦受理入队即按网关编码器计算的理论分片数计入额度，后续上游异步失败不会返还，以免通过失败重试绕过硬上限。PostgreSQL 使用 `(tenant_id, quota_date)` 单行原子计数；file 存储会把计数写入快照；memory 存储重启会清零计数。TPS 是进程内限制，多实例部署时总 TPS 约等于配置值乘以实例数。

## routes

`routes` 决定短信走哪个上游。

```json
[
  {
    "name": "china-mobile",
    "prefix": ["134", "135", "136", "137", "138", "139"],
    "provider": "provider-a",
    "priority": 100
  },
  {
    "name": "default",
    "prefix": [],
    "provider": "provider-b",
    "priority": 1
  }
]
```

匹配规则:

1. 只使用 enabled provider 对应的 route。
2. `priority` 越大越优先。
3. 同优先级下，号码前缀越长越优先。
4. `prefix: []` 是默认兜底路由。

字段:

| 字段 | 说明 |
|---|---|
| `name` | 路由名 |
| `prefix` | 号码前缀列表；空数组表示默认路由 |
| `provider` | 上游 provider 名称 |
| `priority` | 优先级 |

### 权重配比

同一条路由可以使用 `weighted` 按消息分配多个上游。`weight` 是相对权重，例如 `7:3` 表示大量新消息整体上约 70% 选择 `provider-a`、约 30% 选择 `provider-b`：

```json
{
  "name": "default-weighted",
  "prefix": [],
  "priority": 1,
  "weighted": [
    {"provider": "provider-a", "weight": 7},
    {"provider": "provider-b", "weight": 3}
  ]
}
```

权重选择使用每条消息的 `gateway_id` 做稳定哈希，不使用目标号码。因此同一号码的多条消息也会参与配比；同一个 `gateway_id` 的选择保持稳定。只有 enabled provider 会进入权重计算，某个 provider 被禁用后，其余 provider 会按剩余权重重新分配新消息。

`weighted` 和 `failover` 不能在同一路由中同时配置。当前 `failover` 只选择第一个 enabled provider，发送失败后自动切换到下一 provider 尚未实现。

Optional route-level address rewrite:

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
  },
  "dest_addr": {
    "validate": true,
    "allow_short_code": false,
    "country_length_mode": "compat"
  }
}
```

`addr_rewrite` defaults to pass-through. `strip_trunk_zero_after_cc=true` changes a number like `860015013628000` to `8615013628000`; keep it disabled unless the upstream contract explicitly requires removing national trunk zeroes.

Set `dest_addr.validate=false` on a specific route when that route is intentionally for non-E.164 destinations such as short codes. Alternatively set `allow_short_code=true` with optional `min_short_len` and `max_short_len`.

`dest_addr.country_length_mode` controls country-specific total-length checks generated from `docs/public_country.xlsx` and `docs/国家号码规则信息.xlsx`:

- Empty or `off`: keep legacy E.164 validation only. This is the backward-compatible default.
- `compat`: apply a configured country maximum when available; countries without a length rule still use the global E.164 4-15 digit check.
- `strict`: reject countries without a length rule as well as numbers over the configured maximum. Use only after the rule table covers all intended destinations.

The spreadsheet contains maximum total lengths only. It does not prove that a subscriber number or operator range is assigned. `strip_trunk_zero_after_cc` remains explicit because a national trunk zero can be significant in some numbering plans.

## providers

`providers` 描述上游通道。

```json
[
  {
    "name": "provider-a",
    "protocol": "http",
    "endpoint": "https://sms.example.com/send",
    "rule": "http-json-a",
    "enabled": true,
    "http_timeout_ms": 3000,
    "rate_limit": {
      "tps": 50,
      "burst": 100,
      "timeout_ms": 2000
    }
  }
]
```

字段:

| 字段 | 说明 |
|---|---|
| `name` | provider 唯一名称 |
| `protocol` | `mock`、`http`、`https` 或 `smpp` |
| `endpoint` | HTTP 上游 URL，或 SMPP 上游 `host:port` |
| `rule` | HTTP 上游引用 `outbound[].name`；SMPP 上游必须为空 |
| `system_id` / `password` | SMPP 上游 bind 账号和密码；SMPP 密码最多 8 字节 |
| `enabled` | 是否启用 |
| `http_timeout_ms` | 真实 HTTP 请求超时，默认 3000ms |
| `rate_limit.tps` | 每秒令牌数；小于等于 0 表示不启用 provider 限速 |
| `rate_limit.burst` | 突发令牌桶大小；默认等于 tps |
| `rate_limit.timeout_ms` | 等待令牌的最长时间，默认 2000ms |
| `smpp` | SMPP 上游参数，仅 `protocol=smpp` 时允许 |

注意:

- `http_timeout_ms` 是上游 HTTP client 超时。
- `rate_limit.timeout_ms` 是等令牌的超时。
- 两者不是同一个东西。

### SMPP 上游示例

`protocol=smpp` 时，mysmpp 会作为 ESME 主动 bind 上游 SMSC，并把 HTTP/SMPP 下游提交转成 `submit_sm` 发给上游。

```json
{
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

SMPP 字段:

| 字段 | 默认 | 说明 |
|---|---|---|
| `bind_mode` | `transceiver` | 当前支持 `transceiver`；`tx_rx` 预留但会被校验拒绝 |
| `system_type` | 空 | bind PDU 的 system_type，最多 12 字节 |
| `binds` | `1` | 到同一上游的 TCP bind 数 |
| `window_size` | `16` | 单连接在途 submit_sm 窗口 |
| `enquire_period` | `30s` | 主动 `enquire_link` 周期 |
| `response_timeout_ms` | `5000` | 等待 `submit_sm_resp` 的超时 |
| `reconnect_min` / `reconnect_max` | `1s` / `60s` | 断线重连退避区间 |
| `source_ton` / `source_npi` | `-1` / `-1` | `-1` 表示按源地址自动判断；可固定为 0..6 |
| `dest_ton` / `dest_npi` | `1` / `1` | 目的地址 TON/NPI |
| `service_type` | 空 | submit_sm service_type |
| `validity_period` | 空 | submit_sm validity_period |
| `registered_delivery` | `-1` | `-1` 透传下游；`0` 强制关闭；`1` 强制请求 DLR |
| `gsm7_packing` | `unpacked` | `unpacked` 或 `packed` |
| `long_message` | `udh` | `udh`、`payload` 或 `sar`。`payload` 使用 `message_payload` 只发一个 PDU，但日额度仍按受理时的理论分片数扣减，因此是保守多扣。 |
| `message_id_resp_format` | `auto` | `submit_sm_resp` message_id 格式:`auto`、`dec`、`hex` |
| `message_id_dlr_format` | `auto` | DLR receipt message_id 格式:`auto`、`dec`、`hex` |
| `dlr_id_source` | `auto` | DLR ID 来源:`auto`、`tlv`、`text` |
| `retry_on_timeout` | `false` | 兼容字段。严格 fail-closed 出站流程不会在 `sending` 后自动重发；开启时 timeout 记录为 `uncertain`，关闭时按终态失败记录。 |
| `tls` | `false` | 预留字段，当前未实现，配置为 `true` 会被拒绝 |

建议 SMPP 中继部署把 `dispatcher.pending_ttl` 调到 `48h`，避免上游 DLR 晚到时 pending 映射已过期。

## outbound

`outbound` 描述调用 HTTP 上游时如何渲染请求。

```json
[
  {
    "name": "http-json-a",
    "method": "POST",
    "content_type": "application/json",
    "fields": {
      "mobile": "to",
      "content": "text",
      "sender": "from",
      "msgId": "id",
      "encoding": "encoding"
    },
    "headers": {
      "Authorization": "Bearer replace-with-token",
      "X-Request-ID": "{{id}}"
    },
    "response": {
      "id_path": "data.0.messageId",
      "id_regex": ""
    }
  }
]
```

`fields` 左边是上游字段名，右边是网关内部字段名。

可用内部字段:

| 内部字段 | 说明 |
|---|---|
| `id` | gateway_id |
| `from` | 主叫、签名号、源地址 |
| `to` | 被叫号码 |
| `text` | 短信内容 |
| `encoding` | `gsm7` 或 `ucs2` |

请求格式:

| 配置 | 行为 |
|---|---|
| `method=GET` | 参数放到 query string |
| `content_type=application/json` | 参数 JSON 序列化 |
| 其他 content type | 默认 form urlencoded |

响应 provider_id 提取:

```json
"response": {
  "id_path": "data.0.messageId"
}
```

或者:

```json
"response": {
  "id_regex": "MsgID:\\s+([A-Za-z0-9_-]+)"
}
```

如果没有命中 `id_path` 或 `id_regex`，网关不会把整个响应体猜成 provider_id，而是 fallback 到 gateway_id。

## inbound

`inbound` 描述外部系统调用网关的 HTTP 入站规则。常见用途:

- 上游 provider 回调 DLR。
- 上游 provider 推送 MO。
- 下游客户用自定义 HTTP 格式提交消息。

### 普通入站消息

```json
[
  {
    "name": "partner-mo",
    "method": "POST",
    "path": "/callback/partner/mo",
    "content_type": "application/json",
    "auth_header": "X-Token",
    "auth_token": "replace-with-token",
    "fields": {
      "id": "msg_id",
      "from": "src",
      "to": "dst",
      "text": "content"
    },
    "success_status": 200,
    "success_body": "{\"ok\":true}"
  }
]
```

### Provider DLR 回调

```json
[
  {
    "name": "provider-a-dlr",
    "method": "POST",
    "path": "/callback/provider-a/dlr",
    "provider": "provider-a",
    "content_type": "application/json",
    "auth_header": "X-Callback-Token",
    "auth_token": "replace-with-callback-token",
    "fields": {
      "provider_id": "message_id",
      "status": "status",
      "error_code": "error_code"
    },
    "success_status": 200,
    "success_body": "{\"ok\":true}"
  }
]
```

DLR 规则要求:

- 必须有 `provider`。
- 必须映射 `provider_id` 和 `status`。
- 回调会校验 token。
- 回调里的 provider 必须和 pending 记录里的 provider 一致。

## clients

`clients` 控制 HTTP `/v1/messages` 的客户端鉴权。

```json
[
  {
    "client_id": "demo-client",
    "token": "replace-with-token",
    "enabled": true,
    "tenant_id": "customer-a",
    "allowed_ips": ["127.0.0.1/32", "::1/128"]
  }
]
```

字段:

| 字段 | 说明 |
|---|---|
| `client_id` | HTTP 客户端 ID |
| `token` | 请求头 `X-Token` |
| `enabled` | 是否启用 |
| `tenant_id` | 可选，关联 `tenants[].tenant_id`；省略时该 HTTP client 是独立兼容租户 |
| `allowed_ips` | 允许访问的客户端 IP/CIDR；为空表示不限制 |

请求示例:

```bash
curl -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Client-ID: demo-client' \
  -H 'X-Token: replace-with-token' \
  -d '{"from":"10690000","to":"13800138000","text":"hello"}'
```

如果 `clients` 为空，`/v1/messages` 会退回到 admin Basic Auth，而不是允许匿名提交或查询。

配置 `clients` 后，`GET /v1/messages` 只返回当前已鉴权 `client_id` 的消息；租户身份来自鉴权上下文，不接受未验证的请求头覆盖。完整请求字段、返回结构和 DLR 回调说明见 [HTTP_TO_SMPP_API_ZH.md](HTTP_TO_SMPP_API_ZH.md)。

## trusted_proxies

反向代理后如果还要使用 `clients[].allowed_ips`，需要配置受信代理:

```json
["127.0.0.1/32", "::1/128", "10.0.0.0/8"]
```

只有请求直连来源命中 `trusted_proxies` 时，网关才会读取:

- `X-Forwarded-For`
- `X-Real-IP`

`X-Forwarded-For` 会从右往左找第一个非受信代理 IP，避免客户端随便伪造第一个 IP 绕过白名单。

## risk

```json
{
  "blocked_to_prefix": [],
  "blocked_keywords": [],
  "per_number_per_minute": 5,
  "per_number_per_day": 20,
  "per_client_per_second": 100
}
```

字段:

| 字段 | 说明 |
|---|---|
| `blocked_to_prefix` | 禁止发送的号码前缀 |
| `blocked_keywords` | 内容关键词黑名单，大小写不敏感 |
| `per_number_per_minute` | 单号码每分钟上限 |
| `per_number_per_day` | 单号码每天上限 |
| `per_client_per_second` | 单 HTTP 客户端每秒上限 |

限制:

- 当前风控是进程内 map。
- 多实例部署时，每个实例独立计数。
- 生产多副本需要改 Redis 或 Postgres 计数。

## storage

```json
{
  "driver": "postgres",
  "dsn": "postgres://mysmpp:password@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
}
```

driver:

| driver | 说明 | 适用场景 |
|---|---|---|
| `memory` | 内存存储，重启丢失 | 本机开发 |
| `file` / `json` | JSON 文件快照 | 单容器联调 |
| `postgres` / `pg` | Postgres 持久化 | 生产 |

file 模式:

- 如果 `dsn` 为空，会自动使用配置文件同目录的 `store.json`。
- Docker 中默认写到 `/app/data/store.json`。

Postgres 模式:

1. 创建数据库和用户。
2. 按编号顺序执行全部 `migrations/*.up.sql`（当前到 `007`）。
3. 配置 `storage.driver=postgres` 和 DSN。
4. 确保连接池足够，例如 `pool_max_conns=50`。

## admin

```json
{
  "username": "admin",
  "password": "replace-with-password"
}
```

用于登录:

```text
/admin/
/v1/config
/ui/config
```

安全行为:

- 用户名和密码常量时间比较。
- 登录失败按 IP 限流，15 分钟内最多 5 次。
- session cookie 使用 `HttpOnly` 和 `SameSite=Strict`。
- 非 GET 表单带 CSRF token。

## 完整生产配置骨架

```json
{
  "server": {
    "http_addr": "0.0.0.0:19087",
    "shutdown_timeout": "30s"
  },
  "smpp": {
    "addr": "0.0.0.0:29175",
    "system_id": "mysmpp-prod",
    "password": "replace-with-smpp-password",
    "system_type": "gateway",
    "max_sessions": 256,
    "max_sessions_per_system_id": 8,
    "window_size": 100,
    "enquire_period": "30s"
  },
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 5
  },
  "esmes": [
    {
      "system_id": "customer-a",
      "password": "replace-with-esme-password"
    }
  ],
  "routes": [
    {
      "name": "default",
      "prefix": [],
      "provider": "provider-a",
      "priority": 1
    }
  ],
  "providers": [
    {
      "name": "provider-a",
      "protocol": "http",
      "endpoint": "https://sms-provider.example.com/send",
      "rule": "provider-a-json",
      "enabled": true,
      "http_timeout_ms": 3000,
      "rate_limit": {
        "tps": 300,
        "burst": 600,
        "timeout_ms": 2000
      }
    }
  ],
  "inbound": [
    {
      "name": "provider-a-dlr",
      "method": "POST",
      "path": "/callback/provider-a/dlr",
      "provider": "provider-a",
      "content_type": "application/json",
      "auth_header": "X-Callback-Token",
      "auth_token": "replace-with-callback-token",
      "fields": {
        "provider_id": "message_id",
        "status": "status",
        "error_code": "error_code"
      },
      "success_status": 200,
      "success_body": "{\"ok\":true}"
    }
  ],
  "outbound": [
    {
      "name": "provider-a-json",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "mobile": "to",
        "content": "text",
        "sender": "from",
        "requestId": "id"
      },
      "headers": {
        "Authorization": "Bearer replace-with-provider-token",
        "X-Request-ID": "{{id}}"
      },
      "response": {
        "id_path": "data.0.messageId"
      }
    }
  ],
  "clients": [
    {
      "client_id": "api-client-a",
      "token": "replace-with-client-token",
      "enabled": true,
      "allowed_ips": []
    }
  ],
  "trusted_proxies": ["127.0.0.1/32", "::1/128"],
  "risk": {
    "blocked_to_prefix": [],
    "blocked_keywords": [],
    "per_number_per_minute": 5,
    "per_number_per_day": 20,
    "per_client_per_second": 500
  },
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:password@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  },
  "admin": {
    "username": "admin",
    "password": "replace-with-admin-password"
  }
}
```

## 修改配置的方式

### 方式一: 管理后台

```text
http://127.0.0.1:19087/admin/
```

保存后会:

1. 校验完整配置。
2. 热更新 provider 和路由。
3. 原子写回 `-config` 指定的文件。

### 方式二: 直接编辑 JSON

编辑后重启服务:

```bash
docker compose restart mysmpp
```

或者:

```bash
./mysmpp -config /path/to/config.json
```

## 常见校验失败

| 报错 | 原因 | 处理 |
|---|---|---|
| `admin credentials must be changed before deploy` | 仍在使用占位符 | 替换 `admin.password` |
| `smpp password must be changed before deploy` | `smpp.password` 是占位符 | 替换或清理 |
| `client token must be changed before deploy` | HTTP client token 是占位符 | 替换 |
| `storage.dsn is required for postgres` | Postgres 未配置 DSN | 填写 `storage.dsn` |
| `route references unknown provider` | route 指向不存在的 provider | 修正 provider 名 |
| `at least one provider must be enabled` | 所有 provider 都禁用 | 启用一个 provider |
