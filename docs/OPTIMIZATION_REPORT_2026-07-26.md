# mysmpp 优化报告 — 2026-07-26

本报告基于对全项目(~1.2 万行代码 + 5.6k 测试)的系统审查。全部结论均**逐行读代码亲自核实**,并区分「系统代码 bug」与「配置相关问题」。

- 基础健康度:`go build ./...` 干净、`go vet ./...` 无告警、`go test ./...` 全绿。
- 本机无 C 编译器,`-race` 无法运行,并发问题由静态审查 + 并发压力测试佐证。
- **本次只落地不改变业务功能与外部接口语义的加固修复。** 为消除竞态,关闭时序和 file store 并发持久化吞吐会产生必要变化;会改变业务行为/容量的其他修复仅列入下方"待批清单",等确认后再动。

---

## 一、已落地(业务语义不变,happy path 输入输出不变)

这三项都属于把"崩溃/数据损坏"变成"安全无害",正常路径的输入输出和外部接口不变。CDR 与 FileStore 修复附有并发回归测试;Dispatcher 修复为原子读写替换。

### A1. CDR Writer 并发关闭 panic → 进程崩溃
- **文件**:[internal/cdr/writer.go](../internal/cdr/writer.go)
- **问题**:`Emit` 先查 `closed` 再 `in <- e`,与 `Close` 的 `close(in)` 存在 TOCTOU。**配置热重载**([httpgw/rules.go:435-440](../internal/httpgw/rules.go#L435))时先换 CDR sink 指针再 `oldCDR.Close()`,而 worker/DLR goroutine 已 `Load()` 到旧 sink 正要 `Emit` → `send on closed channel` → 整进程崩。`OnFull:"drop"` 的 `select` 同样会 panic。
- **定性**:真·系统 bug,与配置无关,热重载(常规运维动作)即可触发。
- **修复**:新增 `sendMu sync.RWMutex`。`Emit` 持读锁发送,`Close` 持写锁后再关闭 channel。多个 Emit 可并发持读锁(channel 多发送方本就安全),Close 等所有在途 Emit 完成后再关。竞争彻底消除,正常路径行为不变。
- **测试**:`TestWriterEmitConcurrentWithCloseNoPanic`(50 轮 Emit 与 Close 对撞,断言不 panic)。

### A2. FileStore 并发持久化损坏/丢数据
- **文件**:[internal/store/file.go](../internal/store/file.go)
- **问题**:`persist()` 仅用 `RLock` 做内存快照,后续 `Stat→Rename(path→bak)→Rename(tmp→path)` 全程无锁。多 goroutine 并发 persist(RLock 允许并发)时:一个刚把 `path` 改名,另一个的 `Rename(path→bak)` 因源消失而失败(操作误报错);还会发生旧快照覆盖新快照的丢更新。
- **定性**:真代码 bug,但**配置门控**——仅当 `storage.driver = "file"` 时走这段。`production.example` 用 `postgres`、dev 用 `memory`,而 `configs/docker.json` 使用 `file`,因此 Docker 文件存储配置会直接受益。
- **修复**:新增 `persistMu sync.Mutex`,把整个 snapshot+write+rename 串行化。文件持久化并发吞吐会下降,换取正确且稳定的落盘顺序;其他存储驱动不受影响。
- **测试**:`TestFileStoreConcurrentPersistNoErrorOrCorruption`(8×40 并发写,断言无错 + 落盘 JSON 完整、记录数正确)。

### A3. Dispatcher.instanceID 数据竞争
- **文件**:[internal/dispatch/dispatcher.go](../internal/dispatch/dispatcher.go)
- **问题**:`SetInstanceID` 无锁写 `string`,`emitCDR` 在 worker/DLR goroutine 并发无锁读 → 数据竞争(`-race` 会报)。实际影响极小(通常启动时设一次)。
- **定性**:系统 bug,轻微。
- **修复**:字段改为 `atomic.Pointer[string]`,读写走 atomic。
- **测试说明**:本机缺少 C 编译器,无法用 `-race` 动态验证;实现通过静态审查与常规测试验证。

**验证**:改动 3 个文件 + 2 个测试文件,`go build`、`go vet`、`go test ./...` 全绿;新并发测试 `-count=10` 稳定。

---

## 二、待批清单(会改变行为/容量,**未擅自改**)

这些是真实缺陷,但修复会改变外部可观测行为或吞吐能力,按你"别改现有功能和能力"的要求**保持原样**,列出供你决策。

### 🔴 建议优先评估

| 编号 | 问题 | 位置 | 修复的行为影响 |
|---|---|---|---|
| B1 | **限流按「条」不按「段」**:`RateLimitedProvider.SendAll` 每条只扣 1 token,`Pool.SendAll` 却按分段发 N 个 PDU。生产配 `tps:200`,一条 6 段长短信只算 1 → 实际对上游速率可达数倍,可能被运营商限流/拉黑。 | [provider/ratelimit.go:63](../internal/provider/ratelimit.go#L63)、[smppclient/pool.go:60](../internal/smppclient/pool.go#L60) | 改为按 `len(parts)` 扣 token 后,**有效发送吞吐会下降**(变为符合真实 TPS)。属"改容量",需你确认是否接受。 |
| B2 | **上游收发无 deadline**:`writeLoop` 对 `WritePDU` 无写超时;上游 TCP 背压时 writeLoop 卡死 → `out` 满 → `readLoop` 阻塞 → 收不到 submit_resp → window 满 → 全线 submit 停摆。默认 enquire=30s 会在 ~60s 后 `closeConn` 自愈,但那 60s 窗口内在途消息丢失。 | [smppclient/connection.go:178](../internal/smppclient/connection.go#L178) | 加写超时会**主动断开"慢但没死"的连接**,属行为变化,需评估阈值。 |

### 🟠 功能/语义类(需求确认)

| 编号 | 问题 | 位置 | 说明 |
|---|---|---|---|
| B3 | **失效转移链未接线**:`router` 算出 `FailoverChain` 但 dispatcher 丢弃,`failOutbox` 只重试同一 provider,配了 `[A,B]` 也不会切到 B。 | [router.go:140](../internal/router/router.go#L140)、[dispatcher.go:747](../internal/dispatch/dispatcher.go#L747) | 文档标注为未实现的 Phase 4。属**新增能力**,需产品决策。 |
| B4 | **SavePending 部分失败重发**:分段逐个 SavePending,若中途失败则不 Ack,重试时整条重发 → 手机重复收 + 孤儿 pending。 | [dispatcher.go:701](../internal/dispatch/dispatcher.go#L701) | 修复涉及错误路径与幂等逻辑,需谨慎设计。 |
| B5 | **每段各回一条 DLR**:一条 3 段消息产生 3 条 pending,ESME 只发 1 条却收 3 条回执,不符标准 SMPP 语义。 | [dispatcher.go:701-723](../internal/dispatch/dispatcher.go#L701) | 修复会改变回执数量,属行为变化。 |
| B6 | **DLR 查询端点可被拖死**:未知 provider_id 会同步轮询 store 至 `dlrLookupWait`(2s),持 token 者灌垃圾 ID 可耗尽 goroutine。 | [httpgw/rules.go:541](../internal/httpgw/rules.go#L541) | 改为异步/快速失败会改变回调时序。 |

### 🟡 加固类(低影响,可择机)

| 编号 | 问题 | 位置 | 定性 |
|---|---|---|---|
| B7 | `callback_url` 仅校验 scheme=https,可 SSRF 打内网/元数据端点 | [httpgw/rules.go:307](../internal/httpgw/rules.go#L307) | 系统(校验不足);加内网 IP 黑名单会拒绝部分 URL |
| B8 | 反代终止 TLS 时会话 Cookie 缺 `Secure`(未处理 `X-Forwarded-Proto`) | [admin/session.go:125](../internal/admin/session.go#L125) | 配置相关(取决于部署拓扑) |
| B9 | malformed `enquire_period`(如 `"30"` 缺单位)被静默吞成 0 → 关掉活性检测 | [smppclient/connection.go:240](../internal/smppclient/connection.go#L240) | 配置写错才触发;建议解析失败即告警而非静默 |
| B10 | admin 口令明文存储/比较,登录端点无 CSRF、限流 map 无清扫 | [config.go:268](../internal/config/config.go#L268)、[admin/server.go:87](../internal/admin/server.go#L87) | 系统(实现选择);改为哈希会影响现有配置读取 |
| B11 | Postgres `CheckIdempotency` 每次调用全表 DELETE 且吞错 | [store/postgres.go:408](../internal/store/postgres.go#L408) | 系统;改为后台定期清理,行为等价但需新增定时器 |
| B12 | `OnReceiverBound(FlushDLR)` 每次 receiver bind 起未跟踪 goroutine,循环 bind 可无界并发 | [smpp/session.go:348](../internal/smpp/session.go#L348) | 系统;加 single-flight 会改并发行为 |
| B13 | 会话 goroutine 与 worker 循环无 `recover()`,单个解析 panic 拖垮全进程 | [smpp/session.go:175](../internal/smpp/session.go#L175)、[cmd/mysmpp/main.go:116](../cmd/mysmpp/main.go#L116) | 系统;加 recover 改变崩溃语义(建议加) |
| B14 | 重连退避在健康会话后从不重置,周期性掉线会把延迟顶到 maxBackoff | [smppclient/connection.go:71](../internal/smppclient/connection.go#L71) | 系统;bind 成功后 reset 会改重连时序 |
| B15 | CDR 轮转文件名唯一性仅靠 `UnixNano()`,Windows 时钟粗粒度下可能同名 `os.Rename` 覆盖 | [cdr/writer.go:209](../internal/cdr/writer.go#L209) | 系统;加计数器/随机后缀会改文件名格式 |
| B16 | `configs/dev.json` 提交明文口令(含 admin `mysmpp-admin-19087`) | [configs/dev.json](../configs/dev.json) | 配置;dev 无妨,生产勿沿用 |

---

## 三、已核实"良好"的部分(免再查)
- SQL 全参数化,**无注入**;admin 路由鉴权 + CSRF 完整;`html/template` 自动转义,**无 XSS**;无路径穿越。
- PDU 分帧 / C-string / TLV / UDH 边界检查严谨,**未发现越界或远程崩溃**。
- 上游 send-window 槽位记账正确(无泄漏、无 conn 写竞争);限流刷新 goroutine 正常回收;HTTP body 均关闭并限长读。
- `idAllocator` 互斥 + 每实例预留区间,无跨实例碰撞;E.164 前缀表 prefix-free,最短前缀匹配安全。

---

## 四、建议后续顺序
1. **B1 限流分段**、**B2 上游写超时** — 直接影响生产稳定性/合规,优先评估(需接受吞吐/断连行为变化)。
2. **B13 recover 兜底** — 低成本、高收益的健壮性提升。
3. 其余按业务优先级择机处理。

> 文档已在本地全量备份至 `doc_backup_20260726/`(root + docs,含 xlsx 规则表);该目录由 `.gitignore` 排除,不随本报告提交。
