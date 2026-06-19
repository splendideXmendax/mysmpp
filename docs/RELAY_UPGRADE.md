# 中继能力和优化记录

这份文档记录当前 `mysmpp` 已落地的中继能力、安全改造和性能优化，方便后续维护者理解项目状态。

## 当前中继链路

HTTP:

```text
HTTP /v1/messages
  -> Dispatcher.Submit
  -> Store.messages queued
  -> Store.outbox pending
  -> dispatcher worker claim
  -> provider.Send
  -> message sent + pending DLR
  -> provider DLR inbound callback
  -> message final state
```

SMPP:

```text
ESME submit_sm
  -> Session
  -> Dispatcher.Submit
  -> outbox worker
  -> provider.Send
  -> pending DLR
  -> provider/mock DLR
  -> deliver_sm DLR pushed to original ESME session
```

## 已实现项

- SMPP bind 鉴权读取运行时配置，后台修改后无需重启即可影响新连接。
- 管理后台不允许本地绕过，必须使用 `admin.username` / `admin.password`。
- `/admin/` 服务端渲染后台已可用，不依赖 Vue/React。
- 后台 session、CSRF、登录失败限流、配置原子写回已实现。
- `CHANGE_ME_BEFORE_DEPLOY` 占位符会被配置校验拒绝。
- SMPP read loop 不再使用一秒轮询 deadline。
- SMPP `window_size` 会限制并发 `submit_sm`，超限返回 `ESME_RTHROTTLED`。
- SMPP 未 bind 连接 30 秒超时。
- 入站 HTTP token、admin 凭据、client token 都使用常量时间比较。
- 入站规则必须配置 `auth_header` 和 `auth_token`。
- DLR 入站规则必须配置 `provider`，并校验 pending 记录里的 provider。
- HTTP provider 支持 `response.id_path` 和 `response.id_regex` 提取 provider_id。
- `id_path` 支持数组路径，例如 `data.0.messageId`。
- provider_id 未命中时 fallback 到 gateway_id，不再把整个响应体猜成 ID。
- `/v1/messages` 支持 payload 校验、幂等、client token、IP 白名单、基础风控。
- `/v1/messages` GET/POST 都需要 client 鉴权，避免消息列表裸露。
- `trusted_proxies` 支持安全读取 `X-Forwarded-For`。
- Dispatcher 使用 outbox worker + 单 worker 有界并发。
- Dispatcher 参数可配置: worker 数、单 worker 并发、claim limit、poll interval、pending TTL、max attempts。
- Postgres Store 已实现 messages、pending、outbox、idempotency。
- Postgres outbox 使用 `FOR UPDATE SKIP LOCKED` 并发队列模式。
- `/healthz` 返回 storage、pending、outbox 检查。
- Dockerfile 已复制 `go.mod go.sum`，保留依赖校验。

## 配置新增项

Dispatcher:

```json
{
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 5
  }
}
```

HTTP provider timeout:

```json
{
  "providers": [
    {
      "name": "provider-a",
      "protocol": "http",
      "endpoint": "https://sms.example.com/send",
      "rule": "provider-a-json",
      "enabled": true,
      "http_timeout_ms": 3000
    }
  ]
}
```

Client 鉴权:

```json
{
  "clients": [
    {
      "client_id": "demo-client",
      "token": "replace-with-token",
      "enabled": true,
      "allowed_ips": ["127.0.0.1/32", "::1/128"]
    }
  ]
}
```

可信代理:

```json
{
  "trusted_proxies": ["127.0.0.1/32", "::1/128", "10.0.0.0/8"]
}
```

Provider rate limit:

```json
{
  "rate_limit": {
    "tps": 200,
    "burst": 400,
    "timeout_ms": 2000
  }
}
```

HTTP response parsing:

```json
{
  "response": {
    "id_path": "data.0.messageId",
    "id_regex": "MsgID:\\s+([A-Za-z0-9_-]+)"
  }
}
```

DLR inbound:

```json
{
  "name": "provider-a-dlr",
  "method": "POST",
  "path": "/callback/provider-a/dlr",
  "provider": "provider-a",
  "auth_header": "X-Token",
  "auth_token": "replace-with-token",
  "fields": {
    "provider_id": "message_id",
    "status": "status",
    "error_code": "error"
  }
}
```

## 300 TPS 单节点建议

基础配置:

```json
{
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 5
  },
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:password@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

并发估算:

```text
目标 TPS * 上游平均 RTT 秒数 * 安全系数
```

如果 RTT 为 200ms:

```text
300 * 0.2 * 1.5 = 90
```

所以 `10 * 10 = 100` 并发可以作为起点。

## 已知边界

- 长短信拆分当前是消息元数据，HTTP 上游发送仍是完整文本。
- 风控是单进程内存计数，多副本部署会放大限制。
- gateway_id 已通过 store 分段分配；Postgres 使用 `id_alloc` 表，file store 会落盘，memory store 重启后仍会丢失状态。
- pending DLR 已有后台过期清理；messages/outbox 历史归档仍需外部任务或后续实现。
- Metrics 和审计日志还未落地。
- MO 推送到下游 SMPP/HTTP 还未完整实现。

## 验证命令

```bash
go test ./...
```

重点覆盖:

- 配置默认值和校验。
- Dispatcher submit、outbox、DLR。
- HTTP submit、GET 鉴权、IP 白名单、入站 DLR。
- HTTP provider request rendering、timeout、provider_id 提取。
- SMPP bind、submit、enquire、DLR、TLV。
- Memory/Postgres Store 行为。
