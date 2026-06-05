# 管理后台说明

`mysmpp` 的管理后台入口:

```text
http://127.0.0.1:19087/admin/
```

服务器部署时:

```text
http://服务器IP:19087/admin/
```

## 登录账号

账号来自配置:

```json
{
  "admin": {
    "username": "admin",
    "password": "replace-with-password"
  }
}
```

本机开发配置:

```text
admin / mysmpp-admin-19087
```

Docker 首次启动会随机生成密码，查看:

```bash
docker cp mysmpp:/app/data/credentials.txt ./credentials.txt
cat ./credentials.txt
```

## 页面功能

后台当前提供:

- 仪表盘: 显示 routes、providers、ESMEs、clients、inbound、outbound 等数量。
- 线路管理: 新建、编辑、删除 `routes`。
- 上游 provider: JSON 编辑 `providers`。
- 下游 ESME: JSON 编辑 `esmes`。
- HTTP 客户端: JSON 编辑 `clients`。
- 入站规则: JSON 编辑 `inbound`。
- 出站规则: JSON 编辑 `outbound`。
- 风控: JSON 编辑 `risk`。
- SMPP: JSON 编辑 `smpp`。
- 原始 JSON: 一次性编辑完整配置。

保存时会:

1. 校验完整配置。
2. 热更新运行时 provider 和 route。
3. 原子写回启动参数 `-config` 指定的配置文件。

Docker 默认写回:

```text
/app/data/config.json
```

## 安全行为

- 管理后台必须登录，没有本地绕过。
- 用户名和密码使用常量时间比较。
- 登录失败按 IP 限流: 15 分钟内最多 5 次。
- session token 为随机 32 字节。
- Cookie 使用 `HttpOnly` 和 `SameSite=Strict`。
- HTTPS 请求下 Cookie 会自动带 `Secure`。
- 所有非 GET 表单都有 CSRF token。
- `CHANGE_ME_BEFORE_DEPLOY` 占位符会被配置校验拒绝。

## `/ui/config` 和 `/v1/config`

旧入口仍保留:

```text
/ui/config
/v1/config
```

它们也需要 admin Basic Auth。日常建议使用 `/admin/`，因为后台页面更完整，也更适合人工维护。

## 常见问题

### 忘记密码

Docker:

```bash
docker cp mysmpp:/app/data/config.json ./config.json
```

编辑 `admin.password` 后复制回去:

```bash
docker cp ./config.json mysmpp:/app/data/config.json
docker compose restart mysmpp
```

本机运行则直接改 `-config` 指定的 JSON 文件。

### 保存失败

后台保存会先做完整校验。常见原因:

- provider 名重复。
- route 指向不存在的 provider。
- provider 全部 disabled。
- inbound rule 缺少 `auth_header` 或 `auth_token`。
- DLR inbound rule 缺少 `provider_id` / `status` 成对映射。
- 生产模板里还有 `CHANGE_ME_BEFORE_DEPLOY`。
