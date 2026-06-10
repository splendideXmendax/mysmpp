# mysmpp 短信网关

`mysmpp` 是一个轻量级短信网关 MVP。它可以接收下游 SMPP/HTTP 提交，按号码前缀和优先级路由到上游 provider，持久化消息、outbox、pending DLR 和幂等记录，并把状态报告通过 SMPP `deliver_sm` 推回原 ESME 会话。

当前项目已经不是协议骨架，而是可运行、可联调、可继续扩展的网关雏形。

## 已实现能力

- SMPP 3.4 服务端: `bind_receiver`、`bind_transmitter`、`bind_transceiver`、`submit_sm`、`enquire_link`、`unbind`。
- SMPP DLR: 构造 `deliver_sm`，包含 receipt text、`receipted_message_id` TLV、`message_state` TLV。
- SMPP 会话保护: 最大会话数、单 system_id 最大会话数、submit window、未 bind 超时、空闲超时。
- HTTP API: `/v1/messages` 提交和分页查询，支持客户端 token 鉴权和 IP 白名单。
- 路由: 按号码前缀和 priority 选择上游 provider。
- HTTP 上游规则: 支持 JSON、form、query、header 渲染，支持响应 `id_path` / `id_regex` 提取 provider_id。
- SMPP 上游 Provider: `protocol=smpp` 时 mysmpp 作为 ESME bind 上游 SMSC，支持 `submit_sm`、`submit_sm_resp` 对账和上游 `deliver_sm` DLR 解析。
- HTTP 入站规则: 支持 provider DLR 回调、普通入站消息和自定义字段映射。
- 存储: memory、file、Postgres 三种 driver。Postgres outbox 使用 `FOR UPDATE SKIP LOCKED` 并发 claim。
- Dispatcher: 可配置 worker、单 worker 并发、claim 数量、轮询间隔、pending TTL、最大重试次数。
- 管理后台: `/admin/`，用户名密码登录、登录限流、CSRF、防止占位符凭据上线、运行时热更新并写回配置。
- 可靠性辅助: outbox 重试、指数退避、幂等提交、简单风控、健康检查。

## 当前边界

- 长短信在 SMPP 上游可按 UDH/payload/SAR 策略发送；HTTP 上游发送路径仍发送完整原文。
- 风控计数是进程内 map，多实例部署时限额会放大。
- gateway_id 是进程内递增计数，单进程内不会碰撞；生产多实例或重启后强唯一建议后续改为存储层分配。
- pending DLR 清理是惰性清理，没有独立后台归档任务。
- HTTP provider 的 DLR 通过 `inbound` 回调规则进入，`HTTPProvider.OnDLR` 是有意 no-op。
- SMPP 上游 Provider 当前支持 transceiver bind；`tx_rx` 分离 bind、SMPP over TLS、MO 路由到下游和分段 DLR 落库聚合仍是后续项。
- 暴露到公网时请放在 TLS 反向代理后面，不要直接裸奔 HTTP 管理后台。

## 目录结构

```text
cmd/mysmpp        网关主程序
cmd/testesme      本地 SMPP ESME 测试客户端
internal/admin    管理后台、session、CSRF、配置写回
internal/config   配置结构、默认值、校验、首次启动凭据生成
internal/dispatch 统一提交、outbox worker、pending DLR 映射
internal/httpgw   HTTP API、入站规则、风控
internal/httprule HTTP 请求渲染
internal/message  消息模型、编码识别、GSM-7/UCS-2、长短信拆分
internal/provider 上游 provider 接口、HTTP/mock、限速包装
internal/router   前缀和优先级路由
internal/smpp     SMPP PDU、session、submit、DLR、TLV
internal/store    memory/file/Postgres 存储
configs           示例配置
docs              中文文档
migrations        Postgres schema
```

## 本地快速启动

```powershell
go run ./cmd/mysmpp
```

默认使用 `configs/example.json`:

- HTTP/API/Admin: `http://127.0.0.1:19087`
- SMPP: `127.0.0.1:29175`
- 管理后台账号: `admin`
- 管理后台密码: `mysmpp-admin-19087`
- 示例 ESME: `dev-esme` / `esmepw1`

打开后台:

```text
http://127.0.0.1:19087/admin/
```

健康检查:

```powershell
Invoke-RestMethod http://127.0.0.1:19087/healthz
```

Linux:

```bash
curl http://127.0.0.1:19087/healthz
```

## Docker 快速启动

```bash
docker compose up -d --build
docker compose ps
curl http://127.0.0.1:19087/healthz
```

Docker 镜像使用 distroless，容器内没有 `cat` / `sh`。首次启动生成的密码在 volume 里的 `/app/data/credentials.txt`，查看方式:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

## HTTP 提交测试

开发配置的 `clients` 为空时，`/v1/messages` 使用 admin Basic Auth:

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -u admin:mysmpp-admin-19087 \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"hello"}'
```

如果配置了 `clients`，需要带:

```text
X-Client-ID: <client_id>
X-Token: <token>
```

分页查询消息:

```bash
curl -u admin:mysmpp-admin-19087 'http://127.0.0.1:19087/v1/messages?limit=20&offset=0'
```

## SMPP DLR 测试

启动网关后运行:

```bash
go run ./cmd/testesme -text "hello smpp"
```

预期输出类似:

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 ... stat:DELIVRD err:000 text:hello smpp
```

Docker 模式下如果 ESME 密码是首次随机生成的:

```bash
go run ./cmd/testesme -addr 127.0.0.1:29175 -u dev-esme -p '<credentials.txt 中的密码>' -text "hello docker"
```

## 配置入口

常用配置文件:

| 文件 | 用途 | 直接运行 |
|---|---|---|
| `configs/example.json` | 本机开发 | 可以 |
| `configs/dev.json` | 本机开发备用 | 可以 |
| `configs/docker.json` | Docker 首次启动种子配置 | 可以 |
| `configs/production.example.json` | 生产模板 | 需要替换占位符和准备 Postgres |

详细配置见 [docs/CONFIGURATION.md](docs/CONFIGURATION.md)。

部署与配置实战手册见 [docs/DEPLOYMENT_PRACTICAL_GUIDE.md](docs/DEPLOYMENT_PRACTICAL_GUIDE.md)。

完整部署、配置、测试实例见 [docs/DEPLOYMENT_EXAMPLE.md](docs/DEPLOYMENT_EXAMPLE.md)。

Docker 说明见 [docs/DOCKER.md](docs/DOCKER.md) 和 [docs/QUICKSTART_DOCKER.md](docs/QUICKSTART_DOCKER.md)。

管理后台说明见 [docs/ADMIN.md](docs/ADMIN.md)。
SMPP 上游连接页面说明见 [docs/ADMIN_CONNECTIONS.md](docs/ADMIN_CONNECTIONS.md)。

API 参考见 [docs/API_REFERENCE.md](docs/API_REFERENCE.md)。

运维 Runbook 见 [docs/RUNBOOK.md](docs/RUNBOOK.md)。

安全加固清单见 [docs/SECURITY_HARDENING.md](docs/SECURITY_HARDENING.md)。

SMPP 支持矩阵见 [docs/SMPP_SUPPORT_MATRIX.md](docs/SMPP_SUPPORT_MATRIX.md)。

SMPP 上游 Provider 与 SMPP-to-SMPP 中继见 [docs/SMPP_UPSTREAM.md](docs/SMPP_UPSTREAM.md)。

数据模型见 [docs/DATA_MODEL.md](docs/DATA_MODEL.md)。

升级与迁移见 [docs/UPGRADE_MIGRATION.md](docs/UPGRADE_MIGRATION.md)。

## 生产建议

1. 使用 `configs/production.example.json` 复制出自己的配置文件。
2. 替换所有 `CHANGE_ME_BEFORE_DEPLOY`。
3. 使用 Postgres:

```json
{
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:CHANGE_ME@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

4. 启动前执行:

```bash
psql "$MYSMPP_DSN" -f migrations/001_init.up.sql
```

5. 管理后台和 HTTP API 放在 Nginx/Caddy/负载均衡后面，由反向代理负责 TLS。
6. 如果在反向代理后启用 IP 白名单，配置 `trusted_proxies`。
7. 300 TPS 单节点建议从 `dispatcher: 10 x 10`、Postgres pool 50、上游 HTTP timeout 3s 起步，再根据 provider RTT 调整。

## 测试

```bash
go test ./...
```

当前测试覆盖配置校验、管理后台、HTTP API、入站规则、幂等、provider 请求渲染和 ID 提取、消息编解码、SMPP session、DLR/TLV、dispatcher/outbox、store 行为。

## 后续路线

1. 真正逐段发送长短信。
2. 存储层分配 gateway_id，支持重启和多实例强唯一。
3. Redis/Postgres 分布式风控。
4. Metrics 和审计日志。
5. pending/outbox 后台清理和 messages 归档。
6. MO 下发到 SMPP/HTTP 下游。
