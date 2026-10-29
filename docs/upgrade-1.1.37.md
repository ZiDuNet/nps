# NPS 1.1.37 本地升级说明

> 本文对应多 Bridge 连接方式完善版本。正式发布状态以 `lib/version.VERSION`、GitHub Release 标签和发布流水线结果为准。

## 功能变化

- 客户端详情按实际监听状态提供 TCP、TCP + TLS、KCP、WS、WSS、QUIC 和 HTTP/3 共用入口。
- 每个入口可以复制普通 NPC 命令和快捷启动命令。
- TCP、TCP + TLS 快捷命令继续使用历史 v1 格式；其他入口使用 v2 JSON 格式。
- 同一 VKey 可以由不同 NPC 进程使用不同协议同时连接，系统服务名互不覆盖。
- 客户端列表“连接”列显示当前实际协议，展开详情可通过协议页签查看命令和参数。
- 仪表盘显示实际启用的 Bridge 入口、主入口、UDP 要求、WS Path 和 QUIC ALPN；普通用户仪表盘仅显示管理员向客户端开放且可用的入口，不会显示宿主机和监听配置。
- TLS 类连接命令仅在 Bridge 使用自签名证书时写入 `tls_fingerprint`。公网 CA 证书不写固定指纹，NPC 使用系统 CA 和主机名校验，证书续期无需重新分发命令。
- 普通用户 API Token 生成后可在「系统 → API 接入」页面再次查看；旧版本只保存哈希、没有密文的 Token 需要重新生成一次。
- 用户管理新增全部所属客户端共享的并发连接数、上行带宽和下行带宽；`0` 表示不限。用户级带宽修改会即时作用于已有连接，调低并发上限只限制新连接。
- 「系统 → 全局参数」的「用户注册」Tab 集中维护注册入口、默认客户端/隧道/并发连接/上下行带宽/有效期、注册限频、邮箱验证和 SMTP。
- 新注册账号默认不限制客户端、隧道、并发连接、上下行带宽和有效期。P2P 直连不占用用户级连接和带宽额度。
- 旧版 IP 白名单授权已移除。历史 JSON 中的 `IpWhite`、`IpWhitePass`、`IpWhiteList` 会被忽略，不会自动转换为新版登记；需要继续限制来源时，请设置 `RegisteredOnly` 并配置 `RegisterKey`/`RegisterMaxHours`，访问者通过 `/ip/register` 或 `POST /api/v1/ip-register` 登记。

## 配置文件迁移

升级会保留现有 `nps.conf` 的值、注释、字段顺序和自定义项，并自动补齐缺失字段：

```ini
bridge_show_path=
bridge_tcp_show=true
bridge_tls_show=true
bridge_kcp_show=true
bridge_ws_show=true
bridge_wss_show=true
bridge_quic_show=true
bridge_tcp_show_ip=
bridge_tls_show_ip=
bridge_kcp_show_ip=
bridge_ws_show_ip=
bridge_wss_show_ip=
bridge_quic_show_ip=
bridge_tcp_show_port=
bridge_tls_show_port=
bridge_kcp_show_port=
bridge_ws_show_port=
bridge_wss_show_port=
bridge_quic_show_port=
bridge_quic_show_alpn=
register_max_conn=0
register_upload_limit=0
register_download_limit=0
```

迁移原则：

- `bridge_*_show` 是持久布尔值。设为 `false` 后会保留，重启不会恢复为 `true`。
- `bridge_*_show_ip`、`bridge_*_show_port`、`bridge_show_path` 和 `bridge_quic_show_alpn` 是可选展示覆盖，留空即使用自动地址、实际监听端口、`bridge_path` 或 `quic_alpn`。
- 展示覆盖只影响 Web 和 API 生成的客户端命令，不改变服务端实际监听地址。
- 新配置不会关闭任何已有 Bridge 监听。需要真正停止某入口时，继续关闭对应端口或 `tls_enable`。
- 升级只补齐缺失的用户注册并发/带宽字段，不会覆盖已有注册配额、有效期、SMTP 或其他显式设置。旧配置若仍为 `register_expire_days=7`，需要管理员主动改为 `0` 才会使用“注册后永久有效”的新默认值。

## 升级前检查

1. 备份整个 `conf` 目录，至少包含 `nps.conf`、JSON 数据文件、`bridgessl`、`certs` 和平台域名证书。
2. 记录当前实际开放的 Bridge 端口、CDN 回源端口、防火墙和云安全组规则。
3. 使用 KCP 或 QUIC 时确认 UDP 已放行；使用 HTTP/3 共用入口时同时确认 HTTPS 入口和 `http3_proxy_port` 可用。
4. 如果使用 CDN、端口映射或多公网 IP，准备每个入口各自的对外主机名和端口。

## 升级步骤

1. 停止 NPS。
2. 替换 NPS 程序并保留原 `conf` 目录。
3. 启动 NPS，确认日志中配置迁移成功且全部预期 Bridge 监听已启动。
4. 登录 Web，打开「客户端」，逐个切换协议页签，核对地址、端口、Path、ALPN 和 UDP 提示。
5. 先在测试客户端上使用新 NPC/GUI 验证 v2 快捷命令，再升级生产 NPC。
6. 打开仪表盘，确认主入口和其他已启用入口显示正确。
7. 打开「系统 → 全局参数」的「用户注册」Tab，确认注册默认权益和 SMTP 回显正确；需要默认无限权益时，将相关配额、带宽和有效期都设为 `0`。

## 版本兼容

| 组件 | 兼容范围 |
|------|----------|
| NPC `1.1.28` 及以下 | 继续使用历史握手和原有 TCP/KCP 连接方式 |
| 旧 NPC + TCP/TCP + TLS 普通命令 | 继续可用 |
| 旧 NPC + v1 快捷命令 | 继续可用 |
| 旧 NPC/GUI + v2 快捷命令 | 不支持；应先升级 NPC/GUI |
| NPC/GUI `1.1.37` 及以上 | 支持 v1/v2、多协议参数和协议化系统服务名 |

NPS 升级不会强制断开旧客户端。需要测试新协议或 v2 快捷命令时，使用新 NPC/GUI 单独建立测试连接。

## HTTP/3 共用入口

HTTP/3 Bridge 不是独立的新传输类型，而是让 HTTP/3 域名反代的 UDP 端口同时承载 QUIC Bridge。启用条件：

- `http3_proxy_port` 非零；
- HTTPS 入口可用；
- `bridge_http3=true`；
- Bridge 与浏览器反代使用可区分的 `bridge_host`/SNI；
- 防火墙已放行 UDP。

当 HTTPS 入口关闭或 UDP 端口绑定失败时，NPS 会继续提供其他入口，但 Web/API 不再生成 HTTP/3 命令。

## 回滚

回滚 NPS 程序并恢复升级前 `conf` 备份即可。新增展示字段和 v2 快捷命令均为增量能力，不会修改 `clients.json`、`tasks.json` 或 `hosts.json` 的旧有字段。回滚后，已安装的协议化 NPC 系统服务仍可单独操作；需要恢复为旧服务名时，停止对应服务后重新使用旧版 NPC 注册。
