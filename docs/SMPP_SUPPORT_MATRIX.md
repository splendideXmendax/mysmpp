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
| UDH concat | 解析 | 当前解析并记录，HTTP 上游仍发送完整文本 |
| SAR TLV | 解析 | 当前解析并记录 |
| `registered_delivery` | 是 | 非 0 时可回推 DLR |
| data_coding 0 | 是 | ASCII/GSM-7 路径 |
| data_coding 8 | 是 | UCS-2 |
