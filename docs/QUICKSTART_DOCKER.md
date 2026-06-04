# Docker 快速启动与账号路由配置

这份文档面向“拉代码后直接用 Docker 跑起来”的场景。默认配置使用文件持久化、mock 上游供应商和冷门端口，适合开发、联调和验收功能链路。

默认 Docker 启动不会拉起数据库，也不依赖数据库。当前 `docker-compose.yml` 只有 `mysmpp` 一个服务，运行时使用 `storage.driver=file`，配置和运行数据都放在 `mysmpp-data` named volume 的 `/app/data` 下；仓库里的 `migrations/` 只是后续 SQL Store 的预留脚本。

## 一条命令启动

```powershell
git clone <your-repo-url> mysmpp
cd mysmpp
docker compose up -d --build
```

启动后访问：

```text
http://127.0.0.1:19087/admin/
```

首次启动会随机生成密码，并写入容器数据卷：

- HTTP/API/Admin 端口：`19087`
- SMPP 端口：`29175`
- 管理后台用户名：`admin`
- SMPP ESME 用户名：`dev-esme`

Docker 镜像默认启动命令已经写好：

```text
/app/mysmpp -config /app/data/config.json
```

所以 `docker compose up -d --build` 和裸 `docker run` 都不用额外传配置路径。

查看首次生成的密码：

```powershell
docker cp mysmpp:/app/data/credentials.txt .\mysmpp-credentials.txt
Get-Content .\mysmpp-credentials.txt
```

这个文件包含 `admin.password`、`smpp.password` 和 `esmes.dev-esme.password`。它只在首次生成密码时写入；后续重启不会重新生成。

## 验证服务

查看健康检查：

```powershell
Invoke-RestMethod http://127.0.0.1:19087/healthz
```

查看日志：

```powershell
docker compose logs -f mysmpp
```

提交一条 HTTP 测试短信：

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:19087/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello from docker"}'
```

验证 SMPP submit_sm 和 DLR：

```powershell
go run ./cmd/testesme -p <credentials.txt 里的 esmes.dev-esme.password> -text "hello from smpp"
```

`cmd/testesme` 默认会连接 `127.0.0.1:29175`，用户名默认是 `dev-esme`。Docker 模式下密码是首次启动随机生成的值。

## 账号密码在哪里配

Docker 用仓库里的 `configs/docker.json` 作为种子配置，镜像构建时复制到容器内：

```text
/app/configs/docker.json
```

首次启动时，如果 `/app/data/config.json` 不存在，程序会复制种子配置到 `/app/data/config.json`，生成随机密码，写回配置文件，并额外写一份：

```text
/app/data/credentials.txt
```

常用账号字段：

```json
{
  "admin": {
    "username": "admin",
    "password": "<first-run-generated>"
  },
  "esmes": [
    {
      "system_id": "dev-esme",
      "password": "<first-run-generated>"
    }
  ],
  "clients": [
    {
      "client_id": "api-client-a",
      "token": "replace-with-client-token",
      "enabled": true,
      "allowed_ips": ["127.0.0.1/32"]
    }
  ]
}
```

- `admin.username` / `admin.password`：登录 `/admin/` 管理后台。
- `esmes[].system_id` / `esmes[].password`：下游 SMPP 客户端 bind 凭据。
- `clients[].token`：HTTP `/v1/messages` 的 Bearer token；如果 `clients` 为空，开发模式下 HTTP 提交不强制 token。
- `clients[].allowed_ips`：HTTP 客户端 IP 白名单，CIDR 格式；为空表示不限制 IP。

也可以在 `/admin/` 后台修改这些分区：`下游 ESME`、`HTTP 客户端` 和 `原始 JSON`。

## 短信路由怎么配

路由由三块组成：

- `providers`：上游供应商。
- `outbound`：发给 HTTP 供应商时的字段映射。
- `routes`：按手机号前缀选择供应商。

当前默认配置只有一个 mock 上游：

```json
{
  "providers": [
    {
      "name": "dev-mock",
      "protocol": "mock",
      "enabled": true
    }
  ],
  "routes": [
    {
      "name": "default",
      "prefix": [],
      "provider": "dev-mock",
      "priority": 1
    }
  ]
}
```

`prefix: []` 表示默认路由，所有号码都能命中。

增加真实 HTTP 供应商时，可以这样配：

```json
{
  "outbound": [
    {
      "name": "sms-http-json",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "mobile": "to",
        "content": "text",
        "sender": "from",
        "msgId": "id",
        "encoding": "encoding"
      },
      "headers": {
        "Authorization": "Bearer replace-with-provider-token",
        "X-Request-ID": "{{id}}"
      },
      "response": {
        "id_path": "message_id"
      }
    }
  ],
  "providers": [
    {
      "name": "provider-http-a",
      "protocol": "http",
      "endpoint": "https://sms-provider.example.com/send",
      "rule": "sms-http-json",
      "enabled": true,
      "rate_limit": {
        "tps": 50,
        "burst": 100,
        "timeout_ms": 3000
      }
    }
  ],
  "routes": [
    {
      "name": "china-mobile-13x",
      "prefix": ["134", "135", "136", "137", "138", "139"],
      "provider": "provider-http-a",
      "priority": 100
    },
    {
      "name": "default",
      "prefix": [],
      "provider": "dev-mock",
      "priority": 1
    }
  ]
}
```

匹配规则：

1. `priority` 越大越先匹配。
2. 同优先级下，前缀越长越先匹配。
3. `prefix: []` 是兜底默认路由。
4. `routes[].provider` 必须指向一个 `enabled: true` 的 provider。

## 上游 DLR 回调怎么配

供应商回调 DLR 时，使用 `inbound` 规则接收。DLR 规则必须配置 `provider`、`auth_header`、`auth_token`、`provider_id` 和 `status` 映射：

```json
{
  "inbound": [
    {
      "name": "provider-http-a-dlr",
      "method": "POST",
      "path": "/callback/provider-http-a/dlr",
      "provider": "provider-http-a",
      "content_type": "application/json",
      "auth_header": "X-Callback-Token",
      "auth_token": "replace-with-callback-token",
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

网关会校验回调 token，并校验 pending 记录中的 provider，避免一个供应商回调伪造其他供应商的 DLR。

## 后台修改配置

进入：

```text
http://127.0.0.1:19087/admin/
```

可以在后台修改：

- `线路`：增删改 `routes`。
- `上游`：编辑 `providers`。
- `出站规则`：编辑 `outbound`。
- `下游 ESME`：编辑 SMPP bind 账号。
- `HTTP 客户端`：编辑 HTTP `/v1/messages` 调用方 token 和 IP 白名单。
- `入站规则`：编辑回调和自定义 HTTP 入站规则。
- `原始 JSON`：一次性编辑完整配置。

保存时会先校验配置，再热更新运行时配置，并原子写回启动参数 `-config` 指定的文件。Docker 默认就是 `/app/data/config.json`，所以后台保存会落到 named volume。

旧 `/ui/config` / `/v1/config` 入口也会在 `cmd/mysmpp` 正常启动时写回同一个配置文件。也就是说，平台里的配置操作都会持久化到 `/app/data/config.json`。

## 数据和配置持久化位置

Compose 默认创建 named volume：

```powershell
docker volume ls
```

持久化文件：

- `/app/data/config.json`：运行配置，包含首次生成后的密码和后台保存的配置。
- `/app/data/credentials.txt`：首次生成的密码备份。
- `/app/data/store.json`：消息、outbox、pending DLR、幂等记录快照。

裸 Docker 如果不用 Compose，也建议挂载一个 volume：

```powershell
docker run -d --name mysmpp `
  -p 19087:19087 `
  -p 29175:29175 `
  -v mysmpp-data:/app/data `
  mysmpp:local
```

## 停止与重启

```powershell
docker compose down
docker compose up -d --build
```

裸 Docker：

```powershell
docker rm -f mysmpp
docker run -d --name mysmpp -p 19087:19087 -p 29175:29175 mysmpp:local
```

## 数据库说明

当前版本不会自动启动 PostgreSQL、MySQL 或 SQLite，也不会因为没有数据库而启动失败。

- `configs/docker.json` 里是 `"storage": {"driver": "file", "dsn": ""}`。
- `cmd/mysmpp` 会把空 `dsn` 解析为配置文件同目录的 `store.json`。
- `migrations/001_init.up.sql` 是生产持久化 Store 的 schema 预留，现阶段不会被 Docker 自动执行。

影响是：单容器部署已经具备默认持久化；生产多实例共享存储或更强事务能力时，再单独接 SQL Store 和数据库服务。
