# mysmpp

`mysmpp` 是一个短信网关项目骨架，目标是同时兼容 SMPP 和多种 HTTP 短信接口规则。第一版重点放在工程边界和可扩展性：SMPP 会话、HTTP 接入、路由、长短信拆分、上下游规则配置、配置页面和存储接口都已经有了基础结构。

## 项目目标

- 支持下游 SMPP 客户端连接，也提供 HTTP API 提交短信。
- 支持短短信和长短信，区分 GSM-7 与 UCS-2，并按常见长度拆分。
- 支持不同上游供应商的 HTTP 请求规则，例如 JSON、表单、GET query、自定义 header。
- 支持不同下游或供应商的 HTTP 回调规则，例如上行短信、状态回调、客户自定义提交接口。
- 支持按号码前缀和优先级做路由，后续可扩展到客户、国家、短信类型、价格等维度。
- 为 DLR、重试队列、持久化存储、完整 SMPP TLV 能力预留接口。

## 当前目录

```text
cmd/mysmpp          程序入口
internal/config     JSON 配置结构、加载、校验
internal/httpgw     HTTP API、配置页面、入站/出站 HTTP 规则
internal/message    消息模型、编码识别、长短信拆分和重组
internal/router     按号码前缀匹配上游供应商
internal/smpp       SMPP TCP 服务、PDU 读写、基础命令处理
internal/store      存储接口和内存实现
configs             示例配置
docs                配置和部署文档
```

## 本地运行

```powershell
go run ./cmd/mysmpp -config configs/example.json
```

默认地址：

- HTTP API：`:8080`
- SMPP 监听：`:2775`
- SMPP bind：`system_id=mysmpp`，`password=secret`

配置页面：

```text
http://127.0.0.1:8080/ui/config
```

## Docker 运行

使用 Docker Compose 构建并启动：

```powershell
docker compose up -d --build
```

默认暴露：

- HTTP/API/配置页面：`http://127.0.0.1:8080`
- SMPP：`127.0.0.1:2775`

Docker 默认使用 `configs/docker.json`。详细部署、端口、安全组、日志和常见问题见 [docs/DOCKER.md](docs/DOCKER.md)。

## HTTP API

提交一条 MT 短信：

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:8080/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello"}'
```

查看已记录消息：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/messages
```

查看配置：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/config
```

## 配置模型

配置分成几块：

- `server`：HTTP 服务地址和优雅关闭时间。
- `smpp`：SMPP 监听地址、bind 账号、窗口、会话参数。
- `routes`：路由规则，决定短信走哪个上游。
- `providers`：上游供应商配置，包含协议、地址、账号和使用的规则。
- `inbound`：下游或供应商回调到网关时的 HTTP 入站规则。
- `outbound`：网关请求上游供应商时的 HTTP 出站规则。
- `storage`：存储配置，当前默认内存存储。

详细说明见 [docs/CONFIGURATION.md](docs/CONFIGURATION.md)。

## HTTP 规则示例

出站 HTTP 规则把供应商参数映射到网关内部字段：

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
  }
}
```

这可以适配不同供应商的字段名、请求格式和 header，而不需要为每个供应商写一套硬编码逻辑。

## SMPP 支持范围

当前 SMPP 包是基础协议框架，已经支持：

- TCP 监听。
- PDU header 解析和写入。
- `bind_receiver`、`bind_transmitter`、`bind_transceiver`。
- `enquire_link`。
- `unbind`。
- 基础 `submit_sm` 解析和 `submit_sm_resp`。
- 客户端到服务端的会话级测试。

后续要补：

- `deliver_sm`，用于网关向 SMPP 客户端推送上行短信和状态报告。
- SMPP TLV 解析和渲染。
- UCS-2 二进制解码、`data_coding` 完整处理。
- registered delivery 与 DLR 状态机。
- 窗口控制、限速、绑定策略和会话管理。

## 长短信

`internal/message` 会判断文本编码并按常见短信长度拆分：

- GSM-7 单条：160 字符。
- GSM-7 长短信每段：153 字符。
- UCS-2 单条：70 字符。
- UCS-2 长短信每段：67 字符。

长短信分段会包含 `Reference`、`Part`、`Total` 和 `UDH`，后续出站适配器可以根据上游要求选择发完整文本、逐段发送或映射供应商自定义长短信字段。

## 测试

运行全部测试：

```powershell
go test ./...
```

当前测试覆盖：

- 配置加载和校验。
- Docker 配置加载。
- HTTP 提交和路由匹配。
- 动态入站 HTTP 规则。
- 出站 HTTP 请求渲染。
- 长短信拆分和重组。
- SMPP `submit_sm` 解析。
- SMPP 客户端/服务端 bind、submit、enquire_link、unbind 会话。

## 后续路线

1. 增加 SQLite、PostgreSQL、MySQL 存储实现。
2. 增加出站 dispatcher，支持重试、限速、失败切换。
3. 完整实现 SMPP submit/deliver/DLR/TLV。
4. 增加管理 API，查看路由、供应商、队列、消息轨迹。
5. 增加指标、审计日志和配置变更记录。
