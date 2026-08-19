# 升级与迁移指南

## 通用升级步骤

1. 备份配置文件。
2. 备份存储。
3. 阅读新版本 release notes。
4. 先在测试环境运行 `go test ./...` 和 DR 全链路测试。
5. 生产滚动升级时先摘除实例流量，再发送 SIGTERM。

## 配置迁移

### SMPP 密码长度

SMPP v3.4 password 最多 8 字节。旧配置如果使用更长的 `smpp.password` 或 `esmes[].password`，需要改短。

### `AUTO_GENERATE_ON_FIRST_RUN`

`AUTO_GENERATE_ON_FIRST_RUN` 只允许作为 startup bootstrap seed 使用。普通 `Load()` 和运行时配置更新会拒绝该值。

Docker 首次启动仍可使用 `configs/docker.json`，程序会生成真实凭据并写入运行时配置。

### `/v1/messages` 鉴权

旧行为：`clients=[]` 时允许匿名访问。

新行为：`clients=[]` 时必须使用 admin Basic Auth。生产建议显式配置 `clients`。

## 存储迁移

### vNext 租户额度与分片 DLR

升级 PostgreSQL 时，在停止旧版本写入后依次执行：

```bash
psql "$MYSMPP_DSN" -f migrations/006_pending_segments_and_provider_key.up.sql
psql "$MYSMPP_DSN" -f migrations/007_tenant_quota.up.sql
```

`006` 将 pending 主键改为 `(provider, provider_id)` 并增加分片/DLR 投递字段；`007` 增加消息租户字段和 `tenant_quota_usage`。应用程序会在启动时校验这些结构，缺少迁移会拒绝启动，而不会在不完整 schema 上继续运行。

旧配置无需立刻增加 `tenants` 或 `tenant_id`：未绑定账号仍按协议和账号生成独立兼容租户，且默认不限流、不限额。要让 HTTP 与 SMPP 子账号共享额度，必须显式把二者绑定到同一个 `tenants[].tenant_id`。

### memory -> file

无法迁移历史内存数据。停止服务后改：

```json
{
  "storage": {
    "driver": "file",
    "dsn": "data/store.json"
  }
}
```

### file -> postgres

当前没有自动迁移工具。建议：

1. 停止写入。
2. 保留 file JSON 作为审计备份。
3. 初始化 Postgres schema。
4. 切换 `storage.driver=postgres`。

```bash
for migration in migrations/*.up.sql; do
  psql -v ON_ERROR_STOP=1 "$MYSMPP_DSN" -f "$migration" || exit 1
done
```
