# mysmpp 管理后台

`/admin/` 是按 `mysmpp改造方案.md` 落地的服务端渲染后台。它不使用 Vue、React 或其他前端框架，只依赖 Go 标准库的 `net/http`、`html/template` 和普通 HTML 表单。

## 入口

```text
http://127.0.0.1:8080/admin/
```

登录凭据来自配置：

```json
"admin": {
  "username": "admin",
  "password": "change-me"
}
```

## 已实现页面

- 概览：显示 routes、providers、ESMEs、inbound、outbound、clients 数量。
- 线路：支持新建、编辑、删除 routes。
- 上游供应商、下游 ESME、入站规则、出站规则、风控、SMPP：按分区 JSON 表单编辑。
- 原始 JSON：编辑完整配置。

保存动作都会先执行完整配置校验和运行时热更新，再通过临时文件 + rename 原子写回 `-config` 指定的配置文件。未指定 `-config` 时只更新运行时配置。

## 安全行为

- Session token 使用 32 字节随机数，保存在内存中。
- Cookie 设置 `HttpOnly`、`SameSite=Strict`，HTTPS 下自动带 `Secure`。
- 所有非 GET 表单都校验 CSRF token。
- 登录失败按 IP 限制为 15 分钟 5 次。
- 登录错误统一显示“用户名或密码错误”。

## 与旧页面关系

旧 `/ui/config` 仍保留为应急入口，但它只更新运行时配置，不会写回配置文件。日常配置修改建议使用 `/admin/`。
