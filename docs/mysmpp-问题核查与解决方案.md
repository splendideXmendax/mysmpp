# mysmpp 问题核查与解决方案

> 最终核查日期：2026-07-18
>
> 核查依据：源码、`29175.pcap`、`2776.pcap`、线上日志与 PostgreSQL、`public_country.xlsx`、`国家号码规则信息.xlsx`
>
> 本文记录最终证据、已实施修复和仍需按配置启用的能力。

## 结论速览

| # | 问题 | 最终结论 | 处理状态 |
|---|---|---|---|
| 1 | DCS0 / GSM alphabet | 编码表完整；原自动判定漏 15 个合法字符并误收反引号 | 已修复并回归 |
| 2 | message_id 长度 | SMPP 3.4 不限制为 9；目标厂商 profile 的 C-Octet Max 9 要求可见 ID 最多 8 | 新 ID 已改为 8 字符，旧 ID 兼容 |
| 3 | UCS2 长短信 | 截图中的 141 字节为 7 字节 UDH + 134 字节正文，模拟器展示曾误解码；emoji 分段另有真实缺陷 | 模拟器结论修正；emoji 分段已修复 |
| 4 | 国家码 285 | 285 不是有效 E.164 国家码；Excel 中 `id=285` 是马里记录主键，马里国家码为 223 | 现有校验拒绝 |
| 5 | `860015013628000` | 去零必须按路由显式启用；中国最大总长 13 可由规则检查 | 规则已实现，默认关闭 |
| 6 | DLR 未推送 | 线上存在成功发送/确认，也存在无 receiver 或未确认后延期；不能概括为全部未推送 | 机制保留，ID 兼容已增强 |
| 7 | bind 前心跳 | `2776.pcap` 从已建立连接中途开始，无 SYN/bind，不能证明 bind 前心跳 | 当前两端代码均不会在未 bind 时发送 |
| 8 | 重启丢数据 | 线上已使用 PostgreSQL；outbox 是至少一次重试，不承诺绝不重复 | 保持 PostgreSQL并补 ID 恢复迁移 |

## 1. GSM 03.38

`internal/message/codec.go` 的 128 位默认字母表和 10 个扩展字符与 GSM 03.38 一致。原 `DetectEncoding` 维护另一份手写判断，导致以下合法字符自动选择 UCS2：

`¤ ¡ Ä Ö Ñ Ü § ¿ ä ö ñ ü à form-feed €`

同时反引号不在 GSM 03.38 表内，却会被旧判断当成 GSM7，编码后变成 `?`。

修复后自动判定直接复用实际编码映射，不再维护两套字符表。packed/unpacked 出站逻辑保持不变。入站 DCS0 packed/unpacked 本身存在协议歧义，当前仍保留兼容启发式；需要绝对确定时应由 ESME/provider 约定 packing。

2026-07-21 根据新增 `29175(2).pcap` 与 `2776(3).pcap` 复核发现，中继链路若把入站 DCS0 先解码为 Unicode、再按文本重编码，会改变原始 GSM 字节；其中 `09` 和 `60` 被升级为 DCS8，`1b65`、`1b40`、`1b3c` 被错误转为空 payload。修复后 SMPP→SMPP 在过滤器未修改正文时透明保留显式 DCS、正文 payload 字节及 UDH/SAR 分段元数据；不承诺原报文使用的 `short_message` 或 `message_payload` 承载方式不变。过滤器执行 mask 后清除原始 payload，并按修改后的文本重新编码。HTTP 提交和自动 UCS2 选择保持原行为。

用户提供的 [DevelopersHome GSM 7-bit 页面](https://www.developershome.com/sms/gsmAlphabet.asp) 已于 2026-07-18 再次核查：页面列出 128 个默认字符及通过 `0x1B` 转义的 10 个扩展字符，与当前映射一致。该页面不包含国家码或号码长度资料，因此只作为 GSM alphabet 的交叉证据，不能作为号码规则来源。

## 2. message_id

`29175.pcap` 的 bind 版本是 `0x34`。原 13 位 ID 在 `submit_sm_resp`、receipt 文本和 `receipted_message_id` TLV 三处一致，客户端也返回了成功的 `deliver_sm_resp`。因此 13 位不是 SMPP 3.4 协议违规，但对目标厂商截图中的 `C-Octet String Var. Max 9` 不兼容，因为最大长度包含结尾 NUL。

新格式使用 `m` + 7 位 base36：

- 可见长度 8；编码为 C-Octet String 后 9 字节。
- 容量为 `36^7-1`，约 783 亿。
- 继续使用现有 `id_alloc` 高水位，不归零、不迁移历史记录。
- 旧 `g+十进制` 与新 `m+base36` 使用不同前缀解析。
- 历史 pending DLR 继续回旧 `g` ID；新消息三处统一使用新 `m` ID。

`migrations/004_gateway_id_base36.up.sql` 用于灾难恢复时从两种 ID 格式提高分配器高水位，执行过程不会降低已有值。

## 3. UCS2 与 UDH

16-bit 拼接 UDH `06 08 04 ...` 总长度为 7 字节。截图的 `sm_length=141` 实际为 `7 + 134`，正文长度合法；模拟器查询展示必须按 `UDHL+1` 去掉 UDH 后再做 UTF-16BE 解码。

网关的独立缺陷是按 Unicode rune 数量分段。emoji 使用两个 UTF-16 code unit，旧逻辑可能产生超过 140 字节的段。修复后：

- UCS2 按 UTF-16 code unit 计算 70/67 边界。
- 不在代理对中间切分。
- 每段正文加 UDH 不超过 140 字节。
- 覆盖 70/71 BMP、35/36 emoji、混合文本边界。

入站 UDHI 现在严格检查 IE 结构、拼接 IE 长度和 total/part，拒绝 `2776.pcap` 中设置 UDHI 但正文直接以 `MZF...` 开头的畸形首段。合法未知 IE 仍允许。SAR TLV 同样检查长度、重复项和分段范围，UDH 与 SAR 不允许混用。

线上已有的 UDH 透传兼容行为保留：已有合法 UDH 的 `short_message` 在 SMPP 允许的 254 字节内原样透传；原始 UDH 与 payload 合计超过 254 字节时改用单个 `message_payload` TLV 保持字节一致，要求目标上游支持该标准 TLV。

## 4. 国家码和号码长度

数据源：

- `public_country.xlsx`：226 行、223 个唯一目的前缀。
- `国家号码规则信息.xlsx`：53 行、51 个唯一国家码最大总长度。

`go generate ./internal/dispatch` 使用标准库生成 `country_rules.go`，生产运行时不读取 Excel。生成器校验数字、重复规则冲突和 E.164 15 位上限，并记录两份源文件 SHA-256。

不能把 `public_country` 的所有 `region_code` 当作国家码：例如 1242 是国家码 1 下的 NANP 地区前缀。该表还缺少 14 个合法特殊/全球业务码，因此生成时保留：

`246 247 290 672 690 800 808 870 878 881 882 883 888 979`

长度表只有最大总长度，没有最小长度、号段和运营商分配信息，因此只能做上限检查，不能证明号码真实有效。老挝表值 16 会受 E.164 全局 15 位硬上限约束。

路由级 `dest_addr.country_length_mode`：

- 空值或 `off`：旧行为，仅数字、短码和 E.164 4-15 位/国家码检查。默认值，升级不改变旧配置行为。
- `compat`：有规则的国家检查最大总长；无规则国家仍执行旧检查。
- `strict`：缺少长度规则也拒绝，仅适合已补全目标国家规则的路由。

校验顺序保持为：用原号码选路 → 路由地址改写 → 校验改写后的号码。这样不会改变 provider 选择。`strip_trunk_zero_after_cc` 不会全局自动启用，因为部分国家的 0 可能是有效号码组成。

示例：

| 输入 | 模式/改写 | 结果 |
|---|---|---|
| `285032768252` | 任意 E.164 校验 | 拒绝：无有效国家码 |
| `860015013628000` | `compat`，不去零 | 拒绝：超过中国最大总长 13 |
| `860015013628000` | 先按 CN 路由去零 | 变为 `8615013628000`，长度通过 |
| `12425551234` | `compat` | 按国家码 1、最大 11 位通过 |
| 特殊码 246 | `compat` | 无长度规则时保留旧 E.164 行为 |

## 5. DLR 与存储

线上核查时日志存在成功发送/确认、无接收会话延期、未确认延期和重连 flush。排查重点仍是：

1. 下游提交 `registered_delivery` 是否请求 DLR。
2. 下游是否以 receiver/transceiver bind。
3. `system_id` 与原提交是否一致。
4. `pending_ttl` 是否覆盖断线时长。
5. 客户端是否返回成功 `deliver_sm_resp`。

生产使用 PostgreSQL，不会像 memory driver 一样进程退出即清空。outbox 在提交与入队时原子落库，但发送到上游后、标记完成前崩溃仍可能重试，因此语义是至少一次；下游幂等和对账仍然必要。

SMPP 入站原始 payload、UDH/SAR 只在 outbox 待发送或重试期间保存；成功投递并确认 outbox 后会清除这些额外传输副本。SMPP 幂等键升级为 `v2`，摘要包含当前进程随机实例 nonce、SMPP session、带长度边界的原始 payload、UDH 和 SAR：同一会话中完全相同的报文重试仍命中同一键，不同 DCS0 字节、SAR 分段或重连后的新会话不会被误判重复。SMPP `submit_sm` 没有标准业务幂等 ID，因此断线重连后遵循至少一次语义；升级切换瞬间重传也可能重新受理一次，需由下游幂等和对账处理。

过滤器仅能安全修改完整的独立短信。带 UDH 或 SAR 的单独长短信分段命中 mask 时整段拒绝，避免把一个分段拆成孤立短信并使其余拼接段永久缺失。这里的透明性特指 DCS、正文 payload 和 UDH/SAR 分段元数据；并不宣称 `protocol_id`、priority、replace flag、默认消息 ID 或任意 TLV 的完整 PDU 代理。

## 6. 回归门禁

完成的验证：

- `go generate ./internal/dispatch` 连续两次输出一致。
- `go test -count=1 ./...`。
- 核心包 `go test -count=20`。
- `go vet ./...`。
- Linux amd64、CGO disabled 静态构建。
- 国家规则、GSM alphabet、UCS2 emoji、UDH/SAR、ID 新旧兼容、DLR ID 一致、file store 恢复、后台编辑保留高级字段专项测试。

`-race` 在当前 Windows Go 环境因 CGO 未启用无法运行；这不是测试失败，生产部署前如有 CGO 工具链可追加执行。

## 7. 线上部署与最终验收

2026-07-18 已将修复镜像部署到生产服务器（地址脱敏）。生产 `config.json` 未修改，部署前备份与当前 Docker volume 中配置的 SHA-256 均为：

`320e3b9c81c5b964380165bb4ced977f06a468f3df272b1defb26fea0ad0ebf2`

生产验收结果：

- 最终运行镜像 `sha256:a258f40b1e6dfc0cda71639e6f2ab2c8127a43078832573406cb5dae35218cbb`，容器重启次数 0，未发生 OOM，HTTP `19087` 与 SMPP `29175` 均可连接。
- 真实 ESME/provider 首轮链路 `m0000ewx` 和最终镜像链路 `m0000ggh` 均为 8 位 ID、最终状态 `DELIVRD`；最终 DLR 重绑后命中同一 ID 并成功返回 `deliver_sm_resp`。
- 最终镜像部署后的首次真实提交 `m0000fop` 遇到上游 `submit_sm_resp timeout after 5s`，网关按既有策略记录为 failed；网络拓扑恢复并稳定后，`m0000ggh` 完整通过。该失败记录保留用于审计，不删除或改写。
- 原有 pending 保持为 2，outbox pending 为 0；分配器每次预留 1000 个 ID，线上回归后高水位相应上移，属于既有行为。
- 独立的 memory + mock 容器完成 11/11 项线上隔离回归：无效国家码、中国长度边界、显式去零、GSM 特殊字符、合法未知 UDH，以及畸形 UDH、UDH+SAR、截断 TLV、partial/duplicate SAR。
- 回滚材料保存在 `/root/mysmpp-release-backup-20260718-104043`，包含原源码、生产配置、PostgreSQL 备份、容器信息、原镜像标签和 SHA-256 清单。

最终切换时曾因手工重建容器遗漏 `mysmpp_default` 网络，出现 PostgreSQL 主机名无法解析并触发短暂重启；发现后按备份 inspect 修正。最终容器的命令、entrypoint、restart policy、端口、volume 目标和 network mode 六项均与部署前配置一致，生产 `config.json` 与数据库内容未被改写。

2026-07-21 DCS0 修复最终镜像为 `sha256:4f73e07f3515d9eb7d45a5eb8e07ddfe737db873b92caf58a1b231abd0ad1553`。部署前先在服务器用独立 memory 配置和真实 SMPP 双桩完成抓包 16 组 payload 的 16/16 字节回归；部署后再从生产 SMPP 入口提交相同 16 组数据，并在发往现有下游测试桩 `:2776` 的临时抓包中逐条确认 DCS=0、payload 完全一致。对应网关 ID `m0000h89` 至 `m0000h8o` 最终全部 `DELIVRD`，outbox 全部 `done` 且 raw/UDH/SAR 传输副本已清除。

本次生产切换的 network、restart policy、ports、volume、entrypoint 和 cmd 首次即与旧容器匹配；配置 SHA-256 仍为 `320e3b9c81c5b964380165bb4ced977f06a468f3df272b1defb26fea0ad0ebf2`。最终健康正常、RestartCount=0、部署后日志 ERROR=0/WARN=0；messages 从 5256 增至 5272（仅本次 16 条测试数据），pending 保持 2，outbox pending 保持 0。本次回滚材料位于 `/root/mysmpp-dcs0-prod-backup-20260721-165900`。

号码长度能力的最终边界不变：当前 51 条规则只提供最大总长度，不能判断最短长度、移动号段、运营商分配或号码是否真实存在；`strict` 也只适用于规则已覆盖的目标国家，不能贸然全局开启。
