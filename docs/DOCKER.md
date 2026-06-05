# Docker 部署说明

本文说明如何用 Docker 或 Docker Compose 运行 `mysmpp`。

## 镜像特点

- 构建阶段使用 Go 镜像。
- 运行阶段使用 distroless 镜像。
- 运行镜像里没有 `sh`、`cat`、`curl` 等工具。
- 默认入口:

```text
/app/mysmpp -config /app/data/config.json
```

- 首次启动会从 `/app/configs/docker.json` 生成 `/app/data/config.json`。
- 运行数据持久化到 `/app/data`。

## Compose 启动

```bash
docker compose up -d --build
```

查看状态:

```bash
docker compose ps
```

查看日志:

```bash
docker compose logs -f mysmpp
```

停止:

```bash
docker compose down
```

重启:

```bash
docker compose restart mysmpp
```

清空数据重新初始化:

```bash
docker compose down -v
docker compose up -d --build
```

注意: `down -v` 会删除 named volume 中的配置、密码和 file store 数据。

## 端口

`docker-compose.yml` 默认映射:

```yaml
ports:
  - "19087:19087"
  - "29175:29175"
```

用途:

| 端口 | 用途 |
|---:|---|
| `19087` | HTTP API、`/admin/`、`/healthz` |
| `29175` | SMPP TCP |

访问后台:

```text
http://服务器IP:19087/admin/
```

健康检查:

```bash
curl http://127.0.0.1:19087/healthz
```

## 首次密码

首次启动会随机生成密码，并写入容器数据目录:

```text
/app/data/credentials.txt
```

由于运行镜像没有 `cat`，请这样查看:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

运行时配置也可以复制出来:

```bash
docker cp mysmpp:/app/data/config.json ./config.running.json
cat ./config.running.json
```

## 数据持久化

Compose 使用 named volume:

```yaml
volumes:
  - mysmpp-data:/app/data
```

里面包含:

| 文件 | 说明 |
|---|---|
| `/app/data/config.json` | 运行时配置 |
| `/app/data/credentials.txt` | 首次生成的密码备份 |
| `/app/data/store.json` | file store 数据快照 |

## 裸 Docker 启动

构建:

```bash
docker build -t mysmpp:local .
```

运行:

```bash
docker run -d --name mysmpp \
  -p 19087:19087 \
  -p 29175:29175 \
  -v mysmpp-data:/app/data \
  mysmpp:local
```

查看日志:

```bash
docker logs -f mysmpp
```

删除:

```bash
docker rm -f mysmpp
```

## 配置来源

镜像内置种子配置:

```text
/app/configs/docker.json
```

首次启动时，如果 `/app/data/config.json` 不存在，程序会:

1. 复制种子配置到 `/app/data/config.json`。
2. 为 `AUTO_GENERATE_ON_FIRST_RUN` 生成随机密码。
3. 写回 `/app/data/config.json`。
4. 额外写出 `/app/data/credentials.txt`。

后续重启不会重新生成密码。

## 修改配置

推荐用后台:

```text
http://服务器IP:19087/admin/
```

后台保存后会写回:

```text
/app/data/config.json
```

也可以复制出来改，再复制回去:

```bash
docker cp mysmpp:/app/data/config.json ./config.json
vim ./config.json
docker cp ./config.json mysmpp:/app/data/config.json
docker compose restart mysmpp
```

## 生产注意

- Docker 默认 `storage.driver=file`，适合单容器联调。
- 生产建议改成 `postgres`。
- 管理后台暴露公网前必须加 TLS 反向代理。
- 如果用 Nginx/Caddy 反代并启用 HTTP client IP 白名单，需要配置 `trusted_proxies`。
- 多副本部署前需要处理分布式风控和 gateway_id 分配。

完整生产实例见 [DEPLOYMENT_EXAMPLE.md](DEPLOYMENT_EXAMPLE.md)。
