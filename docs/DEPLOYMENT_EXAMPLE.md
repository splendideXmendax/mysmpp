# mysmpp 完整部署与测试实例

这份文档给一个从零启动到验证链路的完整例子。适合服务器首次部署、验收功能、交给运维照着跑。

## 目标

部署后验证这些能力:

- HTTP 服务启动正常。
- 管理后台可以登录。
- HTTP `/v1/messages` 可以提交消息。
- SMPP ESME 可以 bind、submit_sm，并收到 deliver_sm DLR。
- outbox、pending、storage 健康检查正常。
- 可以切换到 Postgres 生产配置。

## 一、Docker Compose 快速部署

进入项目目录:

```bash
cd /path/to/mysmpp
```

构建并启动:

```bash
docker compose up -d --build
```

确认容器:

```bash
docker compose ps
```

预期看到:

```text
mysmpp    mysmpp:local    Up    0.0.0.0:19087->19087/tcp, 0.0.0.0:29175->29175/tcp
```

查看日志:

```bash
docker compose logs -f mysmpp
```

首次启动会出现:

```text
seeded config
generated startup credentials
http listening addr=0.0.0.0:19087
smpp listening addr=0.0.0.0:29175
```

如果用 `Ctrl+Z` 挂起了日志命令，可以清理:

```bash
jobs
kill %1
```

## 二、查看首次生成的密码

镜像是 distroless，容器内没有 `cat` 和 `sh`，不能这样:

```bash
docker compose exec mysmpp cat /app/data/credentials.txt
```

正确方式是从容器复制文件:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

也可以复制运行时配置:

```bash
docker cp mysmpp:/app/data/config.json ./config.running.json
cat ./config.running.json
```

`credentials.txt` 里通常包含:

```text
admin.username=admin
admin.password=...
smpp.system_id=mysmpp-dev
smpp.password=...
esmes.dev-esme.system_id=dev-esme
esmes.dev-esme.password=...
```

## 三、健康检查

本机检查:

```bash
curl http://127.0.0.1:19087/healthz
```

预期:

```json
{
  "checks": {
    "outbox_depth": 0,
    "pending_size": 0,
    "smpp_listener": "ok",
    "storage": "ok"
  },
  "status": "ok"
}
```

远程访问:

```bash
curl http://服务器IP:19087/healthz
```

如果远程不通，检查安全组或防火墙:

```bash
sudo ufw allow 19087/tcp
sudo ufw allow 29175/tcp
```

CentOS/RHEL:

```bash
sudo firewall-cmd --add-port=19087/tcp --permanent
sudo firewall-cmd --add-port=29175/tcp --permanent
sudo firewall-cmd --reload
```

## 四、登录管理后台

浏览器打开:

```text
http://服务器IP:19087/admin/
```

账号:

```text
username: admin
password: credentials.txt 中的 admin.password
```

本机开发配置的固定密码是:

```text
admin / mysmpp-admin-19087
```

Docker 首次启动默认会随机生成密码，以 `credentials.txt` 为准。

## 五、HTTP 提交测试

Docker 默认配置 `clients` 为空，所以可以不带 token 提交:

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"hello from http"}'
```

预期:

```json
{
  "gateway_id": "g0000000001",
  "provider": "dev-mock",
  "route": "default",
  "state": "queued"
}
```

查询消息:

```bash
curl -sS 'http://127.0.0.1:19087/v1/messages?limit=10&offset=0'
```

如果你配置了 HTTP client:

```json
"clients": [
  {
    "client_id": "api-client-a",
    "token": "client-token-123",
    "enabled": true,
    "allowed_ips": []
  }
]
```

提交时要带:

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Client-ID: api-client-a' \
  -H 'X-Token: client-token-123' \
  -d '{"from":"10690000","to":"13800138000","text":"hello auth"}'
```

## 六、SMPP 测试

使用项目自带 ESME 测试客户端:

```bash
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u dev-esme \
  -p '<credentials.txt 中的 esmes.dev-esme.password>' \
  -text 'hello smpp'
```

预期:

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 sub:001 dlvrd:001 submit date:... done date:... stat:DELIVRD err:000 text:hello smpp
```

批量提交 10 条:

```bash
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u dev-esme \
  -p '<password>' \
  -text 'batch smpp' \
  -n 10
```

## 七、配置真实 HTTP 上游示例

假设上游接口:

```text
POST https://sms-provider.example.com/send
Authorization: Bearer PROVIDER_TOKEN
Content-Type: application/json
```

请求体:

```json
{
  "mobile": "13800138000",
  "content": "hello",
  "sender": "10690000",
  "requestId": "g0000000001"
}
```

响应:

```json
{
  "code": 0,
  "data": [
    {
      "messageId": "upstream-123"
    }
  ]
}
```

配置:

```json
{
  "outbound": [
    {
      "name": "provider-a-json",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "mobile": "to",
        "content": "text",
        "sender": "from",
        "requestId": "id"
      },
      "headers": {
        "Authorization": "Bearer PROVIDER_TOKEN",
        "X-Request-ID": "{{id}}"
      },
      "response": {
        "id_path": "data.0.messageId"
      }
    }
  ],
  "providers": [
    {
      "name": "provider-a",
      "protocol": "http",
      "endpoint": "https://sms-provider.example.com/send",
      "rule": "provider-a-json",
      "enabled": true,
      "http_timeout_ms": 3000,
      "rate_limit": {
        "tps": 100,
        "burst": 200,
        "timeout_ms": 2000
      }
    }
  ],
  "routes": [
    {
      "name": "default",
      "prefix": [],
      "provider": "provider-a",
      "priority": 1
    }
  ]
}
```

## 八、配置 Provider DLR 回调

假设上游回调:

```text
POST http://你的域名/callback/provider-a/dlr
X-Callback-Token: CALLBACK_TOKEN
Content-Type: application/json
```

请求体:

```json
{
  "message_id": "upstream-123",
  "status": "DELIVRD",
  "error_code": 0
}
```

配置:

```json
{
  "inbound": [
    {
      "name": "provider-a-dlr",
      "method": "POST",
      "path": "/callback/provider-a/dlr",
      "provider": "provider-a",
      "content_type": "application/json",
      "auth_header": "X-Callback-Token",
      "auth_token": "CALLBACK_TOKEN",
      "fields": {
        "provider_id": "message_id",
        "status": "status",
        "error_code": "error_code"
      },
      "success_status": 200,
      "success_body": "{\"ok\":true}"
    }
  ]
}
```

手工模拟 DLR:

```bash
curl -sS -X POST http://127.0.0.1:19087/callback/provider-a/dlr \
  -H 'Content-Type: application/json' \
  -H 'X-Callback-Token: CALLBACK_TOKEN' \
  -d '{"message_id":"upstream-123","status":"DELIVRD","error_code":0}'
```

注意:

- `message_id` 必须是发送成功后保存到 pending 的 provider_id。
- `provider` 必须匹配 pending 记录里的 provider。
- 否则会返回 403 或 404。

## 九、Postgres 生产部署

安装或准备 Postgres 后创建库和用户:

```bash
sudo -u postgres psql
```

```sql
CREATE USER mysmpp WITH PASSWORD 'CHANGE_ME_STRONG_PASSWORD';
CREATE DATABASE mysmpp OWNER mysmpp;
\q
```

执行迁移:

```bash
export MYSMPP_DSN='postgres://mysmpp:CHANGE_ME_STRONG_PASSWORD@127.0.0.1:5432/mysmpp?sslmode=disable'
psql "$MYSMPP_DSN" -f migrations/001_init.up.sql
```

配置:

```json
{
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:CHANGE_ME_STRONG_PASSWORD@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

建议 Postgres 参数，8C16G 单机可从这里起步:

```conf
shared_buffers = 4GB
effective_cache_size = 10GB
work_mem = 32MB
maintenance_work_mem = 512MB
max_connections = 200
max_wal_size = 4GB
checkpoint_timeout = 15min
checkpoint_completion_target = 0.9
synchronous_commit = off
wal_compression = on
random_page_cost = 1.1
effective_io_concurrency = 200
```

表维护建议:

```sql
ALTER TABLE pending SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_cost_limit = 1000
);

ALTER TABLE outbox SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_cost_limit = 1000
);
```

## 十、300 TPS 调参示例

如果上游平均 RTT 是 200ms，推荐:

```json
{
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 5
  },
  "risk": {
    "per_client_per_second": 500
  },
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:password@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  }
}
```

估算:

```text
总并发 = workers * per_worker_concurrency
       = 10 * 10
       = 100

理论吞吐约 = 总并发 / 上游 RTT
          = 100 / 0.2s
          = 500 TPS
```

扣掉抖动和失败重试，300 TPS 有余量。

## 十一、简单压测示例

用 `seq + xargs` 跑一个轻量 HTTP 并发测试:

```bash
seq 1 1000 | xargs -n1 -P100 -I{} curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"load test"}'
```

统计健康状态:

```bash
curl -sS http://127.0.0.1:19087/healthz
```

更严谨的压测建议:

- 使用固定 QPS 工具，例如 wrk、vegeta、hey 或自写 Go rate limiter。
- 同时观察 `/healthz` 的 `outbox_depth` 和 `pending_size`。
- 观察 Postgres CPU、连接数、慢查询。
- 上游 mock 延迟和真实 provider RTT 差异很大，最终必须接真实上游联调。

## 十二、常见问题

### 1. 容器内 cat 不存在

原因: 镜像基于 distroless。

解决:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

### 2. healthz 正常，但外网打不开后台

检查:

- 云安全组是否放行 19087。
- 服务器防火墙是否放行 19087。
- Docker compose ports 是否映射。

### 3. HTTP 提交返回 401

原因:

- 配置了 `clients`。
- 请求缺少 `X-Client-ID` 或 `X-Token`。

解决:

```bash
curl -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Client-ID: api-client-a' \
  -H 'X-Token: client-token-123' \
  -d '{"from":"10690000","to":"13800138000","text":"hello"}'
```

### 4. HTTP 提交返回 no route matched

原因:

- 没有默认 route。
- provider 被禁用。
- route 指向的 provider 名写错。

建议保留:

```json
{
  "name": "default",
  "prefix": [],
  "provider": "provider-a",
  "priority": 1
}
```

### 5. DLR 回调返回 404

原因:

- `provider_id` 找不到 pending 记录。
- 上游回调的 ID 和发送响应提取的 provider_id 不一致。

检查 `outbound.response.id_path` 或 `id_regex`。

### 6. DLR 回调返回 403

原因:

- token 错误。
- callback rule 的 `provider` 和 pending 记录中的 provider 不一致。

### 7. SMPP bind failed

检查:

- system_id 是否等于 `esmes[].system_id`。
- password 是否等于 `esmes[].password`。
- 单 system_id 会话数是否超过 `max_sessions_per_system_id`。

### 8. outbox_depth 持续增长

可能原因:

- 上游 HTTP 超时或返回 5xx。
- provider rate limit 太低。
- dispatcher 并发太低。
- Postgres 连接池太小。

处理:

- 看日志里的 provider error。
- 调小 `http_timeout_ms`。
- 调高 `dispatcher.per_worker_concurrency`。
- 调高 Postgres `pool_max_conns`。

## 十三、停止、重启、清空数据

停止:

```bash
docker compose down
```

重启:

```bash
docker compose restart mysmpp
```

重新构建并启动:

```bash
docker compose up -d --build
```

清空容器和 named volume，等于重新初始化:

```bash
docker compose down -v
docker compose up -d --build
```

注意: `down -v` 会删除 `/app/data/config.json`、`credentials.txt`、`store.json`，生产环境不要随便执行。
