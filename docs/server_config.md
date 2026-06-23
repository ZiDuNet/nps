# 服务端配置

服务端配置文件为 `conf/nps.conf`。首次启动时如果文件不存在，程序会自动生成默认配置。管理员账号默认为 `admin`，管理员密码、`auth_key` 和 `auth_crypt_key` 会随机生成。

## 配置文件位置

| 运行方式 | 配置位置 |
|----------|----------|
| 直接运行 | 当前目录 `conf/nps.conf` |
| Linux 服务 | `/etc/nps/conf/nps.conf` |
| Windows 服务 | `C:\Program Files\nps\conf\nps.conf` |
| Docker | 挂载目录中的 `/conf/nps.conf` |

修改配置后需要重启 `nps`。

## Web 管理面板

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `web_ip` | Web 面板监听地址 | `0.0.0.0` |
| `web_port` | Web 面板端口 | `8081` |
| `web_username` | 管理员账号 | `admin` |
| `web_password` | 管理员密码 | 首次启动随机生成 |
| `web_base_url` | 子路径部署前缀，例如 `/nps` | 空 |
| `web_open_ssl` | Web 面板是否启用 HTTPS | `false` |
| `web_cert_file` | Web HTTPS 证书路径 | `conf/server.pem` |
| `web_key_file` | Web HTTPS 私钥路径 | `conf/server.key` |
| `open_captcha` | 登录验证码 | `false` |

管理员账号只保存在 `nps.conf` 中。普通用户在 Web 面板「用户管理」中维护，保存到 `conf/users.json`。

## Bridge 客户端连接

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `bridge_type` | 客户端连接类型，支持 `tcp`、`kcp` | `tcp` |
| `bridge_ip` | Bridge 监听地址 | `0.0.0.0` |
| `bridge_port` | Bridge TCP 端口 | `8024` |
| `tls_enable` | 是否启用 TLS Bridge | `true` |
| `tls_bridge_port` | TLS Bridge 端口 | `8025` |
| `disconnect_timeout` | 客户端心跳超时倍数，单位为 5 秒 | `60` |

客户端普通连接使用：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS 连接使用：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

## HTTP/HTTPS 反向代理

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `http_proxy_ip` | HTTP 反向代理监听地址 | `0.0.0.0` |
| `http_proxy_port` | HTTP 反向代理端口 | `80` |
| `https_proxy_port` | HTTPS 反向代理端口 | `443` |
| `show_http_proxy_port` | 非 80 端口时是否在面板显示端口 | `true` |
| `http_add_origin_header` | 是否添加真实来源 IP 相关 Header | `true` |
| `http_cache` | 是否启用 HTTP 缓存 | `false` |
| `http_cache_length` | HTTP 缓存条数 | `100` |

域名规则在 Web 面板「域名」中维护，保存到 `conf/hosts.json`。

## 用户与配额

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `allow_user_login` | 是否允许普通用户登录 | `true` |
| `allow_user_register` | 是否允许自助注册 | `false` |
| `allow_user_change_username` | 是否允许普通用户修改用户名 | `true` |
| `allow_tunnel_num_limit` | 是否显示隧道数量限制配置 | `true` |
| `allow_flow_limit` | 是否显示流量限制配置 | `true` |
| `allow_rate_limit` | 是否显示带宽限制配置 | `true` |
| `allow_connection_num_limit` | 是否显示连接数限制配置 | `true` |

用户级 `MaxTunnelNum` 会统计该用户所有客户端下的普通隧道和域名规则。客户端级 `MaxTunnelNum` 只统计单个客户端下的普通隧道和域名规则。

## 安全与访问控制

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `auth_key` | Web API 鉴权密钥 | 首次启动随机生成 |
| `auth_crypt_key` | 获取加密 authKey 的 AES 密钥，必须 16 位 | 首次启动随机生成 |
| `public_vkey` | 公共客户端密钥，留空关闭 | `123` |
| `ip_limit` | 是否启用 Bridge IP 访问限制 | `false` |
| `allow_ports` | 隧道端口白名单 | 空 |
| `allow_local_proxy` | 是否允许代理到服务端本机 | `false` |

`allow_ports` 示例：

```ini
allow_ports=9001-9009,10001,11000-12000
```

支持单端口、逗号分隔列表和端口范围。

## P2P

| 配置项 | 说明 |
|--------|------|
| `p2p_ip` | P2P 使用的服务端公网 IP |
| `p2p_port` | P2P UDP 起始端口 |
| `p2p_port_range` | P2P 可用端口范围 |

P2P 依赖 NAT 类型，不能保证所有网络都能直连。

## 日志与持久化

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `log_level` | 日志级别，`0` 最少，`7` 最详细 | `6` |
| `log_path` | 日志路径 | `nps.log` |
| `flow_store_interval` | 流量数据持久化间隔，单位分钟；空值表示不定时保存 | `1` |
| `system_info_display` | 是否在仪表盘显示系统信息 | `true` |

JSON 数据文件见 [升级迁移](migrate.md)。
