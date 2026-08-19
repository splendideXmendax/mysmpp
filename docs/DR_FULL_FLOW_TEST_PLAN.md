# mysmpp DR/DLR 上下游全链路测试实施手册

本文档用于指导在不同机器上搭建一套标准测试环境，完整验证 mysmpp 的上下游链路：

- 下游 HTTP 客户端提交短信到 mysmpp；
- 下游 SMPP ESME 提交 `submit_sm` 到 mysmpp；
- mysmpp 按路由把消息发给上游 HTTP provider；
- 上游 provider 回调 mysmpp 的 DLR/DR 地址；
- mysmpp 更新消息状态；
- mysmpp 对 SMPP 下游推送 `deliver_sm` 状态报告。

当前版本中，HTTP 下游提交可以通过 `/v1/messages` 轮询最终状态；如果提交时携带 `callback_url`，mysmpp 也会在最终态 DLR 到达后主动 POST 回调。SMPP 下游链路通过 `deliver_sm` 推送 DLR。

## 1. 术语

本文档里几个角色的含义：

```text
下游客户端 / downstream client
  调用 mysmpp 的客户系统，可以是 HTTP 客户端，也可以是 SMPP ESME。

mysmpp / gateway
  本项目短信网关。负责接收下游提交、路由、发上游、保存 pending、接收 DLR、回推 SMPP DLR。

上游 provider / upstream provider
  短信供应商。本测试用 Python provider stub 模拟。

DR / DLR
  Delivery Receipt，状态报告。本文统一表示上游返回的最终发送状态。
```

## 2. 推荐部署拓扑

三台机器最清晰，也最接近真实联调：

```text
                  1. HTTP /send
        +----------------------------------+
        |                                  v
+--------------------+              +----------------------+
| Machine A          |              | Machine C            |
| mysmpp gateway     |              | upstream provider    |
|                    |              | Python stub          |
| HTTP 0.0.0.0:19087 |              | HTTP 0.0.0.0:18080   |
| SMPP 0.0.0.0:29175 |              +----------------------+
+--------------------+                       |
        ^                                    |
        |                                    |
        | 3. HTTP DLR callback              |
        +------------------------------------+
        ^
        |
        | 0. HTTP /v1/messages 或 SMPP submit_sm
        |
+--------------------+
| Machine B          |
| downstream client  |
| Python HTTP client |
| Python SMPP ESME   |
+--------------------+
```

示例 IP：

```text
Machine A，mysmpp gateway:       10.0.0.10
Machine B，下游测试客户端:        10.0.0.20
Machine C，上游 provider stub:   10.0.0.30
```

三机测试地址和端口必须按下面对齐：

| 角色 | 机器 | 示例 IP | 监听端口 | 对外 URL / 地址 | 说明 |
|---|---|---:|---:|---|---|
| mysmpp HTTP/API/Admin | Machine A | `10.0.0.10` | `19087/tcp` | `http://10.0.0.10:19087` | 下游 HTTP 提交、查询、上游 DLR 回调都进这个端口 |
| mysmpp SMPP | Machine A | `10.0.0.10` | `29175/tcp` | `10.0.0.10:29175` | 下游 SMPP ESME bind/submit 连接这个端口 |
| 模拟下游 HTTP client | Machine B | `10.0.0.20` | 无需监听 | 出站到 `http://10.0.0.10:19087/v1/messages` | 运行 `http-submit` |
| 模拟下游 SMPP ESME | Machine B | `10.0.0.20` | 无需监听 | 出站到 `10.0.0.10:29175` | 运行 `smpp-esme` |
| 模拟上游 HTTP provider | Machine C | `10.0.0.30` | `18080/tcp` | `http://10.0.0.30:18080/send` | 运行 `provider` stub，接收 mysmpp 上游请求 |
| 模拟上游 DLR 回调 | Machine C -> A | `10.0.0.30` -> `10.0.0.10` | 出站到 `19087/tcp` | `http://10.0.0.10:19087/callback/stub-provider/dlr` | provider stub 延迟回调 DLR |

也可以本机单机跑：

```text
gateway:        127.0.0.1:19087, 127.0.0.1:29175
provider stub:  127.0.0.1:18080
client:         127.0.0.1
```

本机测试地址和端口：

| 角色 | IP | 端口 | URL / 地址 |
|---|---:|---:|---|
| mysmpp HTTP/API/Admin | `127.0.0.1` | `19087` | `http://127.0.0.1:19087` |
| mysmpp SMPP | `127.0.0.1` | `29175` | `127.0.0.1:29175` |
| 模拟上游 provider stub | `127.0.0.1` | `18080` | `http://127.0.0.1:18080/send` |
| 模拟上游 DLR 回调 | `127.0.0.1` | `19087` | `http://127.0.0.1:19087/callback/stub-provider/dlr` |
| 模拟下游 HTTP client | `127.0.0.1` | 无需监听 | 调用 `http://127.0.0.1:19087/v1/messages` |
| 模拟下游 SMPP ESME | `127.0.0.1` | 无需监听 | 连接 `127.0.0.1:29175` |

## 3. 网络和防火墙

三机部署时需要放行：

```text
Machine A 入站:
  19087/tcp  mysmpp HTTP API、admin、provider DLR callback
  29175/tcp  mysmpp SMPP server

Machine C 入站:
  18080/tcp  provider stub send endpoint

Machine B 出站:
  -> Machine A:19087
  -> Machine A:29175

Machine A 出站:
  -> Machine C:18080

Machine C 出站:
  -> Machine A:19087
```

Linux 防火墙示例：

```bash
sudo ufw allow 19087/tcp
sudo ufw allow 29175/tcp
sudo ufw allow 18080/tcp
```

CentOS/RHEL 示例：

```bash
sudo firewall-cmd --add-port=19087/tcp --permanent
sudo firewall-cmd --add-port=29175/tcp --permanent
sudo firewall-cmd --add-port=18080/tcp --permanent
sudo firewall-cmd --reload
```

Windows 防火墙示例：

```powershell
New-NetFirewallRule -DisplayName "mysmpp HTTP 19087" -Direction Inbound -Protocol TCP -LocalPort 19087 -Action Allow
New-NetFirewallRule -DisplayName "mysmpp SMPP 29175" -Direction Inbound -Protocol TCP -LocalPort 29175 -Action Allow
New-NetFirewallRule -DisplayName "provider stub 18080" -Direction Inbound -Protocol TCP -LocalPort 18080 -Action Allow
```

## 4. 测试资产

本方案使用一个 Python 标准库测试桩：

```text
tools/dr_flow_stub.py
```

它包含三个模式：

```text
provider     上游 HTTP provider 模拟器
http-submit  下游 HTTP 客户端，提交后轮询状态
smpp-esme    下游 SMPP ESME，提交后等待 deliver_sm DLR
```

查看参数：

```bash
python3 tools/dr_flow_stub.py --help
python3 tools/dr_flow_stub.py provider --help
python3 tools/dr_flow_stub.py http-submit --help
python3 tools/dr_flow_stub.py smpp-esme --help
```

Python 要求：

```text
Python 3.9+ 推荐
只使用标准库，不需要 pip install
```

## 5. 流量和字段映射

### 5.1 mysmpp 发给上游 provider

mysmpp 根据 `outbound` 规则向 provider stub 发送：

```http
POST /send HTTP/1.1
Host: 10.0.0.30:18080
Content-Type: application/json
X-Request-ID: g0000000001

{
  "request_id": "g0000000001",
  "src": "10690000",
  "dst": "13800138000",
  "content": "hello",
  "encoding": "gsm7"
}
```

provider stub 返回：

```json
{
  "code": 0,
  "message_id": "stub-1780000000-000001",
  "data": {
    "message_id": "stub-1780000000-000001"
  }
}
```

mysmpp 使用配置里的：

```json
"response": {
  "id_path": "message_id"
}
```

把 `message_id` 提取为 `provider_id`，写入 pending。

### 5.2 provider stub 回调 mysmpp DLR

provider stub 延迟几秒后请求：

```http
POST /callback/stub-provider/dlr HTTP/1.1
Host: 10.0.0.10:19087
Content-Type: application/json
X-Callback-Token: CALLBACK_TOKEN

{
  "message_id": "stub-1780000000-000001",
  "status": "DELIVRD",
  "error_code": 0
}
```

mysmpp inbound 规则用以下字段解析：

```json
"fields": {
  "provider_id": "message_id",
  "status": "status",
  "error_code": "error_code"
}
```

关键匹配关系：

```text
provider_id 必须等于上游 send 响应里的 message_id。
inbound.provider 必须等于 pending 记录里的 provider，即 stub-provider。
auth_header/auth_token 必须和 provider stub 启动参数一致。
```

### 5.3 SMPP 下游收到 DLR

如果下游是 SMPP，并且提交时 `registered_delivery=1`，mysmpp 收到上游 DLR 后会向同一个 SMPP session 推送：

```text
deliver_sm short_message:
id:g0000000001 sub:001 dlvrd:001 submit date:... done date:... stat:DELIVRD err:000 text:...
```

## 6. 服务端 mysmpp 配置

### 6.1 三机部署配置

在 Machine A 创建：

```bash
cp configs/example.json configs/dr-flow-test.json
```

将内容替换为下面配置。需要改的地方只有：

```text
providers[0].endpoint 里的 10.0.0.30
server/http/smpp 监听端口如有冲突也要改
admin.password、esmes.password、auth_token 如需更安全可改
```

完整配置：

```json
{
  "server": {
    "http_addr": "0.0.0.0:19087",
    "shutdown_timeout": "10s"
  },
  "smpp": {
    "addr": "0.0.0.0:29175",
    "system_id": "mysmpp-dr",
    "password": "smpppw1",
    "system_type": "gateway",
    "max_sessions": 128,
    "max_sessions_per_system_id": 4,
    "window_size": 16,
    "enquire_period": "30s"
  },
  "dispatcher": {
    "workers": 4,
    "per_worker_concurrency": 4,
    "claim_limit": 16,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 3
  },
  "esmes": [
    {
      "system_id": "dr-esme",
      "password": "dresme1"
    }
  ],
  "routes": [
    {
      "name": "default",
      "prefix": [],
      "provider": "stub-provider",
      "priority": 1
    }
  ],
  "providers": [
    {
      "name": "stub-provider",
      "protocol": "http",
      "endpoint": "http://10.0.0.30:18080/send",
      "rule": "stub-json-send",
      "enabled": true,
      "http_timeout_ms": 5000,
      "rate_limit": {
        "tps": 50,
        "burst": 100,
        "timeout_ms": 2000
      }
    }
  ],
  "inbound": [
    {
      "name": "stub-provider-dlr",
      "method": "POST",
      "path": "/callback/stub-provider/dlr",
      "provider": "stub-provider",
      "content_type": "application/json",
      "auth_header": "X-Callback-Token",
      "auth_token": "CALLBACK_TOKEN",
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
      "name": "stub-json-send",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "request_id": "id",
        "src": "from",
        "dst": "to",
        "content": "text",
        "encoding": "encoding"
      },
      "headers": {
        "X-Request-ID": "{{id}}"
      },
      "response": {
        "id_path": "message_id"
      }
    }
  ],
  "clients": [],
  "trusted_proxies": ["127.0.0.1/32", "::1/128"],
  "risk": {
    "blocked_to_prefix": [],
    "blocked_keywords": [],
    "per_number_per_minute": 1000,
    "per_number_per_day": 10000,
    "per_client_per_second": 500
  },
  "storage": {
    "driver": "memory",
    "dsn": ""
  },
  "admin": {
    "username": "admin",
    "password": "mysmpp-admin-19087"
  }
}
```

### 6.2 本机配置

项目里已提供本机测试配置：

```text
configs/dr-flow-test.local.json
```

本机配置里的 endpoint 是：

```json
"endpoint": "http://127.0.0.1:18080/send"
```

本机启动 gateway：

```bash
go run ./cmd/mysmpp -config configs/dr-flow-test.local.json
```

### 6.3 配置字段说明

#### 6.3.1 `server`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `server.http_addr` | `0.0.0.0:19087` | mysmpp HTTP 监听地址。HTTP API、`/admin/`、`/healthz`、provider DLR callback 都走这个监听。 | 三机测试时 Machine B 和 Machine C 都要能访问 Machine A 的 `10.0.0.10:19087`。本机测试可用 `127.0.0.1:19087`。 |
| `server.shutdown_timeout` | `10s` | 收到退出信号后 HTTP graceful shutdown 最长等待时间。 | 通常不用改；压测或慢请求多时可调大。 |

#### 6.3.2 `smpp`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `smpp.addr` | `0.0.0.0:29175` | mysmpp SMPP TCP 监听地址。 | 下游 SMPP ESME 的 `--host 10.0.0.10 --port 29175` 必须连到这里。跨机器不能写 `127.0.0.1:29175`。 |
| `smpp.system_id` | `mysmpp-dr` | mysmpp 在 bind response 中返回给 ESME 的网关 system_id。 | 只是网关身份标识，不是 ESME 登录账号。 |
| `smpp.password` | `smpppw1` | 兼容旧配置的 SMPP 密码字段。当前 bind 鉴权主要使用 `esmes[]`。 | SMPP v3.4 password 最多 8 字节。 |
| `smpp.system_type` | `gateway` | bind response 中的 system_type。 | SMPP v3.4 system_type 最多 12 字节。 |
| `smpp.max_sessions` | `128` | mysmpp 允许的总 SMPP 连接数上限。 | 并发 ESME 多时调大。 |
| `smpp.max_sessions_per_system_id` | `4` | 同一个 ESME `system_id` 最多同时 bind 的连接数。 | 如果重复运行 `smpp-esme` 导致 bind failed，检查旧连接是否还在。 |
| `smpp.window_size` | `16` | 单个 SMPP session 同时未完成的 `submit_sm` 数量。 | 超过会返回 throttled。 |
| `smpp.enquire_period` | `30s` | enquire_link 周期和空闲检测依据。 | ESME 长时间等待 DLR 时要能响应 enquire_link。 |

#### 6.3.3 `dispatcher`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `dispatcher.workers` | `4` | outbox worker 数量。 | 发上游并发约等于 `workers * per_worker_concurrency`。 |
| `dispatcher.per_worker_concurrency` | `4` | 每个 worker 同时调用上游 provider 的并发数。 | 上游 provider stub 的 `18080` 会收到这些并发请求。 |
| `dispatcher.claim_limit` | `16` | 每次从 outbox 取出的最大任务数。 | 通常设置为不小于单 worker 并发。 |
| `dispatcher.poll_interval_ms` | `20` | worker 轮询 outbox 的间隔，单位毫秒。 | 越小延迟越低，但空转更多。 |
| `dispatcher.pending_ttl` | `30m` | provider_id 到 gateway_id 的 DLR 映射保留时间。 | 必须大于 provider stub 的 `--dlr-delay`，否则 DLR 回调会 404。 |
| `dispatcher.max_attempts` | `3` | 进入 `sending` 前可重试失败的最大次数。 | 结果不确定的发送进入 `uncertain`，不得自动重发。 |

#### 6.3.4 `esmes`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `esmes[].system_id` | `dr-esme` | 下游 SMPP ESME bind 用户名。 | `smpp-esme --system-id dr-esme` 必须一致。 |
| `esmes[].password` | `dresme1` | 下游 SMPP ESME bind 密码。 | `smpp-esme --password dresme1` 必须一致，且 SMPP v3.4 password 最多 8 字节。 |

#### 6.3.5 `routes`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `routes[].name` | `default` | 路由名称，用于记录消息走了哪条路由。 | 只允许字母、数字、点、下划线、短横线。 |
| `routes[].prefix` | `[]` | 号码前缀列表。空数组表示兜底路由，匹配所有号码。 | HTTP/SMPP 测试里的 `--dst 13800138000` 必须能命中某条 route。 |
| `routes[].provider` | `stub-provider` | 命中该路由后发往哪个上游 provider。 | 必须等于 `providers[].name`。 |
| `routes[].priority` | `1` | 路由优先级，值越大越优先。 | 多条 route 都命中时使用。 |

#### 6.3.6 `providers`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `providers[].name` | `stub-provider` | 上游 provider 名称。 | 必须和 `routes[].provider`、`inbound[].provider` 完全一致。 |
| `providers[].protocol` | `http` | 上游协议。DR 测试使用 HTTP provider stub。 | 必须是 `http`。 |
| `providers[].endpoint` | `http://10.0.0.30:18080/send` | mysmpp 发给模拟上游的完整 URL。 | IP 是 Machine C，端口是 provider stub 的 `--port 18080`，path 是 `--send-path /send`。本机测试改为 `http://127.0.0.1:18080/send`。 |
| `providers[].rule` | `stub-json-send` | 使用哪条 outbound 规则组装请求。 | 必须等于 `outbound[].name`。 |
| `providers[].enabled` | `true` | 是否启用 provider。 | 为 `false` 时 route 不会选中它。 |
| `providers[].http_timeout_ms` | `5000` | 调上游 HTTP provider 的超时时间，单位毫秒。 | 必须大于 provider stub 正常响应耗时。 |
| `providers[].rate_limit.tps` | `50` | 对该 provider 的每秒限速。 | 压测时避免把模拟上游打爆。 |
| `providers[].rate_limit.burst` | `100` | 限速令牌桶突发容量。 | 短时并发提交会用到。 |
| `providers[].rate_limit.timeout_ms` | `2000` | 等待令牌的最长时间，单位毫秒。 | 超过会认为发送失败并进入重试。 |

#### 6.3.7 `inbound`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `inbound[].name` | `stub-provider-dlr` | 入站规则名称。 | 只用于识别配置。 |
| `inbound[].method` | `POST` | provider DLR 回调使用的 HTTP 方法。 | provider stub 固定用 POST。 |
| `inbound[].path` | `/callback/stub-provider/dlr` | mysmpp 接收 DLR 的路径。 | 必须等于 provider stub `--gateway-dlr-url` 的 path。完整三机 URL 是 `http://10.0.0.10:19087/callback/stub-provider/dlr`。 |
| `inbound[].provider` | `stub-provider` | 这条 DLR 属于哪个上游 provider。 | 必须等于 pending 记录中的 provider，也就是 `providers[].name`。 |
| `inbound[].content_type` | `application/json` | 说明回调数据格式。 | provider stub 发送 JSON。 |
| `inbound[].auth_header` | `X-Callback-Token` | DLR 回调鉴权 header 名。 | 必须等于 provider stub `--dlr-auth-header`。 |
| `inbound[].auth_token` | `CALLBACK_TOKEN` | DLR 回调鉴权 token。 | 必须等于 provider stub `--dlr-auth-token`。 |
| `inbound[].fields.provider_id` | `message_id` | 从 DLR JSON 的哪个字段读取 provider_id。 | 必须等于 provider stub 回调体里的 `message_id`。 |
| `inbound[].fields.status` | `status` | 从 DLR JSON 的哪个字段读取最终状态。 | provider stub `--dlr-status DELIVRD` 会写入这里。 |
| `inbound[].fields.error_code` | `error_code` | 从 DLR JSON 的哪个字段读取错误码。 | provider stub `--dlr-error-code 0` 会写入这里。 |
| `inbound[].success_status` | `200` | mysmpp 成功处理 DLR 后返回给 provider stub 的 HTTP 状态码。 | provider stub 日志应看到 DLR callback status=200。 |
| `inbound[].success_body` | `{"ok":true}` | 成功响应体。 | 只影响 provider stub 日志显示。 |

#### 6.3.8 `outbound`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `outbound[].name` | `stub-json-send` | 出站组包规则名称。 | 必须等于 `providers[].rule`。 |
| `outbound[].method` | `POST` | mysmpp 调上游 provider 的 HTTP 方法。 | provider stub `/send` 只接受 POST。 |
| `outbound[].content_type` | `application/json` | 发给 provider stub 的请求体格式。 | provider stub 会按 JSON 解析。 |
| `outbound[].fields.request_id` | `id` | 发给上游 JSON 的 `request_id` 字段，值来自 mysmpp gateway_id。 | provider stub `/requests` 里能看到。 |
| `outbound[].fields.src` | `from` | 发给上游 JSON 的 `src` 字段，值来自提交里的 `from`。 | 对应 `http-submit --src` 或 `smpp-esme --src`。 |
| `outbound[].fields.dst` | `to` | 发给上游 JSON 的 `dst` 字段，值来自提交里的 `to`。 | 对应 `--dst`，也用于 route 匹配。 |
| `outbound[].fields.content` | `text` | 发给上游 JSON 的 `content` 字段，值来自短信内容。 | 对应 `--text`。 |
| `outbound[].fields.encoding` | `encoding` | 发给上游 JSON 的 `encoding` 字段，值是 mysmpp 检测出的编码。 | ASCII 通常是 `gsm7`，中文通常是 `ucs2`。 |
| `outbound[].headers.X-Request-ID` | `{{id}}` | 发给上游的 HTTP header，模板 `{{id}}` 会替换为 gateway_id。 | provider stub 可从请求头看到。 |
| `outbound[].response.id_path` | `message_id` | 从 provider stub 响应 JSON 中提取 provider_id 的路径。 | provider stub 响应体必须包含 `message_id`，否则 pending 会 fallback 到 gateway_id。 |

#### 6.3.9 `clients`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `clients` | `[]` | HTTP `/v1/messages` 客户端鉴权列表。空数组表示不使用 client token。 | 空数组时 `http-submit` 必须传 `--admin-user admin --admin-password mysmpp-admin-19087`。 |
| `clients[].client_id` | `dr-http-client` | HTTP 下游 client ID。 | 非空 clients 时对应 `http-submit --client-id dr-http-client`。 |
| `clients[].token` | `dr-http-token` | HTTP 下游 token。 | 非空 clients 时对应 `http-submit --token dr-http-token`。 |
| `clients[].enabled` | `true` | 是否启用该 HTTP client。 | 为 `false` 会 401。 |
| `clients[].allowed_ips` | `[]` | 允许访问 `/v1/messages` 的下游 IP/CIDR。 | 三机测试如限制 IP，应包含 Machine B 的 `10.0.0.20/32`。 |

#### 6.3.10 `trusted_proxies`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `trusted_proxies` | `["127.0.0.1/32", "::1/128"]` | 哪些反向代理 IP 可被信任，用于解析 `X-Forwarded-For` / `X-Real-IP`。 | 没有反代时保持默认即可；有 Nginx/Caddy 时加入代理 IP。 |

#### 6.3.11 `risk`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `risk.blocked_to_prefix` | `[]` | 禁止发送的号码前缀。 | 测试时建议为空，避免 `--dst` 被挡。 |
| `risk.blocked_keywords` | `[]` | 禁止发送的关键词。 | 测试时建议为空，避免 `--text` 被挡。 |
| `risk.per_number_per_minute` | `1000` | 单个目标号码每分钟上限。 | 批量测试时调大。 |
| `risk.per_number_per_day` | `10000` | 单个目标号码每天上限。 | 重复验收时调大。 |
| `risk.per_client_per_second` | `500` | 单个 HTTP client 每秒上限。 | HTTP 压测时调大。 |

#### 6.3.12 `storage`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `storage.driver` | `memory` | 存储类型。`memory` 重启丢数据，`file` 写 JSON 快照，`postgres` 用数据库。 | 快速联调用 `memory`；重启恢复测试用 `postgres`。 |
| `storage.dsn` | `""` | 存储连接串或文件路径。 | `memory` 可为空；`postgres` 必须填 DSN。 |

#### 6.3.13 `admin`

| 字段 | 示例值 | 含义 | 联调时必须对应 |
|---|---|---|---|
| `admin.username` | `admin` | 管理后台和无 clients 时 `/v1/messages` Basic Auth 用户名。 | `http-submit --admin-user admin` 和 `curl -u admin:...` 必须一致。 |
| `admin.password` | `mysmpp-admin-19087` | 管理后台和无 clients 时 `/v1/messages` Basic Auth 密码。 | `http-submit --admin-password mysmpp-admin-19087` 必须一致。 |

## 7. 上游 provider stub 配置

Machine C 启动命令：

```bash
cd /path/to/mysmpp
python3 tools/dr_flow_stub.py provider \
  --host 0.0.0.0 \
  --port 18080 \
  --send-path /send \
  --gateway-dlr-url http://10.0.0.10:19087/callback/stub-provider/dlr \
  --dlr-auth-header X-Callback-Token \
  --dlr-auth-token CALLBACK_TOKEN \
  --dlr-delay 2 \
  --dlr-status DELIVRD \
  --dlr-error-code 0
```

参数说明：

```text
--host
  provider stub 监听地址。跨机器用 0.0.0.0。

--port
  provider stub 监听端口，默认 18080。

--send-path
  mysmpp 发上游的路径，必须和 providers.endpoint 的 path 一致。

--accept-status
  provider stub 收到 /send 后返回给 mysmpp 的 HTTP 状态码，默认 200。
  失败场景可改成 500，用来验证 outbox retry。

--gateway-dlr-url
  provider stub 回调 mysmpp 的 DLR URL。
  必须指向 Machine A 的 HTTP 地址和 inbound.path。

--dlr-auth-header
  DLR 回调鉴权 header 名，必须等于 inbound.auth_header。

--dlr-auth-token
  DLR 回调鉴权 token，必须等于 inbound.auth_token。

--dlr-delay
  provider 收到 /send 后延迟多少秒回调 DLR。

--dlr-status
  回调状态，成功用 DELIVRD，失败可用 UNDELIV。

--dlr-error-code
  回调错误码，成功用 0。
```

provider stub 提供两个辅助接口：

```bash
curl http://10.0.0.30:18080/healthz
curl http://10.0.0.30:18080/requests
```

`/requests` 可以查看 provider stub 收到的所有上游提交。

## 8. 下游 HTTP 客户端配置

HTTP 下游测试命令：

```bash
cd /path/to/mysmpp
python3 tools/dr_flow_stub.py http-submit \
  --gateway-messages-url http://10.0.0.10:19087/v1/messages \
  --admin-user admin \
  --admin-password mysmpp-admin-19087 \
  --src 10690000 \
  --dst 13800138000 \
  --text "http downstream dr test" \
  --count 3 \
  --wait 30 \
  --poll-interval 2 \
  --expect-state DELIVRD
```

参数说明：

```text
--gateway-messages-url
  mysmpp HTTP 提交接口。三机测试必须指向 Machine A:19087。
  示例: http://10.0.0.10:19087/v1/messages。

--admin-user
  admin Basic Auth 用户名。
  当 mysmpp 配置 clients=[] 时必须传，示例值 admin。

--admin-password
  admin Basic Auth 密码。
  当 mysmpp 配置 clients=[] 时必须传，示例值 mysmpp-admin-19087。

--src
  短信源地址，对应 HTTP JSON 的 from。

--dst
  目标号码，对应 HTTP JSON 的 to。
  必须能匹配 routes，默认路由 prefix=[] 可以匹配所有号码。

--text
  短信内容。

--count
  提交条数。

--wait
  总等待时长。

--poll-interval
  轮询 /v1/messages 的间隔。

--expect-state
  期望最终状态。成功测试用 DELIVRD，失败测试可用 UNDELIV。

--client-msg-prefix
  HTTP 提交时生成 client_msg_id 的前缀，用来验证幂等字段。
  不想带 client_msg_id 时传空字符串。

--client-id / --token
  如果 mysmpp 配置了 clients 鉴权，改用这两个参数。
  此时不用传 --admin-user / --admin-password。
```

如果服务端开启了 HTTP client 鉴权：

```json
"clients": [
  {
    "client_id": "dr-http-client",
    "token": "dr-http-token",
    "enabled": true,
    "allowed_ips": []
  }
]
```

客户端命令要加：

```bash
python3 tools/dr_flow_stub.py http-submit \
  --gateway-messages-url http://10.0.0.10:19087/v1/messages \
  --client-id dr-http-client \
  --token dr-http-token \
  --count 3 \
  --wait 30
```

预期输出：

```text
[http-submit] submit status=202 body={"gateway_id":"g0000000001",...}
[http-submit] states {'g0000000001': 'queued'}
[http-submit] states {'g0000000001': 'DELIVRD'}
[http-submit] PASS
```

## 9. 下游 SMPP 客户端配置

SMPP 下游测试命令：

```bash
cd /path/to/mysmpp
python3 tools/dr_flow_stub.py smpp-esme \
  --host 10.0.0.10 \
  --port 29175 \
  --system-id dr-esme \
  --password dresme1 \
  --src 10690000 \
  --dst 13800138000 \
  --text "smpp downstream dr test" \
  --count 3 \
  --wait 30
```

参数说明：

```text
--host
  mysmpp SMPP 服务 IP，即 Machine A。

--port
  mysmpp SMPP 端口，默认 29175。

--system-id
  SMPP bind system_id，必须等于 config.esmes[].system_id。

--password
  SMPP bind password，必须等于 config.esmes[].password。

--src
  source_addr。

--dst
  destination_addr。

--text
  短信内容。

--count
  submit_sm 条数。

--wait
  等待 deliver_sm DLR 的总时长。
```

SMPP stub 固定行为：

```text
bind_transceiver
submit_sm registered_delivery=1
收到 deliver_sm 后回复 deliver_sm_resp
收到 enquire_link 后回复 enquire_link_resp
```

预期输出：

```text
[smpp-esme] bound
[smpp-esme] submitted gateway_id=g0000000001
[smpp-esme] DLR 1/3: {'from': '13800138000', 'to': '10690000', 'text': 'id:g0000000001 ... stat:DELIVRD err:000 ...'}
[smpp-esme] submitted=3 dlr_received=3
[smpp-esme] PASS
```

## 10. 启动顺序

推荐按这个顺序启动：

### 10.1 Machine C 启动 provider stub

```bash
python3 tools/dr_flow_stub.py provider \
  --host 0.0.0.0 \
  --port 18080 \
  --gateway-dlr-url http://10.0.0.10:19087/callback/stub-provider/dlr \
  --dlr-auth-token CALLBACK_TOKEN
```

### 10.2 Machine A 启动 mysmpp

```bash
go run ./cmd/mysmpp -config configs/dr-flow-test.json
```

看到日志：

```text
http listening addr=0.0.0.0:19087
smpp listening addr=0.0.0.0:29175
```

### 10.3 Machine B 运行 HTTP 或 SMPP 客户端

HTTP：

```bash
python3 tools/dr_flow_stub.py http-submit \
  --gateway-messages-url http://10.0.0.10:19087/v1/messages \
  --admin-user admin \
  --admin-password mysmpp-admin-19087 \
  --count 3 \
  --wait 30
```

SMPP：

```bash
python3 tools/dr_flow_stub.py smpp-esme \
  --host 10.0.0.10 \
  --port 29175 \
  --system-id dr-esme \
  --password dresme1 \
  --count 3 \
  --wait 30
```

## 11. 验收标准

### 11.1 HTTP 下游验收

必须同时满足：

```text
HTTP submit 返回 202。
provider stub /requests 能看到请求。
provider stub 日志显示 DLR callback status=200。
/v1/messages 中对应 gateway_id 的 State 变为 DELIVRD。
/healthz outbox_depth 回到 0。
/healthz pending_size 回到 0。
```

### 11.2 SMPP 下游验收

必须同时满足：

```text
SMPP bind 成功。
每条 submit_sm 收到 submit_sm_resp status=0。
每条提交都收到 deliver_sm。
deliver_sm 文本包含 stat:DELIVRD err:000。
客户端回复 deliver_sm_resp。
provider stub DLR callback status=200。
/healthz outbox_depth=0。
/healthz pending_size=0。
```

### 11.3 健康检查命令

Machine A：

```bash
curl http://10.0.0.10:19087/healthz
curl -u admin:mysmpp-admin-19087 'http://10.0.0.10:19087/v1/messages?limit=20&offset=0'
```

Machine C：

```bash
curl http://10.0.0.30:18080/healthz
curl http://10.0.0.30:18080/requests
```

## 12. 手工 DLR 注入

用于单独验证 mysmpp inbound DLR 规则。

先提交一条消息，从 provider stub 日志复制：

```text
provider_id=stub-...
```

然后请求：

```bash
curl -sS -X POST http://10.0.0.10:19087/callback/stub-provider/dlr \
  -H 'Content-Type: application/json' \
  -H 'X-Callback-Token: CALLBACK_TOKEN' \
  -d '{"message_id":"stub-REPLACE-ME","status":"DELIVRD","error_code":0}'
```

预期：

```json
{"ok":true}
```

如果返回 404，说明 pending 里没有这个 `provider_id`，常见原因是：

```text
message_id 写错。
上游响应 id_path 配错，pending 保存的 provider_id 不是这个值。
DLR 已经被消费过一次。
pending_ttl 已过期。
```

## 13. 失败场景测试

### 13.1 上游返回失败状态

Machine C 启动：

```bash
python3 tools/dr_flow_stub.py provider \
  --host 0.0.0.0 \
  --port 18080 \
  --gateway-dlr-url http://10.0.0.10:19087/callback/stub-provider/dlr \
  --dlr-status UNDELIV \
  --dlr-error-code 1
```

HTTP 客户端期望：

```bash
python3 tools/dr_flow_stub.py http-submit \
  --gateway-messages-url http://10.0.0.10:19087/v1/messages \
  --admin-user admin \
  --admin-password mysmpp-admin-19087 \
  --expect-state UNDELIV \
  --count 1 \
  --wait 30
```

SMPP 客户端应看到：

```text
stat:UNDELIV err:001
```

### 13.2 DLR token 错误

Machine C 用错误 token：

```bash
python3 tools/dr_flow_stub.py provider \
  --host 0.0.0.0 \
  --port 18080 \
  --gateway-dlr-url http://10.0.0.10:19087/callback/stub-provider/dlr \
  --dlr-auth-token WRONG_TOKEN
```

预期：

```text
provider stub 日志显示 dlr status=401。
mysmpp pending_size 暂时不归零。
消息状态停留在 sent。
```

### 13.3 上游 provider 不可达

停止 Machine C provider stub，然后提交。

预期：

```text
mysmpp 日志出现 provider connection refused 或 timeout。
outbox_depth 会增加。
发送前重试达到 max_attempts 后消息状态变 failed；已进入 `sending` 的模糊结果应为 `UNKNOWN`，且 outbox 为 `uncertain`。
```

检查：

```bash
curl http://10.0.0.10:19087/healthz
curl -u admin:mysmpp-admin-19087 'http://10.0.0.10:19087/v1/messages?limit=20'
```

### 13.4 provider 名称不匹配

把 inbound.provider 改错，例如：

```json
"provider": "wrong-provider"
```

预期：

```text
DLR callback 返回 403。
日志中出现 dlr provider mismatch。
```

## 14. Docker 部署测试

如果 gateway 用 Docker 起，配置要注意容器网络。

### 14.1 provider stub 在宿主机

Docker Desktop 常用：

```json
"endpoint": "http://host.docker.internal:18080/send"
```

Linux Docker 通常建议直接使用宿主机内网 IP：

```json
"endpoint": "http://10.0.0.30:18080/send"
```

### 14.2 gateway 容器端口

确认 compose 暴露：

```yaml
ports:
  - "19087:19087"
  - "29175:29175"
```

### 14.3 启动

```bash
docker compose up -d --build
docker compose logs -f mysmpp
```

健康检查：

```bash
curl http://10.0.0.10:19087/healthz
```

## 15. Postgres 持久化测试配置

如果要验证重启恢复，建议改成 Postgres。

创建库：

```sql
CREATE USER mysmpp WITH PASSWORD 'mysmpp_pg_pass';
CREATE DATABASE mysmpp OWNER mysmpp;
```

执行迁移：

```bash
for migration in migrations/*.up.sql; do
  psql -v ON_ERROR_STOP=1 'postgres://mysmpp:mysmpp_pg_pass@127.0.0.1:5432/mysmpp?sslmode=disable' \
    -f "$migration" || exit 1
done
```

配置：

```json
"storage": {
  "driver": "postgres",
  "dsn": "postgres://mysmpp:mysmpp_pg_pass@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=20&pool_min_conns=2"
}
```

`gateway_id` 号段由存储层的 `id_alloc` 持久化分配；测试重复执行时仍建议使用独立数据库或 schema，避免历史消息和额度计数影响结果。

## 16. Windows PowerShell 命令

Machine C provider stub：

```powershell
python tools\dr_flow_stub.py provider `
  --host 0.0.0.0 `
  --port 18080 `
  --gateway-dlr-url http://10.0.0.10:19087/callback/stub-provider/dlr `
  --dlr-auth-token CALLBACK_TOKEN
```

Machine A gateway：

```powershell
go run .\cmd\mysmpp -config .\configs\dr-flow-test.json
```

Machine B HTTP client：

```powershell
python tools\dr_flow_stub.py http-submit `
  --gateway-messages-url http://10.0.0.10:19087/v1/messages `
  --admin-user admin `
  --admin-password mysmpp-admin-19087 `
  --count 3 `
  --wait 30
```

Machine B SMPP client：

```powershell
python tools\dr_flow_stub.py smpp-esme `
  --host 10.0.0.10 `
  --port 29175 `
  --system-id dr-esme `
  --password dresme1 `
  --count 3 `
  --wait 30
```

## 17. 常见问题定位

### 17.1 HTTP submit 返回 `no route matched`

检查：

```text
routes 是否为空。
provider 是否 enabled=true。
routes[].provider 是否等于 providers[].name。
dst 是否能匹配 prefix。
```

测试配置建议保留默认路由：

```json
{
  "name": "default",
  "prefix": [],
  "provider": "stub-provider",
  "priority": 1
}
```

### 17.2 provider stub 没收到请求

检查：

```text
Machine A 能否 curl http://10.0.0.30:18080/healthz。
providers.endpoint 是否写成了 127.0.0.1。
防火墙是否放行 Machine C:18080。
provider protocol 是否为 http。
provider rule 是否等于 outbound.name。
```

### 17.3 DLR callback 返回 401

检查：

```text
provider stub --dlr-auth-header 是否等于 inbound.auth_header。
provider stub --dlr-auth-token 是否等于 inbound.auth_token。
```

### 17.4 DLR callback 返回 403

检查：

```text
inbound.provider 是否等于 providers[].name。
pending 记录中的 provider 是否和 DLR rule provider 一致。
```

### 17.5 DLR callback 返回 404

检查：

```text
outbound.response.id_path 是否能提取 provider response 的 message_id。
provider 回调里的 message_id 是否和响应里的 message_id 一致。
pending_ttl 是否太短。
DLR 是否重复回调。
```

### 17.6 SMPP bind failed

检查：

```text
--system-id 是否等于 config.esmes[].system_id。
--password 是否等于 config.esmes[].password。
max_sessions_per_system_id 是否被占满。
Machine B 能否连接 Machine A:29175。
```

### 17.7 SMPP submit 成功但收不到 DLR

检查：

```text
客户端是否用 registered_delivery=1，测试桩默认是 1。
provider stub 是否成功回调 DLR。
DLR callback 是否返回 200。
SMPP session 是否在等待期间断开。
pending_size 是否不归零。
```

### 17.8 healthz outbox_depth 持续增加

说明 mysmpp 发上游失败。检查：

```text
Machine C provider stub 是否运行。
providers.endpoint 是否正确。
Machine A 到 Machine C 网络是否通。
provider stub 是否返回 4xx/5xx。
```

## 18. 测试记录模板

建议每次测试记录：

```text
测试日期:
gateway IP:
client IP:
provider stub IP:
mysmpp commit:
配置文件:
测试类型: HTTP / SMPP / failure
提交条数:
provider DLR status:
HTTP submit 结果:
SMPP DLR 条数:
healthz before:
healthz after:
结论: PASS / FAIL
问题记录:
```

## 19. 最小本机测试命令

终端 1：

```bash
python3 tools/dr_flow_stub.py provider \
  --host 127.0.0.1 \
  --port 18080 \
  --gateway-dlr-url http://127.0.0.1:19087/callback/stub-provider/dlr \
  --dlr-delay 1
```

终端 2：

```bash
go run ./cmd/mysmpp -config configs/dr-flow-test.local.json
```

终端 3，HTTP：

```bash
python3 tools/dr_flow_stub.py http-submit \
  --gateway-messages-url http://127.0.0.1:19087/v1/messages \
  --admin-user admin \
  --admin-password mysmpp-admin-19087 \
  --count 1 \
  --wait 15
```

终端 3，SMPP：

```bash
python3 tools/dr_flow_stub.py smpp-esme \
  --host 127.0.0.1 \
  --port 29175 \
  --system-id dr-esme \
  --password dresme1 \
  --count 1 \
  --wait 15
```
