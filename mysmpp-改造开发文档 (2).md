# mysmpp 改造开发文档：动态路由 / 高性能内容过滤 / 话单流水

> 目标：在现有 `mysmpp` 网关上增量落地三项能力——**动态路由**、**高性能内容过滤**、**话单（CDR / 短信流水）生成**（JSON 格式、文本文件、每 1 万条一个文件）。本文是施工级文档，包含设计、可行性审核、逐文件变动点、变动影响与分阶段实施。
>
> 适配代码基线：`go 1.25.3`，唯一外部依赖 `jackc/pgx/v5`；store 支持 memory/file/postgres；提交主链路为 `dispatch.Submit`。

---

## 0. 现状勘察结论（改造前必须理解的事实）

这三点决定了整个改造的挂载方式，施工前请务必确认：

1. **`dispatch.Submit` 是唯一提交漏斗。**
   - SMPP `submit_sm`：`cmd/mysmpp/main.go` 的 `onSubmit` → `dispatcher.Submit(...)`。
   - HTTP `POST /v1/messages`：`internal/httpgw/rules.go` 的 `messages()` → `dispatcher.Submit(...)`。
   - 结论：**内容过滤、动态路由、"受理/拒绝"话单都应挂在 `dispatch.Submit` 里**，一处改造，两条入口同时生效。

2. **现有内容过滤只覆盖 HTTP，SMPP 完全绕过。**
   - 关键词/前缀过滤在 `internal/httpgw/rules.go::applyRisk`，用 `strings.Contains(lowerText, kw)` 逐关键词扫描，复杂度 `O(关键词数 × 文本长度)`。
   - SMPP 路径不经过 `applyRisk`，等于**没有内容过滤**。这是一个正确性缺口，本次一并修复。

3. **路由是线性前缀扫描，只看被叫号码。**
   - `internal/router/router.go`：按 `priority`、最长前缀排序后线性 `HasPrefix(to, prefix)`。
   - 只支持"被叫前缀 + 优先级"，无主叫、无来源、无内容标签、无权重/容灾。
   - `Router` 存放在 `Dispatcher.router atomic.Pointer[router.Router]`，热更新已就绪（`ReloadRoutes`）。

4. **没有任何话单 / 流水机制。** `store` 里的 `messages` 是"当前状态"记录（可被覆盖更新），不是 append-only 流水，无法直接当话单用。

5. **热更新链路已存在。** `Gateway.UpdateConfig(cfg)` → `cfg.Normalize()`+`Validate()` → 重建 provider registry → `dispatcher.ReloadRoutes(...)`。新配置（路由/过滤/话单）都应接到这条链路上。

---

## 1. 总体设计原则

| 原则 | 说明 |
|---|---|
| **单漏斗挂载** | 三大能力全部围绕 `dispatch.Submit` / `processOutbox` / `HandleDLR` 三个点挂载，避免在 SMPP 与 HTTP 两处重复实现导致行为漂移。 |
| **不破坏现有构造签名** | `dispatch.New(...)` 签名保持不变，新能力通过 **Setter 注入**（`SetFilterEngine` / `SetCDRSink`）+ **热重载方法**（`ReloadFilter`）接入。老调用方零改动。 |
| **编译期一次、匹配期极快** | 过滤自动机、路由 trie 均在"配置加载/热更新"时编译一次，存入 `atomic.Pointer`，请求热路径只做只读匹配。 |
| **话单异步、绝不阻塞协议线程** | 话单走 `channel + 单写协程`，与提交/投递线程解耦；背压策略可配置（阻塞或丢弃计数）。 |
| **零新增第三方依赖** | Aho-Corasick、trie、JSONL writer 均在仓库内用标准库实现，保持项目"近零依赖"风格（只保留 pgx）。 |
| **配置向后兼容** | 新增配置项全部可选，缺省值等价于"关闭新能力"，老配置文件不改也能正常启动。 |

新增/改动的包结构：

```text
internal/filter    [新增] 高性能内容过滤引擎（Aho-Corasick + regex + 归一化）
internal/cdr       [新增] 话单写入器（JSONL、按条数/时间轮转、异步落盘）
internal/router    [改造] 增加多维匹配 + trie 候选裁剪 + 选路策略（权重/容灾）
internal/config    [改造] 扩展 RouteConfig、新增 FilterConfig / CDRConfig + 校验
internal/dispatch  [改造] Submit 内挂过滤+路由；三处埋点出话单；可选容灾选路
internal/httpgw    [改造] 内容匹配下沉到 filter；保留限流；热更新带上新配置
cmd/mysmpp         [改造] 装配 filter/cdr，注入 dispatcher，优雅关停 flush 话单
```

依赖方向（无环）：`filter → config,message`；`cdr → config,message`；`router → config,message,filter(仅类型)`；`dispatch → filter,cdr,router,...`。

---

## 2. 功能一：动态路由

### 2.1 需求拆解

"动态"包含两层含义，分别对应不同工作量：

- **(a) 规则热更新（已具备）**：`atomic.Pointer[Router]` + `ReloadRoutes` 已支持不重启换路由表。本次保留并复用。
- **(b) 多维动态选路（需新增）**：按 **主叫前缀 / 来源 system_id / client_id / 内容标签 / 时间窗** 选路，并支持 **权重负载均衡** 与 **容灾链**。这是本次重点。

### 2.2 配置扩展（`internal/config/config.go`）

在 `RouteConfig` 上做**向后兼容扩展**（新增字段全部 `omitempty`，老配置不填 = 老行为）：

```go
type RouteConfig struct {
    Name        string            `json:"name"`
    Prefix      []string          `json:"prefix"`                  // 被叫前缀（原有）
    Provider    string            `json:"provider"`               // 单 provider（原有，兼容）
    Priority    int               `json:"priority"`
    AddrRewrite AddrRewriteConfig `json:"addr_rewrite,omitempty"`
    DestAddr    DestAddrConfig    `json:"dest_addr,omitempty"`

    // —— 新增：多维匹配条件（全部可选，AND 关系；空=不约束）——
    Enabled     *bool             `json:"enabled,omitempty"`       // 缺省 true
    FromPrefix  []string          `json:"from_prefix,omitempty"`   // 主叫前缀
    SystemIDs   []string          `json:"system_ids,omitempty"`    // 来源 SMPP system_id 白名单
    ClientIDs   []string          `json:"client_ids,omitempty"`    // 来源 HTTP client_id 白名单
    ContentTags []string          `json:"content_tags,omitempty"`  // 需命中的内容标签（由 filter 产出）
    TimeWindows []TimeWindow      `json:"time_windows,omitempty"`  // 生效时间窗（本地时区）

    // —— 新增：选路策略（二选一；都空则用 Provider）——
    Weighted    []WeightedProvider `json:"weighted,omitempty"`     // 权重负载均衡
    Failover    []string           `json:"failover,omitempty"`     // 容灾链（按序尝试）
}

type WeightedProvider struct {
    Provider string `json:"provider"`
    Weight   int    `json:"weight"` // >0
}

type TimeWindow struct {
    Days  []string `json:"days,omitempty"`  // ["mon","tue",...]，空=每天
    Start string   `json:"start"`           // "09:00"
    End   string   `json:"end"`             // "21:00"
}
```

`Config.Normalize()` 追加：为 `Enabled==nil` 置 `true`；`Weighted` 里 `Weight<=0` 置 1；`TimeWindow` 补零。
`Config.Validate()` 追加：`Weighted`/`Failover` 引用的 provider 必须存在；`TimeWindow.Start/End` 可解析；`FromPrefix` 走已有 `validPrefix`。

### 2.3 高性能匹配引擎（`internal/router/router.go`）

保留现有 `MatchPhone`（httpgw 无 dispatcher 时的兜底仍用它），**新增 `MatchEngine`**：

```
Match(ctx) 流程：
  1. 用【被叫前缀 trie】做 O(len(to)) 候选裁剪，返回命中该前缀的规则集合（已按 priority 预排序）。
  2. 在候选集合上按 priority 顺序逐条评估二级谓词：from_prefix / system_id / client_id / content_tags / time_window。
  3. 第一条全部满足的规则即为命中规则。
  4. 对命中规则执行【选路策略】：
       - Weighted：按权重选一个 provider（用 hash(gateway_id 或 to) 做确定性选择，保证同一目标稳定路由，便于排障与幂等）。
       - Failover：返回链首 provider，并把整链写入 outbox 供投递失败时切换（见 §2.4）。
       - 都无：用 route.Provider。
```

**性能取舍（务必写进评审记录）**：
- 被叫前缀 trie（按数字逐字符）把候选从"全表线性"降到"命中该前缀路径"。规模在数千条前缀以上收益明显；数百条时线性扫描本就是微秒级，trie 主要收益是**规模可扩展**与**稳定的最长前缀语义**。
- 二级谓词只在少量候选上评估，`ContentTags` 用小 set 交集。整体热路径新增开销 `≈ O(len(to) + 候选数)`，可控。

选路数据结构（示意）：

```go
type Engine struct {
    trie   *prefixTrie      // 被叫前缀 -> 命中的 *compiledRule 列表
    all    []*compiledRule  // 无前缀约束的规则（catch-all）
}
type compiledRule struct {
    cfg        config.RouteConfig
    fromPrefix *prefixTrie   // nil = 不约束
    systemSet  map[string]struct{}
    clientSet  map[string]struct{}
    tagSet     map[string]struct{}
    windows    []compiledWindow
    pick       selector      // weighted / failover / single
}

type MatchInput struct {
    To, From, SystemID, ClientID string
    Tags       map[string]struct{} // 来自 filter
    GatewayID  string              // 用于 weighted 确定性哈希
    Now        time.Time
}
type MatchResult struct {
    Route         config.RouteConfig
    Provider      string   // 本次选中的 provider
    FailoverChain []string // 容灾链（含首个），无则 nil
}
```

### 2.4 容灾链落地（可选，Phase 4）

容灾会触碰投递路径，单列一阶段。核心改动：

- `store.OutboxPayload` 增加 `FailoverChain []string`、`RouteAttempt int`（当前尝试到链上第几个 provider）。
- `dispatch.processOutbox` 失败且**永久错误或达到单 provider 上限**时，若 `RouteAttempt+1 < len(FailoverChain)`，不直接置 `failed`，而是**改写 payload.Provider = 下一个、RouteAttempt++、重置 attempt**，重新入队 `pending`（新增 store 方法 `RerouteOutbox(id, provider, payload)` 或复用 `FailOutbox`+重新 `EnqueueOutbox`）。
- 全链耗尽才置 `failed`，并出 `failed` 话单（记录最终 provider）。

> 若一期不做容灾，`Weighted`/`Failover` 可先只支持 **Weighted**（不触碰投递路径，只在 Submit 时定 provider），风险最低。

---

## 3. 功能二：高性能内容过滤

### 3.1 为什么要重写现有实现

现状 `applyRisk` 逐关键词 `strings.Contains`，复杂度 `O(K×L)`（K=关键词数，L=文本长度）。关键词上千条时热路径明显劣化，且只覆盖 HTTP。目标：**一次扫描命中任意条关键词（与 K 基本无关）**，并对 SMPP/HTTP 统一生效。

### 3.2 引擎设计（新增 `internal/filter`）

```text
filter.Engine（由 FilterConfig 编译，存 atomic.Pointer 热更新）
  ├─ Normalizer   文本归一化（可配）：小写、全角→半角、去零宽字符/多余空白
  ├─ ac *acMatcher Aho-Corasick 多模式自动机（承载所有 literal 关键词，O(L) 扫描）
  └─ regexps []compiledRegex  预编译正则（复杂规则，条数应受控）
```

**Aho-Corasick** 是多模式串匹配标准解法：把所有关键词编译成一个自动机，对文本**扫描一遍** `O(L + 命中数)` 即可知道命中了哪些词，与关键词数量基本无关。纯标准库可实现（约 120~150 行，无第三方依赖）。

规则模型与动作：

```go
type FilterConfig struct {
    Enabled    bool          `json:"enabled"`
    Normalize  NormalizeCfg  `json:"normalize"`
    Rules      []FilterRule  `json:"rules"`
}
type NormalizeCfg struct {
    Lowercase     bool `json:"lowercase"`
    FullToHalf    bool `json:"full_to_half"`     // 全角转半角，防绕过
    StripZeroWidth bool `json:"strip_zero_width"` // 去零宽字符，防插字绕过
}
type FilterRule struct {
    Name     string   `json:"name"`
    Keywords []string `json:"keywords,omitempty"` // 进 Aho-Corasick
    Regex    string   `json:"regex,omitempty"`    // 进正则集
    Action   string   `json:"action"`             // block | mask | tag | pass
    Tag      string   `json:"tag,omitempty"`      // action=tag 时的标签（供路由）
    MaskWith string   `json:"mask_with,omitempty"`// action=mask 的替换串，默认 "*"
    Priority int      `json:"priority"`           // 多规则命中时的裁决序
}
```

`Evaluate` 返回：

```go
type Decision struct {
    Action   Action            // Pass / Block / Mask
    Reason   string            // 命中的规则名（block/mask）
    NewText  string            // Mask 时的改写文本
    Tags     map[string]struct{} // 命中的所有 tag（供动态路由）
}
```

裁决顺序：`block > mask > tag > pass`（block 一票否决）；同类按 `Priority`。

### 3.3 挂载点与执行顺序（`dispatch.Submit`）

**顺序很关键**：动态路由可能依赖内容标签，所以**过滤必须先于路由**。

```
Submit(env):
  1. 规范化 To（去 "+"）                          [原有]
  2. filter.Evaluate(normalize(text))：
       - Block  -> 出【rejected 话单】, 返回 ErrBlocked（HTTP=403/SMPP=submit failed）
       - Mask   -> env.Text = decision.NewText（改写后继续）
       - Tag    -> tags 传给路由
  3. router.MatchEngine.Match({To,From,SystemID,ClientID,tags,...})   [替换 MatchPhone]
       - 无匹配 -> 出【rejected(no_route) 话单】, 返回错误
  4. 地址重写 / 目的校验                            [原有]
  5. 预留 gateway_id、构造 message/outbox、SubmitAtomic   [原有]
  6. 出【accepted 话单】                            [新增]
```

Dispatcher 注入与热更新：

```go
// dispatcher 结构新增
filterEngine atomic.Pointer[filter.Engine]

func (d *Dispatcher) SetFilterEngine(e *filter.Engine) { d.filterEngine.Store(e) }
func (d *Dispatcher) ReloadFilter(e *filter.Engine)    { d.filterEngine.Store(e) }
```

`internal/httpgw/rules.go` 调整：`applyRisk` **删除关键词/前缀 substring 段**（下沉到 filter，SMPP/HTTP 统一），**保留限流**（`PerNumberPerMinute` 等进程内计数）。避免"双重过滤 + 行为分叉"。`saveBlocked` 逻辑由 dispatch 侧的 rejected 话单 + 现有 message 落库替代（或保留 message 落 `blocked` 状态用于后台查询）。

### 3.4 防绕过与合规

- 归一化在**扫描前**做一次（`FullToHalf`+`StripZeroWidth`+`Lowercase`），但**话单与落库存原文长度/哈希**，避免归一化污染取证。
- `mask` 动作在命中区间打码，其余原样；命中区间由 Aho-Corasick 返回的 `(start,end)` 得到。

---

## 4. 功能三：话单 / 短信流水（CDR）

### 4.1 需求确认

- 文本文件、**JSON 格式**、**每 1 万条一个文件**、类似短信流水。
- 采用 **JSONL（JSON Lines，每行一个 JSON 对象）**：天然支持追加与轮转，比"一个大 JSON 数组"更适合流式写入与下游逐行消费（大数组必须读完整文件才能解析，且追加需要改尾部）。

### 4.2 话单记录结构（`internal/cdr`）

```go
type Event struct {
    Seq        uint64 `json:"seq"`         // 进程内单调序号，保证同文件内有序
    Ts         string `json:"ts"`          // 事件时间 RFC3339Nano
    Kind       string `json:"kind"`        // accepted|rejected|sent|failed|retry|dlr
    GatewayID  string `json:"gateway_id"`
    ProviderID string `json:"provider_id,omitempty"`
    From       string `json:"from,omitempty"`   // 可配置脱敏
    To         string `json:"to,omitempty"`     // 可配置脱敏
    TextLen    int    `json:"text_len"`         // 原文长度（不落原文，可选）
    TextHash   string `json:"text_hash,omitempty"`
    Encoding   string `json:"encoding,omitempty"`
    Segments   int    `json:"segments,omitempty"`
    Route      string `json:"route,omitempty"`
    Provider   string `json:"provider,omitempty"`
    ClientID   string `json:"client_id,omitempty"`
    SystemID   string `json:"system_id,omitempty"`
    Source     string `json:"source,omitempty"`   // smpp|http
    State      string `json:"state,omitempty"`     // 消息状态/DLR 状态
    ErrorCode  int    `json:"error_code,omitempty"`
    FilterRule string `json:"filter_rule,omitempty"` // rejected/mask 时
    Instance   string `json:"instance,omitempty"`    // 实例标识（多机去重）
}
```

> 记录形态两种模式：
> - **`events`（推荐，默认）**：受理/发送/DLR 每次状态迁移各写一行。实现最简、吞吐最高，下游按 `gateway_id` 聚合。契合"短信流水"。
> - **`settled`（可选 Phase 2）**：每条短信只出一行最终话单，在内存按 `gateway_id` 聚合、终态（delivered/failed/expired）时落盘，带 TTL 兜底。适合直接计费，但需内存聚合表，复杂度更高。
>
> 一期先做 `events`。

### 4.3 写入器设计（`internal/cdr/writer.go`）

```
Writer:
  in chan Event  (有界，例 65536)
  单写协程 loop：
    - 收到 Event -> json.Marshal -> 写当前文件（带缓冲 bufio.Writer）
    - 计数 +1；达到 MaxRecords(=10000) 或 到达 MaxAge -> rotate()
    - 每 FsyncEvery 条 或 FsyncInterval -> Flush + Sync（可配，权衡吞吐/持久性）
  rotate():
    - Flush + Sync 当前 .writing 文件
    - rename cdr-<openTs>.jsonl.writing -> cdr-<openTs>-<seq起>-<count>.jsonl（成品对下游可见）
    - 打开新 .writing 文件
  Close(): drain channel + rotate 收尾 + Sync
```

- **文件名**：`cdr-YYYYMMDDHHMMSS-<hostname/instance>-<count>.jsonl`；进行中文件带 `.writing` 后缀，轮转完成才 `rename` 成正式名。**下游只采集不带 `.writing` 的文件**，天然避免读到半截。
- **多实例**：文件名含 `instance`（hostname 或配置项），不同实例写不同文件，下游合并；避免共享目录文件名碰撞。
- **背压策略**（`OnFull`）：`block`（对提交线程反压，保证话单不丢，计费场景）或 `drop`（丢弃并计数 `dropped`，best-effort 流水场景）。默认 `block` + 大缓冲。
- **磁盘容量**：`events` 模式每条消息约 3 行；单条 JSON 约 300~500B；1 万条/文件 ≈ 3~5MB/文件。需**保留/清理策略**：本文档建议先用外部 cron/logrotate 清理已采集文件；如需内置，加一个 janitor 协程按保留天数删除（Phase 2）。

### 4.4 挂载点（4 处埋点）

| 埋点 | 位置 | 事件 |
|---|---|---|
| 受理成功 | `Submit` 末尾 | `accepted` |
| 受理拒绝 | `Submit` 内（过滤/无路由/校验失败） | `rejected` |
| 上游发送成功 | `processOutbox` 成功分支 | `sent` |
| 上游发送失败 | `failOutbox`（区分可重试/终态） | `retry` / `failed` |
| DLR 回执 | `HandleDLR` 更新状态后 | `dlr` |

Dispatcher 注入（nil-safe，未配置话单则完全零开销）：

```go
type CDRSink interface { Emit(cdr.Event); Close() error }

// dispatcher 结构新增
cdrSink   CDRSink   // 由 d.mu 保护，或用 atomic.Pointer 包装
instanceID string

func (d *Dispatcher) SetCDRSink(s CDRSink) { d.mu.Lock(); d.cdrSink = s; d.mu.Unlock() }
func (d *Dispatcher) emitCDR(e cdr.Event) {
    d.mu.RLock(); s := d.cdrSink; d.mu.RUnlock()
    if s != nil { e.Instance = d.instanceID; s.Emit(e) }
}
```

---

## 5. 配置样例（改造后 `configs/*.json` 片段）

```json
{
  "routes": [
    {
      "name": "cn-mobile-premium",
      "prefix": ["8613", "8615"],
      "from_prefix": ["1069"],
      "content_tags": ["marketing"],
      "time_windows": [{"days": ["mon","tue","wed","thu","fri"], "start": "09:00", "end": "21:00"}],
      "weighted": [
        {"provider": "prov_a", "weight": 7},
        {"provider": "prov_b", "weight": 3}
      ],
      "failover": ["prov_a", "prov_b", "prov_backup"],
      "priority": 100
    }
  ],
  "filter": {
    "enabled": true,
    "normalize": {"lowercase": true, "full_to_half": true, "strip_zero_width": true},
    "rules": [
      {"name": "gambling", "keywords": ["博彩","赌场","betting"], "action": "block", "priority": 100},
      {"name": "mask-url", "regex": "https?://\\S+", "action": "mask", "mask_with": "[链接]", "priority": 50},
      {"name": "tag-mkt", "keywords": ["优惠","折扣","promo"], "action": "tag", "tag": "marketing", "priority": 10}
    ]
  },
  "cdr": {
    "enabled": true,
    "dir": "data/cdr",
    "mode": "events",
    "max_records": 10000,
    "max_age": "1h",
    "buffer": 65536,
    "on_full": "block",
    "fsync_every": 200,
    "fsync_interval": "2s",
    "instance": "gw-01",
    "mask_to": false,
    "store_text": false
  }
}
```

`config.Config` 顶层新增 `Filter FilterConfig` 与 `CDR CDRConfig` 字段；`Normalize()`/`Validate()` 覆盖默认值与合法性（`max_records>0`、`dir` 可写、`mode∈{events,settled}`、`on_full∈{block,drop}` 等）。

---

## 6. 装配改动（`cmd/mysmpp/main.go`）

```go
// 1) 编译过滤引擎 + 注入
filterEngine, err := filter.Compile(cfg.Filter)      // err 处理
dispatcher.SetFilterEngine(filterEngine)

// 2) 起话单写入器 + 注入
cdrWriter, err := cdr.NewWriter(cfg.CDR)              // 未 enabled 返回 nil sink
dispatcher.SetCDRSink(cdrWriter)
defer cdrWriter.Close()                               // 优雅关停 flush

// 3) 热更新：UpdateConfig 内重建 filterEngine 并 dispatcher.ReloadFilter；
//    CDR 若 dir/instance 变化则换 writer（低频操作，加锁重建即可）
```

`Gateway.UpdateConfig` 追加：`filter.Compile(cfg.Filter)` → `dispatcher.ReloadFilter(...)`；`cfg.CDR` 变化时重建 writer（路径变更需先 `Close()` 旧的再起新的，注意先 flush）。

---

## 7. 可行性审核

| 维度 | 结论 | 依据 / 说明 |
|---|---|---|
| **架构契合度** | ✅ 高 | 三项能力全部挂在既有单漏斗 `Submit` 与投递/DLR 三点，不引入新的分发路径；热更新链路 `UpdateConfig→ReloadX` 现成可复用。 |
| **构造签名兼容** | ✅ 无破坏 | `dispatch.New` 不变，用 Setter 注入；`router.MatchPhone` 保留兜底。老调用方零改动。 |
| **依赖** | ✅ 零新增 | Aho-Corasick / trie / JSONL 全用标准库；项目保持只依赖 pgx 的风格。 |
| **并发安全** | ✅ 可控 | 过滤/路由引擎经 `atomic.Pointer` 原子换；话单单写协程 + 有界 channel，无共享可变状态竞争。需 `go test -race` 验证。 |
| **热更新一致性** | ✅ 可控 | 引擎为不可变编译产物，换指针即生效；in-flight 请求用旧引擎完成，语义清晰。CDR 路径变更为低频运维动作，加锁重建可接受。 |
| **性能（过滤）** | ✅ 达标 | Aho-Corasick 扫描 `O(L)`，与关键词数解耦；相较原 `O(K×L)` 在大词表下量级提升。 |
| **性能（路由）** | ✅ 达标 | trie 候选裁剪 `O(len(to))` + 少量谓词；数千前缀可扩展。数百条时本已微秒级。 |
| **性能（话单）** | ✅ 达标 | 热路径仅"入 channel"（纳秒级）；序列化/落盘在写协程异步完成，`block` 策略下极端满载才反压。 |
| **持久性** | ⚠️ 需权衡 | `fsync_every/interval` 决定崩溃可能丢失的尾部条数。计费严格场景调小间隔（吞吐↓）；流水场景放大（吞吐↑）。文档已参数化。 |
| **多实例** | ⚠️ 需约定 | 话单文件名含 `instance` 防碰撞，下游合并；限流仍是进程内（原有边界，未恶化）。 |
| **磁盘增长** | ⚠️ 需运维 | 需保留/清理策略（外部 logrotate 或 Phase 2 内置 janitor）。 |
| **容灾选路** | ⚠️ 侵入投递 | 触碰 `processOutbox/failOutbox` 与 store，风险高于其余项，单列 Phase 4；一期可只上 Weighted。 |
| **测试可回归** | ✅ | 现有 `go test ./... / -race` 覆盖 dispatcher/store/router/config；新增包各自单测 + bench。 |

**总体结论：可行。** 建议按风险从低到高分阶段落地（见 §10），把高侵入的容灾链放到最后。

---

## 8. 逐文件变动点清单

### 8.1 新增文件

| 文件 | 内容 | 规模(估) |
|---|---|---|
| `internal/filter/engine.go` | `Engine`、`Compile(cfg)`、`Evaluate(text) Decision` | ~180 行 |
| `internal/filter/ahocorasick.go` | AC 自动机构建与扫描 | ~150 行 |
| `internal/filter/normalize.go` | 全角/零宽/小写归一化 | ~80 行 |
| `internal/filter/engine_test.go` | 单测 + `Benchmark` | ~150 行 |
| `internal/cdr/writer.go` | `Writer`、`NewWriter`、`Emit`、`rotate`、`Close` | ~220 行 |
| `internal/cdr/event.go` | `Event` 结构、脱敏/哈希辅助 | ~60 行 |
| `internal/cdr/writer_test.go` | 轮转/并发/关停 flush 测试 | ~150 行 |
| `internal/router/engine.go` | `Engine`、`prefixTrie`、`Match`、选路器 | ~260 行 |
| `internal/router/engine_test.go` | 多维匹配 + 权重确定性测试 | ~150 行 |

### 8.2 改动文件

| 文件 | 变动 | 影响面 |
|---|---|---|
| `internal/config/config.go` | 扩展 `RouteConfig`；新增 `FilterConfig`/`CDRConfig`/`WeightedProvider`/`TimeWindow`；`Normalize()`/`Validate()` 追加 | 中：需同步 `config_test.go` |
| `internal/router/router.go` | 保留 `MatchPhone`；`NewWithProviders` 旁增 `NewEngine`；引擎构建入口 | 低 |
| `internal/dispatch/dispatcher.go` | 新增 `filterEngine`/`cdrSink`/`instanceID` 字段与 Setter；`Submit` 内插过滤+多维路由+受理/拒绝话单；`processOutbox`/`failOutbox`/`HandleDLR` 埋点 | **高**：核心链路，需同步 `dispatcher_test.go` |
| `internal/dispatch/types.go` | `Envelope` 可加 `SystemID/ClientID` 显式字段（现 clientID 走 Meta，system 走 Source） | 低 |
| `internal/store/*.go`（memory/file/postgres/接口） | 仅 Phase 4 容灾：`OutboxPayload` 加 `FailoverChain/RouteAttempt`，新增 `RerouteOutbox` | **高**（Phase 4 才动） |
| `internal/httpgw/rules.go` | `applyRisk` 移除关键词/前缀 substring 段（下沉 filter），保留限流；`UpdateConfig` 带上 filter/cdr 重建 | 中：同步 `rules_test.go` |
| `internal/admin/server.go` | 无逻辑改动，`UpdateConfig` 已覆盖新配置（配置页可选增编辑项） | 低 |
| `cmd/mysmpp/main.go` | 装配 filter/cdr，注入 dispatcher，`defer Close`；SMPP `onSubmit` 对 `ErrBlocked` 映射 SMPP 状态码 | 中 |

---

## 9. 变动影响分析

### 9.1 行为影响
- **SMPP 提交行为变化**：原来 SMPP 不做内容过滤，改造后会过滤。被过滤消息返回 `submit_sm_resp` 失败状态（建议 `ESME_RSUBMITFAIL` 或自定义），下游 ESME 会看到提交失败——**这是预期修复，但属于对下游可见的行为变更，需在上线说明中告知客户**。
- **HTTP 提交**：关键词命中的响应码由现有 `429`（applyRisk 归到限流码）应改为 `403 Forbidden`（内容拒绝语义更准）；若客户依赖 429，需评估兼容或保留原码。

### 9.2 性能与资源影响
- **热路径延迟**：新增 `归一化+AC 扫描 O(L)` + `trie 候选+谓词` + `话单入队`。文本几十~几百字符量级，合计通常 < 数十微秒，相对上游网络 IO 可忽略。
- **内存**：AC 自动机与 trie 为编译产物，随词表/路由表规模线性增长（KB~MB 级）；话单 channel 缓冲 `buffer×sizeof(Event)`（65536×~200B ≈ 13MB 上限）。
- **磁盘 IO**：话单顺序追加 + 周期 fsync；`events` 模式约 3 行/短信。需容量规划与清理。
- **CPU**：AC 扫描与 JSON 序列化带来可测 CPU 增量，写协程单核承载；超高 TPS 时可将 CDR 序列化前移到多协程、写协程只落盘（Phase 2 优化点）。

### 9.3 一致性 / 可靠性影响
- **话单不入事务**：话单是旁路观测，不参与 `SubmitAtomic` 事务。崩溃时可能丢失尾部未 fsync 的话单，或出现"消息已入库但话单缺失"。若要求话单与消息严格一致，需 `settled` 模式 + 与状态机对账（Phase 2 权衡）。
- **事件顺序**：跨协程事件非全局有序，靠 `seq`+`ts` 排序，下游按 `gateway_id` 聚合，不依赖行序。
- **热更新窗口**：换引擎瞬间，新旧请求分别用新旧引擎，无锁停顿；容灾链热变更时 in-flight outbox 仍持旧链，直至下次入队。

### 9.4 多实例 / 部署影响
- 话单文件名带 `instance`，避免多实例共享目录碰撞；下游采集需按实例合并。
- 限流仍进程内（原有边界未恶化）；如需全局限流是独立课题，不在本次范围。
- Docker distroless 镜像内话单目录需挂 volume；`data/cdr` 要有写权限。

### 9.5 回滚策略
- 三项均可**配置级关闭**：`filter.enabled=false`、`cdr.enabled=false`、路由不填新字段即退回原前缀路由。出现问题可热更新关闭，无需回滚二进制。
- Phase 4 容灾涉及 store schema（postgres 迁移），需独立 up/down 迁移，回滚走 `002→...` 反向迁移。

---

## 10. 分阶段实施计划（按风险从低到高）

| 阶段 | 内容 | 侵入性 | 交付物 |
|---|---|---|---|
| **P0** | 建基线：`go test ./... && go vet ./... && go test -race ./...` 全绿存档 | 无 | 基线报告 |
| **P1 话单** | `internal/cdr` + dispatcher 4 处埋点 + config + 装配 + 关停 flush | 低（旁路，可开关） | events 话单落盘、轮转、单测/bench |
| **P2 内容过滤** | `internal/filter`（AC+归一化+规则）+ Submit 挂载 + httpgw 下沉 + SMPP 状态码映射 | 中（改核心 Submit、改 HTTP 响应码） | 统一过滤、mask/tag/block、bench |
| **P3 动态路由（选路）** | `router.Engine`（trie+多维谓词+Weighted）+ RouteConfig 扩展 + Submit 用新引擎 | 中（换匹配，投递不动） | 多维+权重路由、确定性哈希测试 |
| **P4 容灾链** | `OutboxPayload` 扩展 + `processOutbox/failOutbox` 切链 + store 方法 + PG 迁移 | 高（改投递+存储） | 失败自动切下一 provider、迁移脚本 |

每阶段独立可上线、可回滚（配置开关）。建议 P1→P2→P3 串行，P4 视需要单独排期。

---

## 11. 测试计划

- **单元**：`filter`（AC 正确性、归一化绕过用例、mask 区间、tag 汇聚、bench 大词表）；`cdr`（满 1 万轮转、`.writing`→正式名、并发 Emit、Close drain、on_full=drop 计数）；`router`（多维 AND、优先级、Weighted 确定性、time_window 命中/错过）。
- **集成**：复用 `tools/dr_flow_stub.py` 与 `docs/DR_FULL_FLOW_TEST_PLAN.md` 全链路，验证 SMPP 与 HTTP 两路都被过滤、都出话单、路由命中正确。
- **竞态**：`go test -race ./internal/dispatch/... ./internal/cdr/... ./internal/filter/...`。
- **压测**：构造 1k+ 关键词、1k+ 路由前缀，`go test -bench` 对比改造前后 Submit p99；话单在目标 TPS 下 `block` 策略无长时反压、`drop` 计数为 0。
- **回归**：现有 `config_test/dispatcher_test/rules_test/router_test` 全部更新并通过。

---

## 12. 施工检查清单（Definition of Done）

- [ ] 新增 config 项有默认值，老配置文件不改可启动（向后兼容）
- [ ] `filter.enabled=false` / `cdr.enabled=false` 时热路径零额外分配（nil 短路）
- [ ] SMPP 与 HTTP 两路提交都经过过滤，行为一致
- [ ] 被过滤消息：HTTP 返回明确状态码，SMPP 返回明确 `submit_sm_resp` 状态
- [ ] 话单每满 10000 条轮转一个 `.jsonl`，进行中文件带 `.writing`，成品原子 rename
- [ ] 多实例文件名含 instance，无碰撞
- [ ] 优雅关停时 `Close()` 完成 drain + fsync，无尾部丢失（block 模式）
- [ ] 热更新过滤/路由/话单配置不重启生效
- [ ] `go test ./... && go vet ./... && go test -race ./...` 全绿
- [ ] bench 对比：过滤与大词表下 Submit 无量级劣化
- [ ] 上线说明覆盖"SMPP 现在会过滤"这一对客户可见的行为变更

---

# 附录 A：v1.1 硬化补丁（对照代码复审后的修订）

> 本附录是对照真实源码复审后，对正文若干节点的**修正与加固**。凡与正文冲突处，**以本附录为准**。

## A.1 复审发现的 5 个问题（含 2 个真 bug）

| # | 问题 | 代码证据 | 严重度 | 修正 |
|---|---|---|---|---|
| ① | 加权选路用 `hash(gateway_id)` 不成立：路由在前、gateway_id 分配在后 | `dispatcher.go` L160 `MatchPhone` → L179 `newGatewayID` | **Bug** | 加权哈希改用 `hash(To)`（匹配期已存在的稳定值） |
| ② | "受理即 emit accepted" 漏去重分支，重复提交会多记话单 | 幂等去重在 `SubmitAtomic` 内（`memory.go` L482），发生在过滤/分配之后；`duplicate` 分支 L228 | **Bug** | `accepted` 话单仅在 `duplicate==false` emit；可选 emit `duplicate` 事件 |
| ③ | DLR 话单取不到 `client_id` | `client_id` 仅存 `msg.Metadata`；`Pending`/`OutboxPayload` 无该字段 | 中 | 给 `Pending` 增 `ClientID` 字段，或 DLR 话单不带、下游按 gateway_id join |
| ④ | 热更新非"先校验后生效"，加会失败的编译步会半生效 | `UpdateConfig` L400：`Validate()`→`g.cfg=cfg`(先mutate)→`ReloadRoutes` | 中 | 正则/权重/时间窗校验塞进 `Validate()`；引擎先构建到局部变量，全成功再统一切换 |
| ⑤ | httpgw `dispatcher==nil` 兜底路径绕过 dispatch，无过滤/新路由/话单 | `rules.go` L195/L431，老 `router.New().Match`+`SaveMessage` | 低 | 明确：让其也过 filter，或声明为 test-only/降级路径不做保证 |

## A.2 现有验证逻辑的可配置性现状（必须知道的事实）

- **输入验证 `validateSubmitRequest`（`rules.go` L277）全硬编码**：`from 1-32`、`text 1-1000`、`meta ≤10 keys`、`callback 必须 https`。若要"验证逻辑可配置"，这是正文之外的**额外 scope**。
- 代码库地道的可配模式是 `DestAddrConfig.ValidateEnabled(global bool)`（`config.go` L762）：`*bool` 为 `nil`→继承全局，非 `nil`→本条覆盖。**所有新增开关一律沿用此 `*bool` 模式**。

## A.3 校验节点可配置性矩阵（改造目标）

| 校验节点 | 现状 | 改造后可配 | 模式 |
|---|---|---|---|
| 目的地址校验 | 已可配 | 保持 | 全局 `validate_dest_addr` + 逐路由 `dest_addr.validate *bool` |
| 输入长度校验 | 硬编码 | 可选（额外scope） | 新增 `limits{...}`，逐 client 覆盖 |
| 内容过滤总开关 | 无 | 是 | `filter.enabled` + 逐规则 `rule.enabled *bool` |
| 单条过滤规则 | 无 | 是 | `keywords/regex/action/priority`，`Compile` 校验 |
| 路由规则启用 | 无 | 是 | 逐路由 `route.enabled *bool` |
| 路由多维条件 | 无 | 是 | `from_prefix/system_ids/client_ids/content_tags/time_windows` |
| 话单开关/脱敏 | 无 | 是 | `cdr.enabled/mask_to/store_text` |
| 配置合法性 `Validate()` | 有 | **扩展** | 正则可编译/权重>0/时间窗可解析/failover provider 存在 |

## A.4 修正后的 Submit 关键节点序列（权威版）

```
CP0  TrimPrefix(To,"+")                                    [原有]
CP1  filter.Evaluate(normalize(text))     [可配 filter.enabled；nil 引擎短路]
       Block → CDR{rejected,reason=rule}  ★拒绝话单 → return ErrBlocked
       Mask  → 改写 env.Text
       Tag   → tags 传入 CP2
CP2  router.Match({To,From,SystemID,ClientID,tags,Now})
       Weighted 用 hash(To) 确定性选择         ← 修正①
       无匹配 → CDR{rejected,reason=no_route} ★拒绝话单 → return ErrNoRoute
CP3  AddrRewrite                                           [原有·可配]
CP4  validateDestAddr（ValidateEnabled(global) 时）         [原有·可配]
       非法 → CDR{rejected,reason=bad_dest}  ★拒绝话单 → return ErrInvalidDestAddr
CP5  gatewayID = newGatewayID()                            [原有]
CP6  SubmitAtomic(...)  → 幂等去重在此（store 内）
       duplicate==true → receiptForExisting()；不 emit accepted   ← 修正②
CP7  duplicate==false → CDR{accepted}     ★受理话单
```
投递侧：`processOutbox` 成功→`sent`；`failOutbox`→`retry`/`failed`；`HandleDLR`→`dlr`（client_id 见修正③）。
`ErrBlocked` 为新增哨兵错误，`main.go::onSubmit` 映射 SMPP `StatusSubmitFailed(0x45)` 或 `StatusThrottled`（对下游可见的行为变更，写入上线说明）。

## A.5 两阶段热更新（先全校验，再全切换）

```
UpdateConfig(cfg):
  1. Normalize()
  2. Validate()   ← 扩展：正则/权重/时间窗/failover 引用全在此校验（任一失败整体返回）
  3. 局部构建（不碰运行时）：newProviders / newRouter / newFilter / newCDR
        任一 err != nil → return err   // 运行时状态零改动
  4. 统一切换（加锁，原子换指针）：
        g.cfg=cfg; registry.Replace(newProviders)
        dispatcher.SwapRouter(newRouter); dispatcher.ReloadFilter(newFilter)
        if newCDR!=nil { old:=g.cdr; g.cdr=newCDR; old.Close() }  // 先切后关，先 flush
```
- CDR 可热改字段：`max_records/max_age/fsync_*/buffer`；重建类字段：`dir/instance/mode`（重建失败保留旧 writer，绝不半切）。
- **CDR sink 用独立 `atomic.Pointer[cdrSink]`，不要挂在 `d.mu`**（该锁 DLR 热路径也在用，避免每次 emit 抢锁）。

## A.6 DoD 增补

- [ ] 加权选路对同一 `To` 稳定（不依赖 gateway_id）
- [ ] 重复提交（幂等命中）不产生第二条 `accepted` 话单
- [ ] 热更新任一新引擎编译失败时，运行时配置零改动（可回归验证）
- [ ] 明确 httpgw 无 dispatcher 兜底路径的过滤/话单语义（覆盖或声明豁免）
- [ ] `dlr` 话单的 client_id 来源已定（Pending 加字段 或 下游 join）
- [ ] CDR sink 走独立 atomic.Pointer，`go test -race` 无竞态
