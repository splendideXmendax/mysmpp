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
| `text` | 是 | 非空短信正文，支持 ASCII 控制字符（含 NUL，须 JSON 转义）；自动按编码拆分，默认最多 20 段，由 `dispatcher.max_message_segments` 控制 |
| `client_msg_id` | 否 | 幂等键，1-64 非空白字符 |
| `callback_url` | 否 | 客户 DLR 回调 URL，支持完整的公网 `http://` 或 `https://` 地址；v1.2.5 起每个网关消息只通知最终聚合结果，失败重试使用同一 `delivery_id` |
| `callback_rule` | 否 | 可选回调规则标识，会原样出现在回调 JSON 的 `callback_rule` 字段 |
| `meta` | 否 | 最多 10 个键，每个值最多 200 字符 |

成功响应 `200 OK`：

```json
{
  "gateway_id": "g0000000001",
  "state": "queued"
}
```

租户提交及幂等重试响应只返回 `gateway_id`、`state`，不返回供应商、路由或上游消息 ID。幂等命中时仍返回原 `gateway_id` 和该消息当前状态。无 `clients` 时的管理员 Basic Auth 兼容模式保留内部字段。

常见错误：

| HTTP 状态 | 说明 |
|---:|---|
| `400` | JSON 或字段校验失败 |
| `401` | 缺少 client/admin 鉴权 |
| `403` | client IP 不在白名单、主租户禁用或内容过滤拒绝 |
| `429` | 风控限流/黑名单、主租户 TPS 或日分片额度超限 |
| `500` | 查询或非 dispatcher 模式存储失败 |
| `502` | 无路由、入队失败或 dispatcher 提交失败 |

状态码保持原有语义。租户错误响应为固定提示，不透传包含线路、供应商或数据库详情的内部错误；运维应通过服务日志排查。

## `GET /v1/messages`

分页查询消息。

查询参数：

| 参数 | 默认 | 说明 |
|---|---:|---|
| `limit` | `100` | 返回条数，最大 1000 |
| `offset` | `0` | 起始偏移 |

客户端 token 鉴权模式只返回当前租户的消息，并在租户过滤后应用 `limit` / `offset`。管理员 Basic Auth 模式返回全部租户和历史消息。

租户响应为消息数组，保留原字段名大小写和业务字段，但移除 `Provider`、`ProviderID`、`Route`。管理员 Basic Auth 模式保留完整字段。以下为租户响应节选：

```json
[
  {
    "ID": "g0000000001",
    "Direction": "mt",
    "From": "10690000",
    "To": "13800138000",
    "Text": "hello",
    "State": "DELIVRD"
  }
]
```

## 推送给租户的 DLR

按提交时的 `callback_url` 发送 HTTP/HTTPS POST JSON。回调同样不包含 `provider`、`provider_id`、`route`，详见 [HTTP 接入文档](HTTP_TO_SMPP_API_ZH.md#5-dlr-回调)。

- 消息关联使用 `gateway_id` 或客户提交的 `client_msg_id`。
- HTTP 长短信按 `gateway_id` 聚合，只通知一个最终结果；`segment_count` 保留上游分片总数，不再返回 `segment_index`。
- `state`、`message_state` 均表示整条最终状态，`error_code` 使用聚合错误码；`final` 为 `true`。
- 回调去重使用 `delivery_id`，v1.2.5 起为整条消息的 `http-final:<opaque-fingerprint>`，同一消息重试时保持不变。

升级后尚未完成的旧分片任务也转为最终聚合通知；旧版 `delivery:` ID 与新版 `http-final:` ID 不同，跨版本可额外按 `gateway_id + final` 去重。已发送的历史通知无法撤回。回调失败、应答丢失或进程重启可能造成同一最终通知重试，接收端返回 `2xx` 并按 `delivery_id` 幂等处理。没有新增配置或数据库迁移，内部逐片回执、CDR、管理员排障数据和 SMPP DR 保留。

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
