# Docker 部署文档

这份文档说明如何使用 Docker 或 Docker Compose 部署 `mysmpp`。当前项目默认使用内存存储，适合本地测试、协议联调和早期网关验证。生产环境建议尽快接入持久化存储和外部监控。

## 端口

容器默认暴露两个端口：

- `8080/tcp`：HTTP API 和配置页面。
- `2775/tcp`：SMPP 监听端口。

启动后访问：

```text
http://127.0.0.1:8080/ui/config
```

## 配置文件

Docker Compose 默认挂载：

```text
./configs/docker.json -> /app/configs/config.json
```

容器启动命令：

```text
/app/mysmpp -config /app/configs/config.json
```

生产部署前至少要修改：

- `smpp.system_id`
- `smpp.password`，如果继续使用旧的单账号兼容模式
- `esmes[].system_id`
- `esmes[].password`
- `inbound[].auth_token`
- `providers[].endpoint`
- `providers[].system_id`
- `providers[].password`
- `routes[]`

## 使用 Docker Compose

构建并启动：

```powershell
docker compose up -d --build
```

查看日志：

```powershell
docker compose logs -f mysmpp
```

停止：

```powershell
docker compose down
```

验证 HTTP：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
```

提交测试短信：

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:8080/v1/messages `
  -ContentType "application/json" `
  -Body '{"from":"10690000","to":"13800138000","text":"hello from docker"}'
```

验证 SMPP DLR：

```powershell
go run ./cmd/testesme -addr 127.0.0.1:2775 -u esme1 -p secret -text "hello from docker"
```

预期测试客户端会先显示 `submitted msg_id=...`，随后收到一条 `deliver_sm` DLR：

```text
[DLR] 13800138000 -> 10690000 : id:g0000000001 ... stat:DELIVRD err:000 text:hello from docker
```

## 使用裸 Docker

构建镜像：

```powershell
docker build -t mysmpp:local .
```

运行容器：

```powershell
docker run -d --name mysmpp `
  -p 8080:8080 `
  -p 2775:2775 `
  -v ${PWD}/configs/docker.json:/app/configs/config.json:ro `
  mysmpp:local -config /app/configs/config.json
```

查看日志：

```powershell
docker logs -f mysmpp
```

删除容器：

```powershell
docker rm -f mysmpp
```

## Linux 服务器示例

```bash
git clone https://github.com/splendideXmendax/mysmpp.git
cd mysmpp
cp configs/docker.json configs/prod.json
vi configs/prod.json
docker compose up -d --build
```

如果使用云服务器安全组，建议：

- `8080` 只对内网或管理 IP 开放。
- `2775` 只对下游 SMPP 客户端 IP 开放。

## Dockerfile 说明

镜像使用多阶段构建：

1. `golang:1.25-bookworm` 编译静态二进制。
2. `gcr.io/distroless/static-debian12:nonroot` 运行服务。

优点：

- 最终镜像不包含 Go 编译器。
- 默认非 root 用户运行。
- 攻击面比完整 Linux 发行版更小。

## Compose 配置说明

```yaml
services:
  mysmpp:
    build:
      context: .
      dockerfile: Dockerfile
    image: mysmpp:local
    ports:
      - "8080:8080"
      - "2775:2775"
    volumes:
      - ./configs/docker.json:/app/configs/config.json:ro
    command: ["-config", "/app/configs/config.json"]
```

字段说明：

- `ports`：把宿主机端口映射到容器端口。
- `volumes`：挂载外部配置，修改配置文件后重启容器即可生效。
- `command`：指定容器内配置文件路径。
- `restart: unless-stopped`：异常退出后自动重启。

## 配置页面与容器

配置页面的 `PUT /v1/config` 会更新运行时配置，但不会写回挂载的 JSON 文件。容器重启后仍以 `/app/configs/config.json` 为准。

推荐流程：

1. 在配置页面临时调整并验证规则。
2. 把确认后的 JSON 写回 `configs/docker.json` 或生产配置文件。
3. 重启容器。

```powershell
docker compose restart mysmpp
```

## 健康检查建议

当前服务提供：

```text
GET /healthz
```

当前运行镜像使用 distroless，不包含 `curl` 或 `wget`。生产环境更建议使用外部负载均衡、Prometheus blackbox exporter、Nginx 或云探针访问 `/healthz`。

如果必须使用 Compose 内置 healthcheck，可以改成带 HTTP 客户端工具的运行镜像，例如 `debian:bookworm-slim`，但镜像体积和攻击面会变大。

## 持久化与日志

当前 `storage.driver=memory`，容器重启会丢失消息记录。生产版本建议扩展：

- SQLite：适合单机小规模部署。
- PostgreSQL/MySQL：适合多实例和长期运营。
- Redis/队列：适合重试、限速和异步派发。

日志输出到 stdout，可用 Docker 标准日志查看：

```powershell
docker compose logs -f mysmpp
```

## 常见问题

### 页面打不开

确认容器运行：

```powershell
docker compose ps
```

确认端口映射没有被占用：

```powershell
docker compose logs mysmpp
```

### SMPP 连不上

检查：

- 宿主机是否开放 `2775`。
- 客户端 bind 的 `system_id` / `password` 是否存在于 `esmes`。
- 如果没有配置 `esmes`，再检查旧兼容字段 `smpp.system_id` 和 `smpp.password` 是否和客户端一致。
- 客户端 bind 类型是否为 receiver、transmitter 或 transceiver。

### submit 成功但没有 DLR

检查：

- 客户端提交 `submit_sm` 时是否设置了 `registered_delivery`。
- 客户端 bind 类型是否能接收下行 PDU，建议使用 `bind_transceiver`。
- ESME 连接是否在 mock provider 回执返回前断开。
- 日志中是否有 `submit_sm` 和 `push dlr`。

### 修改配置后重启丢失

配置页面只改运行时内存配置。需要把最终配置保存到挂载的 JSON 文件，或者在后续版本增加“保存到文件”的能力。
