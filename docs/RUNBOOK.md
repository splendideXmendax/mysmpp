# 操作 Runbook

## 日常检查

```bash
curl http://127.0.0.1:19087/healthz
```

重点看：

| 指标 | 正常 | 异常含义 |
|---|---|---|
| `status` | `ok` | `degraded` 表示 outbox 积压，`unhealthy` 表示存储或队列检查失败 |
| `checks.storage` | `ok` | 数据库或文件存储不可用 |
| `checks.outbox_depth` | 接近 0 | 上游发送失败、provider 慢、dispatcher 并发不足 |
| `checks.pending_size` | 随 DLR 消费下降 | 上游 DLR 未回调或回调失败 |

## 常见告警处理

### outbox_depth 持续增长

1. 检查 provider 是否可达。
2. 检查 `providers[].endpoint` 是否写错 IP/端口/path。
3. 检查 provider 返回是否 4xx/5xx。
4. 检查 `dispatcher.workers * per_worker_concurrency` 是否低于目标吞吐。
5. Postgres 模式检查连接池和慢查询。

### pending_size 持续增长

1. 检查上游是否真的回调 DLR。
2. 检查 `inbound[].path` 和 provider 回调 URL 是否一致。
3. 检查 `auth_header` / `auth_token` 是否一致。
4. 检查 `outbound.response.id_path` 提取出的 provider_id 是否和 DLR `message_id` 一致。
5. 检查 `pending_ttl` 是否短于 DLR 延迟。

如果上游是 SMPP provider：

1. 检查上游是否真的下发 `deliver_sm` DLR。
2. 检查 `providers[].smpp.message_id_resp_format` 和 `message_id_dlr_format` 是否匹配上游进制。
3. 检查 `providers[].smpp.dlr_id_source`，必要时固定为 `tlv` 或 `text`。
4. 中继场景建议 `dispatcher.pending_ttl=48h`。

### HTTP 提交返回 401

`clients` 非空时使用：

```bash
-H 'X-Client-ID: ...' -H 'X-Token: ...'
```

`clients` 为空时使用：

```bash
-u admin:<admin.password>
```

### SMPP bind failed

下游 ESME bind mysmpp 失败：

1. `--system-id` 必须等于 `esmes[].system_id`。
2. `--password` 必须等于 `esmes[].password`。
3. SMPP password 最多 8 字节。
4. 检查 `max_sessions_per_system_id` 是否已满。

上游 SMPP provider bind SMSC 失败：

1. `providers[].endpoint` 必须是上游 `host:port`。
2. `providers[].system_id` / `password` 必须等于上游分配值。
3. `providers[].password` 最多 8 字节，`providers[].smpp.system_type` 最多 12 字节。
4. 当前只支持 `providers[].smpp.bind_mode=transceiver`。

### SMPP 上游 submit 超时

1. 检查上游是否已 bind 成功。
2. 检查 `providers[].smpp.window_size` 是否过小。
3. 检查 `providers[].smpp.response_timeout_ms` 是否小于上游 RTT。
4. 检查上游是否返回 `submit_sm_resp`。
5. 默认 `retry_on_timeout=false`，超时会按永久失败处理，避免重复下发。

### SMPP 上游 DLR 找不到 pending

1. 查消息里的 `ProviderID`。
2. 对比上游 DLR receipt text 的 `id:` 或 `receipted_message_id` TLV。
3. 如果 submit_sm_resp 是 `0000004F`，DLR 是 `79`，配置：

```json
{
  "message_id_resp_format": "hex",
  "message_id_dlr_format": "dec"
}
```

4. 如果上游同时给 TLV 和文本 ID，必要时设置 `dlr_id_source` 为 `tlv` 或 `text`。

## 重启顺序

推荐：

1. 停止接入流量或从负载均衡摘除实例。
2. 发送 SIGTERM。
3. 等 HTTP graceful shutdown 完成。
4. dispatcher 停止，等待 in-flight worker 完成。
5. 关闭 provider registry 和存储连接。

当前程序已按 HTTP shutdown 后再关闭 dispatcher 的顺序执行，避免停机期间 HTTP submit 入队后没有 worker 消费。

## 故障切换

单机 memory/file 模式没有跨节点状态共享，不建议做热切。

Postgres 模式建议：

1. 所有实例使用同一个 Postgres。
2. 外部负载均衡只把流量打到健康实例。
3. 故障实例摘除后，新实例可继续 claim pending outbox。
4. DLR 回调 URL 指向负载均衡地址，而不是单实例地址。
