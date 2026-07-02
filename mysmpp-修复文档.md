# mysmpp 严重问题修复文档

> 基于对仓库 `splendideXmendax/mysmpp` 全量源码（约 1.26 万行 Go）的通读。代码整体质量很高（`FOR UPDATE SKIP LOCKED` 队列、原子提交、常数时间鉴权、心跳/重连等都做得规范），本文只聚焦**会导致数据丢失、投递不全、功能不生效或稳定性受损的严重问题**，并给出可落地的修复方案。

问题按严重程度分为三档：

- **P0** — 数据丢失 / 投递不全 / 功能实际不生效
- **P1** — 可靠性 / 可用性受损（高峰或异常下触发）
- **P2** — 正确性 / 一致性缺陷（特定条件触发）

每条包含：现象 → 根因（含文件与代码位置）→ 影响 → 修复方案。

---

## 目录

| 编号 | 档位 | 问题 |
|---|---|---|
| P0-1 | P0 | 长短信只跟踪第一段的上游 ID，DLR「回一半」 |
| P0-2 | P0 | HTTP `callback_url` 从未实现，HTTP 客户端永远收不到 DLR |
| P0-3 | P0 | `SavePending` 失败被忽略，静默丢失 DLR 映射 |
| P0-4 | P0 | 每个 providerID 只转发第一条 DLR，中间态顶掉最终态 |
| P1-5 | P1 | DLR 处理同步跑在上游读循环 + 2s 轮询，阻塞连接 |
| P1-6 | P1 | SMPP 提交在读循环内同步跑 DB 事务，每会话串行 |
| P1-7 | P1 | 慢 DLR / 离线接收方的 DLR 因 TTL 被无条件清除 |
| P1-8 | P1 | 离线补发只补 500 条且仅在 bind 时触发，无周期重刷 |
| P2-9 | P2 | 路由「最长前缀」匹配实现不正确，可能误路由 |
| P2-10 | P2 | HTTP 入站消息用非持久自增 ID，重启后覆盖旧记录 |
| P2-11 | P2 | DLR 最后一跳「至多一次 + 乐观删除」，会话抖动即丢 |
| P2-12 | P2 | SMPP 提交无幂等，配合超时重发产生重复投递 |

---

## P0-1　长短信只跟踪第一段的上游 ID，DLR「回一半」

**现象**：一条需要分段的长短信（如下游用 `message_payload` 一次性提交整条，由网关自行分段），下游最多只收到 1 条 DLR，其余段的回执全部丢失；消息最终状态也只反映第一段。

**根因**：
- `internal/smppclient/pool.go` `Pool.Send`：对多段消息逐段 `submit`，上游为每段返回**不同的** message ID，但代码只保留第一段：

```go
id = NormalizeID(id, p.cfg.SMPP.MessageIDRespFormat)
if firstID == "" {
    firstID = id          // 第 2..N 段的 ID 被丢弃
}
...
return firstID, nil
```

- `internal/dispatch/dispatcher.go` `processOutbox`：只用返回的 `firstID` 存了**一条** `SavePending`。上游随后对每段各回一条 DLR，只有第一段能匹配，其余命中 `HandleDLR` 的 `dlr mapping not found` 被丢弃。

**影响**：所有由网关分段的长短信 DLR 回传不完整。这是「回一半」最主要的来源。

**修复方案**：让 `Send` 返回全部段的 provider ID，dispatcher 为每段各建一条 pending 映射（共享同一 `gateway_id`）；只有当某 `gateway_id` 下所有段的 DLR 都到齐后，才向下游合成/回推一条最终 DLR（或按策略回传每段）。

第一步，`Pool.Send` 返回全部 ID：

```go
// pool.go
func (p *Pool) Send(ctx context.Context, msg Message) ([]string, error) {
    parts := BuildSubmitSM(msg, p.cfg.SMPP)
    if len(parts) == 0 {
        return nil, errors.New("empty smpp submit")
    }
    ids := make([]string, 0, len(parts))
    for _, part := range parts {
        conn, ok := p.pick()
        if !ok {
            return nil, errors.New("no bound smpp upstream connection")
        }
        id, err := conn.submit(ctx, part.Body)
        if err != nil {
            // 已成功的段应记录，避免部分成功被当作整体失败重发（见 P2-12）
            return ids, err
        }
        ids = append(ids, NormalizeID(id, p.cfg.SMPP.MessageIDRespFormat))
    }
    return ids, nil
}
```

第二步，`Provider.Send` 接口与 `processOutbox` 相应改为处理 `[]string`，对每个 provider ID 建 pending，并在 pending 里记录 `SegmentTotal` / `SegmentIndex`。第三步，`HandleDLR` 收齐同 `gateway_id` 的全部段后再回推。

> 若不想做多段聚合，最小修复是：至少为**每段**建立 pending 映射并逐段回推 DLR，先消除「丢段」，聚合可作为后续增强。

---

## P0-2　HTTP `callback_url` 从未实现，HTTP 客户端永远收不到 DLR

**现象**：通过 `POST /v1/messages` 提交并携带 `callback_url` 的客户端，永远收不到投递回执回调；服务端仅打印一行日志。

**根因**：`callback_url` / `callback_rule` 被接收、校验、塞进 `Envelope.Source`，然后**在整条链路里再无任何使用**：

- `internal/httpgw/rules.go`：校验 `callback_url must be https`，写入 `dispatch.SubmitSource{CallbackURL, CallbackRule}`。
- `internal/dispatch/dispatcher.go` `Submit`：只读取了 `Source.Kind` / `SMPPSessionID` / `SMPPSystemID`，`CallbackURL` / `CallbackRule` **未写入** `OutboxPayload`，也未写入 `Pending`，就此丢失。
- `HandleDLR` 的 `SourceHTTPAPI` 分支：

```go
case SourceHTTPAPI.String():
    d.logger.Info("dlr for http source", "gateway_id", rec.GatewayID, "state", dlr.State)
    _ = d.store.DeletePending(ctx, dlr.ProviderID)   // 只记日志 + 删映射，没有任何回调
```

（全仓库 `grep callback` 可确认 `CallbackURL` 除赋值处外无任何消费点。）

**影响**：这是一个 **API 表面上支持、实际完全不生效**的半成品功能。HTTP 客户端无法拿到异步 DLR，问题隐蔽（提交返回 202，看起来正常）。

**修复方案**：把 callback 信息持久化到 pending，并在 `HandleDLR` 的 HTTP 分支实际发起带重试的回调。

1）`store.Pending` 与 `store.OutboxPayload` 增加字段 `CallbackURL`、`CallbackRule`（Postgres 迁移新增两列，见文末迁移示例）。

2）`Submit` 写入 payload：

```go
payload := store.OutboxPayload{
    // ...原字段...
    CallbackURL:  env.Source.CallbackURL,
    CallbackRule: env.Source.CallbackRule,
}
```

`processOutbox` 里 `SavePending` 时一并带上 `CallbackURL` / `CallbackRule`。

3）`HandleDLR` 的 HTTP 分支发起回调（放入内部队列异步执行，避免阻塞，见 P1-5）：

```go
case SourceHTTPAPI.String():
    if rec.CallbackURL != "" {
        d.enqueueCallback(callbackJob{
            URL:       rec.CallbackURL,
            Rule:      rec.CallbackRule,
            GatewayID: rec.GatewayID,
            State:     dlr.State,
            ErrorCode: dlr.ErrorCode,
            DoneAt:    dlr.DoneAt,
        }) // 独立 worker：带指数退避重试、超时、TLS 校验
    } else {
        d.logger.Info("dlr for http source without callback", "gateway_id", rec.GatewayID)
    }
    _ = d.store.DeletePending(ctx, dlr.ProviderID)
```

回调 worker 复用现有 `httprule` 渲染出站请求体，失败进重试队列（可复用 outbox 的退避策略）。

> 若短期不实现回调，至少应在 API 层**明确拒绝** `callback_url`（返回「暂不支持」），避免给调用方错误预期。

---

## P0-3　`SavePending` 失败被忽略，静默丢失 DLR 映射

**现象**：Postgres 出现瞬时抖动（连接池打满、超时、主从切换）时，个别消息虽已发给上游，但其 DLR 映射未落库，导致该消息的 DLR 必然「找不到映射」被丢，且不会重试。

**根因**：`internal/dispatch/dispatcher.go` `processOutbox`：

```go
if err := d.store.SavePending(ctx, store.Pending{...}); err != nil {
    d.logger.Warn("save pending failed", ...)   // 仅记日志，不中断
}
if err := d.store.AckOutbox(ctx, item.ID); err != nil {   // 照常 ack，消息标记已发
    d.logger.Warn("ack outbox failed", ...)
}
```

`SavePending` 失败不阻止 `AckOutbox`。消息已从 outbox 出队、状态置为已发，但没有 pending 映射。

**影响**：Postgres 压力越大概率越高的静默 DLR 丢失。属于「没全回」的一类，且难以排查（只有一行 Warn）。

**修复方案**：把「发送成功 + 保存 pending」视为一个整体，任一失败都不 ack，交由 outbox 重试（配合 P2-12 的幂等避免重复投递）。

```go
if providerID == "" {
    providerID = payload.GatewayID
}
// 先保证 pending 落库，再 ack
if err := d.store.SavePending(ctx, pending); err != nil {
    d.logger.Error("save pending failed, will requeue", "gateway_id", payload.GatewayID, "err", err)
    d.failOutbox(ctx, item, fmt.Errorf("save pending: %w", err)) // 不 ack，走重试
    return
}
if err := d.store.UpdateMessageSent(ctx, payload.GatewayID, providerID); err != nil {
    d.logger.Warn("update sent failed", "gateway_id", payload.GatewayID, "err", err)
}
if err := d.store.AckOutbox(ctx, item.ID); err != nil {
    d.logger.Warn("ack outbox failed", "outbox_id", item.ID, "err", err)
}
```

更彻底的做法：把 `SavePending` 与 `AckOutbox` 放进同一事务（新增 `store` 方法 `AckOutboxWithPending`），保证「出队」与「建映射」原子完成。

---

## P0-4　每个 providerID 只转发第一条 DLR，中间态顶掉最终态

**现象**：对接会先发中间态（`ENROUTE`/`ACCEPTD`）再发最终态（`DELIVRD`/`UNDELIV`）的上游时，下游可能只收到中间态，永远收不到最终态。

**根因**：`internal/dispatch/dispatcher.go` `HandleDLR`，一旦成功推送即无条件删除 pending：

```go
if err := d.pushSMPPDLR(rec, dlr); err != nil { ... }
_ = d.store.DeletePending(ctx, dlr.ProviderID)   // 首条推送后即删，后续 DLR 命中「mapping not found」
```

只有**最先到达**的那条 DLR 会被转发，同一 providerID 的后续 DLR 全部被丢。

**影响**：对只发最终态的上游无影响；对发多态的上游，下游拿到的可能是中间态而非最终结果，状态不准确。

**修复方案**：以「最终态」为删除条件，非最终态转发但**不删** pending；仅在最终态到达后才删除。

```go
func isFinalDLRState(state string) bool {
    switch strings.ToUpper(state) {
    case "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "REJECTD", "UNKNOWN":
        return true
    default:
        return false // ENROUTE / ACCEPTD 视为中间态
    }
}

// HandleDLR SMPP 分支：
if err := d.pushSMPPDLR(rec, dlr); err != nil {
    // ...原离线/失败处理...
}
if isFinalDLRState(dlr.State) {
    _ = d.store.DeletePending(ctx, dlr.ProviderID)
} else {
    // 保留映射，等待最终态；可选择更新 pending 的最近状态用于观测
    _ = d.store.UpdateMessageState(ctx, rec.GatewayID, dlr.State, dlr.ErrorCode)
}
```

注意：保留映射会延长 pending 生命周期，需与 P1-7 的 TTL 策略配合（最终态到达或过期才清）。

---

## P1-5　DLR 处理同步跑在上游读循环 + 2s 轮询，阻塞连接

**现象**：DLR 高峰、或大量 DLR 暂时找不到映射时，上游连接吞吐骤降，甚至出现 `submit_sm` 超时。

**根因**：调用链完全同步，且跑在**上游连接的读循环 goroutine** 上：

```
connection.readLoop (收到 deliver_sm)
  → c.onDLR(dlr)                              // 同步
    → pool.handleDLR → registry.onDLR
      → dispatcher.OnDLR → HandleDLR
        → getPendingForDLR                    // 最长轮询 dlrLookupWait(2s)，每 50ms 查一次
```

`internal/dispatch/dispatcher.go` `getPendingForDLR` 会在映射未就绪时轮询等待最长 2 秒。这 2 秒里，该连接的 `readLoop` **无法读取任何后续 PDU**，包括 `submit_sm_resp`（窗口 ACK）与排队的其他 DLR。

**影响**：单条找不到映射的 DLR 就能让一条上游连接停摆 2s → 窗口迟迟不释放 → 发送吞吐下降、`submit` 超时；DLR 越多、miss 越多，雪崩越明显。

**修复方案**：把 DLR 处理与上游读循环解耦——`readLoop` 收到 `deliver_sm` 并回 `deliver_sm_resp` 后，只把 DLR 投递到一个带缓冲的内部 channel，由独立的一组 DLR worker 消费（worker 里再做 `getPendingForDLR` 轮询）。

```go
// dispatcher 内新增
dlrCh chan provider.DLR   // 例如容量 4096

func (d *Dispatcher) OnDLR(dlr provider.DLR) {
    select {
    case d.dlrCh <- dlr:
    default:
        // 满则落库为 ready 或记指标告警，绝不阻塞上游读循环
        d.logger.Warn("dlr channel full, deferring", "provider_id", dlr.ProviderID)
        _ = d.store.MarkDLRReady(context.Background(), dlr.ProviderID, dlr.State, dlr.ErrorCode, dlr.DoneAt)
    }
}

func (d *Dispatcher) dlrWorker(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case dlr := <-d.dlrCh:
            if err := d.HandleDLR(ctx, dlr); err != nil {
                d.logger.Warn("dlr handle failed", "provider_id", dlr.ProviderID, "err", err)
            }
        }
    }
}
```

这样 2s 轮询不再阻塞上游连接。HTTP 入站 DLR（`handleInboundRule`）同理，改为投递到队列后立即返回 2xx，避免 HTTP handler 被 2s 轮询占用。

---

## P1-6　SMPP 提交在读循环内同步跑 DB 事务，每会话串行

**现象**：单个 ESME 会话的提交吞吐被单条 DB 往返限制（如远程 PG 5ms 往返 ≈ 每会话上限约 200 TPS），窗口大小形同虚设。

**根因**：`internal/smpp/session.go` `dispatch`：`submit_sm` 分支里 `s.inflight.Add(1)` 允许最多 `WindowSize` 条在途，但紧接着**同步**调用 `s.cfg.OnSubmit(s, msg)`；而 `OnSubmit`（`cmd/mysmpp/main.go`）里同步执行 `dispatcher.Submit` → `SubmitAtomic`（一个 PG 事务）。`dispatch` 又是在 `readLoop` 里被同步调用的，于是每条 `submit_sm` 都会阻塞读循环直到 DB 事务完成，窗口内并发根本没发生。

**影响**：吞吐受限；且读循环被占用会延迟对方 `enquire_link` / 后续 PDU 的处理，叠加 P1-5 更糟。

**修复方案**：`OnSubmit` 内部改为把提交丢给一个有界工作池异步处理，处理完再 `session.Send(resp)` 与 `CompleteSubmit()`。窗口计数（`inflight`）保持不变，天然成为背压上限。

```go
onSubmit := func(session *smpp.Session, submit smpp.SubmitSM) {
    submitPool.Submit(func() {                 // 有界 goroutine 池，容量 = 会话数 * 窗口
        defer session.CompleteSubmit()
        receipt, err := dispatcher.Submit(context.Background(), dispatch.Envelope{ /* ... */ })
        resp := smpp.PDU{CommandID: smpp.CommandSubmitSMResp, SequenceID: submit.SequenceID}
        // ...按 err 填 Status / Body...
        session.Send(resp)
    })
}
```

注意保持 `submit_sm_resp` 的 `SequenceID` 与请求一致（现有代码已正确），乱序返回对 SMPP 是允许的。工作池容量要有上限，防止慢 DB 时 goroutine 无限增长。

---

## P1-7　慢 DLR / 离线接收方的 DLR 因 TTL 被无条件清除

**现象**：迟到超过 30 分钟的 DLR 丢失；接收方长时间离线时，已收到、正等待补发的 DLR 也被清掉。

**根因**：
- `pendingTTL` 默认 `30m`（`internal/dispatch/dispatcher.go` `New`），`SavePending` 时 `ExpiresAt = now + pendingTTL`。
- `internal/store/postgres.go` `SweepExpiredPending`：`DELETE FROM pending WHERE expires_at < $1`，**不区分** `dlr_ready`。所以 `MarkDLRReady` 存下的、正等接收方上线补发的 DLR，一旦过期照删不误。

**影响**：真实 DLR 常延迟数小时（关机、store-and-forward）；接收方离线超过 TTL 时攒下的 ready DLR 全部丢失。

**修复方案**：
1）`pendingTTL` 默认值按对接上游的最长 DLR 延迟调大（如 24h~72h），并作为可配置项。
2）Sweep 时**保护已 ready 的 DLR**，给它们更长（或单独）的保留期：

```sql
-- 只清「未收到 DLR」的过期映射；已 ready 的用更长的独立过期时间
DELETE FROM pending
WHERE provider_id IN (
    SELECT provider_id FROM pending
    WHERE dlr_ready = FALSE AND expires_at < $1
    ORDER BY expires_at LIMIT 10000
);
```

为 ready 的 DLR 增加单独的 `ready_expires_at`（如 7 天），到期再清并计入指标。

---

## P1-8　离线补发只补 500 条且仅在 bind 时触发，无周期重刷

**现象**：接收方离线期间积压超过 500 条 DLR 时，重新上线后一次只补 500，剩余要等下一次 bind 才补；若长时间不重连，backlog 一直排不空。

**根因**：`internal/dispatch/dispatcher.go` `FlushDLR` 由 `SetReceiverBoundHandler` 挂在 bind 成功回调上，只在 bind 时触发一次；`internal/store/postgres.go` `ListReadyDLR` 固定 `LIMIT 500`（入参默认 500）。没有周期性重刷，也没有「补完 500 后继续补下一批」的循环。

**影响**：大 backlog 场景 DLR「回不全」，需人为触发重连才继续。

**修复方案**：
1）`FlushDLR` 内部分批循环直到无 ready 或推送受阻：

```go
func (d *Dispatcher) FlushDLR(systemID string) {
    ctx := context.Background()
    for {
        items, err := d.store.ListReadyDLR(ctx, systemID, 500)
        if err != nil || len(items) == 0 {
            return
        }
        progressed := false
        for _, rec := range items {
            dlr := provider.DLR{ /* 由 rec 组装 */ }
            if err := d.pushSMPPDLR(rec, dlr); err != nil {
                return // 接收方又离线或出错，留待下次
            }
            _ = d.store.DeletePending(ctx, rec.ProviderID)
            progressed = true
        }
        if !progressed {
            return
        }
    }
}
```

2）增加一个**周期性重刷**协程（类似 `pendingSweepLoop`），定期对所有在线 receiver 的 `system_id` 触发 `FlushDLR`，不依赖 bind 事件。

---

## P2-9　路由「最长前缀」匹配实现不正确，可能误路由

**现象**：存在多条前缀路由时，命中的可能不是最具体（最长前缀）的那条。

**根因**：`internal/router/router.go`：
- `sortRoutes` 在同优先级下按 `longestPrefix(route.Prefix)`（该路由**所有前缀里最长的一个**）排序，而非按实际命中的前缀长度。
- `MatchPhone` 在单条路由内**首个 `HasPrefix` 命中即返回**，不比较前缀长度。

反例：同优先级下 RouteA 前缀 `["44"]`，RouteB 前缀 `["4","1234"]`。RouteB 的 route 级最长前缀为 4（`"1234"`），排在 RouteA（2）前面。号码 `44xxxx` 先匹配 RouteB 的 `"4"` 即返回，尽管 RouteA 的 `"44"` 更具体。

**影响**：号码被路由到非预期供应商，属于难察觉的配置陷阱。

**修复方案**：改为「全局最长匹配前缀」语义——遍历所有（同优先级）路由的所有前缀，选出实际能匹配且最长的那个。

```go
func (r *Router) MatchPhone(to string) (config.RouteConfig, bool) {
    best := -1
    var bestRoute config.RouteConfig
    var catchAll *config.RouteConfig
    for i := range r.routes {
        route := r.routes[i]
        if len(route.Prefix) == 0 {
            if catchAll == nil { c := route; catchAll = &c }
            continue
        }
        for _, p := range route.Prefix {
            if strings.HasPrefix(to, p) && len(p) > best {
                best = len(p)
                bestRoute = route
            }
        }
    }
    if best >= 0 {
        return bestRoute, true
    }
    if catchAll != nil {
        return *catchAll, true
    }
    return config.RouteConfig{}, false
}
```

（优先级仍优先于前缀长度：可先按 priority 分组，只在最高优先级组内做最长前缀选择。）

---

## P2-10　HTTP 入站消息用非持久自增 ID，重启后覆盖旧记录

**现象**：入站 MO 消息、被风控拦截的 blocked 消息，其 `gateway_id` 在进程重启后从头重复，`SaveMessage` 的 `ON CONFLICT DO UPDATE` 会用新消息覆盖同 ID 的旧消息，造成历史数据丢失/串号。

**根因**：`internal/httpgw/rules.go`：

```go
var fallbackIDSeq atomic.Uint64
func newID() string { return fmt.Sprintf("g%010d", fallbackIDSeq.Add(1)) }
```

`newID()` 用进程内自增计数（重启归零），被 MO 保存、blocked 保存、以及无 dispatcher 时的降级路径使用。而正常 MT 走的是持久化的 `idAllocator`（`g%012d`）。虽然位宽不同（10 vs 12）避免了与 MT 的直接冲突，但**同类入站消息在重启后互相覆盖**。

**影响**：MO / blocked 记录不可靠，跨重启丢失。

**修复方案**：入站消息也走持久化 ID 分配（复用 `store.ReserveGatewayIDRange` / dispatcher 的 `newGatewayID`），或改用带时间/随机前缀的唯一 ID：

```go
func newID() string {
    var b [8]byte
    _, _ = crand.Read(b[:])
    return fmt.Sprintf("mo-%d-%s", time.Now().UnixNano(), base64.RawURLEncoding.EncodeToString(b[:]))
}
```

更一致的做法是把入站保存也接入 dispatcher，用同一套 ID 分配器，保证全局唯一且可持久。

---

## P2-11　DLR 最后一跳「至多一次 + 乐观删除」，会话抖动即丢

**现象**：向下游回推 DLR 时，若下游会话恰在此刻断开，DLR 丢失且不再补发。

**根因**：`internal/dispatch/dispatcher.go` `pushSMPPDLR` 把 `deliver_sm` 交给 `session.Send` 即视为成功、随后 `DeletePending`；而 `internal/smpp/session.go` `Send` 只是入缓冲 channel（cap 64），会话已关闭时静默丢弃：

```go
func (s *Session) Send(p PDU) {
    select {
    case s.out <- p:      // 仅入队
    case <-s.closed:      // 已关闭：静默丢
    }
}
```

且**不等待下游 `deliver_sm_resp`**（`session.dispatch` 里 `deliver_sm_resp` 仅 debug 打印，不做确认）。入队成功 → 立即删 pending，但 PDU 可能还没真正写到 socket，或写失败。

**影响**：下游会话抖动窗口内的 DLR 静默丢失，属于「没全回」。

**修复方案**：引入 `deliver_sm` 的应答确认与「未确认前不删除」。
1）为回推的 `deliver_sm` 分配序列号并登记待确认表；`session.dispatch` 收到对应 `deliver_sm_resp(status=OK)` 后，回调 dispatcher 执行 `DeletePending`。
2）`pushSMPPDLR` 不再入队即删；`Send` 改为可感知失败（返回 bool），入队失败/超时则保留 pending（回落到 `MarkDLRReady`），由 P1-8 的周期重刷补发。

```go
ok := session.SendTracked(pdu, func(status uint32) {
    if status == smpp.StatusOK {
        _ = d.store.DeletePending(context.Background(), rec.ProviderID)
    }
})
if !ok {
    return errNoReceiverOnline // 触发 MarkDLRReady，稍后重刷
}
// 不在此处 DeletePending，等 resp 回调
```

这样把最后一跳从「至多一次」提升为「收到 resp 才算送达」的至少一次语义（配合幂等去重）。

---

## P2-12　SMPP 提交无幂等，配合超时重发产生重复投递

**现象**：ESME 因未及时收到 `submit_sm_resp`（尤其在 P1-5/P1-6 导致读循环阻塞时）而重发，网关侧生成新的 `gateway_id`、重新投递上游，造成重复短信。

**根因**：SMPP 提交路径（`main.go onSubmit → dispatcher.Submit`）未传 `ClientID` / `ClientMsgID`，`SubmitAtomic` 的幂等分支不生效；每条 `submit_sm` 都是全新消息。HTTP 侧有 `client_msg_id` 幂等，SMPP 侧没有等价机制。

**影响**：叠加 P1-5/P1-6 的超时后，重复投递概率上升。

**修复方案**：为 SMPP 提交构造幂等键。SMPP 无天然消息级幂等标识，可用 `(system_id, sequence_id + 内容摘要)` 或对端在短窗口内的 `(system_id, from, to, text)` 摘要作为键：

```go
// onSubmit 内
clientMsgID := fmt.Sprintf("smpp:%s:%d:%s",
    session.SystemID(), submit.SequenceID, shortHash(submit.From, submit.To, submit.Text))
env := dispatch.Envelope{
    // ...
    ClientID:    "smpp:" + session.SystemID(),
    ClientMsgID: clientMsgID,
}
```

配合 `SubmitAtomic` 已有的幂等 upsert，短窗口内的重发会返回既有 `gateway_id` 而非二次投递。注意：`sequence_id` 会随连接重置，跨连接重发建议用内容摘要为主键组成部分，TTL 设短（如几分钟）以免误判不同消息。

---

## 建议的修复顺序

1. **P0-2、P0-3、P0-1**：影响面最大、且属「看起来正常实则丢数据/丢功能」，优先。
2. **P1-5、P1-6**：解耦 DLR 与提交的同步阻塞，是吞吐与稳定性的地基，也让 P2-12 的超时重发问题自然缓解。
3. **P0-4、P1-7、P1-8、P2-11**：DLR 完整性与最终态正确性，一并处理收敛。
4. **P2-9、P2-10、P2-12**：正确性与一致性收尾。

## 附：Postgres 迁移示例（配合 P0-2 / P0-4 / P1-7）

```sql
-- 003_dlr_reliability.up.sql
ALTER TABLE pending ADD COLUMN IF NOT EXISTS callback_url  text;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS callback_rule text;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS segment_total int  NOT NULL DEFAULT 1;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS segment_index int  NOT NULL DEFAULT 1;
ALTER TABLE pending ADD COLUMN IF NOT EXISTS ready_expires_at timestamptz;

-- ready 补发用索引
CREATE INDEX IF NOT EXISTS pending_ready_idx
    ON pending (source_system, received_at)
    WHERE dlr_ready = TRUE;

-- outbox 也需带上 callback 字段（若不放 payload JSON 内则加列）
```

---

## 说明

- 本文所有代码片段为修复示意，字段名/签名需按实际类型定义对齐（如 `store.Pending`、`provider.Provider` 接口的连锁改动）。
- 修复 P0-3、P2-11、P2-12 时请一起验证幂等，避免「不丢」变成「重复」。
- 建议为每个修复补充针对性单测（现有测试框架已覆盖 DLR 回推、离线补发、幂等、分段，扩展成本低）。
