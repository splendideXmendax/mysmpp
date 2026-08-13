# mysmpp v1.0.1

发布日期：2026-08-13

## 变更

- weighted 路由改为使用每条消息的 `gateway_id` 做稳定哈希，不再按目标号码分流。
- 同一号码的多条消息现在分别参与权重配比；同一个 `gateway_id` 仍稳定选择同一 provider。
- 权重计算升级为 64 位哈希和 64 位累加，并防御非法权重及总权重溢出。
- 保持 route 匹配、号码改写和目标号码校验在分配 `gateway_id` 之前，拒绝请求不会消耗消息 ID。
- 保证选中的 provider 同时固化到 receipt、message 和 outbox；异步重试不会重新哈希换通道。
- 增加 HTTP 转 SMPP 中文接口文档，包含当前部署、鉴权、提交、查询和 DLR 回调说明。

## 兼容性

- 未配置 `weighted` 的单 provider 路由行为不变。
- 已配置 `weighted` 的路由升级后会重新落桶，这是本版本的预期行为。
- 数据库 schema 和配置文件结构没有变化，无需执行 migration。
- `failover` 仍只选择第一个 enabled provider，发送失败后自动切换备用 provider 尚未实现。

## 验证

- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/router ./internal/dispatch ./internal/httpgw ./internal/store`
- 核心 weighted、幂等和拒绝路径压力测试连续 100 轮
- 全量测试随机顺序连续 5 轮
- Windows 386 目标核心包测试
- `go build ./...`
