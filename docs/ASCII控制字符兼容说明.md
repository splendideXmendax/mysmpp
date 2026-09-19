# ASCII 控制字符无损兼容（v1.2.4）

## 问题与行为

HTTP `text` 包含 ASCII 0x00–0x7F 时，JSON 解码可成功，但 PostgreSQL TEXT 不允许 U+0000（NUL），原来在受理事务中报 22021 并返回 502。JSONB 中的 `\u0000` 同样不能保存，因此只改 messages 会在 outbox、pending 或持久化 DR 快照处再次失败。

本次完整保留原正文，不删除或替换控制字符，不把含 NUL 的正常文本请求改成 400。成功仍返回 HTTP 200，查询接口 JSON 解码后可取回原文。输入必须为合法 JSON；应通过 JSON 序列化器发送，不能把未转义的控制字节直接拼进 JSON 文档。

可用于测试的 Python 正文：

```python
text = ''.join(chr(i) for i in range(128))
assert len(text) == 128 and text[0] == '\x00' and text[-1] == '\x7f'
body = {
    'from': 'YourBrand',
    'to': '+8613800138000',  # 仅在确认路由指向测试桩后使用
    'text': text,
    'client_msg_id': 'ascii-test-unique-id',
}
# requests.post(url, headers=headers, json=body)
```

这里的真实 NUL 与字面量反斜杠文本 `\u0000`、`\x00` 不同，两种均原样保存，不能混淆。前者在 JSON 中写作 `"\u0000"`，后者例如 `"\\u0000"`；使用 JSON 序列化器可避免手动转义出错。

## 修改边界

生产代码仅修改 PostgreSQL Store 的正文存取和持久化 JSON 编解码，增加 migration 010。协议入口、编码器、路由、配比、账号鉴权、TPS、额度事务顺序、上游重试和 DR 状态机不改；没有新依赖和新配置项。

| 存储位置 | 不含 NUL | 含 NUL |
|---|---|---|
| messages | 原 text 字段，text_bytes 为 NULL | 原始 UTF-8 字节写入新增 BYTEA text_bytes，text 为空；查询恢复原文 |
| pending | 原 text 字段，text_bytes 为 NULL | 同样使用 text_bytes；DR、过期处理读取时恢复原文 |
| outbox.payload | 原 JSON 格式 | 数据库内部 text_bytes 保存 Base64，text 为空；领取任务时恢复原文 |
| receipt_jobs.payload | 原 JSON 格式 | delivery 快照的原始正文保存在内部 text_bytes；领取 DR 时恢复原文 |

二进制/Base64 表示只存在于数据库边界，不暴露为新的客户字段。普通短信不做 Base64 转换，不新增逐条 SQL 查询或事务锁；NUL 正文增加一次线性扫描及必要编解码。原来带 `\\u0000` 字面量的历史短信不会被当成 NUL 解析。文件和内存 Store 原本支持 JSON 转义，本次实现不改，补回归验证。

范围是短信正文 `text`，不把账号、号码、client_msg_id、回调地址或 meta 任意二进制化。SMPP 原始 payload、DCS 和 UDH/SAR 透传规则保持原样；共用 PostgreSQL 存储读取正文时也会无损还原。

## 编码与额度

HTTP 仍按 GSM-7 可表示则 DCS 0、否则 DCS 8 的现有规则。本次不引入 DCS 3 自动兜底。完整 128 个 ASCII 字符含非 GSM-7 字符，当前选择 DCS 8，按 67 个 UTF-16 单元每片拆成 2 段，受理扣 2 段额度。相同账号和 client_msg_id 的重试返回同一 gateway_id，不重复发送或扣日额。

网关验证范围是无损受理、存储、查询和编码后传给上游；不可见字符在实际手机上的显示方式、供应商是否过滤控制字符仍由其实现决定。不能仅凭 DELIVRD 推断手机按可见字符显示了全部控制字符。

## 回归与验收

- 原始 0x00–0x7F、单个 NUL、NUL 混合中文/Emoji、普通文本、反斜杠字面量的存储编解码；损坏 Base64 明确失败。
- memory/file/PostgreSQL 生命周期：受理、幂等、额度恰好耗尽、重开存储、查询原文、发送完成、pending、正常/拒绝/过期 DR 快照。
- HTTP JSON → Dispatcher → Store → 生产 SMPP 编码器：拼回两个 submit_sm 的原始 payload，与正文的 UTF-16BE 编码逐字节比较；同时检查 200、幂等、429 和查询正文。
- 独立 PostgreSQL 数据库验证 migration、历史正文兼容、缺迁移拒绝启动、更新正文清除旧二进制字段，以及防止有损降级。
- Windows 全量 race 和 vet；Linux 独立 PostgreSQL Store/HTTP race 回归及 vet。测试数据库与业务数据库隔离，限制资源，完成后移除。
- 测试环境仅使用 tenant-c 的 stub 路由发送少量验收短信，核对实际 HTTP DR、消息及任务终态、配置哈希和其他服务启动时间。

## 部署与回退边界

先备份 mysmpp 数据库与配置，再以短锁等待执行 migration 010，只新增两个可空 BYTEA 字段，不回填、不重写历史正文。迁移成功后才替换 mysmpp。其他服务、密码、租户额度、路由和 HTTP DR 配置保持原值。

旧版本不认识 text_bytes。**一旦新版本受理了 NUL 正文，不可直接切回旧版本处理这些记录**，否则可能读取到空正文。应优先向前修复；如必须降级，先停止新增受理、处理完相关任务并完整保全二进制正文。down migration 检测到任一新增存储格式即拒绝，避免静默丢失正文。不得通过清空这些字段强行回退。

直接查询数据库的运维脚本需识别 text_bytes；不能只查 text 判断含 NUL 短信为空。客户继续使用既有 HTTP 查询接口即可获得原文。

## 2026-09-20 现场结果

01:01（Asia/Shanghai）完成 mysmpp:v1.2.4 部署，源码提交 `08d1060`。构建与运行二进制 SHA256 一致：`c6f0637d19c4cd927ad7d9920aeeb66c9dc371d8596b990a9f5f1056cd9b7c6e`。数据库备份已完成，migration 010 在短锁等待事务中成功，配置文件及运行配置保持不变。

| 用例 | 结果 |
|---|---|
| 完整 ASCII 0x00–0x7F：`m012i7sx` | 首次/幂等重试均 200，同一 ID；128 个原始字节在数据库和 HTTP 查询中完全一致；2 个 DR 到达 HTTP 测试桩、最终 DELIVRD |
| 单个 NUL：`m012i7sz` | 200，数据库和查询保留唯一的 0x00；1 个 DR 实际到达、最终 DELIVRD |
| 字面量反斜杠文本：`m012i7t1` | 200，未被当成真实 NUL；1 个 DR 实际到达、最终 DELIVRD |
| 普通短/长短信：`m012i7t3`、`m012i7t5` | 首次/幂等重试均 200，1+3 个 DR 实际到达 HTTP 测试桩 |
| SMPP 三片：`m012i7t7`、`m012i7t8`、`m012i7t9` | 按 2、3、1 乱序提交，均走同一路由/供应商；三个 submit_sm_resp 和 DR 均成功 |
| 错误请求 | 错误 Token 仍为 401，非法回调协议仍为 400；额度 429 在隔离回归中验证 |
| 环境 | 配置哈希与部署前一致，其他 8 个服务容器 ID/启动时间未变；5 个上游均 bound、无连接错误；健康正常、重启计数 0 |

Windows 全量 `go test -race -count=1 ./...`、`go vet ./...` 通过。Linux 独立 PostgreSQL 的全 Store/HTTP race 回归通过，包含此次新增用例及既有迁移、并发额度、回执去重、过期竞争、多批清理回归；Linux vet 通过。临时数据库容器已移除。

本轮验收共 11 个分片回执完成。日志出现两次既有快速回执 `receipt deferred / not found`，已核对两个 inbox 在第 2 次尝试完成；验收结束无未完成回执任务和出站任务。测试桩在网关重连后各有一次旧连接关闭报错，当前连接与验收正常。不能将有限用例通过表述为持续 200 TPS 下所有故障场景均已验证。
