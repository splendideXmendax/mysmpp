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
  "max_attempts": 5
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
| `max_attempts` | 上游发送失败最大尝试次数 | `5` |

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

## esmes

`esmes` 是允许 bind 到网关的 SMPP 客户端账号。

```json
[
  {
    "system_id": "dev-esme",
    "password": "esmepw1"
  }
]
```

字段:

| 字段 | 说明 |
|---|---|
| `system_id` | ESME bind 用户名，必须唯一 |
| `password` | ESME bind 密码 |

生产环境不要使用示例密码。配置校验会拒绝 `CHANGE_ME_BEFORE_DEPLOY` 占位符。

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
| `protocol` | `mock`、`http` 或 `https` |
| `endpoint` | HTTP 上游地址 |
| `rule` | 引用 `outbound[].name` |
| `system_id` / `password` | 预留给需要账号密码的上游 |
| `enabled` | 是否启用 |
| `http_timeout_ms` | 真实 HTTP 请求超时，默认 3000ms |
| `rate_limit.tps` | 每秒令牌数；小于等于 0 表示不启用 provider 限速 |
| `rate_limit.burst` | 突发令牌桶大小；默认等于 tps |
| `rate_limit.timeout_ms` | 等待令牌的最长时间，默认 2000ms |

注意:

- `http_timeout_ms` 是上游 HTTP client 超时。
- `rate_limit.timeout_ms` 是等令牌的超时。
- 两者不是同一个东西。

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
2. 执行 `migrations/001_init.up.sql`。
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
