# 长短信与回执可靠性修复说明

本次改动集中在提交校验、分片路由绑定、上游发送结果保存和回执投递。租户账号、密码、路由权重、日额度、TPS、国家号码规则均不要求修改；不引入 Redis。

## 问题处理口径

| 问题 | 本次行为 |
| --- | --- |
| DA 国家码不存在 | 保留现有国家码校验，285 等无效国家码拒绝 |
| 国家码后多一个 0 | 保留路由级 `strip_trunk_zero`，不自动启用、不擅自改写客户号码 |
| 16 位国际号码 | 保留 E.164 最多 15 位的限制，不能按“老挝号码”统一放宽 |
| bind 前心跳 | 保留现有先 bind 再心跳机制 |
| HTTP 正常提交返回 202 | 保留异步受理契约；202 不代表上游已送达 |
| 超过日额度/TPS | HTTP 429；SMPP `ESME_RTHROTTLED=0x00000058`；不新增消息、不扣额 |
| 超额后已发送短信无 DR | 回执队列独立于提交准入；已有 DR 不受日额度、TPS 或新提交拒绝影响 |
| 长短信没有统一上限 | HTTP/SMPP 统一默认 20 段，可用 `dispatcher.max_message_segments` 设置 1–255；0/省略表示 20 |
| 长短信分流 | 对 SMPP UDH8、UDH16、SAR 分片持久化绑定同一路由和供应商 |
| 明确的上游非零 submit 状态 | 生成失败回执；保留此前已受理分片的上游 ID，整条不重发 |
| 上游提交超时/连接中断 | 不推断成明确拒绝，不自动重发；不确定部分记录 UNKNOWN |
| 重启丢上游 DR | 上游回执落库后才成功 ACK；客户投递独立持久化、可重试 |
| HTTP / HTTPS 回调 | 当前源码同时接受两种协议；HTTPS 正常验证证书；两种协议均拒绝跳转、URL 账号密码及内网连接 |

## 长短信按什么标识绑定

HTTP 一次提交的长文本原本就只选一次供应商。需要修复的是客户先拆片、逐条提交的 SMPP 消息。

分组依据：解析后的租户与协议账号、源地址、目的地址、DCS、拼接方式及拼接引用号。UDH 的附加信息元素（如应用端口）也纳入区分；UDH8、UDH16、SAR 使用不同命名空间。总片数存入绑定并校验一致性，改变总片数不会创建另一条路由绑定。

例如，同一账号向同一号码发送 `UDH reference=42,total=3,part=1/2/3`：首个到达分片确定路由和供应商，后两片读取该绑定。第一片也可以乱序到达；多个连接和进程共享 PostgreSQL 的原子绑定。

每个 `submit_sm` 保留独立 `gateway_id`、`submit_sm_resp`、幂等记录、额度扣减和 DR。拼接引用号不是客户业务 ID，也不是上游 provider_id；不能只用 gateway_id 或只有 8 位的 reference 跨账号归组。

绑定窗口为 10 分钟，引用号在窗口内不得复用于相同账号、地址和编码的另一条短信。相同片序出现不同内容或总数冲突时拒绝。到期后仅第 1 片可以建立下一组；过期的非首片拒绝。过期绑定保留 7 天作为检测迟到分片的记录。这是有界关联窗口，不是跨任意时间的拼接重组：客户必须合理管理引用号，网关无法区分全部标识和内容都相同的两条短信。

热加载路由权重时保留已有绑定的路由快照及地址改写规则。管理员移除/禁用绑定的路由或供应商后，剩余片拒绝，不自动切供应商。同一供应商的不同 SMPP bind 仍可按原连接池策略使用。

## 长度、额度和分片计数

- 常规 GSM7：单段 160 septets，拼接每段 153；扩展字符占两个 septets。
- UCS2：单段 70 个 UTF-16 单元，拼接每段 67；补充平面字符占两个单元。
- 原始二进制和客户预拆片按 DCS、原始字节及 UDH 长度校验，不依赖解码后字符串长度，不重新编码客户原始负载。
- UDH16 比 UDH8 多占一个字节，分片承载量相应减少；SAR 的 total/part/ref 必须有效。
- 限制在生成 gateway_id、消息/额度事务之前校验。超限 HTTP 400，SMPP `ESME_RINVMSGLEN=0x00000001`。
- 客户已拆成 3 个 submit_sm 时，每次按 1 段扣额度，不能每片重复扣 3 段。
- TPS 仍按提交请求计算；日额度仍是受理的短信段数。`long_message=payload` 的长文本可能用一个 PDU 提交、由上游继续拆分；虚拟短信段数用于额度，`segment_count` 是本 gateway_id 的上游提交结果数量，两者不保证相等。
- HTTP `client_msg_id` 是客户提供的业务幂等 ID；SMPP 当前使用网关生成的请求幂等键，并非自动从客户业务系统读取同名 ID。

## 回执持久化与客户接口

链路为：接收上游 DLR → 持久化 `receipt_jobs/inbox` → 成功 ACK 上游 → 原子更新分片/整条状态并保存客户投递快照 → 独立工作线程推送客户 → 保存完成状态。

找不到 pending 的早到回执留在 inbox 中重试；供应商与 provider_id 必须同时匹配。存储不可用时 SMPP 返回非零 `deliver_sm_resp`，HTTP 上游回调返回 503，要求上游重试。不能承诺修复部署前已经 ACK 后丢失且上游不再重推的历史事件。

客户接收失败、离线或未 ACK 时重试客户回执，不重新发送短信。任务采用有期限的数据库租约，过期可被新进程接手，旧租约不能提交完成状态。指数退避最高 5 分钟，客户投递最多保留 48 小时，同时设置最多 1000 次尝试；达到边界进入 dead，保留 7 天供审计。已完成任务清除正文，仅保留去重记录至有效期结束。

回调是至少一次投递：客户收到后、网关记录成功前发生故障，可能重复通知。HTTP 客户应以 `delivery_id` 幂等处理，并尽快返回任意 2xx；同一任务重试的正文保持一致。多个分片的通知不保证到达顺序，客户应遵守 `final` 与 `message_state`，不能把较迟收到的 `final=false` 覆盖已经处理的终态。

```json
{
  "delivery_id": "delivery:<stable-event-hash>",
  "gateway_id": "m0000001",
  "client_msg_id": "order-001",
  "provider_id": "upstream-001",
  "provider": "provider-a",
  "route": "route-a",
  "segment_index": 2,
  "segment_count": 3,
  "state": "DELIVRD",
  "message_state": "PENDING",
  "final": false,
  "error_code": 0,
  "done_at": "2026-09-18T08:00:00Z"
}
```

v1.2.1 支持公网 HTTP 和 HTTPS 回调，无需增加配置开关；v1.2.0 仅支持 HTTPS。两种协议均禁止跳转和连接内网、回环、链路本地地址；DNS 解析后的 IP 在实际连接前校验，避免 DNS 重绑定。HTTPS 正常验证证书，IP 地址形式的 URL 仍需证书包含该 IP；HTTP 按客户提供的明文协议投递，不自动升级或降级。地址取自每条请求的 `callback_url`，不存在全局默认回调地址。

上游部分分片明确拒绝时，已成功分片继续跟踪真实 DR；未提交分片标记 UNDELIV，不确定片标记 UNKNOWN。SMPP 文本回执错误字段保持三位格式，超范围映射为 999；完整 uint32 上游状态保存在 pending 的 upstream_status，以及 HTTP/CDR 的 submit_status，不会因数据库整数范围而保存失败。消息状态聚合按失败优先级确定，不能由最后一片到达顺序覆盖成成功。

新版本受理的记录带 `pending.reliability_managed=true`。到期还缺真实 DR 或缺少分片记录时，保存 EXPIRED 投递快照再清理映射，避免永远 PENDING。旧记录保持原清理策略，不因升级批量补推历史 HTTP 回调。

SMPP 回执会再次校验接收会话所属 system_id。进程重启后 session ID 被其他客户复用，也不能串发 DR；历史记录若缺少 system_id 则拒绝猜测收件人。

## 修改边界与部署

主要修改 `internal/dispatch`、`internal/store`、SMPP 上游回调接口以及入口错误映射。路由匹配规则、租户解析、日额度原子事务和客户端鉴权保持原模型；只在租户配置变化时不重建未变更的 Provider。变更 Provider 本身仍按既有机制重连。

迁移 `008_reliable_receipts_multipart.up.sql` 增加两张表和两个带默认值的 pending 列，不修改账号、路由或额度数据。必须先执行迁移再启动新二进制。使用数据库备份、短 lock_timeout 和事务；迁移失败则保持旧版本运行。不要在有业务回执任务时执行 down migration。

只替换 mysmpp 容器，保留原配置卷、端口、网络、用户、重启和日志设置。其他业务容器与 PostgreSQL 服务不重启。对照更新前后的配置 SHA256 和其他容器 ID/启动时间。

回滚旧二进制不需要删除新增表，但旧版本不认识新的回执队列：需先排空或保留任务，修复后恢复新版本处理。上游提交结果不确定时保持 sending/uncertain，不自动重放短信。memory driver 重启丢失全部内存状态；file driver 支持正常保存后的重启恢复，但先改内存再保存文件，落盘失败时不具有 PostgreSQL 事务的同等保证。要求故障条件下的持久化准入及回执保证时，应使用 PostgreSQL。

v1.2.1 修复了 v1.2.0 引入的单轮清理上限、SMPP 接收循环同步等候回执落库，以及 inbox 与过期清理的竞争。新增迁移 `009_receipt_inbox_expiry_index.up.sql` 只增加活动 inbox 的映射索引。清理按时间预算连续处理多批，完整终态组批量删除；未完成组仍事务生成 EXPIRED 快照。已持久化且 pending/claimed 的 inbox 会阻止该消息过期，入库和过期决策通过消息行锁串行化。SMPP 每连接使用 128 个待处理回执槽位及一个工作线程，落库成功再 ACK；满队列或落库失败返回非零 ACK，上游需要重试。详见 [修复报告](修复报告-2026-09-19.md)，短时回归不代表已经证明任意规模下的长期容量。

排查队列时只读查询，不包含短信正文或客户令牌：

```sql
SELECT state, COUNT(*) FROM outbox GROUP BY state;
SELECT kind, state, COUNT(*) FROM receipt_jobs GROUP BY kind, state;
SELECT id, kind, attempt, expires_at, last_error
FROM receipt_jobs WHERE state='dead' ORDER BY expires_at DESC LIMIT 50;
```

## 回归覆盖

测试包含：真实网关进程的 SMPP 成功响应、额度 0x58、长度 0x01、超额后的延迟 DR；HTTP HTTPS 校验；UDH/SAR 字节边界；配置变更和重启后的分片绑定；并发首次绑定；引用冲突；部分发送失败与不重发；上游 ACK 等待持久化及存储失败 NACK；客户回调失败重试和固定正文；供应商 ID 冲突保护；跨账号 session ID 复用；租约恢复及旧租约拒绝；并发聚合、迟到非终态、缺片超时；memory/file/PostgreSQL 一致行为及迁移。

运行全部单元和协议测试：`go test -count=1 ./...`、`go vet ./...`。在独立测试 PostgreSQL 上设置 `MYSMPP_TEST_POSTGRES_DSN` 后运行 `go test -race -count=1 ./...`；未设置 DSN 时 PostgreSQL 集成测试会明确跳过，不能把跳过当通过。
