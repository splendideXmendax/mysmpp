# mysmpp v1.0.2

发布日期：2026-08-14

## 变更

- HTTP 客户端查询按已鉴权的 `client_id` 隔离，避免跨租户读取消息。
- 租户身份只接受鉴权中间件写入的上下文，不信任管理员模式下额外携带的 `X-Client-ID` 请求头。
- PostgreSQL 新消息同步写入既有 `messages.client_id` 列。
- 增加迁移 `005_message_client_id`，回填历史 `meta.client_id` 并建立租户分页查询索引。
- 更新完整的 HTTP 转 SMPP 中文接口文档和 A/B/C 请求示例。

## 兼容性

- 未配置 HTTP `clients` 时仍使用管理员 Basic Auth，并保留全量消息查询。
- HTTP 提交字段、返回结构和 SMPP 提交行为不变。
- 启用 HTTP `clients` 后，客户端查询只返回自己的消息，这是本版本的安全修复。
