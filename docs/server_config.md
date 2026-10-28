# 服务端配置

服务端配置文件为 `conf/nps.conf`。首次启动时如果文件不存在，程序会自动生成默认配置。管理员账号默认为 `admin`，管理员密码、`auth_key` 和 `auth_crypt_key` 会随机生成并打印到终端；发布模板中的占位值也会在启动时自动轮换。已有自定义配置不会被覆盖，请以当前 `conf/nps.conf` 为准。

## 配置文件位置

| 运行方式 | 配置位置 |
|----------|----------|
| 直接运行 | 当前目录 `conf/nps.conf` |
| Linux 服务 | `/etc/nps/conf/nps.conf` |
| Windows 服务 | `C:\Program Files\nps\conf\nps.conf` |
| Docker | 挂载目录中的 `/conf/nps.conf` |

Linux/macOS 上可通过 `nps reload` 重新加载 `web_username`、`web_password`、`auth_key` 和 `auth_crypt_key`；Windows 以及监听端口、Bridge、代理和隧道相关配置仍需执行 `nps restart`。

## Web 管理面板

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `web_ip` | Web 面板监听地址，新安装默认仅本机访问 | `127.0.0.1` |
| `web_port` | Web 面板端口 | `8081` |
| `web_username` | 管理员账号 | `admin` |
| `web_password` | 管理员密码 | 首次启动随机生成 |
| `web_base_url` | 子路径部署前缀，例如 `/nps` | 空 |
| `web_open_ssl` | Web 面板是否启用 HTTPS | `false` |
| `web_cert_file` | Web HTTPS 证书路径 | `conf/server.pem` |
| `web_key_file` | Web HTTPS 私钥路径 | `conf/server.key` |
| `allow_x_real_ip` | 是否读取前置反向代理覆盖后的 `X-Real-IP`/`X-Forwarded-For`；默认开启，Docker + Nginx 场景需确保代理覆盖而不是追加这些请求头 | `true` |
| `trusted_proxy_ips` | 旧版可信代理列表兼容字段；当前 Web 登录和 HTTP/IP 登记来源识别不要求额外填写此列表 | `127.0.0.1` |
| `x_nps_http_only` | 可选的反代共享密钥；请求携带匹配的 `X-NPS-Http-Only` 时允许使用真实 IP 头并保持 HTTP 入口 | 空 |
| `open_captcha` | 登录验证码 | `false` |
| `audit_log_path` | 操作审计日志 JSONL 文件路径；相对路径以程序运行目录为准 | `audit.log` |

管理面板默认使用简体中文，登录页右上角可以切换中英文。若浏览器仍显示旧页面，请重启服务或清理缓存后重新加载。

管理员账号只保存在 `nps.conf` 中。普通用户在 Web 面板「用户管理」中维护，保存到 `conf/users.json`。

管理操作审计日志默认保存为 `audit.log`，记录登录、用户/客户端/隧道/Host/全局配置等控制面变更，不记录密码、验证密钥、API Token 原文或代理请求体。文件按 JSONL 追加写入，权限为仅服务账号可读写；管理员可在面板的「操作审计」页面查询。

安全部署建议：保持 `web_ip=127.0.0.1`，通过 HTTPS 反向代理访问；若必须远程直连，只允许受控管理网段并启用 `web_open_ssl`。反代需要保留浏览器的 `Host`，或覆盖设置 `X-Forwarded-Host` 与 `X-Forwarded-Proto`；NPS 会按这些转发后的访问权威检查浏览器 Origin，不要求额外登记代理 IP/域名。`allow_x_real_ip` 默认开启，代理必须覆盖而不是追加真实 IP 头，避免客户端伪造；若面板必须直接暴露且允许任意客户端直达，应显式设为 `false`。Docker 容器可用 `NPS_WEB_IP=0.0.0.0` 让容器内反向代理访问，但宿主机端口应只发布到回环或管理网卡。防火墙只开放实际使用的 Bridge、代理和管理端口。不要启用公共密钥 `public_vkey`，除非确实需要；配置和 JSON 数据文件应限制为服务账号可读。

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

HTTP 正向代理与 SOCKS5 在 Web 面板统一为“HTTP / SOCKS 混合代理”。一条混合代理隧道共用一个公网端口，可分别启用 HTTP 和 SOCKS5，至少启用一种；旧的 `httpProxy`、`socks5` 数据仍可继续使用，编辑后会迁移为混合代理。

管理员还可以从「全局参数 → 服务端配置」维护 `nps.conf`。页面提供“可视化配置”和“源码视图”两种模式：可视化模式支持按功能搜索、展开/收起、字段级启用开关和准确的默认值提示；源码模式展示完整文件并保留注释、空行、字段顺序和自定义项，保存前会检查语法、已知字段类型和关键证书/邮件配置关系。源码保存使用文件修订号检测并发修改，避免覆盖其他窗口刚提交的内容，文件上限为 1 MiB。两种视图都仅限管理员访问。

密码、Token 和 API 密钥不会在可视化表单中回显；私钥文件路径会正常显示以便核对，但 NPS 不会读取或展示私钥内容。可选监听、证书、证书私钥路径、日志归档和调试项关闭后会从文件中移除对应有效配置键。保存后必须重启 NPS 才会应用监听端口、Bridge、TLS、Web 和日志等运行配置；历史和自定义键只读展示并在保存时原样保留。

## 用户与配额

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `allow_user_login` | 是否允许普通用户登录 | `true` |
| `allow_user_register` | 是否允许自助注册 | `false` |
| `register_captcha` | 注册提交是否要求图片验证码 | `true` |
| `register_max_clients` | 新注册用户默认最大客户端数；`0` 表示不限 | `0` |
| `register_max_tunnels` | 新注册用户默认总隧道数；域名反代也计入，`0` 表示不限 | `0` |
| `register_expire_days` | 新注册用户默认有效天数；`0` 表示永久有效 | `7` |
| `register_rate_window` | 注册频率统计窗口（秒） | `60` |
| `register_rate_limit` | 同一 IP 或用户名在一个窗口内允许的注册提交次数；`0` 表示不限 | `5` |
| `register_ip_rate_limit` | 同一来源 IP 在一个窗口内允许成功创建的账号数；`0` 表示不限 | `3` |
| `register_email_verify` | 是否要求邮箱验证 | `false` |
| `register_email_code_ttl` | 邮箱验证码有效期（秒） | `600` |
| `register_email_code_cooldown` | 验证码发送冷却时间（秒） | `60` |
| `smtp_host` / `smtp_port` / `smtp_security` | SMTP 主机、端口和加密方式；加密方式可选 `starttls`、`ssl`、`none` | 空 / `587` / `starttls` |
| `smtp_username` / `smtp_password` / `smtp_from` | SMTP 认证与发件人配置；启用邮箱验证时必须提供主机、端口和发件人 | 空 |
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
| `public_vkey` | 公共客户端密钥，留空关闭 | 空（关闭） |
| `public_register_key` | IP 登记页面的公共登记密钥，可为启用“仅允许已登记 IP”的资源登记来源 IP | 空（关闭） |
| `ip_register_max_hours` | 公共登记密钥的默认最长登记时长，单位小时；客户端单独设置的时长不能超过该上限 | `2` |
| `ip_register_ip` | 独立 IP 登记监听地址；仅 `ip_register_port` 非 0 时生效，未设置时回环监听 | `127.0.0.1` |
| `ip_register_port` | 独立 IP 登记监听端口；`0` 表示不启动独立监听，继续使用面板的 `/ip/register` | `0` |
| `ip_register_cert_file` / `ip_register_key_file` | 同时填写时为独立登记监听启用 HTTPS；相对路径以程序运行目录为准 | 空 |
| `ip_limit` | 是否只允许已注册的来源 IP 访问代理资源 | `false` |
| `allow_ports` | 隧道端口白名单 | 空 |
| `allow_local_proxy` | 是否允许代理到服务端本机 | `false` |

`allow_ports` 示例：

```ini
allow_ports=9001-9009,10001,11000-12000
```

支持单端口、逗号分隔列表和端口范围。

IP 登记页面可使用 `/ip/register`。填写 `ip_register_port` 后，NPS 会启动只包含登记页面的独立 HTTP 监听，不会暴露管理面板的其他路由；默认绑定 `127.0.0.1`，需要公网或内网其他机器访问时，应显式将 `ip_register_ip` 设为对应网卡地址，并建议同时配置证书与私钥。登记页面默认只相信 TCP 对端地址；若经过反向代理，开启 `allow_x_real_ip=true` 后读取代理覆盖的 `X-Real-IP`（无效时回退到 `X-Forwarded-For` 首地址），也可以使用匹配的 `X-NPS-Http-Only` 密钥只对特定入口启用。页面会标明当前使用的是反代传递的公网 IP 还是 NPS 看到的代理 IP；浏览器本身不能可靠地直接提供公网 IP。`trusted_proxy_ips` 仅为旧配置保留，不是 Docker + Nginx 的额外必填项。

登记密钥可以是客户端单独配置的登记密钥/验证密钥，也可以是 `public_register_key`（该密钥将登记应用到所有启用“仅允许已登记 IP”的客户端资源）。登记记录仅保存在内存中，服务端重启后需重新登记。

## P2P

| 配置项 | 说明 |
|--------|------|
| `p2p_ip` | P2P 使用的服务端公网 IP |
| `p2p_port` | P2P UDP 起始端口 |

P2P 依赖 NAT 类型，不能保证所有网络都能直连。

## 日志与持久化

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `log_level` | 日志级别，`0` 最少，`7` 最详细；同时接受历史文本值 `trace`/`debug`/`info`/`warn`/`error`/`fatal`/`panic`/`off` | `6` |
| `log_path` | 日志路径 | `nps.log` |
| `flow_store_interval` | 流量数据持久化间隔，单位分钟；留空或 `0` 关闭定时持久化 | `1` |
| `system_info_display` | 是否在仪表盘显示系统信息 | `true`（默认开启） |

JSON 数据文件见 [升级迁移](migrate.md)。
