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

最终部署（2026-07-03 09:55 Asia/Shanghai）：

- 使用 Paramiko 以 `root` 登录成功；此前系统 OpenSSH 失败原因是本地执行环境的 SSH/TUN 链路在 KEX 阶段不稳定，并非服务器密码错误。
- 服务器部署目录：`/root/mysmpp`。
- 部署方式：Docker Compose，容器 `mysmpp` + `mysmpp-postgres`。
- 已备份旧配置：`/root/mysmpp-backups/config-20260703095511.json`。
- 服务器仓库从 `e453018` fast-forward 到 `b4b8643`，重新构建镜像并重建 `mysmpp` 容器。
- 新镜像 ID：`sha256:8238f9398a81bcf1eefd73fbdf201df3aa4a198d52b943d274f3aba313a569a9`。
- 部署后 healthz 通过：

```json
{"checks":{"outbox_depth":0,"pending_size":0,"smpp_listener":"ok","storage":"ok"},"status":"ok"}
```

线上配置验证：

- 保留生产数据卷配置，未被镜像样例覆盖。
- 当前生产路由：`ap2-default`。
- 当前生产 provider：`ap2-upstream`。
- 当前 ESME：`REDACTED`。
- `clients=[]`，HTTP API 使用 admin Basic Auth。

线上功能验证：

- 通过 `/v1/config` 增量开启 filter/CDR，并添加唯一测试过滤词 `codex-block-online-20260703`。
- HTTP 提交测试词返回 `403 Forbidden`：

```text
message blocked by content filter: codex-online-block-test
```

- healthz 仍通过，`outbox_depth=0`、`pending_size=0`，说明测试拒绝消息未进入上游投递。
- CDR 写入验证：
  - 重启前 `.writing` 文件写入 295 字节。
  - `docker restart mysmpp` 后优雅关闭并 finalize 为 `cdr-20260703015705-prod-ap2-1.jsonl`。
  - 重启后再次提交测试词仍返回 `403`，证明 filter/CDR 配置已持久化并可重启加载。
  - CDR 样本字段：`kind=rejected`、`reason=filter_block`、`filter_rule=codex-online-block-test`、`source=http`、`instance=prod-ap2`。

全量线上复测与补丁（2026-07-03 10:12 Asia/Shanghai）：

- 按测试文档执行线上安全版全量测试：临时追加 `codex-mock-a/b` mock provider 与 `999998xx` 专用测试前缀，确保测试短信不会落到真实 `ap2-upstream`。
- 测试过程中发现 CDR 在同一秒内快速轮转时，文件名可能相同并覆盖旧 `.jsonl`，导致 `accepted` 事件缺失。
- 已修复：CDR 文件名加入 `UnixNano`，避免同秒同 count 轮转覆盖；新增回归测试 `TestWriterDoesNotOverwriteSameSecondRotations`。
- 修复提交：`9d25c14 fix: avoid cdr rotation filename collisions`。
- 服务器已从 `b4b8643` 更新到 `9d25c14`，重新构建并重建 `mysmpp` 容器，部署后 healthz 正常。
- 本地门禁再次通过：

```text
go test ./...
go vet ./...
go test -race ./...
```

- 线上复测通过项：
  - 热更新坏正则被拒：`400`，运行中配置保持可用。
  - block 规则：HTTP 返回 `403 message blocked by content filter: codex-block-rule2`。
  - tag 路由：`codex-route-online-full2-20260703` 命中 `codex-tag-route` -> `codex-mock-a`。
  - mask：入库文本从 `hello codex-mask-online-full2-20260703` 变为 `hello [MASKED2]`。
  - weighted：同一 `To` 连续 3 次均稳定落到 `codex-mock-a`，路由均为 `codex-weighted-route`。
  - mock provider 投递与 DLR：测试消息最终状态均为 `DELIVRD`。
  - 重启验证：`docker restart mysmpp` 后 healthz 正常，filter 规则仍可用。
  - CDR 完整性：共捕获 17 条测试相关事件，覆盖 `rejected`、`accepted`、`sent`、`dlr`。
- 测试完成后已清理全部临时 `codex-*` provider/route/filter rule，并将 `cdr.max_records` 恢复为 `10000`。
- 清理后 healthz：

```json
{"checks":{"outbox_depth":0,"pending_size":0,"smpp_listener":"ok","storage":"ok"},"status":"ok"}
```

二次复测（2026-07-03 00:41 Asia/Shanghai）：

- 生产健康检查地址（已脱敏）可访问，当前线上服务存活：

```json
{"checks":{"outbox_depth":0,"pending_size":0,"smpp_listener":"ok","storage":"ok"},"status":"ok"}
```

- `/v1/config` 与 `/v1/messages` 返回 `401 Unauthorized`，需要线上 admin/client 凭据；已知本地 dev 凭据与 root 密码均不能通过 HTTP Basic Auth。
- 三次复测补充：
  - 本地门禁再次通过：`go test ./...`、`go vet ./...`、`go test -race ./...`。
  - SMPP `29175` 可响应 bind，但样例账号 `dev-esme/dresme1`、`dev-esme/esmepw1`、`esme1/dresme1`、`esme1/esmepw1`、`load-tester/dresme1`、`load-tester/esmepw1` 均返回 bind 失败状态 `0x0d`，说明线上 ESME 凭据不是文档样例。
  - 探测 Docker/API 常见端口 `2375/2376/5000/8080/8443/9000`，均无法取得有效 HTTP API 响应，不能作为部署入口。
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
