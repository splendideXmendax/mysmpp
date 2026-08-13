# HTTP 转 SMPP 接口文档

本文面向通过 HTTP 调用 mysmpp、再由网关转换为 SMPP `submit_sm` 的接入方。

## 1. 当前部署信息

截至 2026-08-14，服务器 `8.219.56.23` 的部署情况如下：

| 项目 | 当前值 |
|---|---|
| HTTP API | `http://8.219.56.23:19087` |
| HTTP 提交接口 | `POST /v1/messages` |
| HTTP 查询接口 | `GET /v1/messages` |
| 上游协议 | SMPP 3.4 |
| HTTP 客户端 | `tenant-a`、`tenant-b`、`tenant-c` |
| 上游 provider | `ap2-upstream`、`stub-2777`、`stub-2778`、`stub-2779`、`stub-2780` |
| HTTP 路由 | 按已鉴权 `client_id` 匹配租户，再按 `gateway_id` 加权选择 provider |
| 上游连接 | 5 个 transceiver bind，当前均可提交 |
| 存储 | PostgreSQL |

HTTP 请求进入 `POST /v1/messages` 后会经过字段校验、风控、内容过滤和路由选择，成功受理后进入 outbox，由 worker 转换为 SMPP `submit_sm` 异步发送至上游。

> 注意：`202 Accepted` 只表示网关已受理并排队，不表示上游已接受，也不表示短信已经送达。最终结果应通过查询接口或 DLR 回调确认。

## 2. 鉴权

当前部署已配置三个独立 HTTP client。请求必须携带对应租户的两个请求头：

```http
X-Client-ID: tenant-a
X-Token: <tenant-a-token>
```

调用示例中的 token 使用环境变量，不应把真实凭据写入代码或日志：

```bash
export MYSMPP_CLIENT_ID='tenant-a'
export MYSMPP_TOKEN='<tenant-a-token>'
```

三个租户的 token 保存在服务器 root-only 文件中：

```text
/root/mysmpp-backups/http-tenant-credentials-v1.0.2.txt
```

当 `clients` 为空时，接口才回退到管理员 HTTP Basic Auth。该兼容模式没有 HTTP 租户身份，不适合 A/B/C 配比或按租户幂等。

当前端口提供的是明文 HTTP。Token 必须通过 HTTPS 才能避免在传输途中泄露；对公网开放前应在网关前部署 HTTPS 反向代理，并限制源 IP，不建议让调用方长期直连公网 `19087`。

## 3. 提交短信

### 请求

```http
POST /v1/messages HTTP/1.1
Host: 8.219.56.23:19087
X-Client-ID: tenant-a
X-Token: <tenant-a-token>
Content-Type: application/json
```

```json
{
  "from": "YourBrand",
  "to": "+8613800138000",
  "text": "验证码 123456，请勿泄露。",
  "client_msg_id": "order-20260813-000001",
  "callback_url": "https://client.example.com/sms/dlr",
  "callback_rule": "order-service",
  "meta": {
    "tenant": "demo",
    "biz_type": "verification"
  }
}
```

### 字段

| 字段 | 必填 | 规则 |
|---|---:|---|
| `from` | 是 | 源地址或签名，1-32 个 Unicode 字符；还需符合上游允许的 source address 规则 |
| `to` | 是 | 11 位纯数字，或 `+` 开头的 E.164 号码；路由前会去掉开头的 `+` |
| `text` | 是 | 非空；自动识别 GSM-7 或 UCS-2，最多拆分为 20 个短信分片 |
| `client_msg_id` | 否 | 1-64 个非空白字符；配置独立 client 后，同一 client 下 24 小时内用作幂等键 |
| `callback_url` | 否 | DLR 回调地址，只允许完整的 `https://` URL |
| `callback_rule` | 否 | 调用方自定义标识；配置后会原样返回在 DLR 回调中 |
| `meta` | 否 | 最多 10 个键；键不能为空，每个值最多 200 个 Unicode 字符 |

当前部署使用独立 HTTP client，同一 `X-Client-ID` 下重复提交相同 `client_msg_id`，24 小时内会返回原 `gateway_id`，不会重复入队。不同租户可使用相同的 `client_msg_id`，互不冲突。

### curl 示例

```bash
curl --fail-with-body \
  --header 'Content-Type: application/json' \
  --header "X-Client-ID: $MYSMPP_CLIENT_ID" \
  --header "X-Token: $MYSMPP_TOKEN" \
  --data '{
    "from": "YourBrand",
    "to": "+8613800138000",
    "text": "test message",
    "client_msg_id": "order-20260813-000001"
  }' \
  'http://8.219.56.23:19087/v1/messages'
```

这是真实发送接口。不要使用未授权的号码测试；请求成功后可能产生费用并实际送达。

### 成功响应

HTTP 状态为 `202 Accepted`：

```json
{
  "gateway_id": "m0000abc",
  "provider_id": "",
  "provider": "stub-2778",
  "route": "tenant-a-http-weighted",
  "state": "queued"
}
```

| 字段 | 说明 |
|---|---|
| `gateway_id` | mysmpp 生成的消息 ID，应由调用方保存 |
| `provider_id` | 异步入队时通常为空；上游返回 `submit_sm_resp` 后才产生 |
| `provider` | 本次路由选中的上游 provider |
| `route` | 命中的路由 |
| `state` | 提交响应通常为 `queued`；幂等命中旧请求时可能返回旧消息当前状态 |

## 4. 查询消息状态

接口只支持分页查询，目前不支持按 `gateway_id` 直接过滤：

```bash
curl --fail-with-body \
  --header "X-Client-ID: $MYSMPP_CLIENT_ID" \
  --header "X-Token: $MYSMPP_TOKEN" \
  'http://8.219.56.23:19087/v1/messages?limit=100&offset=0'
```

| 参数 | 默认值 | 规则 |
|---|---:|---|
| `limit` | `100` | `1-1000`；越界时回退为 100 |
| `offset` | `0` | 不能小于 0；负数按 0 处理 |

客户端模式会先按已鉴权 `client_id` 过滤，再应用分页；A、B、C 只能查询各自消息。请求头中的租户 ID 只有在 token 校验成功后才生效。

响应是 JSON 数组。当前版本的字段名使用 Go 结构体字段名，区分大小写：

```json
[
  {
    "ID": "m0000abc",
    "ProviderID": "123456789",
    "Direction": "mt",
    "From": "YourBrand",
    "To": "8613800138000",
    "Text": "test message",
    "Encoding": "gsm7",
    "Route": "tenant-a-http-weighted",
    "Provider": "stub-2778",
    "SourceKind": "http",
    "State": "DELIVRD",
    "ErrorCode": 0,
    "Metadata": {},
    "SubmittedAt": "2026-08-13T01:00:00Z",
    "SentAt": "2026-08-13T01:00:01Z",
    "DoneAt": "2026-08-13T01:00:02Z"
  }
]
```

常见状态：

| 状态 | 含义 |
|---|---|
| `queued` | 已进入网关 outbox，等待发送 |
| `sent` | 上游已返回成功的 `submit_sm_resp` |
| `DELIVRD` | 最终送达成功 |
| `UNDELIV` | 最终未送达 |
| `EXPIRED` | 短信过期 |
| `REJECTD` | 被上游或下游网络拒绝 |
| `failed` | 网关向上游提交失败，且重试已结束 |

## 5. DLR 回调

提交时提供 `callback_url` 后，mysmpp 收到上游 SMPP `deliver_sm` 状态报告时，会向该 URL 发送 JSON：

```http
POST /sms/dlr HTTP/1.1
Content-Type: application/json
```

```json
{
  "gateway_id": "m0000abc",
  "provider_id": "123456789",
  "provider": "stub-2778",
  "route": "tenant-a-http-weighted",
  "state": "DELIVRD",
  "error_code": 0,
  "done_at": "2026-08-13T01:00:02.123456789Z",
  "callback_rule": "order-service"
}
```

回调接收端应返回任意 `2xx` 状态。代码会对收到的每条上游 DLR 触发回调，因此接收端应允许中间状态和重复事件，并以 `gateway_id`、`provider_id`、`state` 做幂等处理。当前回调没有签名或自定义鉴权头，接收端不应仅凭请求体来源执行敏感操作。

最终状态包括：`DELIVRD`、`EXPIRED`、`DELETED`、`UNDELIV`、`REJECTD`、`UNKNOWN`。

## 6. 错误响应

错误响应当前为 `text/plain`，不是 JSON：

| HTTP 状态 | 典型原因 |
|---:|---|
| `400 Bad Request` | JSON 无效、字段校验失败、目标号码校验失败、短信超过 20 个分片 |
| `401 Unauthorized` | 缺少或错误的 Basic Auth / client 凭据 |
| `403 Forbidden` | client 来源 IP 不在白名单，或内容过滤规则拒绝 |
| `429 Too Many Requests` | 命中号码或 client 维度的风控限流 |
| `502 Bad Gateway` | 没有可用路由、入队失败或调度器提交失败 |
| `405 Method Not Allowed` | 对 `/v1/messages` 使用了 GET/POST 之外的方法 |

上游在异步发送阶段拒绝 `submit_sm` 不会改写已经返回的 HTTP `202`；调用方需通过查询或 DLR 判断后续结果。

## 7. 健康检查

```bash
curl --fail-with-body 'http://8.219.56.23:19087/healthz'
```

```json
{
  "checks": {
    "outbox_depth": 0,
    "pending_size": 0,
    "smpp_listener": "ok",
    "storage": "ok"
  },
  "status": "ok"
}
```

`smpp_listener: ok` 只代表 mysmpp 自身的 SMPP 服务端监听状态，不代表上游 SMPP provider 已 bind。上游实时状态需要在管理后台 `/admin/connections` 查看，正常状态为 `bound`。

## 8. 当前租户配比

| HTTP 客户端 | SMPP 上游端口配比 |
|---|---|
| `tenant-a` | `2776=10%`、`2777=30%`、`2778=60%` |
| `tenant-b` | `2779=50%`、`2780=50%` |
| `tenant-c` | `2776=0%`、`2777=20%`、`2778=80%`；权重列表省略 0% 通道 |

权重使用每条消息的 `gateway_id` 做稳定哈希。同一个目标号码的多条消息分别参与配比；单次小样本允许波动，消息量增加后会趋近配置比例。

## 9. 当前部署核查结果

2026-08-14 的上线核查结果：

- mysmpp 容器运行正常，restart count 为 0。
- HTTP `19087` 和 SMPP `29175` 均绑定到宿主机所有 IPv4/IPv6 地址。
- 健康检查为 `status: ok`，PostgreSQL、outbox、pending 均正常。
- `2776-2780` 五个测试上游均已建立连接。
- 三租户 HTTP 提交、SMPP 转发、DLR、幂等和查询隔离均需在每次升级后回归验证。
