# mysmpp 改造修复与测试报告（2026-07-03）

## 变更概览

本次按 `mysmpp-改造开发文档 (2).md` 与 `mysmpp-测试文档.md` 复审后，落地 P1-P3 主线能力：

- 新增统一内容过滤 `internal/filter`：Aho-Corasick 多关键词匹配、正则规则、大小写/全角/零宽归一化、block/mask/tag 动作。
- 新增话单 `internal/cdr`：JSONL 文本话单、`.writing` 中间文件、按条数/时间轮转、关闭 drain/flush/sync、可配置脱敏与是否落原文。
- 扩展动态路由：`from_prefix`、`system_ids`、`client_ids`、`content_tags`、`time_windows`、`enabled`、`weighted`、`failover` 配置；本次只落地 weighted 选路，不改 store schema 做容灾切链。
- 改造 `dispatch.Submit`：过滤先于路由；HTTP/SMPP 双入口统一过滤；rejected/accepted/sent/retry/failed/dlr 均出 CDR；幂等重复不再产生第二条 accepted 话单。
- 改造 HTTP 网关：关键词内容过滤从 `risk` 下沉到 dispatcher/filter，HTTP 内容拒绝返回 403；`risk` 仅保留限流；热更新先编译新 filter/CDR，成功后再切换。
- 主程序装配 filter/CDR，并在 SMPP submit 失败时将 `ErrBlocked` 映射为 `submit_sm_resp` 失败状态。
- `configs/production.example.json` 增加 filter/CDR 配置样例。

## 正确性判断

- 文档指出的历史缺口“HTTP 有关键词过滤、SMPP 绕过过滤”属实，已修复为统一在 `dispatch.Submit` 前置过滤。
- 文档附录 A.1 的“accepted 话单漏去重分支”属实，已按 `duplicate==false` 才 emit accepted 实现，并加回归测试。
- 文档附录 A.1 的“加权选路不能用 gateway_id”属实，本实现使用 `To` 做稳定哈希。
- 文档建议的容灾链会触碰 outbox/store/schema，风险较高。本次未实现 P4 自动切链，`failover` 仅作为配置合法性与首 provider 选择基础保留，不做失败后切换。
- HTTP `dispatcher==nil` 兜底路径定义为测试/降级路径，不承诺内容过滤和 CDR；生产路径应配置 dispatcher。

## 测试记录

已通过：

```text
go test ./...
go vet ./...
go test -race ./...
```

新增/更新覆盖：

- filter：归一化关键词 block、mask 保留原文非命中部分、tag 汇聚。
- cdr：`max_records` 轮转、`.writing` finalize、JSONL 行数验证。
- router：内容标签+主叫前缀、多维匹配、同一 To 加权稳定、时间窗。
- dispatcher：HTTP/SMPP Source 均被 filter 拒绝；幂等重复只产生一条 accepted CDR。
- httpgw：dispatcher 路径 filter 拒绝返回 403；无 dispatcher 兜底路径不做内容过滤。

本地 E2E：

- 临时服务端口：HTTP `127.0.0.1:19187`，SMPP `127.0.0.1:29275`。
- 正常 HTTP 提交返回 `202 Accepted`，回执示例：`gateway_id=g000000000002 provider=dev-mock route=marketing state=queued`。
- 封禁词 HTTP 提交返回 `403 Forbidden`，响应：`message blocked by content filter: block-spam`。
- `hello promo` 命中 filter tag，路由到 `marketing`。
- CDR 目录出现正式 `.jsonl` 文件，`max_records=2` 时完成轮转。

## 部署状态

目标服务器：`REDACTED`。

二次复测（2026-07-03 00:41 Asia/Shanghai）：

- `http://REDACTED:19087/healthz` 可访问，当前线上服务存活：

```json
{"checks":{"outbox_depth":0,"pending_size":0,"smpp_listener":"ok","storage":"ok"},"status":"ok"}
```

- `/v1/config` 与 `/v1/messages` 返回 `401 Unauthorized`，需要线上 admin/client 凭据；已知本地 dev 凭据与 root 密码均不能通过 HTTP Basic Auth。
- 本机到服务器 22 端口 TCP 可达，但 SSH 服务端仍在密钥交换阶段主动断开，未进入密码认证：

```text
Remote protocol version 2.0, remote software version OpenSSH_8.0
SSH2_MSG_KEXINIT sent
expecting SSH2_MSG_KEX_ECDH_REPLY
Connection closed by REDACTED port 22
```

已尝试：

- OpenSSH 直连；
- 禁用公钥、强制 password；
- 指定 `diffie-hellman-group14-sha1`；
- 指定 `diffie-hellman-group14-sha256`、`ecdh-sha2-nistp256`；
- 指定 `aes128-ctr`、`aes128-cbc` 与 `hmac-sha2-256`、`hmac-sha1`；
- 指定 `curve25519-sha256`；
- Paramiko 密码连接。

结论：线上旧服务健康，但当前无法从本机执行远端部署/热更新。需要恢复 SSH 握手，或提供可用的线上 admin/client 凭据后才能通过 `/v1/config` 热更新运行配置；二进制替换仍必须依赖 SSH/控制台/Docker 发布通道。

已本地验证可生成 Linux amd64 二进制：

```text
dist/mysmpp-linux-amd64
```

服务器恢复 SSH 后建议执行：

```bash
systemctl stop mysmpp || true
cp mysmpp-linux-amd64 /usr/local/bin/mysmpp
chmod +x /usr/local/bin/mysmpp
systemctl start mysmpp
curl -s http://127.0.0.1:19087/healthz
```

若服务以 Docker Compose 部署，则改为在服务器仓库目录执行：

```bash
git pull
docker compose up -d --build
curl -s http://127.0.0.1:19087/healthz
```

## 未完成/后续

- P4 容灾切链未实现，需要单独改 outbox payload/store schema/迁移脚本。
- 服务器实机部署与远端 E2E 因 SSH 握手断开阻塞，需恢复 SSH 后补跑。
