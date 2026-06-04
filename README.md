# mysmpp

`mysmpp` 是一个短信网关项目骨架，目标是同时兼容 SMPP 和多种 HTTP 短信接口规则。当前版本已经跑通最小 SMPP 中转链路：ESME 通过 `submit_sm` 提交短信，网关返回 message_id，mock provider 异步生成 DLR，网关再用 `deliver_sm` 把回执推回原 ESME 会话。

## 项目目标

- 支持下游 SMPP 客户端连接，也提供 HTTP API 提交短信。
- 支持短短信和长短信，区分 GSM-7 与 UCS-2，并按常见长度拆分。
- 支持不同上游供应商的 HTTP 请求规则，例如 JSON、表单、GET query、自定义 header。
- 支持不同下游或供应商的 HTTP 回调规则，例如上行短信、状态回调、客户自定义提交接口。
- 支持按号码前缀和优先级做路由，后续可扩展到客户、国家、短信类型、价格等维度。
- 跑通 MVP 级 DLR 回执链路，并为真实上游、重试队列、持久化存储、完整 SMPP TLV 能力预留接口。

## 当前目录

```text
cmd/mysmpp          网关入口
cmd/testesme        本地 SMPP ESME 测试客户端
internal/config     JSON 配置结构、加载、校验
internal/core       SMPP 中转核心、provider 调度、DLR 映射
internal/httpgw     HTTP API、配置页面、入站/出站 HTTP 规则
internal/message    消息模型、编码识别、长短信拆分和重组
internal/provider   上游适配器接口和 mock provider
internal/router     按号码前缀匹配上游供应商
internal/smpp       SMPP TCP 服务、session、PDU 读写、submit/DLR 处理
internal/store      存储接口和内存实现
configs             示例配置
docs                配置和部署文档
```

## 本地运行

`configs/example.json` 是生产前检查模板，包含 `CHANGE_ME_BEFORE_DEPLOY` 占位符。首次运行前请复制一份本地配置并替换 admin、SMPP ESME、HTTP client token 等占位值。

```powershell
go run ./cmd/mysmpp -config configs/example.json
```

默认地址：

- HTTP API：`:8080`
- SMPP 监听：`:2775`
- SMPP 网关标识：`system_id=mysmpp`
- 示例 ESME bind：`system_id=esme1`，`password=<你在配置里设置的密码>`

配置页面：

```text
http://127.0.0.1:8080/ui/config
```

## SMPP DLR 验证

启动网关后，另开一个终端运行测试 ESME：

```powershell
go run ./cmd/testesme -addr 127.0.0.1:2775 -u esme1 -p <你的ESME密码> -text ping
```

预期能看到类似输出：

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 ... stat:DELIVRD err:000 text:ping
```

这条链路不依赖真实短信供应商，当前由内置 mock provider 模拟上游投递和 DLR。

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
- `smpp`：SMPP 监听地址、网关 system_id、窗口、会话参数。
- `esmes`：允许 bind 进来的下游 SMPP 客户端凭证。
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
- `submit_sm` 解析和 `submit_sm_resp`。
- `registered_delivery` 识别。
- mock provider 异步生成 DLR。
- `deliver_sm` 推送 DLR，包含 receipt text、`receipted_message_id` TLV 和 `message_state` TLV。
- UCS-2 big-endian 短信内容解码。
- 客户端到服务端的会话级测试。

后续要补：

- 真实上游 SMPP/HTTP provider。
- 上行短信 MO 推送。
- 更完整的 SMPP TLV 解析和渲染。
- `data_coding` 完整处理和 GSM-7 packed 编解码。
- DLR 状态机和持久化映射。
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
- UCS-2 编解码。
- SMPP `submit_sm` 解析。
- SMPP 客户端/服务端 bind、submit、enquire_link、unbind 会话。
- SMPP 中转链路：submit_sm -> mock provider -> deliver_sm DLR。
- DLR receipt text、方向反转和标准 TLV。

## 后续路线

1. 增加 SQLite、PostgreSQL、MySQL 存储实现，持久化消息和 DLR mapping。
2. 增加真实上游 provider，支持 SMPP/HTTP 投递、重试、限速、失败切换。
3. 完整实现 SMPP deliver_sm、MO、TLV、长短信和窗口控制。
4. 增加管理 API，查看路由、供应商、队列、消息轨迹。
5. 增加指标、审计日志和配置变更记录。
