# SMPP 支持矩阵

## 服务端命令

| 命令 | 支持 | 说明 |
|---|---:|---|
| `bind_receiver` | 是 | 支持 bind 和会话数限制 |
| `bind_transmitter` | 是 | 支持提交 MT |
| `bind_transceiver` | 是 | 推荐测试模式 |
| `submit_sm` | 是 | 支持 GSM-7/ASCII、UCS-2、UDH/SAR 识别 |
| `deliver_sm` | 是 | 用于向 SMPP 下游推送 DLR |
| `deliver_sm_resp` | 是 | 接收并记录 debug |
| `enquire_link` | 是 | 支持请求和响应 |
| `unbind` | 是 | 返回 `unbind_resp` |
| `query_sm` | 否 | 当前未实现业务查询 |
| `outbind` | 否 | 当前未实现 |

## 协议限制

| 字段 | 限制 |
|---|---:|
| `system_id` | 最多 15 字节 |
| `password` | 最多 8 字节 |
| `system_type` | 最多 12 字节 |
| address | 最多 20 字节 |
| PDU length | 最大 1 MiB |

## submit_sm

| 能力 | 支持 | 说明 |
|---|---:|---|
| `short_message` | 是 | 最大 254 字节由客户端控制 |
| `message_payload` TLV | 是 | 优先使用 TLV payload |
| UDH concat | 是 | 下游解析；SMPP 上游可按 UDH 分段发送 |
| SAR TLV | 是 | 下游解析；SMPP 上游可按 SAR TLV 分段发送 |
| `registered_delivery` | 是 | 非 0 时可回推 DLR |
| data_coding 0 | 是 | ASCII/GSM-7 路径 |
| data_coding 8 | 是 | UCS-2 |

## 上游 SMPP Provider

| 能力 | 支持 | 说明 |
|---|---:|---|
| `bind_transceiver` | 是 | mysmpp 作为 ESME 主动连接上游 SMSC |
| 多 bind | 是 | `providers[].smpp.binds` 控制连接数 |
| submit 窗口 | 是 | `providers[].smpp.window_size` 控制单连接在途窗口 |
| `submit_sm_resp` 对账 | 是 | 按 sequence_id 等待响应并提取 provider message_id |
| 上游 `deliver_sm` DLR | 是 | 解析 receipt text、`receipted_message_id` TLV、`message_state` TLV |
| message_id 归一化 | 是 | 支持 `auto`、`dec`、`hex` |
| 长短信上游策略 | 是 | `udh`、`payload`、`sar` |
| `enquire_link` | 是 | 主动心跳并响应上游心跳 |
| 断线重连 | 是 | `reconnect_min` / `reconnect_max` 指数退避 |
| `bind_transmitter` + `bind_receiver` 分离 | 否 | `tx_rx` 配置当前会被拒绝 |
| SMPP over TLS | 否 | `tls=true` 当前会被拒绝 |
| MO 路由到下游 | 否 | 当前只为后续保留 |
