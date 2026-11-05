# NPS 1.1.41 本地升级说明

> 本文对应客户端实例身份、历史用量、HTTP 转发策略和 Bridge 证书热加载版本。
> 正式发布状态以 `lib/version/version.go`、GitHub Release 标签和发布流水线结果为准。

## 功能变化

- 同一 VKey 可同时建立多个 NPC 实例。服务端会记录实例 ID、主机名、操作系统、
  架构、局域网 IP、Bridge 协议和连接时间，客户端详情可查看并单独断开实例。
- 新增 `GET /api/v1/clients/{id}/instances` 和
  `POST /api/v1/clients/{id}/instances/disconnect`。
- 新增历史用量采集和持久化。首页可查看最近 24 小时、7 天和 30 天趋势；
  `GET /api/v1/status/history` 可查询全局、用户、客户端、隧道和域名规则。
  普通用户 Token 只能读取自身授权资源，P2P 直连不计入。
- HTTP(S) 域名代理补齐来源地址和协议请求头、分阶段读写超时、SSE/gRPC/升级连接
  豁免，以及默认关闭的后端连接复用。
- Bridge TLS 证书在页面保存或外部文件原子替换后热加载。无效、缺失或证书与私钥
  不匹配时继续使用最近一次有效运行证书，已有连接不会被主动断开。
- 修复大屏快捷地址和 NPC 更新流程；Windows 系统服务更新后使用实际安装路径，
  不再继续启动桌面上未更新的旧二进制。

## 配置变化

新增配置均有默认值，旧 `nps.conf` 不需要手工补字段：

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `usage_history_enabled` | `true` | 是否采集历史用量 |
| `usage_history_path` | `conf/usage_history.json` | 历史快照文件 |
| `usage_history_interval` | `60` | 采样间隔，单位秒 |
| `usage_history_1m_hours` | `48` | 分钟精度保留时长 |
| `usage_history_5m_days` | `90` | 五分钟精度保留天数 |
| `usage_history_save_every` | `5` | 每采样多少次落盘 |
| `usage_history_max_resources` | `20000` | 历史资源上限 |
| `http_backend_keepalive` | `false` | 是否启用隔离后端连接池 |

HTTP 超时和请求头策略字段已出现在「全局参数 → 服务端配置 → HTTP 转发与缓存」。
所有 `0` 秒超时均表示不限制；生产环境建议保留读写停滞超时，避免断线或长时间
不响应的连接无限占用资源。除非后端明确需要连接复用，否则保持
`http_backend_keepalive=false`。

## 数据与备份

升级不会改写现有 `clients.json`、`tasks.json`、`hosts.json` 或 `users.json`
结构。首次成功采样后会创建 `conf/usage_history.json`，该文件可随时删除以清空
历史趋势，但不会清空实时流量累计和配额计数。

升级前仍建议完整备份 `conf` 目录，尤其是 JSON 数据、证书、`nps.conf` 和日志。

## 升级步骤

1. 备份整个 `conf` 目录。
2. 停止 NPS 和需要同步升级的 NPC。
3. 替换 NPS/NPC 程序并保留原 `conf` 目录和客户端配置。
4. 启动 NPS，登录管理面板。
5. 检查首页“用量趋势”、客户端在线实例、证书状态和 HTTP(S) 域名转发。
6. 如需实例主机名、局域网 IP 和最新更新链路，将 NPC 一并升级到 `1.1.41`。
7. 浏览器保留旧静态资源时强制刷新一次。

## 版本兼容

| 组件 | 兼容范围 |
| --- | --- |
| NPS `1.1.41` + 旧版 NPC | 继续使用既有 Bridge 握手、快捷命令和控制消息 |
| NPS `1.1.41` + NPC `1.1.41` | 支持实例身份、局域网地址和完整更新流程 |
| 旧 `nps.conf` | 缺少新增字段时使用安全默认值 |
| `/api/v1` | 原有路径、字段和响应信封保持不变 |

历史用量和 HTTP 转发增强主要位于服务端。旧 NPC 不领取新增实例身份请求，连接和
代理仍可正常工作；客户端详情中的主机名、局域网 IP 等信息可能为空。

## 回滚

停止服务并替换回 `1.1.40` 即可。新增的 `conf/usage_history.json` 会被旧版本
忽略，可以保留或删除。旧版本不会使用 1.1.41 新增的请求头、超时和历史用量配置，
回滚前请恢复升级前的 `nps.conf` 备份。
