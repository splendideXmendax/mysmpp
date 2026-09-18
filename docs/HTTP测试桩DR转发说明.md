# HTTP 测试桩 DR 精确转发（v1.2.2）

HTTP 客户的 DR 以 HTTP POST JSON 推送，SMPP 客户的 DR 仍通过 SMPP deliver_sm。本功能解决测试桩 HTTP 公网地址无法从网关回流访问、但固定内网地址可达的场景，不涉及协议转换。

## 转发范围

管理员在 `dispatcher.callback_forwarding` 中登记 **租户 + 完整 HTTP callback_url → 固定内网 HTTP target_url**。代码默认没有规则，保持原来的公网 HTTP/HTTPS 回调行为。规则通过已有管理员配置入口管理，不接受客户请求中的转发目标或 meta 参数。

示例（地址为占位值，不能直接覆盖完整环境配置）：

```json
{
  "dispatcher": {
    "callback_forwarding": [
      {
        "tenant_id": "tenant-c",
        "callback_url": "http://callbacks.example.com:2761/sl_callback/callback/sms_report",
        "target_url": "http://10.20.30.40:2761/sl_callback/callback/sms_report"
      }
    ]
  }
}
```

客户继续在提交请求中传登记的 HTTP `callback_url`，无需传 target_url，也不需要修改鉴权、短信路由或接收程序。不填写 callback_url 不会自动回调。匹配使用持久化的 tenant_id 与原始 URL 字符串，协议、端口、路径必须完全一致；其他租户、其他路径、查询参数、HTTPS 地址都不会套用该规则。

## 安全与行为边界

- 两个 URL 都必须是 HTTP，有明确路径，不含账号密码、查询参数或 fragment。HTTPS 不通过本规则降级。
- target_url 仅允许明确端口的私有 IP 字面量，不接受动态 DNS、公网 IP、回环、链路本地或元数据地址。
- 每条转发规则的连接器只允许访问该固定 IP:port，不走环境代理，不跟随重定向，不按请求指定其他目标。
- 普通回调仍使用原公网地址检查。此功能是管理员授予指定租户、指定回调的单点内网访问能力，不是全局放开 SSRF 防护。
- 不改客户已保存的 callback_url，不改 DR JSON、delivery_id、gateway_id、client_msg_id、分片字段和短信路由。
- 接收端成功返回任意 2xx 后才标记 delivery 完成。连接失败、超时、5xx 继续既有持久化重试，不重发短信；重定向属于安全拒绝。
- 规则可热加载，已排队但仍待重试的 DR 在下次尝试时应用新规则。已进入 dead 或已丢失的历史事件不会自动复活。删除规则后恢复该 URL 的普通公网投递方式。
- HTTP 明文传输保持用户指定方式；配置和文档不得夹带客户 Token 或接收端密码。

## 本次代码边界与回归

变更仅为 `internal/config` 的配置/校验、`internal/dispatch` 的回调出口选择及转发连接器。没有数据库迁移，没有修改 Store、短信提交、租户准入、SMPP 编码/心跳/DR、路由权重或原有回执状态机。

新增回归覆盖：配置往返；未知租户、重复规则、HTTPS 降级、动态目标、私网以外地址拒绝；租户与完整 URL 精确匹配；错误租户/路径不转发；固定连接目标；503 重试和固定正文；跳转拒绝；配置并发替换；已有持久化回执在重启后应用转发并完成。

运行 `go test -race -count=1 ./...`、`go vet ./...`。PG 存储实现未变；本次没有对业务数据库进行故障注入或压测。

## 环境验收标准

1. 从 mysmpp 网络访问固定内网 HTTP 接收端成功。
2. 对测试租户登记精确映射，其他配置在结构化比较中一致。
3. 之前公网超时的待重试 DR 自动变为 done，测试桩日志出现相同 gateway_id/delivery_id；不能手工修改任务状态或补发短信来代替验收。
4. 新 HTTP 短信和长短信提交返回 202，上游 DR 后测试桩收到 POST，网关 delivery 完成；SMPP 原有链路继续通过。
5. 测试桩、Redis、PostgreSQL 等其他已有服务的容器 ID、启动时间保持不变。

公网端口本身不因该转发层自动开放。内部投递成功与外部可直接访问是两件事；外部测试仍须使用网络可达的网关提交入口。

## 2026-09-19 现场验证结果

已为 tenant-a、tenant-b、tenant-c 配置到现有 HTTP 测试桩的精确转发，仅新增这 3 条规则。租户 TPS、日额、账号密码、供应商及短信路由均未改。未新增对外监听端口、共享防火墙规则或独立转发服务。

| 验证项 | 结果 |
|---|---|
| Windows/Linux 全量 race 与 vet | 通过；本次未重跑独立 PG 集成，存储代码和数据库结构未改 |
| 新 HTTP 短信 `m012i69d` | 1 个 DR 实际到达 HTTP 测试桩，delivery 完成 |
| 新 HTTP 长短信 `m012i69f` | 3 个分片 DR 实际到达 HTTP 测试桩，segment_count=3，包含 final=true / DELIVRD |
| 原积压 `m012i5hl`、`m012i5hn` | 1+3 个 DR 按原重试时间自动补达，无手工改状态或重新发送短信 |
| SMPP `m012i69h/i/j` 三片 | 均走同一路由/供应商，3 个提交响应和 DR 均成功 |
| 回执队列 | 14 个 inbox done、14 个 delivery done，pending/claimed 为 0 |
| 上游发送队列 | pending/claimed/sending/uncertain 为 0 |

此次关闭的是“登记的测试租户 HTTP DR 到测试桩”的链路问题。客户必须传登记的 HTTP URL；旧 HTTPS URL、其他路径或新租户不自动改写为此目标。
