# API Reference

本文档说明 mysmpp 暴露的 HTTP 端点、鉴权、参数、响应和常见错误。

## 鉴权

`/v1/messages` 有两种鉴权模式：

| 配置 | 请求头 |
|---|---|
| `clients` 非空 | `X-Client-ID: <client_id>` 和 `X-Token: <token>` |
| `clients` 为空 | HTTP Basic Auth，用户名密码来自 `admin.username` / `admin.password` |

`/v1/config` 和 `/ui/config` 始终使用 admin Basic Auth。

`clients` 非空时，`GET /v1/messages` 只返回已鉴权 `client_id` 的消息；未配置 `clients` 的管理员模式保留全量查询。

自定义 inbound callback 使用各自规则里的 `auth_header` / `auth_token`。

SMPP 上游 provider 的 DLR 不走 HTTP inbound callback；它通过上游 `deliver_sm` 进入，并复用 pending 映射更新消息状态。

## `GET /healthz`

健康检查。

响应示例：

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

## `POST /v1/messages`

提交 MT 短信。

请求：

```json
{
  "from": "10690000",
  "to": "13800138000",
  "text": "hello",
  "client_msg_id": "optional-idempotency-key",
  "callback_url": "https://client.example.com/dlr",
  "callback_rule": "optional-rule",
  "meta": {
    "tenant": "demo"
  }
}
```

字段：

| 字段 | 必填 | 说明 |
|---|---:|---|
| `from` | 是 | 源地址，1-32 字符 |
| `to` | 是 | 目标号码，支持 11 位数字或 E.164 |
| `text` | 是 | 非空短信正文，自动按编码拆分，最多 20 个短信分片 |
| `client_msg_id` | 否 | 幂等键，1-64 非空白字符 |
| `callback_url` | 否 | HTTP 下游 DLR 回调 URL，必须为 `https://`；最终态 DLR 到达后网关会 POST JSON 回调 |
| `callback_rule` | 否 | 可选回调规则标识，会原样出现在回调 JSON 的 `callback_rule` 字段 |
| `meta` | 否 | 最多 10 个键，每个值最多 200 字符 |

成功响应 `202 Accepted`：

```json
{
  "gateway_id": "g0000000001",
  "provider_id": "",
  "provider": "stub-provider",
  "route": "default",
  "state": "queued"
}
```

常见错误：

| HTTP 状态 | 说明 |
|---:|---|
| `400` | JSON 或字段校验失败 |
| `401` | 缺少 client/admin 鉴权 |
| `403` | client IP 不在白名单 |
| `429` | 风控限流或命中黑名单 |
| `502` | 无路由、入队失败或 dispatcher 提交失败 |

## `GET /v1/messages`

分页查询消息。

查询参数：

| 参数 | 默认 | 说明 |
|---|---:|---|
| `limit` | `100` | 返回条数，最大 1000 |
| `offset` | `0` | 起始偏移 |

客户端 token 鉴权模式只返回当前租户的消息，并在租户过滤后应用 `limit` / `offset`。管理员 Basic Auth 模式返回全部租户和历史消息。

响应为消息数组。Go JSON 当前使用结构体字段名：

```json
[
  {
    "ID": "g0000000001",
    "Direction": "mt",
    "From": "10690000",
    "To": "13800138000",
    "Text": "hello",
    "State": "DELIVRD",
    "Provider": "stub-provider",
    "ProviderID": "stub-..."
  }
]
```

## `GET /v1/config`

读取运行时配置。需要 admin Basic Auth。

## `PUT /v1/config` / `POST /v1/config`

替换运行时配置。需要 admin Basic Auth。

请求体是完整 `Config` JSON。服务会执行 `Normalize()`、`Validate()`，成功后热更新 provider 和 route；如果 gateway 配置了 `configPath`，会原子写回配置文件。

## 自定义 inbound callback

路径来自 `inbound[].path`。常见 DLR 示例：

```http
POST /callback/stub-provider/dlr
X-Callback-Token: CALLBACK_TOKEN
Content-Type: application/json
```

```json
{
  "message_id": "stub-123",
  "status": "DELIVRD",
  "error_code": 0
}
```

常见错误：

| HTTP 状态 | 说明 |
|---:|---|
| `401` | `auth_header` / `auth_token` 不匹配 |
| `403` | DLR provider 和 pending provider 不一致 |
| `404` | pending 中找不到 provider_id |
