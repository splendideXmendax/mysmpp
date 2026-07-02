# mysmpp 架构说明

本文描述当前代码的真实结构、运行时数据流和可靠性设计。它面向接手开发、部署联调和排查问题的人。

`mysmpp` 不是单纯的 SMPP 协议骨架。当前实现已经包含下游 SMPP/HTTP 接入、路由、上游 HTTP/SMPP provider、异步 outbox 投递、DLR 映射与回推、运行时配置热更新、管理后台和多种存储 driver。

## 总体数据流

```text
下游 ESME / HTTP client
        |
        | submit_sm / POST /v1/messages
        v
SMPP server / HTTP gateway
        |
        | dispatch.Submit
        v
route match + address rewrite + destination validation
        |
        | SubmitAtomic
        v
messages + outbox + idempotency
        |
        | dispatcher workers claim outbox
        v
provider.Send
        |
        +--> HTTP provider -> upstream HTTP API
        |
        +--> SMPP provider -> smppclient pool -> upstream SMSC
        |
        v
provider_id saved in messages + pending
        |
        | upstream DLR callback / deliver_sm
        v
pending lookup -> messages state update -> SMPP deliver_sm back to ESME
```

关键原则:

- 下游提交成功不等于上游最终送达。提交成功表示消息已经通过路由校验并写入队列。
- 上游发送由 dispatcher worker 异步完成，失败后按 outbox 重试策略处理。
- DLR 通过 `provider_id` 匹配 `pending`，再找回 `gateway_id` 和原始下游来源。
- HTTP 来源当前只记录 DLR 状态；SMPP 来源如果请求了 DLR，会通过 `deliver_sm` 回推给可接收的 RX/TRX 会话。

## 主要模块

| 路径 | 职责 |
|---|---|
| `cmd/mysmpp` | 主程序入口，加载配置、初始化 store/provider/dispatcher、启动 HTTP 和 SMPP 服务，处理优雅关停 |
| `cmd/testesme` | 本地 SMPP ESME 测试客户端，用于 submit 和接收 DLR |
| `internal/admin` | 管理后台、session、登录限流、CSRF、配置编辑与写回 |
| `internal/config` | 配置结构、默认值、校验、首次启动随机凭据生成、原子写配置 |
| `internal/dispatch` | 统一提交入口、路由后入队、outbox worker、重试、pending DLR、gateway_id 分配 |
| `internal/httpgw` | HTTP API、客户端鉴权、IP 白名单、风控、入站 HTTP 规则和配置 API |
| `internal/httprule` | HTTP 上游请求渲染，支持 JSON/form/query/header |
| `internal/message` | 消息模型、GSM-7/UCS-2 编解码、长短信分段 |
| `internal/provider` | 上游 provider 抽象，包含 HTTP、SMPP、mock 和限速包装 |
| `internal/router` | 按 priority 和最长前缀匹配路由，只启用 enabled provider 的路由 |
| `internal/smpp` | SMPP 服务端 PDU、session、bind、submit_sm、deliver_sm DLR、TLV |
| `internal/smppclient` | mysmpp 作为 ESME 连接上游 SMSC，连接池、bind、窗口流控、重连、DLR 解析 |
| `internal/store` | memory/file/Postgres 存储实现，统一承载 messages、outbox、pending、idempotency、ID 分配 |
| `migrations` | Postgres schema 和升级 SQL |

## SMPP 下游服务端

`internal/smpp` 实现了基础 SMPP 3.4 服务端能力:

- 支持 `bind_receiver`、`bind_transmitter`、`bind_transceiver`。
- bind 前有 30 秒超时，避免连接建立后长期不认证。
- bind 后按 `enquire_period` 发送 `enquire_link`，超过 2 倍周期没有入站活动会关闭会话。
- 单会话 submit window 满时返回 throttled。
- 认证使用配置中的 `esmes`，密码比较使用常量时间比较。
- DLR 回推使用 `deliver_sm`，包含 receipt text、`receipted_message_id` 和 `message_state` TLV。

下游 SMPP `submit_sm` 进入 `dispatch.Submit` 后，会先完成路由、号码重写、目的地址校验和持久化，再返回 `submit_sm_resp`。如果目的地址非法，会映射为 SMPP `ESME_RINVDSTADR`。

## SMPP 上游客户端

`internal/smppclient` 用于 `protocol=smpp` 的 provider。mysmpp 此时作为 ESME bind 到上游 SMSC。

实现要点:

- 每个 provider 可以配置多个 bind 连接，pool 会轮询选择已绑定连接。
- 连接断开后使用指数退避重连。
- bind 成功后才启动 enquire_link 空闲探测。
- submit 使用窗口流控，按 sequence ID 等待 `submit_sm_resp`。
- `submit_sm_resp` 的 `message_id` 会按配置归一化，作为 pending 的 `provider_id`。
- 上游 `deliver_sm` 如果是 DLR，会解析 provider message ID、状态和错误码，并交给 dispatcher。
- 上游 MO 当前会被识别并忽略，不会路由给下游。

长短信在 SMPP 上游可按 `udh`、`payload` 或 `sar` 策略发送。HTTP 上游当前仍按完整原文发给 provider。

## Dispatcher 和 Outbox

`internal/dispatch` 是发送链路的核心。

提交路径:

1. 规范化目标号码，去掉前导 `+`。
2. 使用 router 按 priority 和最长前缀匹配 route。
3. 按 route 配置执行可选地址重写。
4. 按全局和 route 配置进行目的地址校验。
5. 从 store 预留 gateway_id。
6. 构造 message 和 outbox payload。
7. 通过 `SubmitAtomic` 原子写入 message、outbox 和 idempotency。

发送路径:

1. worker 周期性 claim `pending` outbox。
2. 每个 worker 使用有界并发调用 provider。
3. 发送成功后更新 message 为 `sent`，保存 pending DLR 映射，然后 ack outbox。
4. 发送失败后按指数退避设置 `next_retry_at`。
5. 达到最大尝试次数或遇到永久错误时，message 标记为 `failed`，outbox 标记为 `failed`。

可靠性要点:

- Postgres claim 使用 `FOR UPDATE SKIP LOCKED`，适合多 worker 和多实例并发消费。
- `claim_timeout` 会把超时未 ack/fail 的 `claimed` outbox 退回 `pending`，避免进程崩溃后任务卡死。
- HTTP 幂等提交使用 `(client_id, client_msg_id)`，并在 store 事务里先插入幂等记录。
- gateway_id 通过 store 的 `ReserveGatewayIDRange` 分段分配。Postgres 使用 `id_alloc` 表，file store 会落盘；memory store 重启后仍会丢失状态。

## 存储模型

统一 store 接口覆盖:

- `messages`: 消息主记录和状态。
- `outbox`: 待发送队列。
- `pending`: provider_id 到 gateway_id 和下游来源的 DLR 映射。
- `idempotency`: HTTP 客户端幂等键。
- `id_alloc`: Postgres 下的 gateway_id 高水位。

driver 区别:

| driver | 用途 | 持久化 | 适用场景 |
|---|---|---:|---|
| `memory` | 纯内存 | 否 | 本地快速开发、单元测试 |
| `file` / `json` | JSON 文件快照 | 是 | Docker 单机试运行、轻量联调 |
| `postgres` / `pg` | PostgreSQL | 是 | 生产和多实例部署 |

生产环境建议使用 Postgres，并在启动前执行 migrations。

## 热更新配置

配置可以通过管理后台或 `/v1/config` 更新。更新流程:

1. 对新配置执行 `Normalize()` 和 `Validate()`。
2. 根据新配置构建 provider map。
3. 替换运行时配置。
4. 替换 provider registry，并关闭未保留的旧 provider。
5. 重建 dispatcher router。
6. 如果配置了 `configPath`，原子写回配置文件。

注意: 热更新 provider 时，旧 SMPP 上游连接会被关闭；正在发送的 in-flight 请求可能失败并进入 outbox 重试。因此生产环境建议在低峰期变更关键上游配置。

## HTTP API 和入站规则

`internal/httpgw` 提供:

- `/healthz`: 存储、pending、outbox 健康检查。
- `/v1/messages`: HTTP 提交和分页查询。
- `/v1/config`: 管理员 Basic Auth 的配置读写 API。
- `/ui/config`: 简单配置页面。
- 动态 inbound rule: 接收 provider DLR 或普通入站 HTTP 消息。

鉴权模式:

- 如果 `clients` 为空，`/v1/messages` 使用 admin Basic Auth。
- 如果配置了 `clients`，提交方必须带 `X-Client-ID` 和 `X-Token`。
- `allowed_ips` 会结合 `trusted_proxies` 判断真实客户端 IP。

入站 DLR 规则必须同时映射 `provider_id` 和 `status`，并指定 provider。

## 管理后台

`/admin/` 是独立的 HTML 管理后台，包含:

- 用户名密码登录。
- 登录失败限流。
- HttpOnly session cookie。
- CSRF token。
- 路由和配置编辑。
- SMPP 上游连接状态查看。
- 上游连接测试发送。

公网暴露时必须放在 TLS 反向代理后面。

## 可靠性边界

当前已经实现的保护:

- outbox 持久化和重试。
- stale claimed outbox 回收。
- pending DLR 延迟补投。
- HTTP 幂等提交。
- Postgres 多 worker 安全 claim。
- SMPP session window 和上游 window。
- 目的地址基础校验和可配置地址重写。

仍需注意的边界:

- `memory` driver 重启后会丢失消息、outbox、pending、幂等和 ID 状态。
- 风控计数是进程内 map，多实例部署时限额会按实例数放大。
- HTTP 来源 DLR 会更新内部状态；提交时携带 `callback_url` 时会主动 POST 回调。
- HTTP 上游不做长短信逐段发送。
- SMPP 上游暂不支持 `tx_rx` 分离 bind 和 TLS。
- 上游 MO 当前识别后忽略，尚未路由给下游。
- 没有 Prometheus 指标和完整审计日志。

## 验证命令

```bash
go test ./...
go vet ./...
go test -race ./...
```

这些测试覆盖配置校验、HTTP/API/Admin、入站规则、provider 渲染、消息编码、SMPP session、DLR/TLV、dispatcher/outbox 和 store 行为。
