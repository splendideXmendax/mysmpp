# mysmpp 配置设计

这份文档描述 `mysmpp` 的配置思路。核心原则是把短信网关拆成四层：下游接入、标准消息模型、路由决策、上游投递。

## 角色定义

- 下游：你的客户、业务系统、SMPP 客户端、HTTP 调用方。它们把短信交给网关，或接收网关回调。
- 上游：运营商、短信供应商、聚合通道。网关把短信投递给它们，或接收它们的状态报告和上行短信。
- 路由：根据号码、客户、短信类型、优先级等条件决定走哪个上游。
- 规则：把不同 HTTP 接口的字段名、鉴权、请求格式转换到统一消息模型。

## 配置页面

启动后访问：

```text
http://127.0.0.1:8080/ui/config
```

页面会调用：

```text
GET /v1/config
PUT /v1/config
```

当前实现是运行时生效，不会自动写回 `configs/example.json`。这是有意保守的第一版，避免页面误操作覆盖本地配置文件。后续可以加 `config_path` 和保存到文件的能力。

## 完整结构

```json
{
  "server": {},
  "smpp": {},
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
- `system_id` / `password`：下游 SMPP 客户端 bind 凭证。
- `system_type`：保留字段，后续可用于区分客户类型。
- `max_sessions`：最大会话数，当前文档化，后续在会话管理中强制执行。
- `window_size`：SMPP 并发窗口，后续用于 submit/deliver 流控。
- `enquire_period`：链路保活周期，后续用于主动 enquire_link。

## 下游入站 HTTP inbound

`inbound` 描述“外部系统调用我”的规则，适合接收上行短信、状态回调，或客户通过自定义 HTTP 接口提交短信。

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

字段含义：

- `name`：规则名，也会记录到消息的 `Provider` 字段，方便追踪来源。
- `method`：允许的 HTTP 方法，默认 `POST`。
- `path`：回调路径，必须以 `/` 开头。
- `content_type`：说明预期类型；当前请求解析支持 JSON、query、form。
- `auth_header` / `auth_token`：简单 header token 鉴权。
- `fields`：统一字段到外部字段的映射。
- `success_status` / `success_body`：处理成功后返回给对方的响应。

`fields` 的左边是网关内部字段，右边是外部请求字段：

```json
{
  "id": "msg_id",
  "from": "src",
  "to": "dst",
  "text": "content"
}
```

## 上游供应商 providers

`providers` 描述“我要发给谁”。供应商自身只记录连接信息和引用哪条 outbound 规则。

```json
{
  "name": "provider-a",
  "protocol": "http",
  "endpoint": "https://sms-a.example.com/send",
  "rule": "http-form-a",
  "enabled": true
}
```

- `name`：供应商唯一名。
- `protocol`：当前建议 `http` 或 `smpp`，第一版实现以 HTTP 规则渲染为主。
- `endpoint`：HTTP URL 或后续 SMPP 连接地址。
- `rule`：引用 `outbound[].name`。
- `system_id` / `password`：上游需要账号时使用。
- `enabled`：是否启用。

## 路由 routes

`routes` 描述“哪些短信走哪个上游”。当前实现是按手机号前缀和优先级匹配。

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

建议设计：

- 给精确号段更高优先级。
- 保留一个 `prefix: []` 的默认路由。
- 不同客户的专属通道后续可以增加 `customer_id`、`country`、`message_type` 等字段。

## 上游出站 HTTP outbound

`outbound` 描述“发给供应商时怎么组织请求”。

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

这里 `fields` 的左边是供应商参数名，右边是网关内部消息字段：

- `id`：网关消息 ID。
- `from`：主叫、签名号、短信端口。
- `to`：被叫手机号。
- `text`：短信内容。
- `encoding`：`gsm7` 或 `ucs2`。
- 其他值会尝试从 `message.Metadata` 读取。

请求格式：

- `method=GET`：字段会进入 query string。
- `content_type=application/json`：字段会序列化成 JSON。
- 其他情况：默认按 `application/x-www-form-urlencoded` 发送。

## 长短信策略

统一消息进入网关后会拆成 `segments`：

- GSM-7：单条 160，长短信每段 153。
- UCS-2：单条 70，长短信每段 67。
- 每段包含 `Part`、`Total`、`Reference`、`UDH`。

出站适配器应该根据供应商要求选择：

- 供应商自动拆分：只发完整 `text`。
- 供应商要求 UDH：逐段发送并带 `UDH`。
- 供应商要求自定义长短信字段：把 `Reference`、`Part`、`Total` 映射到供应商参数。

当前 outbound 规则先覆盖完整 `text` 模式。逐段投递会在 dispatcher 阶段加入。

## 推荐生产配置方向

1. 把下游客户独立成 `clients`，每个客户有 SMPP/HTTP 凭证、限速、签名策略。
2. 把上游通道独立健康状态，支持失败熔断和备用通道。
3. 路由条件扩展到国家码、号段、客户、内容类型、模板、价格优先级。
4. 持久化消息、分段、回执、重试记录。
5. 配置页面增加保存到文件、导入导出、规则测试器和变更审计。
