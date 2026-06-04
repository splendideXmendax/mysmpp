# mysmpp 配置设计

这份文档说明 `mysmpp` 的配置模型。整体思路是把短信网关拆成四层：下游接入、统一消息模型、路由决策、上游投递。

## 角色定义

- 下游：客户系统、业务系统、SMPP 客户端、HTTP 调用方。它们把短信交给网关，或者接收网关回调。
- 上游：运营商、短信供应商、聚合通道。网关把短信投递给它们，也会接收它们的上行短信或状态报告。
- 路由：根据号码、客户、短信类型、优先级等条件决定走哪个上游。
- 规则：把不同 HTTP 接口的字段名、鉴权方式、请求格式转换到统一消息模型。

## 配置页面

推荐使用新的服务端渲染管理后台：

```text
http://127.0.0.1:8080/admin/
```

它使用 `admin.username` / `admin.password` 登录，session cookie 为 `HttpOnly`、`SameSite=Strict`，所有 POST 表单都带 CSRF token。后台当前提供概览、线路 CRUD、`providers` / `esmes` / `inbound` / `outbound` / `risk` / `smpp` 分区 JSON 编辑，以及完整原始 JSON 编辑。保存时会先执行完整配置校验并更新运行时配置，再原子写回启动参数 `-config` 指定的配置文件。

旧配置页面仍保留：

```text
http://127.0.0.1:8080/ui/config
```

配置页面使用以下 API：

```text
GET /v1/config
PUT /v1/config
```

旧页面修改的是运行时内存配置，不会自动写回 `configs/example.json` 或 `configs/docker.json`。生产使用建议优先使用 `/admin/`，旧页面只作为应急入口。

## 完整结构

```json
{
  "server": {},
  "smpp": {},
  "esmes": [],
  "routes": [],
  "providers": [],
  "inbound": [],
  "outbound": [],
  "storage": {}
}
```

## server

```json
{
  "http_addr": ":8080",
  "shutdown_timeout": "10s"
}
```

- `http_addr`：HTTP API 和配置页面监听地址。
- `shutdown_timeout`：收到退出信号后的优雅关闭等待时间。

## smpp

```json
{
  "addr": ":2775",
  "system_id": "mysmpp",
  "password": "secret",
  "system_type": "gateway",
  "max_sessions": 128,
  "window_size": 16,
  "enquire_period": "30s"
}
```

- `addr`：SMPP TCP 监听地址。
- `system_id`：网关在 bind 响应里返回给 ESME 的 system_id。
- `password`：兼容旧配置的单 ESME bind 密码；新配置建议使用 `esmes`。
- `system_type`：保留字段，后续可用于区分客户类型。
- `max_sessions`：最大会话数，当前已配置化，后续会在会话管理中强制执行。
- `window_size`：SMPP 并发窗口，后续用于 submit/deliver 流控。
- `enquire_period`：链路保活周期，后续用于主动 `enquire_link`。

## esmes

`esmes` 描述允许连接到网关的下游 SMPP 客户端账号。SMPP 客户端 bind 时提交的 `system_id` 和 `password` 会先匹配这个数组；如果没有命中，会再兼容旧配置里的 `smpp.system_id` / `smpp.password`。

```json
[
  {
    "system_id": "esme1",
    "password": "secret"
  },
  {
    "system_id": "load-tester",
    "password": "test123"
  }
]
```

字段说明：

- `system_id`：下游 ESME 的 bind 用户名，必须唯一。
- `password`：下游 ESME 的 bind 密码。

当前版本的 DLR mapping 会记录 ESME 所在 session。mock provider 返回 DLR 后，网关根据 mapping 找回原 session，并用 `deliver_sm` 把回执推回该连接。

## SMPP 中转与 DLR

当前 SMPP 中转 MVP 的链路如下：

```text
ESME submit_sm -> Session -> Core -> Mock Provider -> Core -> deliver_sm DLR -> ESME
```

`submit_sm_resp` 中返回的 message_id 形如 `g0000000001`。如果 ESME 在 `registered_delivery` 中请求回执，mock provider 会在几秒后生成 `DELIVRD` 状态，网关构造 DLR：

- `esm_class=0x04`，表示 SMSC Delivery Receipt。
- `short_message` 包含 SMPP receipt text，例如 `id:g0000000001 ... stat:DELIVRD ...`。
- TLV 包含 `receipted_message_id` 和 `message_state`。
- DLR 的 `source_addr` / `destination_addr` 会相对原 submit_sm 反向。

本地验证：

```powershell
go run ./cmd/mysmpp -config configs/example.json
go run ./cmd/testesme -addr 127.0.0.1:2775 -u esme1 -p secret -text ping
```

## 下游入站 HTTP：inbound

`inbound` 描述“外部系统调用网关”的规则，适合接收上行短信、状态回调，或者客户通过自定义 HTTP 接口提交短信。

```json
{
  "name": "partner-callback",
  "method": "POST",
  "path": "/callback/partner",
  "content_type": "application/json",
  "auth_header": "X-Token",
  "auth_token": "change-me",
  "fields": {
    "id": "msg_id",
    "from": "src",
    "to": "dst",
    "text": "content"
  },
  "success_status": 200,
  "success_body": "{\"status\":\"ok\"}"
}
```

字段说明：

- `name`：规则名，也会记录到消息来源字段，方便排查。
- `method`：允许的 HTTP 方法，默认 `POST`。
- `path`：回调路径，必须以 `/` 开头。
- `content_type`：预期请求类型；当前解析支持 JSON、query、form。
- `auth_header` / `auth_token`：简单 header token 鉴权。所有入站规则都必须配置，回调校验使用常量时间比较。
- `fields`：内部字段到外部请求字段的映射。
- `success_status` / `success_body`：处理成功后返回给对方的响应。

`fields` 左边是网关内部字段，右边是外部请求字段：

```json
{
  "id": "msg_id",
  "from": "src",
  "to": "dst",
  "text": "content"
}
```

供应商 DLR 回调规则使用 `provider_id` 和 `status` 映射，并且必须把 `provider` 设置为对应供应商名称。网关会同时校验 token 和 pending 记录里的 provider，避免一个回调入口伪造其他供应商的状态。

```json
{
  "name": "provider-a-dlr",
  "method": "POST",
  "path": "/callback/provider-a/dlr",
  "provider": "provider-a",
  "auth_header": "X-Token",
  "auth_token": "change-me",
  "fields": {
    "provider_id": "message_id",
    "status": "status",
    "error_code": "error"
  }
}
```

## 上游供应商：providers

`providers` 描述“短信要发给谁”。供应商配置只记录连接信息，并引用一条 `outbound` 规则。

```json
{
  "name": "provider-a",
  "protocol": "http",
  "endpoint": "https://sms-a.example.com/send",
  "rule": "http-form-a",
  "enabled": true
}
```

- `name`：供应商唯一名称。
- `protocol`：当前建议使用 `http` 或 `smpp`，第一版以 HTTP 规则渲染为主。
- `endpoint`：HTTP URL，或后续 SMPP 上游连接地址。
- `rule`：引用 `outbound[].name`。
- `system_id` / `password`：上游账号。
- `enabled`：是否启用。

## 路由规则：routes

`routes` 描述“哪些短信走哪个上游”。当前实现按手机号前缀和优先级匹配。

```json
{
  "name": "china-mobile",
  "prefix": ["134", "135", "136", "137", "138", "139"],
  "provider": "provider-a",
  "priority": 100
}
```

匹配规则：

1. 按 `priority` 从大到小排序。
2. `prefix` 为空代表默认路由。
3. 号码 `to` 命中某个前缀后使用对应 `provider`。

建议：

- 给精确号段更高优先级。
- 保留一条 `prefix: []` 的默认路由。
- 后续可增加 `customer_id`、`country`、`message_type`、`price_level` 等条件。

## 上游出站 HTTP：outbound

`outbound` 描述“发给供应商时如何组织 HTTP 请求”。

```json
{
  "name": "http-json-b",
  "method": "POST",
  "content_type": "application/json",
  "fields": {
    "messageId": "id",
    "sender": "from",
    "receiver": "to",
    "body": "text",
    "coding": "encoding"
  },
  "headers": {
    "X-Request-ID": "{{id}}"
  }
}
```

这里 `fields` 左边是供应商参数名，右边是网关内部字段：

- `id`：网关消息 ID。
- `from`：主叫、签名号或短信端口。
- `to`：被叫手机号。
- `text`：短信内容。
- `encoding`：`gsm7` 或 `ucs2`。
- 其他值会尝试从 `message.Metadata` 读取。

请求格式：

- `method=GET`：字段进入 query string。
- `content_type=application/json`：字段序列化成 JSON。
- 其他情况：默认按 `application/x-www-form-urlencoded` 发送。

## 长短信策略

统一消息进入网关后会拆成 `segments`：

- GSM-7：单条 160 字符，长短信每段 153 字符。
- UCS-2：单条 70 字符，长短信每段 67 字符。
- 每段包含 `Part`、`Total`、`Reference`、`UDH`。

出站适配器后续可以根据供应商要求选择：

- 供应商自动拆分：只发完整 `text`。
- 供应商要求 UDH：逐段发送并带 `UDH`。
- 供应商要求自定义长短信字段：把 `Reference`、`Part`、`Total` 映射到供应商参数。

当前 outbound 规则先覆盖完整 `text` 模式，逐段投递会在 dispatcher 阶段加入。

## 推荐生产配置方向

1. 增加 `clients` 配置，把每个下游客户的 SMPP/HTTP 凭证、限速、签名策略独立出来。
2. 为上游通道增加健康状态、失败熔断、备用通道和价格策略。
3. 路由条件扩展到国家码、号段、客户、内容类型、模板、价格优先级。
4. 持久化消息、分段、回执和重试记录。
5. 配置页面增加保存到文件、导入导出、规则测试器和变更审计。
