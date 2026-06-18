# 数据模型

## messages

存储所有消息。

| 字段 | 说明 |
|---|---|
| `gateway_id` | mysmpp 生成的消息 ID，主键 |
| `provider_id` | 上游 provider 返回的 ID |
| `direction` | `mt` 或 `mo` |
| `from_addr` / `to_addr` | 源地址和目标号码 |
| `text` | 短信内容 |
| `encoding` / `data_coding` | 编码信息 |
| `segments` | 分段数量 |
| `route` | 命中的 route |
| `provider` | 命中的 provider |
| `source_kind` | `http_api` 或 `smpp` |
| `source_session` | SMPP session ID |
| `state` | `queued`、`sent`、`DELIVRD`、`failed` 等 |
| `error_code` | DLR 或失败错误码 |
| `received_at` / `sent_at` / `done_at` | 生命周期时间 |
| `meta` | JSON 元数据 |

## outbox

待发送队列。dispatcher worker 从这里 claim 任务并调用 provider。

| 字段 | 说明 |
|---|---|
| `id` | 队列 ID |
| `gateway_id` | 对应 messages.gateway_id |
| `provider` | 目标 provider |
| `payload` | 发送上游所需完整 JSON |
| `state` | `pending`、`claimed`、`done`、`failed` |
| `claimed_by` / `claimed_at` | worker claim 信息 |
| `next_retry_at` | 下次重试时间 |
| `attempt` / `max_attempts` | 尝试次数 |
| `last_error` | 最近失败原因 |

If an item remains in `claimed` longer than `dispatcher.claim_timeout`, the dispatcher requeues it to `pending`. This protects Postgres deployments from rows getting stuck after a process crash between claim and ack/fail.

## pending

DLR 映射表。上游回调只知道 provider_id，mysmpp 用 pending 找回 gateway_id 和下游来源。

| 字段 | 说明 |
|---|---|
| `provider_id` | 上游消息 ID，主键 |
| `gateway_id` | mysmpp 消息 ID |
| `source_kind` | 原始提交来源 |
| `source_session` | SMPP session ID |
| `source_system` | SMPP system_id |
| `registered_delivery` | 是否需要回推 DLR |
| `expires_at` | 过期时间 |

SMPP 上游 provider 的 `provider_id` 来自 `submit_sm_resp` 的 `message_id`，会按 `providers[].smpp.message_id_resp_format` 归一化后保存。上游 `deliver_sm` DLR 进来时，也会按 `message_id_dlr_format` 归一化后用这个字段查 pending。

中继场景建议把 `dispatcher.pending_ttl` 设置为 `48h` 或更长，避免 SMSC 晚到 DLR 找不到映射。

## idempotency

HTTP client 幂等记录。

| 字段 | 说明 |
|---|---|
| `client_id` | HTTP client ID |
| `key` | `client_msg_id` |
| `gateway_id` | 首次提交生成的 gateway_id |
| `expires_at` | 过期时间 |
