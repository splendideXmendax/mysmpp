# 安全加固清单

## 必做

- 替换所有 `CHANGE_ME_BEFORE_DEPLOY`。
- 不要把 `AUTO_GENERATE_ON_FIRST_RUN` 用在普通配置加载路径；它只允许 startup bootstrap。
- 生产使用 HTTPS 反向代理暴露 HTTP API 和 admin。
- `/admin/` 不要裸露到公网，至少限制来源 IP。
- 配置 `clients`，不要长期依赖 admin Basic Auth 提交短信。
- 为 `clients[].allowed_ips` 配置下游固定 IP/CIDR。
- 在反向代理后启用 IP 白名单时，正确配置 `trusted_proxies`。
- SMPP `esmes[].password` 必须符合 SMPP v3.4 限制，最多 8 字节。

## 凭据

| 凭据 | 建议 |
|---|---|
| `admin.password` | 长随机字符串 |
| `clients[].token` | 长随机字符串 |
| `inbound[].auth_token` | 长随机字符串 |
| `esmes[].password` | 8 字节以内随机字符串 |

## 网络

- HTTP/API/Admin: 默认 `19087/tcp`。
- SMPP: 默认 `29175/tcp`。
- provider callback 只需要访问 HTTP 端口。
- provider send endpoint 只需要 mysmpp 出站可达。

## 配置校验

程序会拒绝：

- 占位符凭据。
- 普通加载路径中的 auto-generate 占位符。
- 过长 SMPP C-Octet 字段。
- HTTP 和 SMPP 监听同一地址。
- inbound path 覆盖内置路由。
- route prefix 冲突。
