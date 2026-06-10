# mysmpp 部署与配置实战手册

> 目标:照这份文档跑下来,**30 分钟内你就有一个能收 HTTP 提交、能接 SMPP 客户端、能管理后台改配置的短信网关**。
> 适用版本:当前仓库版本及之后。
> 阅读对象:从未接触过这个项目的运维或开发。

---

## 目录

1. [Docker 部署:5 分钟跑起来](#1-docker-部署5-分钟跑起来)
2. [登录管理后台](#2-登录管理后台)
3. [配置上游通道(发出去)](#3-配置上游通道发出去)
4. [配置下游接入(收进来)](#4-配置下游接入收进来)
5. [全部配置字段速查](#5-全部配置字段速查)
6. [演示 → 生产升级路径](#6-演示--生产升级路径)
7. [故障排查](#7-故障排查)
8. [完整生产配置示范](#8-完整生产配置示范)

---

## 1. Docker 部署:5 分钟跑起来

### 1.1 准备

任何能跑 Docker 的 Linux 机器都行。

```bash
# 检查环境
docker --version          # 需要 20+
docker compose version    # 需要 v2+
```

### 1.2 拉代码

```bash
git clone https://github.com/splendideXmendax/mysmpp.git
cd mysmpp
```

### 1.3 启动

```bash
docker compose up -d --build
```

第一次会拉 Go 镜像 + 编译,大约 1-3 分钟。完成后:

```bash
docker compose ps
```

应该看到:

```text
NAME      IMAGE          STATUS          PORTS
mysmpp    mysmpp:local   Up X seconds    0.0.0.0:19087->19087/tcp, 0.0.0.0:29175->29175/tcp
```

两个端口:
- **19087**:HTTP API + 管理后台
- **29175**:SMPP TCP

### 1.4 拿首次随机密码

容器是 distroless,**没有 `cat`、`sh`**,必须从外面拷:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

你会看到类似:

```text
# mysmpp generated credentials
# Generated once on first startup. Keep this file private.
generated_at=2026-06-09T08:00:00Z
admin.username=admin
admin.password=Xy9k_lQwert-zPnvBcD8E
smpp.system_id=mysmpp-dev
smpp.password=Ab9Xy2K_
esmes.dev-esme.password=Pz8Yw4L_
```

**记好这三个**:
- `admin.password` — 登录后台用
- `esmes.dev-esme.password` — SMPP 客户端 bind 用(8 字符)
- `smpp.system_id` — 服务端的标识(出现在 bind 响应里)

⚠️ 这些密码**只生成一次**。如果删除容器卷重新启动会重新生成。

### 1.5 验证

```bash
curl http://127.0.0.1:19087/healthz
```

预期返回:

```json
{"status":"ok","checks":{"outbox_depth":0,"pending_size":0,"smpp_listener":"ok","storage":"ok"}}
```

`status=ok` 就成功了。

---

## 2. 登录管理后台

### 2.1 打开页面

浏览器访问:

```text
http://<服务器IP>:19087/admin/
```

本机就是 `http://127.0.0.1:19087/admin/`。

### 2.2 登录

- 用户名:`admin`
- 密码:`credentials.txt` 里的 `admin.password`

登录失败 5 次后该 IP 会被锁 15 分钟。

### 2.3 后台页面结构

登录后左侧导航有 9 个入口:

| 菜单 | 作用 | 你什么时候用 |
|---|---|---|
| **概览** | 显示路由 / Provider / ESME 等数量 | 看快速指标 |
| **路由管理** | 增删改路由 | 决定"号码走哪个上游" |
| **上游 Provider** | JSON 编辑上游通道 | 接入新的短信供应商 |
| **下游 ESME** | JSON 编辑 SMPP 客户端账号 | 给客户开 SMPP 账号 |
| **HTTP 客户端** | JSON 编辑 HTTP 客户端 | 给客户开 API 账号 |
| **入站规则** | JSON 编辑入站 HTTP | 接收上游 DLR 或下游回调 |
| **出站规则** | JSON 编辑出站 HTTP 模板 | 适配上游字段格式 |
| **风控** | 黑名单 + 限流 | 防滥用 |
| **SMPP** | SMPP 服务端参数 | 调整最大会话、窗口 |
| **原始 JSON** | 一次性改完整配置 | 高级用户、批量改 |

**所有保存操作**都会:
1. 完整校验配置(任何一项失败就拒绝整个保存)
2. 热更新运行时(新连接立即生效)
3. 原子写回配置文件(`/app/data/config.json` in Docker)
4. CSRF token 校验

### 2.4 改 admin 自己的密码

进入"原始 JSON",找到:

```json
"admin": {
  "username": "admin",
  "password": "Xy9k_..."
}
```

改完保存。**重要**:占位符 `CHANGE_ME_BEFORE_DEPLOY` 和 `AUTO_GENERATE_ON_FIRST_RUN` 在运行时会被拒绝,只能填真实密码。

---

## 3. 配置上游通道(发出去)

"上游"=短信供应商。mysmpp 把消息发给他们,他们再下发到手机。

需要配两样东西:

1. **Outbound Rule**(出站规则):告诉网关"调用上游时 HTTP 请求长什么样"
2. **Provider**(上游通道):指向 outbound rule,设置 endpoint、限速、超时

最后用 **Route**(路由)把"号码前缀"和"provider"关联起来。

### 3.1 上游接口约定示例

假设你用的供应商接口长这样:

```http
POST https://sms-provider.example.com/api/send
Content-Type: application/json
Authorization: Bearer YOUR_API_TOKEN

{
  "mobile": "13800138000",
  "content": "您的验证码是 123456",
  "sender": "10690000",
  "requestId": "g0000000001"
}
```

成功响应:

```json
{
  "code": 0,
  "data": {
    "messageId": "provider-uuid-abc",
    "status": "accepted"
  }
}
```

### 3.2 配 Outbound Rule(出站请求模板)

后台 → 出站规则 → 编辑 JSON:

```json
[
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
      "Authorization": "Bearer YOUR_API_TOKEN",
      "X-Request-ID": "{{id}}"
    },
    "response": {
      "id_path": "data.messageId"
    }
  }
]
```

**关键概念**:

- `fields` 是**字段映射**。**左边**是上游接口要的字段名(`mobile`、`content`...),**右边**是网关内部字段名。
- 内部字段固定 5 个:`id`(gateway_id)、`from`、`to`、`text`、`encoding`。其他名字会从消息 metadata 取(POST `/v1/messages` 里的 `meta`)。
- `headers` 里 `{{id}}` 是模板,会被替换成实际 gateway_id。
- `response.id_path` 是 JSON 路径,告诉网关从响应里抠出供应商的消息 ID(后面对账 DLR 用)。`data.messageId` 表示 `response.data.messageId`。数组用 `data.0.messageId`。
- **没命中 `id_path` 会 fallback 到 gateway_id**(不会乱用整个 body)。

如果上游用 form 表单(很多老接口这样):

```json
{
  "name": "provider-b-form",
  "method": "POST",
  "content_type": "application/x-www-form-urlencoded",
  "fields": {
    "account": "10690000",
    "mobile": "to",
    "msg": "text",
    "src": "from"
  },
  "response": {
    "id_regex": "MsgID:\\s*([A-Za-z0-9_-]+)"
  }
}
```

注意:
- `account` 右边写**死值**也行,字段值是字符串字面量(只有 `id/from/to/text/encoding` 5 个是变量)
- 如果上游返回的是纯文本(不是 JSON),用 `id_regex` 抓 ID
- 默认 `method=POST` `content_type=application/x-www-form-urlencoded` 可以省略

### 3.3 配 Provider(上游通道)

后台 → 上游 Provider → 编辑 JSON:

```json
[
  {
    "name": "provider-a",
    "protocol": "http",
    "endpoint": "https://sms-provider.example.com/api/send",
    "rule": "provider-a-json",
    "enabled": true,
    "http_timeout_ms": 3000,
    "rate_limit": {
      "tps": 50,
      "burst": 100,
      "timeout_ms": 2000
    }
  }
]
```

**字段含义**:

| 字段 | 含义 | 推荐值 |
|---|---|---|
| `name` | 唯一标识,Route 用它引用 | 字母数字下划线 |
| `protocol` | `http`、`https`、`mock`(测试) | `http` |
| `endpoint` | 真实供应商 URL | 完整 URL |
| `rule` | 关联的 outbound 规则名 | 上一步建的 name |
| `enabled` | 是否启用 | `true` |
| `http_timeout_ms` | 单次 HTTP 请求超时 | 3000(3 秒) |
| `rate_limit.tps` | 每秒允许多少次请求(0 = 不限) | 按供应商签约配,如 50 |
| `rate_limit.burst` | 令牌桶突发容量 | 一般 = tps 的 2 倍 |
| `rate_limit.timeout_ms` | 等令牌的最长时间 | 2000 |

### 3.4 配 Route(路由)

后台 → 路由管理 → 新建:

```text
路由名:china-mobile
号段前缀(每行一个):
  134
  135
  136
  137
  138
  139
上游供应商:provider-a (从下拉选)
优先级:100
```

或者用原始 JSON:

```json
[
  {
    "name": "china-mobile",
    "prefix": ["134", "135", "136", "137", "138", "139"],
    "provider": "provider-a",
    "priority": 100
  },
  {
    "name": "default",
    "prefix": [],
    "provider": "provider-a",
    "priority": 1
  }
]
```

**匹配规则**:

1. 只对 `enabled=true` 的 provider 路由生效
2. `priority` 越大越优先
3. 同 priority 下,**前缀越长越优先**(如 `13800` 胜过 `138`)
4. `prefix: []` 是兜底默认路由
5. 前缀只能用 `0-9 + * #`
6. **前缀之间不能互相"包含"**——如果一个路由有 `138`,另一个有 `13800`,保存会拒绝,要求你合并

### 3.5 验证上游

```bash
curl -X POST http://127.0.0.1:19087/v1/messages \
  -u admin:<admin.password> \
  -H 'Content-Type: application/json' \
  -d '{"from":"10690000","to":"13800138000","text":"hello"}'
```

返回:

```json
{
  "gateway_id": "g0000000001",
  "provider": "provider-a",
  "route": "china-mobile",
  "state": "queued"
}
```

`route=china-mobile` 说明路由命中了。

### 3.6 配置上游 DLR 回调

供应商发完短信后会回调一个 URL 告诉你结果。你需要在 mysmpp 开一个 inbound 路径接收。

后台 → 入站规则 → 编辑 JSON:

```json
[
  {
    "name": "provider-a-dlr",
    "method": "POST",
    "path": "/callback/provider-a/dlr",
    "provider": "provider-a",
    "content_type": "application/json",
    "auth_header": "X-Callback-Token",
    "auth_token": "long-random-token-min-24-chars-aJk9Lm",
    "fields": {
      "provider_id": "messageId",
      "status": "status",
      "error_code": "errorCode"
    },
    "success_status": 200,
    "success_body": "{\"ok\":true}"
  }
]
```

然后给供应商配置回调 URL:

```text
URL: https://<你的域名>/callback/provider-a/dlr
请求头:X-Callback-Token: long-random-token-min-24-chars-aJk9Lm
方法:POST
JSON 字段:
  messageId    短信 ID(必须等于他们 send 接口返回给我们的 ID)
  status       状态(DELIVRD / UNDELIV / EXPIRED / REJECTD ...)
  errorCode    错误码
```

**关键字段**:

| 字段 | 必需 | 含义 |
|---|---|---|
| `path` | 是 | URL 路径,必须 `/` 开头,**不能**用 `/v1/...`、`/healthz`、`/admin/...` 这些保留路径 |
| `provider` | DLR 必填 | 这个回调属于哪个 provider,用来防止 A 的回调骗 B 的消息 |
| `auth_header` | 是 | 鉴权头名 |
| `auth_token` | 是 | 鉴权头值,建议 ≥ 24 字符随机串 |
| `fields.provider_id` | DLR 必填 | 上游回调里**哪个字段**是消息 ID。**必须配合 status** |
| `fields.status` | DLR 必填 | DLR 状态字段 |
| `fields.error_code` | 可选 | DLR 错误码字段 |

**状态字符串约定**(用标准 SMPP 词):

```text
DELIVRD  已送达
EXPIRED  超时
DELETED  已删
UNDELIV  未送达
ACCEPTD  已接受
UNKNOWN  未知
REJECTD  拒绝
```

供应商返回的别的字符串会被映射成 `UNKNOWN`。

---

## 4. 配置下游接入(收进来)

"下游"=你的客户。他们把消息提交给 mysmpp。两种方式:**HTTP** 或 **SMPP**。

### 4.1 给客户开 HTTP 账号

后台 → HTTP 客户端 → 编辑 JSON:

```json
[
  {
    "client_id": "api-client-bank-a",
    "token": "long-random-token-min-24-chars-Xy9Zm2",
    "enabled": true,
    "allowed_ips": ["203.0.113.10/32", "203.0.113.11/32"]
  }
]
```

**字段含义**:

| 字段 | 含义 |
|---|---|
| `client_id` | 客户唯一标识,出现在请求头 `X-Client-ID` |
| `token` | 鉴权 token,出现在请求头 `X-Token`,建议 ≥ 24 字符 |
| `enabled` | 禁用某客户时设 `false`(请求会被拒绝) |
| `allowed_ips` | IP/CIDR 列表。**留空表示不限**;有内容时只允许命中的 IP |

### 4.2 客户怎么调

```bash
curl -X POST https://<你的域名>/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Client-ID: api-client-bank-a' \
  -H 'X-Token: long-random-token-min-24-chars-Xy9Zm2' \
  -d '{
    "from": "10690000",
    "to": "13800138000",
    "text": "您的验证码是 123456",
    "client_msg_id": "biz-20260609-0001"
  }'
```

**请求体字段**:

| 字段 | 必填 | 约束 | 含义 |
|---|---|---|---|
| `from` | 是 | 1-32 字符 | 主叫 / 签名号 |
| `to` | 是 | 11 位数字或 E.164(`+...`,8-16 位) | 被叫 |
| `text` | 是 | 1-1000 字符 | 短信内容 |
| `client_msg_id` | 否 | 1-64 非空白字符 | 客户端幂等键。**同一 client_id + 同一 client_msg_id 重复提交返回相同 gateway_id**,24 小时内 |
| `callback_url` | 否 | 必须 `https://` | 当前版本仅校验,**不主动回调**(路线图功能) |
| `meta` | 否 | 最多 10 键,值 ≤ 200 字符 | 客户业务元数据,可被 outbound rule 引用 |

**响应**:

```json
{
  "gateway_id": "g0000000001",
  "provider_id": "",
  "provider": "provider-a",
  "route": "china-mobile",
  "state": "queued"
}
```

**HTTP 状态码**:

| 状态 | 含义 |
|---|---|
| 202 Accepted | 提交成功,已入队 |
| 400 Bad Request | 参数错(看响应文本) |
| 401 Unauthorized | client_id/token 错或缺失 |
| 403 Forbidden | IP 不在白名单 |
| 429 Too Many Requests | 风控拦截(看响应文本判断原因) |
| 502 Bad Gateway | 无匹配路由 / 入队失败 |

### 4.3 给客户开 SMPP 账号

后台 → 下游 ESME → 编辑 JSON:

```json
[
  {
    "system_id": "client-bank-a",
    "password": "Ab9Xy2K_"
  }
]
```

**关键限制**(SMPP v3.4 协议硬限制,**违反直接 bind 拒绝**):

| 字段 | 限制 |
|---|---|
| `system_id` | 最多 15 字符 |
| `password` | **最多 8 字符** |

⚠️ 这是协议规定,不是 mysmpp 的 bug。超过 8 字符会返回 `0x0E ESME_RINVPASWD`,客户端连不上。

### 4.4 客户的 SMPP 客户端怎么配

把这些参数给客户:

```text
Host:              <你的服务器 IP 或域名>
Port:              29175
Bind type:         transceiver(推荐) 或 transmitter+receiver
System ID:         client-bank-a
Password:          Ab9Xy2K_
System Type:       可空,或 ≤ 12 字符
Enquire link:      30 秒(必须 < 服务端 2×enquire_period = 60s)
Window size:       ≤ 100(看你 SMPP 配置的 window_size)
```

提交 submit_sm:

```text
source_addr:         主叫(最多 20 字符)
destination_addr:    被叫(最多 20 字符)
data_coding:         0 = GSM-7/ASCII,8 = UCS-2(中文必须 8)
short_message:       消息体,≤ 254 字节;超过用 message_payload TLV
registered_delivery: 1 = 想收 DLR,0 = 不收
```

DLR 通过 `deliver_sm` PDU 回推,客户端必须能处理 `deliver_sm` 并回 `deliver_sm_resp`。

### 4.5 测一下 SMPP

mysmpp 自带一个测试客户端:

```bash
# 容器内不能 go run,在宿主机或另一台 Linux 上:
git clone https://github.com/splendideXmendax/mysmpp.git
cd mysmpp
go run ./cmd/testesme \
  -addr 127.0.0.1:29175 \
  -u client-bank-a \
  -p Ab9Xy2K_ \
  -src 10690000 \
  -dst 13800138000 \
  -text "hello smpp" \
  -wait 15s
```

预期输出:

```text
bound. sending...
submitted msg_id=g0000000001
[DLR] 13800138000 -> 10690000 : id:g0000000001 sub:001 dlvrd:001 submit date:2606091200 done date:2606091200 stat:DELIVRD err:000 text:hello smpp
```

看到 `[DLR] ... stat:DELIVRD` 就说明 SMPP 上下游闭环跑通了。

---

## 5. 全部配置字段速查

下面是完整 JSON 结构,每个字段都标注了含义、默认值、限制。

### 5.1 `server`(HTTP 服务)

```json
"server": {
  "http_addr": "0.0.0.0:19087",
  "shutdown_timeout": "10s"
}
```

| 字段 | 默认 | 含义 |
|---|---|---|
| `http_addr` | `127.0.0.1:19087` | HTTP API + 管理后台监听地址。容器内必须 `0.0.0.0:xxxx` |
| `shutdown_timeout` | `10s` | 收到 SIGTERM 后等待 in-flight 请求的最长时间 |

### 5.2 `smpp`(SMPP 服务)

```json
"smpp": {
  "addr": "0.0.0.0:29175",
  "system_id": "mysmpp-prod",
  "password": "reserved",
  "system_type": "gateway",
  "max_sessions": 256,
  "max_sessions_per_system_id": 8,
  "window_size": 100,
  "enquire_period": "30s"
}
```

| 字段 | 默认 | 含义 |
|---|---|---|
| `addr` | `127.0.0.1:29175` | SMPP TCP 监听地址 |
| `system_id` | `mysmpp` | 网关自己的标识,会在 bind 响应里告诉 ESME。**≤ 15 字符** |
| `password` | 必填 | 兼容字段,实际登录用 `esmes[]`。**≤ 8 字符** |
| `system_type` | `gateway` | 网关类型字段。**≤ 12 字符** |
| `max_sessions` | 128 | 总会话数上限,超过则新连接被拒 |
| `max_sessions_per_system_id` | 4 | 单个客户最多同时 bind 几个会话 |
| `window_size` | 16 | 单会话内未响应的 submit_sm 上限。生产建议 ≥ 100 |
| `enquire_period` | `30s` | 心跳周期。**`2 × 该值`内**没有任何 PDU 来回,会被踢 |

### 5.3 `dispatcher`(投递引擎)

```json
"dispatcher": {
  "workers": 10,
  "per_worker_concurrency": 10,
  "claim_limit": 20,
  "poll_interval_ms": 20,
  "pending_ttl": "30m",
  "max_attempts": 5
}
```

| 字段 | 默认 | 含义 |
|---|---|---|
| `workers` | 10 | outbox 消费 worker 数 |
| `per_worker_concurrency` | 10 | 单个 worker 内的并发数。总并发 = `workers × per_worker_concurrency` |
| `claim_limit` | 20 | 单次从 outbox 拿多少条 |
| `poll_interval_ms` | 20 | 轮询 outbox 间隔(毫秒) |
| `pending_ttl` | `30m` | pending DLR 记录的过期时间 |
| `max_attempts` | 5 | 上游失败最多重试几次,之后标 failed |

**并发估算公式**:

```text
所需并发 ≈ 目标 TPS × 上游平均 RTT(秒) × 1.5(余量)
```

例:目标 300 TPS,上游 RTT 200ms → 90 并发,配 `10×10=100` 即可。

### 5.4 `esmes`(下游 SMPP 账号)

```json
"esmes": [
  {
    "system_id": "client-a",
    "password": "Pz8Yw4L_"
  }
]
```

| 字段 | 限制 |
|---|---|
| `system_id` | ≤ 15 字符,必须唯一 |
| `password` | **≤ 8 字符**,SMPP 协议硬限制 |

### 5.5 `routes`(路由表)

```json
"routes": [
  {
    "name": "china-mobile",
    "prefix": ["134", "135", "136", "137", "138", "139"],
    "provider": "provider-a",
    "priority": 100
  }
]
```

| 字段 | 含义 |
|---|---|
| `name` | 路由名,仅 `[A-Za-z0-9][A-Za-z0-9_.-]*` |
| `prefix` | 号码前缀数组。仅 `0-9 + * #`。**空数组 = 兜底** |
| `provider` | 指向 `providers[].name` |
| `priority` | 数字越大越优先 |

### 5.6 `providers`(上游通道)

```json
"providers": [
  {
    "name": "provider-a",
    "protocol": "http",
    "endpoint": "https://api.example.com/send",
    "rule": "provider-a-json",
    "enabled": true,
    "http_timeout_ms": 3000,
    "rate_limit": {
      "tps": 100,
      "burst": 200,
      "timeout_ms": 2000
    }
  }
]
```

| 字段 | 含义 |
|---|---|
| `name` | 通道唯一标识 |
| `protocol` | `http` / `https` / `mock` |
| `endpoint` | 上游 URL(`mock` 时可空) |
| `rule` | 引用 `outbound[].name` |
| `system_id` | 预留字段。部分上游需要账号时可记录账号名,当前 HTTP provider 不自动使用 |
| `password` | 预留字段。部分上游需要密码时可记录密钥,当前 HTTP provider 不自动使用 |
| `enabled` | 禁用某通道设 `false`,路由会跳过 |
| `http_timeout_ms` | 单次请求超时,默认 3000 |
| `rate_limit.tps` | 每秒最多发出次数。`0`=不限 |
| `rate_limit.burst` | 令牌桶突发容量,默认 = tps |
| `rate_limit.timeout_ms` | 等令牌最长时间,默认 2000 |

### 5.7 `outbound`(出站 HTTP 模板)

```json
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
      "Authorization": "Bearer token-xxx",
      "X-Request-ID": "{{id}}"
    },
    "response": {
      "id_path": "data.messageId",
      "id_regex": ""
    }
  }
]
```

| 字段 | 含义 |
|---|---|
| `name` | 模板名 |
| `method` | `GET` / `POST` / `PUT` 等,默认 `POST` |
| `content_type` | `application/json` 或 `application/x-www-form-urlencoded`,默认 form |
| `fields` | **左 = 上游字段名,右 = 内部字段** |
| `headers` | 静态 HTTP 头,值里可用 `{{id}}` `{{from}}` `{{to}}` `{{text}}` `{{encoding}}` |
| `response.id_path` | 从 JSON 响应抠 provider_id 的路径,如 `data.messageId` |
| `response.id_regex` | 用正则从文本响应抠 provider_id |

`outbound` 复用同一个规则结构,但下列字段只用于 inbound,出站规则里通常留空:

| 字段 | 出站规则里的含义 |
|---|---|
| `path` | 不使用 |
| `provider` | 不使用 |
| `auth_header` | 不使用 |
| `auth_token` | 不使用 |
| `success_status` | 不使用 |
| `success_body` | 不使用 |

**内部字段名**(`fields` 右边可填的):

| 内部字段 | 含义 |
|---|---|
| `id` | gateway_id(网关生成) |
| `from` | 主叫 |
| `to` | 被叫 |
| `text` | 内容 |
| `encoding` | `gsm7` / `ucs2` |

不在上面 5 个的字段名,会从消息 `meta` 取值。

### 5.8 `inbound`(入站 HTTP)

两种用途:**接收上游 DLR** 或 **接收 MO/自定义提交**。

**DLR 接收示例**:

```json
{
  "name": "provider-a-dlr",
  "method": "POST",
  "path": "/callback/provider-a/dlr",
  "provider": "provider-a",
  "auth_header": "X-Callback-Token",
  "auth_token": "abc...",
  "fields": {
    "provider_id": "messageId",
    "status": "status",
    "error_code": "errorCode"
  }
}
```

**MO 接收示例**:

```json
{
  "name": "partner-mo",
  "method": "POST",
  "path": "/callback/partner-mo",
  "auth_header": "X-Token",
  "auth_token": "xyz...",
  "fields": {
    "id": "msg_id",
    "from": "src",
    "to": "dst",
    "text": "content"
  },
  "success_status": 200,
  "success_body": "{\"ok\":true}"
}
```

| 字段 | 必需 | 含义 |
|---|---|---|
| `name` | 是 | 规则名 |
| `method` | 否 | 默认 `POST` |
| `path` | 是 | URL 路径,`/` 开头,不能用保留路径 |
| `provider` | DLR 必填 | 用来校验 DLR 归属 |
| `auth_header` | 是 | 鉴权头名 |
| `auth_token` | 是 | 鉴权头值 |
| `fields` | 是 | DLR:必须有 `provider_id`+`status`;MO:必须有 `from`+`to`+`text` |
| `headers` | 否 | 不使用。该字段仅 outbound 会读取 |
| `success_status` | 否 | 成功响应的 HTTP 状态,默认 200 |
| `success_body` | 否 | 成功响应的 body |
| `content_type` | 否 | 如果上游必须 form 而非 JSON,显式指定 |
| `response.id_path` | 否 | 不使用。该字段仅 outbound 会读取 |
| `response.id_regex` | 否 | 不使用。该字段仅 outbound 会读取 |

### 5.9 `clients`(下游 HTTP 账号)

```json
"clients": [
  {
    "client_id": "api-client-a",
    "token": "long-token-xxx",
    "enabled": true,
    "allowed_ips": ["203.0.113.10/32"]
  }
]
```

| 字段 | 含义 |
|---|---|
| `client_id` | 客户标识 |
| `token` | 鉴权 token,**≥ 24 字符** |
| `enabled` | 禁用设 `false` |
| `allowed_ips` | IP/CIDR 白名单。**空数组 = 不限** |

⚠️ **如果 `clients` 为空数组**,`/v1/messages` 退回到 admin Basic Auth(老旧行为)。**生产环境永远不要让 clients 为空**。

### 5.10 `trusted_proxies`(可信反向代理)

```json
"trusted_proxies": ["10.0.0.0/8", "172.16.0.0/12"]
```

**只有直连 IP 命中这里的才信任 `X-Forwarded-For` 和 `X-Real-IP`**。

如果你不通过反向代理(直接暴露 19087 端口),留空数组即可。

⚠️ 如果你套了 nginx/Cloudflare,**必须配上代理 IP/CIDR**,否则:
- 客户 IP 白名单全部失效(全是代理 IP)
- 管理后台登录失败限流变成全局限流

### 5.11 `risk`(风控)

```json
"risk": {
  "blocked_to_prefix": ["100", "110", "120"],
  "blocked_keywords": ["澳门", "赌博", "投资"],
  "per_number_per_minute": 5,
  "per_number_per_day": 20,
  "per_client_per_second": 500
}
```

| 字段 | 含义 |
|---|---|
| `blocked_to_prefix` | 命中前缀直接拦截 |
| `blocked_keywords` | 命中关键词直接拦截(大小写不敏感) |
| `per_number_per_minute` | 单号码每分钟上限 |
| `per_number_per_day` | 单号码每天上限 |
| `per_client_per_second` | 单 HTTP 客户端每秒上限 |

⚠️ 计数器是**进程内 map**,多节点部署时每节点独立计数(限额会被放大 N 倍)。

### 5.12 `storage`(存储)

```json
"storage": {
  "driver": "postgres",
  "dsn": "postgres://mysmpp:PWD@127.0.0.1:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
}
```

| driver | 适用 |
|---|---|
| `memory` | 本机开发,重启丢数据 |
| `file` | ⚠️ 仅功能验证,**不能持续压测**,每次操作整文件写盘 |
| `postgres` | **生产** |

| 字段 | 含义 |
|---|---|
| `driver` | 存储驱动:`memory`、`file`/`json`、`postgres`/`pg` |
| `dsn` | 存储连接串。`postgres` 必填;`file` 当前使用默认数据文件;`memory` 可空 |

Postgres 启动前必须建 schema:

```bash
psql "$DSN" -f migrations/001_init.up.sql
```

### 5.13 `admin`(管理后台凭据)

```json
"admin": {
  "username": "admin",
  "password": "Xy9k_lQwert-zPnvBcD8E"
}
```

| 字段 | 含义 |
|---|---|
| `username` | 管理后台和 admin Basic Auth 用户名 |
| `password` | 管理后台和 admin Basic Auth 密码 |

⚠️ 密码建议 ≥ 16 字符随机串。如果你用 `AUTO_GENERATE_ON_FIRST_RUN`,**只在首次启动时被替换一次**,之后不能再写这个占位符。

---

## 6. 演示 → 生产升级路径

Docker 默认配置只适合**演示**。上量前必须改:

### 6.1 切 Postgres

```bash
# 启 Postgres(本机)
docker run -d --name pg \
  -e POSTGRES_USER=mysmpp \
  -e POSTGRES_PASSWORD=ChangeMe \
  -e POSTGRES_DB=mysmpp \
  -p 5432:5432 \
  postgres:15

# 建 schema
docker cp ./migrations/001_init.up.sql pg:/tmp/init.sql
docker exec pg psql -U mysmpp -d mysmpp -f /tmp/init.sql

# 改 mysmpp 配置
docker cp mysmpp:/app/data/config.json ./config.json
```

编辑 `config.json` 的 storage 段:

```json
"storage": {
  "driver": "postgres",
  "dsn": "postgres://mysmpp:ChangeMe@<宿主机IP或docker网络名>:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
}
```

```bash
docker cp ./config.json mysmpp:/app/data/config.json
docker compose restart mysmpp
```

注意:Docker 网络下,mysmpp 容器访问宿主机的 Postgres 用 `host.docker.internal`(Mac/Windows)或宿主机内网 IP(Linux)。更好的做法是把 Postgres 也加入同一个 compose:

```yaml
# docker-compose.yml 加一个服务
services:
  postgres:
    image: postgres:15
    environment:
      POSTGRES_USER: mysmpp
      POSTGRES_PASSWORD: ChangeMe
      POSTGRES_DB: mysmpp
    volumes:
      - pg-data:/var/lib/postgresql/data
  mysmpp:
    depends_on:
      - postgres
    # ...

volumes:
  pg-data:
```

然后 DSN 写 `postgres://mysmpp:ChangeMe@postgres:5432/mysmpp?...`。

### 6.2 套反向代理 + HTTPS

不要直接把 19087 暴露公网。前面套 nginx/Caddy 做 TLS。

**Caddyfile 示例**(自动 HTTPS):

```text
sms.example.com {
    # 管理后台仅内网
    @admin path /admin/* /ui/*
    handle @admin {
        @internal {
            remote_ip 10.0.0.0/8 172.16.0.0/12
        }
        handle @internal {
            reverse_proxy 127.0.0.1:19087
        }
        respond "Forbidden" 403
    }
    
    # 业务 API
    reverse_proxy 127.0.0.1:19087
}
```

mysmpp 配置同步加:

```json
"trusted_proxies": ["127.0.0.1/32"]
```

### 6.3 调优 dispatcher

按你上游 RTT 调:

```json
"dispatcher": {
  "workers": 10,
  "per_worker_concurrency": 10,
  "claim_limit": 20,
  "poll_interval_ms": 20
}
```

- 上游 RTT 100ms → 5×5 或 10×5
- 上游 RTT 200ms → 10×10(默认)
- 上游 RTT 500ms → 10×25

### 6.4 上量前 checklist

- [ ] Postgres 已切,跑过迁移
- [ ] `admin.password` 长度 ≥ 16
- [ ] `clients[]` 至少一个真客户,token ≥ 24 字符
- [ ] 反向代理 + HTTPS 部署完
- [ ] `trusted_proxies` 正确配置
- [ ] `risk.*` 数值业务确认过
- [ ] `inbound[].auth_token` 长随机串
- [ ] 至少跑过一次 testesme 验证 SMPP 链路
- [ ] 至少跑过一次 curl 验证 HTTP 链路
- [ ] 跑过 `psql -c "SELECT 1"` 验证 DB 连接
- [ ] **不要用 `storage.driver=file`**

---

## 7. 故障排查

### 7.1 健康检查

```bash
curl http://127.0.0.1:19087/healthz
```

| 字段 | 正常 | 异常含义 |
|---|---|---|
| `status` | `ok` | `degraded`=outbox 积压,`unhealthy`=存储或队列故障 |
| `checks.storage` | `ok` | DB 不可达或错误 |
| `checks.outbox_depth` | 接近 0 | 上游发送失败 / 慢 / 并发不足 |
| `checks.pending_size` | 随 DLR 消费下降 | DLR 不回调 / 配置错 |

### 7.2 常见错误

**HTTP 401**:

- `clients` 为空 → 用 `-u admin:PASS` Basic Auth
- `clients` 有配 → 加 `X-Client-ID` + `X-Token` 头
- token 错 → 检查后台 client 配置

**HTTP 403**:

- IP 不在 `allowed_ips`
- 反向代理后忘记配 `trusted_proxies`

**HTTP 429**:

- 看响应文本:`number rate limit exceeded`(单号码超限)、`client rate limit exceeded`(客户超限)、`blocked keyword`、`blocked destination`

**HTTP 502**:

- `no route matched` → 加 default 兜底路由,或检查号码前缀
- `provider X not found` → provider name 写错

**SMPP bind 失败 `0x0E`**:

- 密码超过 8 字符(SMPP 协议硬限制)
- 改短密码

**SMPP bind 失败 `0x0D`**:

- system_id 不存在 / 密码错
- 检查 `esmes[]` 配置

**SMPP submit 失败 `0x58 THROTTLED`**:

- 单会话窗口满
- 客户端等 submit_sm_resp 返回再发下一条,或加大 `smpp.window_size`

**outbox 一直在增长**:

1. `docker compose logs mysmpp` 看上游请求错误
2. 检查 `providers[].endpoint` URL
3. 检查 `outbound[].headers` 鉴权头
4. 提高 `dispatcher.workers` × `per_worker_concurrency`
5. **如果用 file driver,立刻切 postgres**

**pending 一直在增长**:

1. 检查上游真的回调了吗(看日志)
2. 检查 inbound `path` 和上游配置的 URL 一致
3. 检查 `auth_token` 一致
4. 检查 `fields.provider_id` 抠出的 ID 和上游发来的 message_id 一致(用 outbound `response.id_path` 时尤其要小心)

### 7.3 看消息状态

```bash
curl -u admin:PASS 'http://127.0.0.1:19087/v1/messages?limit=20' | python3 -m json.tool
```

字段含义:

| `state` | 含义 |
|---|---|
| `queued` | 已接收,等待发送 |
| `sent` | 已发上游,等 DLR |
| `DELIVRD` | 成功送达 |
| `UNDELIV` | 未送达 |
| `EXPIRED` | 超时 |
| `failed` | 重试用尽 |
| `blocked` | 被风控拦截 |

### 7.4 重启容器

```bash
docker compose restart mysmpp           # 重启
docker compose logs -f mysmpp           # 看实时日志
docker compose down                     # 停止
docker compose down -v                  # 停止 + 删除卷(慎用,会丢配置和密码)
```

### 7.5 改完配置文件后

后台保存会自动热更新。如果你直接改 `/app/data/config.json`:

```bash
docker compose restart mysmpp
```

---

## 8. 完整生产配置示范

照抄改一改就能用。

`production.json`:

```json
{
  "server": {
    "http_addr": "0.0.0.0:19087",
    "shutdown_timeout": "30s"
  },
  "smpp": {
    "addr": "0.0.0.0:29175",
    "system_id": "mysmpp",
    "password": "smpppw1",
    "system_type": "gateway",
    "max_sessions": 256,
    "max_sessions_per_system_id": 8,
    "window_size": 100,
    "enquire_period": "30s"
  },
  "dispatcher": {
    "workers": 10,
    "per_worker_concurrency": 10,
    "claim_limit": 20,
    "poll_interval_ms": 20,
    "pending_ttl": "30m",
    "max_attempts": 5
  },
  "esmes": [
    {
      "system_id": "client-a",
      "password": "Ab9Xy2K_"
    },
    {
      "system_id": "client-b",
      "password": "Pz8Yw4L_"
    }
  ],
  "routes": [
    {
      "name": "china-mobile",
      "prefix": ["134", "135", "136", "137", "138", "139", "147", "150", "151", "152", "157", "158", "159", "172", "178", "182", "183", "184", "187", "188", "198"],
      "provider": "provider-cmcc",
      "priority": 100
    },
    {
      "name": "china-unicom",
      "prefix": ["130", "131", "132", "145", "155", "156", "166", "171", "175", "176", "185", "186", "196"],
      "provider": "provider-cucc",
      "priority": 100
    },
    {
      "name": "china-telecom",
      "prefix": ["133", "149", "153", "173", "174", "177", "180", "181", "189", "190", "191", "193", "199"],
      "provider": "provider-ctcc",
      "priority": 100
    },
    {
      "name": "default",
      "prefix": [],
      "provider": "provider-cmcc",
      "priority": 1
    }
  ],
  "providers": [
    {
      "name": "provider-cmcc",
      "protocol": "http",
      "endpoint": "https://sms-cmcc.example.com/api/send",
      "rule": "provider-cmcc-json",
      "enabled": true,
      "http_timeout_ms": 3000,
      "rate_limit": {
        "tps": 300,
        "burst": 600,
        "timeout_ms": 2000
      }
    },
    {
      "name": "provider-cucc",
      "protocol": "http",
      "endpoint": "https://sms-cucc.example.com/api/send",
      "rule": "provider-cucc-form",
      "enabled": true,
      "http_timeout_ms": 3000,
      "rate_limit": {
        "tps": 200,
        "burst": 400,
        "timeout_ms": 2000
      }
    },
    {
      "name": "provider-ctcc",
      "protocol": "http",
      "endpoint": "https://sms-ctcc.example.com/api/send",
      "rule": "provider-ctcc-json",
      "enabled": true,
      "http_timeout_ms": 3000,
      "rate_limit": {
        "tps": 150,
        "burst": 300,
        "timeout_ms": 2000
      }
    }
  ],
  "outbound": [
    {
      "name": "provider-cmcc-json",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "mobile": "to",
        "content": "text",
        "sender": "from",
        "requestId": "id"
      },
      "headers": {
        "Authorization": "Bearer cmcc-token-replace-me",
        "X-Request-ID": "{{id}}"
      },
      "response": {
        "id_path": "data.messageId"
      }
    },
    {
      "name": "provider-cucc-form",
      "method": "POST",
      "content_type": "application/x-www-form-urlencoded",
      "fields": {
        "account": "your-account",
        "mobile": "to",
        "msg": "text",
        "src": "from"
      },
      "response": {
        "id_regex": "MsgID:\\s*([A-Za-z0-9_-]+)"
      }
    },
    {
      "name": "provider-ctcc-json",
      "method": "POST",
      "content_type": "application/json",
      "fields": {
        "phone": "to",
        "message": "text",
        "from": "from",
        "msgid": "id"
      },
      "headers": {
        "X-Api-Key": "ctcc-key-replace-me"
      },
      "response": {
        "id_path": "messageId"
      }
    }
  ],
  "inbound": [
    {
      "name": "provider-cmcc-dlr",
      "method": "POST",
      "path": "/callback/cmcc/dlr",
      "provider": "provider-cmcc",
      "content_type": "application/json",
      "auth_header": "X-Callback-Token",
      "auth_token": "cmcc-callback-token-replace-me-min-24-chars",
      "fields": {
        "provider_id": "messageId",
        "status": "status",
        "error_code": "errorCode"
      },
      "success_status": 200,
      "success_body": "{\"ok\":true}"
    },
    {
      "name": "provider-cucc-dlr",
      "method": "POST",
      "path": "/callback/cucc/dlr",
      "provider": "provider-cucc",
      "auth_header": "X-Token",
      "auth_token": "cucc-callback-token-replace-me-min-24-chars",
      "fields": {
        "provider_id": "MsgID",
        "status": "Status",
        "error_code": "ErrorCode"
      }
    },
    {
      "name": "provider-ctcc-dlr",
      "method": "POST",
      "path": "/callback/ctcc/dlr",
      "provider": "provider-ctcc",
      "content_type": "application/json",
      "auth_header": "X-Api-Key",
      "auth_token": "ctcc-callback-token-replace-me-min-24-chars",
      "fields": {
        "provider_id": "messageId",
        "status": "status",
        "error_code": "code"
      }
    }
  ],
  "clients": [
    {
      "client_id": "bank-a",
      "token": "bank-a-token-replace-me-min-24-chars-xxxxx",
      "enabled": true,
      "allowed_ips": ["203.0.113.10/32", "203.0.113.11/32"]
    },
    {
      "client_id": "ecommerce-b",
      "token": "ecommerce-b-token-replace-me-min-24-chars-x",
      "enabled": true,
      "allowed_ips": ["198.51.100.0/24"]
    }
  ],
  "trusted_proxies": ["127.0.0.1/32", "10.0.0.0/8"],
  "risk": {
    "blocked_to_prefix": ["100", "110", "120"],
    "blocked_keywords": [],
    "per_number_per_minute": 5,
    "per_number_per_day": 20,
    "per_client_per_second": 500
  },
  "storage": {
    "driver": "postgres",
    "dsn": "postgres://mysmpp:CHANGE_ME@postgres:5432/mysmpp?sslmode=disable&pool_max_conns=50&pool_min_conns=10"
  },
  "admin": {
    "username": "admin",
    "password": "ChangeMeToALongRandomString_X9k2Lm7Pq4Rt8"
  }
}
```

把这份保存为 `production.json`,覆盖到容器的 `/app/data/config.json` 后重启即可:

```bash
docker cp ./production.json mysmpp:/app/data/config.json
docker compose restart mysmpp
```

或者用后台的"原始 JSON"页面粘贴保存——会自动校验后热更新。

---

## 9. 总结一张图

```text
                                    ┌──────────────────────┐
                                    │  上游 SMS 供应商     │
                                    │  (移动/联通/电信)    │
                                    └──────────┬───────────┘
                                               │ provider_a-dlr 回调
                            HTTP POST send     │ /callback/cmcc/dlr
                            (outbound rule)    │
                                               ▼
┌────────────┐ HTTP /v1/messages  ┌─────────────────────────┐
│  HTTP 客户 │ ─────────────────► │                         │
│  bank-a    │                    │       mysmpp 网关       │
└────────────┘  X-Client-ID:xxx   │                         │
                X-Token:yyy       │  ┌───────────────────┐  │
                                  │  │  路由 + 风控       │  │
┌────────────┐ SMPP submit_sm     │  ├───────────────────┤  │
│  SMPP 客户 │ ─────────────────► │  │  outbox 队列       │  │
│  client-a  │ ◄───────────────── │  │  worker × 100      │  │
└────────────┘ SMPP deliver_sm    │  ├───────────────────┤  │
                (DLR 回推)        │  │  pending 映射表    │  │
                                  │  └───────────────────┘  │
                                  │                         │
                                  │  ┌───────────────────┐  │
                                  │  │  Postgres 持久化   │  │
                                  │  └───────────────────┘  │
                                  └─────────────────────────┘
                                           ▲       
                                           │ /admin/
                                  ┌─────────────────┐
                                  │  管理员浏览器    │
                                  └─────────────────┘
```

---

## 10. 一句话总结

**Docker 启动→拷凭据→登后台→配 outbound + provider + route→配 client/esme→压测→切 postgres→上反向代理→上量**。整套流程照本手册走 30 分钟完成。
