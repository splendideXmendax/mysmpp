# mysmpp 测试文档（总纲 + 用例规格）

> 覆盖范围：现有功能（SMPP/HTTP 接入、路由、上游 provider、outbox 投递、DLR 回推、存储、管理后台）**与**三项改造功能（动态路由、内容过滤、话单）。
> 本文是测试**总纲与用例规格**，负责"测什么、怎么判定、怎么门禁"。已有的端到端联调操作手册见 `docs/DR_FULL_FLOW_TEST_PLAN.md`（1200+ 行，含三机拓扑/防火墙/字段映射/手工 DLR 注入/排障），本文**引用而不重复**。

---

## 1. 测试目标与分层

| 层级 | 目的 | 工具 | 门禁 |
|---|---|---|---|
| L1 单元 | 函数/模块逻辑正确 | `go test ./...` | 必过，覆盖率不降 |
| L2 竞态 | 并发无数据竞争 | `go test -race ./...` | 必过 |
| L3 静态 | 编译期问题 | `go vet ./...` | 必过 |
| L4 集成 | 跨模块（dispatch↔store↔provider）真实协作 | Go 集成用例 + 内存/文件/PG | 必过 |
| L5 端到端 | SMPP/HTTP 下游 → 上游 → DLR 回推全链路 | `tools/dr_flow_stub.py`、`cmd/testesme` | 发版必过 |
| L6 性能 | 过滤/路由/话单在目标 TPS 下不劣化 | `go test -bench`、压测脚本 | 发版基线对比 |
| L7 可靠性/混沌 | 崩溃、重启、上游不可达、背压下的行为 | 故障注入 + 重启 | 发版必过 |
| L8 长稳 | 长时间运行无泄漏、话单不断档 | soak（≥8h） | 大版本必过 |

**基线校验命令（每次改动后必跑，对应 `docs/ARCHITECTURE.md` 第 19 节）：**
```bash
go test ./...
go vet ./...
go test -race ./...
```

---

## 2. 测试环境与资产

### 2.1 存储三态（务必三态都覆盖关键用例）
| driver | 配置 | 前置 | 用途 |
|---|---|---|---|
| `memory` | `storage.driver=memory` | 无 | 单测、快速回归；**重启丢数据**，不测持久化 |
| `file`/`json` | `storage.driver=file`, `dsn=data/store.json` | 目录可写 | 落盘快照、单机联调 |
| `postgres`/`pg` | `storage.driver=postgres`, `dsn=...` | 先跑 `migrations/*.up.sql` | 生产语义、多 worker `SKIP LOCKED`、并发 claim |

### 2.2 现成测试工具
- **`tools/dr_flow_stub.py`**（纯标准库）三模式：
  - `provider`：模拟上游 HTTP provider，并回调 mysmpp 的 DLR URL；
  - `http-submit`：向 `/v1/messages` 提交并轮询消息状态；
  - `smpp-esme`：作为 ESME bind、submit MT、等 DLR。
- **`cmd/testesme`**（Go）：`-addr -u -p -src -dst -text -n -wait`，bind_transceiver + submit_sm + 等 deliver_sm(DLR)。
- **`/healthz`**：返回 `storage`、`pending_size`、`outbox_depth`，作为 outbox 是否积压、pending 是否堆积的探针。
- 端点：`POST/GET /v1/messages`、`/v1/config`、`/ui/config`、`/admin/`、`/healthz`。

### 2.3 测试数据/夹具约定
- 号码：CN 手机 `86138xxxxxxxx`、短码、非法 `abc123`、无国家码 `9999`；主叫 `1069xxxx`。
- 文本：GSM-7 短(≤160)、GSM-7 长(>160 触发分段)、UCS-2 中文、含扩展字符（`{}[]|^~\€`）、含 URL、含关键词、含全角/零宽绕过样本。
- 幂等：固定 `client_id`+`client_msg_id` 重复提交。

---

## 3. 现有功能测试矩阵（含已覆盖基线与补测缺口）

> "已覆盖"列引用现有 `*_test.go` 作为回归基线；"补测"是本次改造后需要新增/加强处。

### 3.1 配置 config（`internal/config`，已 22 例）
| 项 | 已覆盖用例 | 补测 |
|---|---|---|
| Normalize 默认值回填 | `TestNormalize*Defaults`、`enquire_test` | 新增 `filter/cdr/route 扩展字段`默认值 |
| Validate 拒绝非法配置 | `TestValidateRejects*`（inbound/storage/addr_rewrite/smpp/凭据） | **新增：正则不可编译、权重≤0、time_window 不可解析、failover 引用不存在的 provider 必须被拒**（详见 §6 两阶段热更新） |
| 启动种子/凭据生成 | `TestLoadStartupSeedsAndGeneratesSecrets` | 保持 |

### 3.2 消息编解码 message（`internal/message`，已 9 例）
| 项 | 已覆盖 | 补测 |
|---|---|---|
| 分段（GSM-7/UCS-2、扩展字符 septet 计数） | `TestSplitLongGSM7Message`、`TestSplitCountsGSM7ExtensionSeptets`、`TestDetectUCS2` | filter `mask` 改写文本后**重新分段**是否正确（改写可能改变长度/编码） |
| 编解码往返 | `TestGSM7/UCS2CodecRoundTrip`、`TestDecodeSubmitText*` | 保持 |

### 3.3 路由 router（`internal/router`，已 1 例）
| 项 | 已覆盖 | 补测（新引擎，见 §4） |
|---|---|---|
| 优先级相同取最长前缀 | `TestRouterPrefersLongestPrefixWhenPriorityTies` | 多维匹配、权重确定性、时间窗、trie 候选正确性 |

### 3.4 提交/投递 dispatch（`internal/dispatch`，已 9 例）
| 项 | 已覆盖 | 补测 |
|---|---|---|
| 路由并提交 | `TestDispatcherRoutesAndSubmits` | 过滤挂载后仍正确（CP1→CP7 顺序） |
| 未分配国家码拒绝 | `TestDispatcherRejectsUnassignedCountryCode` | 保持 |
| 去掉国家码后主干 0 重写 | `TestDispatcherCanRewriteTrunkZeroAfterCountryCode` | 保持 |
| 并发幂等只入队一次 | `TestDispatcherConcurrentIdempotencyQueuesOnce` | **回归：幂等命中不产生第二条 accepted 话单**（REG-02） |
| 路由级关闭目的校验（短码） | `TestDispatcherRouteCanDisableDestValidationForShortCode` | 保持（验证"逐路由 *bool 覆盖"模式） |
| SMPP DLR 延迟到 RX bind | `TestDispatcherDefersSMPPDLRUntilReceiverBound` | DLR 埋点后仍正确 |
| DLR 早到等待 pending | `TestDispatcherWaitsForPendingWhenDLRArrivesEarly` | 保持 |
| 按 system_id flush DLR | `TestDispatcherFlushesSMPPDLRToReceiverBySystemID` | dlr 话单 emit 正确 |
| 忽略 disabled provider 路由 | `TestDispatcherIgnoresDisabledProviderRoutes` | 保持 |

### 3.5 HTTP 网关 httpgw（`internal/httpgw`，已 20 例）
| 项 | 已覆盖 | 补测 |
|---|---|---|
| 提交走 dispatcher | `TestMessageSubmitUsesDispatcherWhenConfigured` | 保持 |
| 幂等返回同 gateway_id | `TestMessageSubmitIdempotencyReturnsSameGatewayID` | 保持 |
| 关键词命中落 blocked | `TestMessageSubmitRiskBlockedKeywordStoresBlockedMessage` | **迁移：内容匹配下沉 filter 后，此断言改为验证 filter 拒绝 + rejected 话单**；限流仍保留在 httpgw |
| 鉴权/分页/入站规则/配置 API/写回 | `TestMessagesGET*`、`TestConfigAPI*`、`TestDynamicInboundRule*` | 配置 API 热更新需带上 filter/cdr（IT-RELOAD） |
| **兜底路径 `dispatcher==nil`** | 无专门用例 | **补测：明确该路径的过滤/话单语义**（决策见 §7 D-3） |

### 3.6 SMPP 服务端/客户端（`internal/smpp`,`smppclient`，已 ~20 例）
保持现有：bind 三态、submit 解析（UDH/payload/SAR TLV）、心跳/空闲关闭、最大会话/单 system_id 限制、DLR 构造、上游 bind/窗口/重连/MO 拒绝、长短信 UDH/SAR 透传。
**补测**：SMPP 提交被 filter 拒绝时的 `submit_sm_resp` 状态码（见 REG/IT-FILTER-PARITY）。

### 3.7 存储 store（`internal/store`，已 6 例）
| 项 | 已覆盖 | 补测 |
|---|---|---|
| SubmitAtomic 原子写 msg+outbox+幂等 | `TestMemoryStoreSubmitAtomicStores...` | **三态一致性**：同一用例跑 memory/file/pg |
| 过期 pending/幂等清扫、旧消息裁剪、stale outbox 回收 | `TestMemoryStoreSweeps*`、`TrimsOldMessages`、`RequeuesStaleOutbox` | 保持 |
| file 落盘持久化 | `TestFileStorePersists...` | Phase4 若加 `Pending.ClientID`/`OutboxPayload.FailoverChain`，落盘/回放需补测 |

---

## 4. 新功能：动态路由 测试规格

前置：新 `router.Engine`（trie 候选 + 多维谓词 + 选路器），配置见改造文档 §2.2。

| ID | 用例 | 前置/输入 | 期望 |
|---|---|---|---|
| UT-ROUTE-01 | 被叫前缀 trie 最长匹配 | 规则 `138` 与 `13800` 同优先级，to=`13800138000` | 命中 `13800`（延续现有语义） |
| UT-ROUTE-02 | 优先级高者优先 | 两条都匹配，priority 100 vs 10 | 命中 priority=100 |
| UT-ROUTE-03 | 主叫前缀谓词 | 规则含 `from_prefix=["1069"]`，from=`10690001` / `13800000` | 前者命中，后者落下一条/无匹配 |
| UT-ROUTE-04 | system_id 白名单 | `system_ids=["esmeA"]`，来源 esmeA/esmeB | esmeA 命中，esmeB 不命中 |
| UT-ROUTE-05 | client_id 白名单 | `client_ids=["c1"]` | 同上语义 |
| UT-ROUTE-06 | 内容标签驱动路由 | filter 打 `marketing` tag，规则 `content_tags=["marketing"]` | 有 tag 命中营销路由，无 tag 走默认 |
| UT-ROUTE-07 | 时间窗命中/错过 | window `09:00-21:00 mon-fri`；Now 注入窗内/窗外/周末 | 窗内命中，窗外/周末不命中 |
| UT-ROUTE-08 | **加权确定性（回归 REG-01）** | `weighted a:7 b:3`，同一 to 提交 100 次 | **同一 to 每次落同一 provider**（hash(To) 稳定，**不得依赖 gateway_id**） |
| UT-ROUTE-09 | 加权分布 | 大量不同 to | a:b 分布近似 7:3（±容差） |
| UT-ROUTE-10 | 无匹配 | to 不命中任何前缀且无 catch-all | 返回"无路由"，触发 rejected 话单（reason=no_route） |
| UT-ROUTE-11 | 逐路由 enabled *bool | `route.enabled=false` | 该路由被排除，落下一条 |
| UT-ROUTE-12 | disabled provider 路由剔除 | provider.enabled=false | 引用它的路由不参与匹配（延续 `TestDispatcherIgnoresDisabledProviderRoutes`） |
| BENCH-ROUTE-01 | 大规模前缀匹配 | 1k~10k 前缀规则 | trie 匹配 O(len(to))，p99 与规则数近似无关；对比线性扫描 |
| IT-ROUTE-FAILOVER-01 | 容灾切链（Phase4） | `failover=[a,b,backup]`，a 永久失败 | outbox 自动切 b；b 也失败切 backup；全耗尽才 `failed` + failed 话单 |
| IT-ROUTE-FAILOVER-02 | 切链幂等/不重复计费 | 同上 | 每次切换是"重投"，不产生重复 accepted 话单 |

---

## 5. 新功能：内容过滤 测试规格

前置：新 `internal/filter`（Aho-Corasick + regex + 归一化），挂在 `dispatch.Submit` 的 CP1。

### 5.1 匹配正确性
| ID | 用例 | 输入 | 期望 |
|---|---|---|---|
| UT-FILTER-01 | AC 单关键词命中 | keywords `["赌场"]`，文本含"赌场" | Block，reason=规则名 |
| UT-FILTER-02 | AC 多关键词一次扫描 | 上千关键词，文本命中其中一条 | 命中且只扫一遍（bench 佐证） |
| UT-FILTER-03 | 无命中放行 | 文本不含任何词 | Pass |
| UT-FILTER-04 | 重叠/子串关键词 | `["赌","赌场"]` | 均可命中，命中区间正确 |
| UT-FILTER-05 | 正则规则 | regex `https?://\S+` | 命中 URL |
| UT-FILTER-06 | 正则不可编译 | 坏正则 | **在 `config.Validate()` 阶段即失败**，不进运行时（见 REG-04） |

### 5.2 归一化防绕过（可配 normalize.*）
| ID | 用例 | 输入 | 期望 |
|---|---|---|---|
| UT-FILTER-10 | 大小写 | keyword `promo`，文本 `PROMO` | lowercase 开时命中 |
| UT-FILTER-11 | 全角→半角 | 全角数字/字母绕过样本 | full_to_half 开时命中 |
| UT-FILTER-12 | 零宽字符插入 | "赌\u200b场" | strip_zero_width 开时命中 |
| UT-FILTER-13 | 归一化关闭 | 同上但开关关 | 不命中（验证开关生效） |
| UT-FILTER-14 | **取证不被污染** | 归一化命中后 | 话单/落库记录**原文长度/哈希**，非归一化文本 |

### 5.3 动作与裁决
| ID | 用例 | 规则 | 期望 |
|---|---|---|---|
| UT-FILTER-20 | block 一票否决 | 同时命中 tag+block | 最终 Block |
| UT-FILTER-21 | mask 打码 | action=mask, mask_with=`[链接]` | 命中区间替换，其余原样；改写后 `env.Text` 更新 |
| UT-FILTER-22 | mask 后重新分段 | mask 改变长度/编码 | Segments 依据新文本重算（联动 message.Split） |
| UT-FILTER-23 | tag 汇聚 | 命中多个 tag 规则 | tags 集合传给路由（联动 UT-ROUTE-06） |
| UT-FILTER-24 | 同类按 priority | 两条 block 命中 | reason=priority 高者 |
| UT-FILTER-25 | 逐规则 enabled *bool | rule.enabled=false | 该规则不参与 |
| UT-FILTER-26 | 引擎总开关 | filter.enabled=false | **热路径短路，零额外开销**（nil 引擎） |

### 5.4 双入口一致性（关键，修复历史缺口）
| ID | 用例 | 期望 |
|---|---|---|
| IT-FILTER-PARITY-01 | 同一被封关键词，分别经 HTTP `/v1/messages` 与 SMPP `submit_sm` 提交 | **两路都被拒**（历史上 SMPP 绕过过滤，此为回归护栏） |
| IT-FILTER-PARITY-02 | HTTP 被拒响应码 | 明确 `403`（内容拒绝语义），与限流 `429` 区分 |
| IT-FILTER-PARITY-03 | SMPP 被拒响应 | `submit_sm_resp` 返回既定状态码（`ESME_RSUBMITFAIL 0x45` 或 `throttled`），与 `ErrBlocked` 映射一致 |

### 5.5 性能
| ID | 用例 | 期望 |
|---|---|---|
| BENCH-FILTER-01 | 词表 10 / 1k / 10k 下 Evaluate 耗时 | 耗时 ≈ O(文本长度)，与词表规模基本无关；对比旧 `strings.Contains` 循环量级提升 |
| BENCH-FILTER-02 | Submit 端到端 p99（开/关过滤） | 增量在可接受阈值内（记录基线） |

---

## 6. 新功能：话单 CDR 测试规格

前置：新 `internal/cdr`（JSONL、按条数/时间轮转、异步单写协程），埋点在 `Submit`/`processOutbox`/`failOutbox`/`HandleDLR`。

### 6.1 记录与轮转
| ID | 用例 | 期望 |
|---|---|---|
| UT-CDR-01 | JSONL 格式 | 每行一个合法 JSON 对象，字段符合 `Event` schema |
| UT-CDR-02 | **满 1 万条轮转** | 写满 `max_records=10000` 立即轮转出一个 `.jsonl`；边界（第 10000/10001 条）正确 |
| UT-CDR-03 | 时间轮转 | `max_age=1h` 到点即使未满也轮转 |
| UT-CDR-04 | `.writing`→正式名原子 rename | 进行中文件带 `.writing`；轮转后为 `cdr-<ts>-<instance>-<count>.jsonl`，下游只见正式文件 |
| UT-CDR-05 | seq 单调 | 同一文件内 `seq` 递增，供下游排序 |
| UT-CDR-06 | 关停 drain+flush | `Close()` 把 channel 排空、fsync、收尾 rename，无尾部丢失（block 模式） |

### 6.2 埋点覆盖（对齐 CP 序列）
| ID | 事件 | 触发 | 断言 |
|---|---|---|---|
| IT-CDR-10 | accepted | 正常提交（CP7） | 一条 accepted，带 route/provider/client_id/system_id/source |
| IT-CDR-11 | rejected(rule) | filter Block（CP1） | reason=规则名，无 gateway_id 或合成 id |
| IT-CDR-12 | rejected(no_route) | 无路由（CP2） | reason=no_route |
| IT-CDR-13 | rejected(bad_dest) | 目的校验失败（CP4） | reason=bad_dest |
| IT-CDR-14 | sent | 上游发送成功（processOutbox） | 带 provider_id |
| IT-CDR-15 | retry/failed | 发送失败（failOutbox） | 区分可重试/终态 |
| IT-CDR-16 | dlr | DLR 回执（HandleDLR） | 带最终 state/error_code；**client_id 来源已定**（Pending 加字段 或 下游 join，见 REG-03） |
| IT-CDR-17 | **幂等不重复**（回归 REG-02） | 同 client_msg_id 重复提交 | **只一条 accepted**，可选一条 duplicate |

### 6.3 并发/背压/持久性
| ID | 用例 | 期望 |
|---|---|---|
| UT-CDR-20 | 并发 Emit | 多 worker 并发写，单写协程串行落盘，`-race` 无竞态 |
| UT-CDR-21 | on_full=block | 缓冲满时对提交反压，话单不丢（计费语义） |
| UT-CDR-22 | on_full=drop | 缓冲满时丢弃并计数 `dropped`，热路径不阻塞（best-effort） |
| UT-CDR-23 | fsync 策略 | `fsync_every/interval` 生效；崩溃丢失量 ≤ 未 fsync 尾部（记录 RTO/RPO） |
| REL-CDR-01 | 崩溃后恢复 | kill -9 后重启，`.writing` 文件可被下游安全跳过/续采（不产生半截 JSON 行被当正式采集） |
| IT-CDR-30 | 多实例文件名隔离 | 两实例共享目录，文件名含 `instance`，无碰撞；下游可合并 |
| UT-CDR-31 | 脱敏开关 | `mask_to=true`/`store_text=false` | to 打码、不落原文，仅长度/哈希 |

### 6.4 容量与运维
- 估算校验：`events` 模式 ≈ 3 行/短信；单条 ~300–500B；1 万条/文件 ≈ 3–5MB。压测后核对实际文件大小与轮转频率。
- 保留/清理：验证外部 logrotate/cron 或（Phase2）内置 janitor 按保留天数删除已采集文件，磁盘不无限增长。

---

## 7. 回归测试：针对复审发现的 5 个问题（改造文档附录 A.1）

> 这五条是**必须常驻**的回归护栏，防止改造把已知坑重新引入。

| ID | 对应问题 | 用例 | 判定 |
|---|---|---|---|
| REG-01 | ① 加权哈希误用 gateway_id | 见 UT-ROUTE-08 | 同一 to 稳定落同一 provider；断言实现里**不引用 gateway_id** 做选路哈希 |
| REG-02 | ② 受理话单漏去重分支 | 见 IT-CDR-17 / dispatcher 幂等用例 | 幂等命中**不产生第二条 accepted 话单**，gateway_id 不二次分配 |
| REG-03 | ③ DLR 话单缺 client_id | 见 IT-CDR-16 | dlr 话单能关联到 client_id（字段直带 或 gateway_id join 可还原） |
| REG-04 | ④ 热更新非原子 | 见 IT-RELOAD-02 | 任一新引擎编译失败时**运行时配置零改动** |
| REG-05 | ⑤ httpgw 兜底路径绕过 | 见 IT-FILTER-PARITY / D-3 决策 | 兜底路径的过滤/话单语义被明确覆盖或显式声明豁免 |

**待决策项（影响用例，需你拍板）：**
- **D-3**：httpgw `dispatcher==nil` 兜底路径——(a) 让其也过 filter 并出话单，或 (b) 声明为 test-only/降级路径不做保证。定了才能敲定 REG-05 的断言方向。

---

## 8. 横切：热更新两阶段 与 幂等交互

| ID | 用例 | 期望 |
|---|---|---|
| IT-RELOAD-01 | 正常热更新 | 经 `/v1/config` 或后台改路由/过滤/话单配置，不重启生效；in-flight 请求用旧引擎完成 |
| IT-RELOAD-02 | **校验失败原子回滚**（REG-04） | 提交含坏正则/权重≤0/坏 time_window/failover 引用不存在 provider 的配置 → `Validate()` 拒绝，**运行时保持旧配置**，服务不中断 |
| IT-RELOAD-03 | CDR 重建类字段 | 改 `dir/instance/mode` → 先切新 writer 再关旧（先 flush）；重建失败保留旧 writer |
| IT-RELOAD-04 | CDR 热改类字段 | 改 `max_records/max_age/fsync_*/buffer` → 应用到运行中 writer，不丢事件 |
| IT-IDEM-01 | 并发幂等 | 延续 `TestDispatcherConcurrentIdempotencyQueuesOnce`，叠加过滤/话单后仍只入队一次、只一条 accepted |

---

## 9. 端到端场景（引用 `docs/DR_FULL_FLOW_TEST_PLAN.md` + 新功能增量）

沿用手册的拓扑/启动顺序/验收命令（第 10、11 节）与失败场景（第 13 节：上游失败状态、DLR token 错误、上游不可达、provider 名不匹配）。**新增以下端到端断言：**

| ID | 场景 | 步骤（基于 stub/testesme） | 验收 |
|---|---|---|---|
| E2E-01 | SMPP 全链路 + 话单 | `testesme -n 5` 提交 → 上游 stub 回 DLR | 收到 deliver_sm(DLR)；话单出现 accepted→sent→dlr 三类，gateway_id 可串联 |
| E2E-02 | HTTP 全链路 + 话单 | `dr_flow_stub.py http-submit` | 消息状态推进；话单三类齐全 |
| E2E-03 | 内容过滤双入口 | 同一封禁词经 HTTP 与 SMPP 提交 | 两路都被拒（HTTP 403 / SMPP 既定状态码），各出一条 rejected 话单 |
| E2E-04 | 动态路由多维 | 构造命中 from_prefix+content_tag+time_window 的规则 | 命中目标 provider；stub 侧收到对应上游请求 |
| E2E-05 | 加权稳定性 | 同一 to 多次提交 | 全部落同一上游（REG-01 端到端佐证） |
| E2E-06 | 话单轮转 | 持续提交 >1 万条 | 目录出现多个正式 `.jsonl`，每个 ≤1 万行；进行中文件带 `.writing` |
| E2E-07 | 容灾切链（Phase4） | 主上游 stub 下线 | 自动切备用上游成功；话单体现切换后的 provider |

---

## 10. 性能与压力测试计划

**目标（示例，按硬件校准后固化为基线）：**
- 提交吞吐：开启过滤+话单后，Submit p99 增量 ≤ 基线的约定阈值。
- 过滤：`Evaluate` 与词表规模解耦（BENCH-FILTER-01）。
- 话单：目标 TPS 下 `block` 模式无长时反压、`drop` 计数为 0；写协程单核可承载。

**方法：**
1. 微基准：`go test -bench=. -benchmem ./internal/filter/... ./internal/router/... ./internal/cdr/...`，对比改造前后。
2. 端到端压测：`dr_flow_stub.py http-submit`/`smpp-esme` 并发拉起，`/healthz` 监控 `outbox_depth`、`pending_size` 不持续增长。
3. 话单容量：核对 3–5MB/万条估算与实际，观察轮转频率与磁盘增长。
4. Postgres：验证多 worker `FOR UPDATE SKIP LOCKED` 无重复消费、无热锁。

---

## 11. 可靠性 / 混沌测试

| ID | 注入 | 期望 |
|---|---|---|
| REL-01 | 进程 kill -9 后重启（file/pg） | 消息/outbox/pending/幂等从存储恢复；`.writing` 话单被下游安全处理 |
| REL-02 | worker 崩溃（claim 后未 ack） | `claim_timeout` 到期 stale outbox 回 pending 重投（延续 `TestMemoryStoreRequeuesStaleOutbox`） |
| REL-03 | 上游 provider 不可达 | 指数退避重试；达 `max_attempts` 或永久错误置 failed + failed 话单 |
| REL-04 | DLR 早于 pending 到达 | 等待窗口内补上（延续 `TestDispatcherWaitsForPendingWhenDLRArrivesEarly`） |
| REL-05 | RX 会话未 bind 时来 DLR | 标记 ready，bind 后 flush（延续 `Defers/FlushesSMPPDLR`） |
| REL-06 | 话单磁盘写满 | on_full 策略生效；不拖垮提交主链路；有可观测的失败信号 |
| SOAK-01 | ≥8h 持续中等负载 | 无内存/句柄泄漏；话单不断档；outbox 不积压 |

---

## 12. 需求↔用例 追溯矩阵

| 需求 | 主要用例 |
|---|---|
| 动态路由（多维+权重） | UT-ROUTE-03~09, BENCH-ROUTE-01, E2E-04/05, REG-01 |
| 动态路由（容灾，Phase4） | IT-ROUTE-FAILOVER-01/02, E2E-07 |
| 高性能内容过滤 | UT-FILTER-01~26, BENCH-FILTER-01/02, IT-FILTER-PARITY-01~03, E2E-03 |
| 话单（1万/文件 JSON） | UT-CDR-01~06, IT-CDR-10~17, E2E-06 |
| 话单可靠性/背压 | UT-CDR-20~23/31, REL-CDR-01, IT-CDR-30 |
| 验证/校验可配置 | UT-ROUTE-11, UT-FILTER-25/26, config §3.1 补测, IT-RELOAD-02 |
| 热更新一致性 | IT-RELOAD-01~04, REG-04 |
| 双入口一致性（历史缺口） | IT-FILTER-PARITY-*, E2E-03, REG-05 |

---

## 13. CI 门禁与发布准出

**每次 PR（快速门禁）：** `go vet ./...` → `go test ./...` → `go test -race ./...` → 覆盖率不低于基线。
**每日/合并到主干：** 追加 L4 集成（三态 store）+ L6 微基准对比。
**发版准出（Exit Criteria）：**
- L1–L4 全绿，`-race` 全绿；
- 三态 store 关键用例通过；
- E2E-01~06 通过（Phase4 上线追加 E2E-07）；
- REG-01~05 全部通过（历史坑护栏）；
- 性能基线无量级回退；
- 上线说明覆盖两处对客户可见的行为变更：**SMPP 现在会过滤**、**内容拒绝 HTTP 返回 403**。

---

## 14. 测试记录模板（沿用手册第 18 节风格）

```text
用例ID:        环境(memory/file/pg):        构建版本:
前置:
步骤:
预期:
实际:
话单核对(accepted/sent/dlr/rejected 各1?):
healthz(outbox_depth/pending_size):
结论(PASS/FAIL):        备注/缺陷号:
```

---

## 附：一键回归脚本骨架（建议放 `tools/`）

```bash
#!/usr/bin/env bash
set -euo pipefail
echo "[1/4] vet";   go vet ./...
echo "[2/4] unit";  go test ./...
echo "[3/4] race";  go test -race ./...
echo "[4/4] bench"; go test -bench=. -benchmem \
  ./internal/filter/... ./internal/router/... ./internal/cdr/... | tee bench.txt
echo "对比 bench.txt 与基线，确认无量级回退"
```
