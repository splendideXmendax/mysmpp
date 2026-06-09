# Docker 快速开始

这份文档只保留最短路径: 拉代码、启动、拿密码、进后台、跑 HTTP 和 SMPP 测试。

## 1. 启动

```bash
cd /path/to/mysmpp
docker compose up -d --build
```

确认:

```bash
docker compose ps
curl http://127.0.0.1:19087/healthz
```

预期:

```json
{"status":"ok"}
```

## 2. 拿初始密码

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

注意: 容器是 distroless，没有 `cat`，所以不要用 `docker compose exec mysmpp cat ...`。

## 3. 登录后台

```text
http://服务器IP:19087/admin/
```

账号:

```text
admin
```

密码:

```text
credentials.txt 中的 admin.password
```

## 4. HTTP 提交一条短信

Docker 默认配置没有 HTTP client token，此时使用 admin Basic Auth 提交:

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -u admin:'<credentials.txt 中的 admin.password>' \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"hello docker"}'
```

查询:

```bash
curl -sS -u admin:'<credentials.txt 中的 admin.password>' 'http://127.0.0.1:19087/v1/messages?limit=10&offset=0'
```

如果你在后台配置了 HTTP client，就要加:

```bash
curl -sS -X POST http://127.0.0.1:19087/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Client-ID: api-client-a' \
  -H 'X-Token: client-token-123' \
  -d '{"from":"10690000","to":"13800138000","text":"hello auth"}'
```

## 5. SMPP submit_sm 和 DLR 测试

从 `credentials.txt` 找到 `esmes.dev-esme.password`，然后:

```bash
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u dev-esme \
  -p '<esme-password>' \
  -text 'hello smpp'
```

预期:

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 ... stat:DELIVRD ...
```

## 6. 配置真实上游

进入后台:

```text
/admin/
```

修改这三块:

1. `outbound`: 写上游 HTTP 字段映射。
2. `providers`: 写上游 endpoint、rule、timeout、限速。
3. `routes`: 把默认 route 指向真实 provider。

最小示例:

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
        "Authorization": "Bearer PROVIDER_TOKEN"
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
      "http_timeout_ms": 3000
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

## 7. 停止和重启

```bash
docker compose restart mysmpp
docker compose down
```

清空所有数据重来:

```bash
docker compose down -v
docker compose up -d --build
```

更多完整说明见 [DEPLOYMENT_EXAMPLE.md](DEPLOYMENT_EXAMPLE.md)。
