# Docker 部署文档

这份文档说明如何使用 Docker 或 Docker Compose 运行 `mysmpp`。当前 Docker 默认配置面向开发和联调：文件持久化、mock provider、冷门端口、首次启动随机凭据。生产环境请复制配置后替换供应商信息。

更短的“拉代码直接跑、账号密码和短信路由怎么配”说明见 [QUICKSTART_DOCKER.md](QUICKSTART_DOCKER.md)。

默认 Compose 不会启动数据库，当前运行链路也不依赖数据库。`migrations/` 目录里的 SQL 只是后续 SQL Store 预留，不会被 Docker 自动执行。

## 端口

默认端口：

- `19087/tcp`：HTTP API、`/admin/` 管理后台、`/ui/config` 应急配置页。
- `29175/tcp`：SMPP 监听端口。

启动后访问：

```text
http://127.0.0.1:19087/admin/
```

默认用户名：

- 管理后台：`admin`
- ESME：`dev-esme`

首次启动随机生成密码，查看方式：

```powershell
docker cp mysmpp:/app/data/credentials.txt .\mysmpp-credentials.txt
Get-Content .\mysmpp-credentials.txt
```

## 使用 Docker Compose

构建并启动：

```powershell
docker compose up -d --build
```

Compose 使用镜像内置默认启动命令：`/app/mysmpp -config /app/data/config.json`，不需要额外传 `command` 或配置路径。第一次启动时会用 `/app/configs/docker.json` 种子配置生成 `/app/data/config.json`。

查看日志：

```powershell
docker compose logs -f mysmpp
```

验证 HTTP：

```powershell
Invoke-RestMethod http://127.0.0.1:19087/healthz
```

提交测试短信：

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:19087/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello from docker"}'
```

验证 SMPP DLR：

```powershell
go run ./cmd/testesme -p <credentials.txt 里的 esmes.dev-esme.password> -text "hello from docker"
```

停止：

```powershell
docker compose down
```

## 使用裸 Docker

构建镜像：

```powershell
docker build -t mysmpp:local .
```

运行容器：

```powershell
docker run -d --name mysmpp `
  -p 19087:19087 `
  -p 29175:29175 `
  mysmpp:local
```

镜像的 `ENTRYPOINT` 和 `CMD` 已经指向 `/app/data/config.json`，所以裸 `docker run` 也会直接启动服务。裸 Docker 建议挂载 `/app/data`：

```powershell
docker run -d --name mysmpp `
  -p 19087:19087 `
  -p 29175:29175 `
  -v mysmpp-data:/app/data `
  mysmpp:local
```

查看日志：

```powershell
docker logs -f mysmpp
```

删除容器：

```powershell
docker rm -f mysmpp
```

## 配置文件

镜像内置种子配置：

```text
/app/configs/docker.json
```

运行配置写入：

```text
/app/data/config.json
```

对应仓库种子文件是 `configs/docker.json`。它使用 `0.0.0.0:19087` 和 `0.0.0.0:29175`，方便 Docker 端口映射。生产部署建议在后台或 `/app/data/config.json` 中替换供应商信息。

生产部署前至少要修改：

- `admin.password`
- `esmes[].password`
- `clients[].token`
- `inbound[].auth_token`
- `providers[].endpoint`
- `providers[].system_id`
- `providers[].password`
- `routes[]`

本机直接运行时可用 `configs/example.json`，它监听 `127.0.0.1:19087` 和 `127.0.0.1:29175`；Docker 中不要使用这个文件，因为容器内绑定 `127.0.0.1` 会导致宿主机端口映射访问不到服务。

## Compose 配置说明

```yaml
services:
  mysmpp:
    build:
      context: .
      dockerfile: Dockerfile
    image: mysmpp:local
    ports:
      - "19087:19087"
      - "29175:29175"
    volumes:
      - mysmpp-data:/app/data
```

字段说明：

- `ports`：把宿主机冷门端口映射到容器端口。
- `volumes`：保存运行配置、首次随机密码和文件 Store 快照。
- `restart: unless-stopped`：异常退出后自动重启。

## 配置页面与容器

`/admin/` 保存配置时会原子写回 `-config` 指定的配置文件。Docker 默认 `-config=/app/data/config.json`，且 Compose 挂载 `mysmpp-data:/app/data`，所以后台保存会持久化。

旧 `/ui/config` 和 `/v1/config` 也会写回同一个配置文件。

## 健康检查建议

当前服务提供：

```text
GET /healthz
```

运行镜像使用 distroless，不包含 `curl` 或 `wget`。生产环境更建议使用外部负载均衡、Prometheus blackbox exporter、Nginx 或云探针访问 `/healthz`。

## 持久化与日志

当前 Docker 默认 `storage.driver=file`。`/app/data/store.json` 会持久化消息记录、outbox、pending DLR 和幂等记录；容器重启后会继续使用同一份数据。多实例共享存储或更强事务能力需要后续接 SQL Store。

日志输出到 stdout，可用 Docker 标准日志查看：

```powershell
docker compose logs -f mysmpp
```
