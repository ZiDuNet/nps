# 部署安全与参数速查

本页只说明上线时最容易出错的安全边界和端口口径。`nps.conf` 的完整字段、
默认值和 Web 维护方式统一见[服务端配置文件参考](server/server_config.md)，
不要在本页另行维护第二份字段表。

## 首次启动与凭据

- 首次启动时管理员用户名默认为 `admin`；`web_password`、`auth_key`、
  `auth_crypt_key` 会随机生成并打印到终端。
- 发布模板中的 `CHANGE_ME` 和历史弱占位值会自动轮换；显式自定义值和显式
  空值不会被无条件覆盖。
- 历史共享密钥 `public_vkey=123` 会被自动关闭。不要为了省事重新启用公共
  VKey，除非 Bridge 入口本身受网络访问控制保护。
- 管理员账号只保存在 `nps.conf`；普通用户、客户端、隧道、Host 和全局参数
  保存在同目录 JSON 文件中。升级和迁移前应完整备份 `conf` 目录。

## Web 管理面板

新安装默认使用 `web_ip=127.0.0.1`、`web_port=8081`。推荐让 Web 只监听回环，
再由 Nginx、Caddy 等前置代理提供 HTTPS。

Docker 部署时可在容器内设置 `NPS_WEB_IP=0.0.0.0` 让前置代理访问，但宿主机
端口仍应只发布到回环或受控管理网卡，例如
`-p 127.0.0.1:8081:8081`。

`allow_x_real_ip=true` 时，管理面板、IP 登记页和 HTTP Host 来源识别会优先读取
`X-Real-IP`，其次读取 `X-Forwarded-For` 的第一项。前置代理必须**覆盖**这些
请求头，不能保留客户端提交值后再追加。该功能不要求填写
`trusted_proxy_ips`；后者仅为旧 Web 部署保留的兼容字段。

当前 Web 管理面板不执行全局同源/Origin 校验。反向代理应保留正确的 `Host`，
并按需设置 `X-Forwarded-Host` 和 `X-Forwarded-Proto`；`web_base_url` 只负责
子路径部署，不等于安全边界。

## Bridge 真实 IP

`bridge_real_ip_header` 和 `bridge_trusted_ips` 只用于 WS/WSS Bridge 后端恢复
NPC 来源地址，不要和 Web/HTTP Host 的 `allow_x_real_ip` 混为一谈。

- 只有 WS/WSS 的 TCP 对端命中 `bridge_trusted_ips` 时，服务端才会读取
  `bridge_real_ip_header` 指定的请求头。
- 默认只信任 `127.0.0.1`。Docker 或独立反代应填写实际代理地址/CIDR。
- 未配置请求头或对端不在可信列表时，继续使用 TCP 对端地址。

管理员可在「系统 → 全局参数」的「服务端配置」Tab 中维护相同字段，保存后重启生效。

## 端口与防火墙

| 入口 | 默认端口 | 协议 | 说明 |
| --- | --- | --- | --- |
| Web 管理 | `8081` | TCP | 新安装默认仅本机监听 |
| HTTP 域名代理 | `80` | TCP | `http_proxy_port` 留空关闭 |
| HTTPS 域名代理 | `443` | TCP | `https_proxy_port` 留空关闭 |
| Bridge TCP | `8024` | TCP | `bridge_port` |
| Bridge TLS | `8025` | TCP | `tls_enable=true` 时启用 |
| KCP / QUIC | 按配置 | UDP | 独立监听，需放行 UDP |
| WS / WSS | 按配置 | TCP | 需放行 Upgrade；WSS 使用 Bridge 证书 |
| HTTP/3 域名代理 | 按配置 | UDP | 同时需要 HTTPS 入口、证书和 `http3_proxy_port` |

只开放实际使用的端口。Bridge TLS、WSS、QUIC 和加密内层链路共用
`bridge_cert_file` / `bridge_key_file`；替换证书后需重启 NPS。

P2P 若设置 `p2p_port=6000`，旧版 P2P 使用 UDP `6000-6002`，新版 NAT
observer 还需 `6003-6005`。新旧端口不能只开放一组。

## 反向代理要点

- HTTP 反代到 NPS 时必须保留原始 `Host`。
- 需要真实 IP 时，代理应设置
  `proxy_set_header X-Real-IP $remote_addr;`，并直接覆盖
  `X-Forwarded-For`，不要信任客户端自带的同名头。
- WebSocket Bridge 需要透传 `Upgrade` 与 `Connection`，并保持
  `bridge_path` 一致。
- 子路径部署时设置 `web_base_url`，并确保代理不剥离该前缀。
- 生产环境使用 TLS 1.2/1.3 和有效证书；不要沿用旧文档中的
  `TLSv1`、`TLSv1.1` 或 Nginx `ssl on` 语法。

具体代理示例见[服务端增强功能](server/nps_extend.md#与-nginx-配合)。

## 上线检查

1. 确认 `conf` 只对服务账号可读，并已拿到首次随机凭据。
2. 确认 Web 面板不是无保护地直接暴露在公网。
3. 确认反代覆盖真实 IP 头，或明确关闭 `allow_x_real_ip`。
4. 确认实际启用的 Bridge、HTTP(S)、UDP 和 P2P 端口已在云安全组与系统防火墙放行。
5. 从外部依次验证登录、NPC 连接、TCP/UDP、域名反代和证书链。
6. 查看启动日志中的配置迁移、无效时区、DNS、证书和监听失败提示。
