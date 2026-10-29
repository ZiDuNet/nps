# 服务端配置文件

配置文件路径：`<nps 可执行文件目录>/conf/nps.conf`


> 首次启动时若 `conf/nps.conf` 不存在会**自动生成默认配置**。用户名默认为 `admin`，`web_password`、`auth_key`、`auth_crypt_key` 会随机生成并打印到终端。升级时会把新版模板中缺失的必需配置项追加到已有 `nps.conf`，不改已有值（包括显式空值）、注释和自定义项；通过 Web 面板关闭并移除的可选监听、证书、日志和调试配置不会在下次启动时被重新添加。仅已知的不安全占位密钥会按原有安全策略轮换。历史 Bridge 字段会按所选 `bridge_type` 迁移：明确使用 TCP 时只从 `bridge_tcp_port` / `bridge_tcp_ip` 获取主监听值；明确使用 KCP 时优先使用 `bridge_kcp_port` / `bridge_kcp_ip`；未指定类型时根据现有字段判断。旧字段保留不删除。修改通过同目录临时文件替换，并保留原文件权限。升级前仍建议备份配置。

发布包中的已知占位值会在启动时自动轮换；显式填写的自定义值和空值不会被覆盖。生产环境建议将 Web 面板限制在管理网段、启用 HTTPS，并按最小权限开放端口。

## Web 面板维护 nps.conf

管理员登录后打开「系统 → 全局参数」的「服务端配置」Tab 即可查看当前 `nps.conf`。页面按 HTTP(S) 入口、Bridge 与传输、客户端认证、日志、Web 管理、权限开关等功能分组；证书、私钥和 ACME 字段统一在「证书设置」Tab 中维护：

- 标记为“可维护”的字段可以直接编辑；保存会保留原文件中的注释、自定义键和文件权限，并通过同目录临时文件原子替换。
- 可选监听、密钥、日志归档和调试功能带有“启用此配置”开关。关闭后对应键会从有效配置文件移除，NPS 使用代码内置默认值或保持关闭。
- 页面支持“可视化配置”和“源码视图”切换。源码视图展示完整 `nps.conf`，保留注释、空行、字段顺序与自定义项；保存前会校验语法、已知字段类型和关键配置关系，并使用修订号阻止并发覆盖，文件上限为 1 MiB。
- 管理员密码、API 密钥、公共验证密钥、TOTP 等敏感值不会回显；密码/Token 输入框留空表示保持原值。私钥文件路径会正常显示以便核对，但私钥内容不会被读取。明确关闭可选字段才会删除对应键。
- 当前版本无法安全在线修改的历史键和自定义键会在“兼容与自定义字段”中只读展示，并在保存时原样保留。
- 保存成功后必须重启 NPS 才会应用端口、Bridge、TLS、Web 和日志设置。由系统服务托管的进程可以在页面确认后发起重启；直接前台运行的进程会提示手动重启。

配置页不会把生产环境配置复制到仓库或浏览器；页面只读取当前运行目录下的 `conf/nps.conf`。

本文中的带宽统一使用面板显示的 `KB/s`，流量配额统一使用 `MB`，均按 1024
进制换算：`1 KB/s = 1024 B/s`，`1 MB = 1,048,576 B`。流量累计值、当前速率
和限速配置均沿用这一口径。

## 通用配置

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| bridge_type | 客户端与服务端连接方式 `tcp` 或 `kcp` | `tcp` |
| bridge_select_mode | 同一 VKey 存在多个在线 Bridge 会话时的新流量选择策略：`Primary`、`RoundRobin` 或 `Random`；无效值回退 `Primary` | `Primary` |
| bridge_ip | 桥接监听 IP | `0.0.0.0` |
| bridge_port | 服务端客户端通信端口 | `8024` |
| bridge_kcp_port | 可选并行 KCP Bridge UDP 端口；`0` 表示关闭；与主 KCP Bridge 使用同端口时不会重复监听 | `0` |
| bridge_ws_port | 可选 WebSocket Bridge TCP 端口；`0` 表示关闭 | `0` |
| bridge_wss_port | 可选 TLS WebSocket Bridge TCP 端口；`0` 表示关闭，使用 Bridge TLS 证书 | `0` |
| bridge_quic_port | 可选 QUIC Bridge UDP 端口；`0` 表示关闭 | `0` |
| bridge_quic_ip | QUIC Bridge 监听 IP；为空时跟随 `bridge_ip` | 空 |
| bridge_cert_file | Bridge 全协议共用的 TLS 证书链路径；留空时生成临时自签名证书 | 空 |
| bridge_key_file | Bridge 全协议共用的 TLS 私钥路径；必须与证书同时配置 | 空 |
| quic_alpn | QUIC Bridge 的 TLS ALPN 标识，服务端和 NPC 必须一致 | `nps` |
| quic_keep_alive_period | QUIC 空闲保活周期（秒） | `10` |
| quic_max_idle_timeout | QUIC 最大空闲超时（秒） | `30` |
| quic_max_incoming_streams | 单个 QUIC 会话允许的最大并发流数 | `100000` |
| bridge_path | WS/WSS Bridge URL 路径，NPC 与服务端必须一致 | `/ws` |
| bridge_show_path | Web 面板生成 WS/WSS 命令时展示的 Path；留空跟随 `bridge_path`，只影响展示 | 空 |
| bridge_real_ip_header | WS/WSS Bridge 位于可信前置代理后时，用于恢复 NPC 来源地址的请求头，例如 `X-Real-IP`；留空不读取 | 空 |
| bridge_trusted_ips | 允许使用 `bridge_real_ip_header` 的代理 IP/CIDR，逗号、换行或空白分隔；只有 TCP 对端命中时才信任该请求头 | `127.0.0.1` |
| bridge_tcp_show | 是否生成 TCP 连接命令；关闭不会停止监听 | `true` |
| bridge_tls_show | 是否生成 TCP + TLS 连接命令；关闭不会停止监听 | `true` |
| bridge_kcp_show | 是否生成 KCP 连接命令；关闭不会停止 UDP 监听 | `true` |
| bridge_ws_show | 是否生成 WS 连接命令；关闭不会停止监听 | `true` |
| bridge_wss_show | 是否生成 WSS 连接命令；关闭不会停止监听 | `true` |
| bridge_quic_show | 是否生成 QUIC 连接命令；关闭不会停止 UDP 监听 | `true` |
| bridge_tcp_show_ip / bridge_tcp_show_port | TCP 命令的对外主机名和端口覆盖；留空使用自动地址和实际监听端口 | 空 |
| bridge_tls_show_ip / bridge_tls_show_port | TCP + TLS 命令的对外主机名和端口覆盖 | 空 |
| bridge_kcp_show_ip / bridge_kcp_show_port | KCP 命令的对外主机名和 UDP 端口覆盖 | 空 |
| bridge_ws_show_ip / bridge_ws_show_port | WS 命令的对外主机名和端口覆盖，适合 CDN 或端口映射 | 空 |
| bridge_wss_show_ip / bridge_wss_show_port | WSS 命令的对外主机名和端口覆盖 | 空 |
| bridge_quic_show_ip / bridge_quic_show_port | QUIC 命令的对外主机名和 UDP 端口覆盖 | 空 |
| bridge_quic_show_alpn | QUIC 命令展示的 ALPN；留空跟随 `quic_alpn` | 空 |
| bridge_http3 | 是否允许浏览器 HTTP/3 共用的 QUIC 入口同时承载 Bridge；需要 HTTPS 入口、HTTP/3 UDP 监听、证书和 `bridge_host` 正确配合 | `false` |
| bridge_addr | 客户端列表中生成 NPC 命令使用的对外 Bridge 主机名/IP；留空时按 Web 访问地址自动识别 | （空） |
| bridge_host | Bridge 端口复用时标识 Bridge 流量的 Host/SNI；应与管理域名及普通站点域名区分 | （空） |
| tls_enable | 是否开启 TLS。开启后客户端可通过 `-tls_enable=true` 连接 `tls_bridge_port` | `true` |
| tls_bridge_port | TLS 桥接端口，默认 `8025`。**与 `bridge_port` 并存**：客户端可分别用 `bridge_port`（明文）或 `tls_bridge_port`（TLS）接入 | `8025` |
| disconnect_timeout | 未收到 mux 心跳回包的最大次数；每次检查间隔为 5 秒，默认 `60`（即约 5 分钟） | `60` |
| flow_store_interval | 流量数据持久化间隔，单位分钟；留空或 `0` 关闭定时持久化 | `1` |
| client_history_limit | 客户端详情中保留的最近连接会话数；`0` 关闭，最大 `100` | `5` |
| log_level | 日志级别：当前格式支持 `0`~`7`，也接受文字别名 `trace`、`debug`、`info`、`warn`、`error`、`fatal`、`panic`、`off` | `6` |
| log_path | 日志文件路径；`-log_path` 命令行参数优先，归档保存在同一目录 | `nps.log` |
| log_max_size | 单个日志文件大小上限（MB）；`0` 关闭大小轮转 | `2` |
| log_max_files | 最多保留的轮转归档数量；`0` 不限制 | `10` |
| log_max_days | 归档保留天数；`0` 关闭按时间清理 | `7` |
| log_compress | 是否将轮转归档压缩为 gzip；活动日志保持纯文本 | `false` |
| log_rotate_daily | 日期变化时是否轮转日志 | `true` |
| ip_limit | 旧版 Bridge 来源 IP 登记总开关；启用后只允许已写入旧 Bridge 登记表的来源建立代理连接。它不影响新版客户端、隧道或 Host 的 `RegisteredOnly`，新部署请使用「仅允许已登记 IP」 | - |

`bridge_type` 选择主传输，默认 `tcp`。WS/WSS、并行 KCP 和 QUIC 是可选独立监听，配置后需同时在防火墙开放相应 TCP/UDP 端口。Web 客户端详情按实际启用入口分页生成普通命令和快捷启动命令；`bridge_*_show` 是持久布尔值，关闭后仍保留在配置中，只隐藏对应命令，不会关闭监听。`bridge_*_show_ip/port` 和 `bridge_show_path` 只覆盖命令展示，不改变监听地址。旧版 NPC（`1.1.28` 及以下）继续使用其原有 TCP/KCP 传输；多协议 v2 快捷命令需要新版 NPC/GUI。

服务安装/运行模式使用支持大小、日期轮转及保留策略的文件日志，压缩在后台日志工作线程执行；交互式前台运行仍写入控制台。升级时缺少的必需日志配置会自动追加到 `nps.conf`，不会覆盖现有值；通过 Web 面板关闭的可选日志路径与轮转配置保持移除状态。

旧部署中可能还会看到 Beego 兼容字段 `runmode`、`appname` 和旧版服务端别名
`https_just_proxy`。这些字段会被原样保留，便于回滚和人工核对，但不要依赖它们
配置当前 NPS 的运行行为。请使用本页列出的 `web_open_ssl` / `web_cert_file` /
`web_key_file` 及 Host 证书字段配置 HTTPS。`https_default_cert_file` 和
`https_default_key_file` 仍是当前有效字段，用于 HTTP/3 等需要默认证书的场景，
统一在「证书设置 → HTTPS 默认证书」维护；升级程序不会自动删除旧字段。

### Bridge TLS 证书

「系统 → 全局参数」的「证书设置」Tab 中，“Bridge TLS 证书”区块是 `bridge_cert_file`、`bridge_key_file` 的
管理员界面，不保存额外的配置副本。当前 Bridge 全协议共用一组证书，适用于
TCP TLS、WSS、QUIC/HTTP/3 和加密内层链路。

- 支持路径、上传和粘贴三种录入方式；上传或粘贴后文件写入
  `conf/bridgessl/bridge_server.pem` 与 `conf/bridgessl/bridge_server.key`，`nps.conf`
  只保存这两个文件路径。
- 保存前会校验证书与私钥匹配、当前有效期和可选域名/SAN。只填写证书或只填写
  私钥时拒绝保存。
- 页面同时显示运行中证书与配置证书的 SHA-256 指纹；两者不一致时标记为
  “待重启”。
- 保存、替换或清空配置后均需重启 NPS。当前版本不会在保存动作中主动重载监听
  证书，也不会断开已有 TLS 连接。
- 清空证书和私钥后，NPS 重启时会生成临时自签名证书；需要稳定身份或客户端
  证书固定时，应配置可长期使用的正式证书。


## HTTP(S) 代理

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| http_proxy_ip | http(s) 代理监听 IP，默认 `0.0.0.0` | `0.0.0.0` |
| http_proxy_port | 域名代理 http 监听端口（默认 `80`），留空关闭 | `80` |
| https_proxy_port | 域名代理 https 监听端口（默认 `443`），留空关闭。详见 [使用 https](nps_extend.md#使用-https) | `443` |
| http_proxy_access_port | 管理面板生成 HTTP 域名访问链接时使用的对外端口；留空跟随监听端口，不改变监听 | （空） |
| https_proxy_access_port | 管理面板生成 HTTPS 域名访问链接时使用的对外端口；留空跟随监听端口，不改变监听 | （空） |
| show_http_proxy_port | 域名隧道访问地址是否拼接非 80/443 端口，`true` / `false` | `true` |
| http_add_origin_header | 是否在 http(s) 请求头中追加 `X-Forwarded-For` / `X-Real-IP`，用于内网获取真实 IP | `true` |
| http_cache | 是否开启静态文件缓存 | `false` |
| http_cache_length | 缓存条目数上限，`0` 表示不限制 | `100` |
| http_proxy_response_timeout | 反向代理等待后端响应头的超时时间，单位秒 | `100` |

HTTP 正向代理和 SOCKS5 现在在 Web 面板中统一显示为“HTTP / SOCKS 混合代理”。一条混合代理隧道共用一个公网端口，可在表单中分别开启 HTTP、SOCKS5，至少保留一种。旧版本保存的 `httpProxy`、`socks5` 记录仍可访问；编辑后会迁移为混合代理记录，服务端和 API 继续兼容旧模式值。


## Web 管理后台

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| web_host | web 管理使用的二级域名，端口复用时区分用 | `a.o.com` |
| web_username | web 后台用户名 | `admin` |
| web_password | web 后台密码 | 首次启动随机生成 |
| totp_secret | 管理员登录 TOTP 密钥；留空关闭 | （空） |
| web_port | web 管理端口，留空关闭 web | `8081` |
| web_ip | web 管理监听 IP，新安装默认仅本机访问 | `127.0.0.1` |
| web_base_url | web 管理子路径，例如 `/nps`，用于反代到子路径时使用 | （空） |
| instance_key_file | Web 会话实例密钥路径；留空使用 `conf/instance.key` | （空） |
| web_close_on_not_found | 访问不存在的管理路径时尽力直接关闭连接，不返回 404；排障和常规反向代理部署通常应保持关闭 | `false` |
| web_open_ssl | web 管理是否启用 https；远程直连时应开启。证书路径在「证书设置」维护 | `false` |
| web_cert_file | web 管理 https 证书路径；在「证书设置 → 管理面板 HTTPS」维护 | `conf/server.pem` |
| web_key_file | web 管理 https 私钥路径；在「证书设置 → 管理面板 HTTPS」维护 | `conf/server.key` |
| allow_x_real_ip | 是否读取前置反向代理覆盖后的 `X-Real-IP`/`X-Forwarded-For`；默认开启，Docker + Nginx 场景需确保代理覆盖而不是追加这些请求头 | `true` |
| trusted_proxy_ips | 旧版可信代理列表兼容字段；当前 Web 登录和 HTTP/IP 登记来源识别不要求额外填写此列表 | `127.0.0.1` |
| x_nps_http_only | 可选反代共享密钥；请求携带匹配的 `X-NPS-Http-Only` 时允许使用真实 IP 头并保持 HTTP 入口 | 空 |
| open_captcha | 登录是否开启验证码校验 | `false` |
| force_pow | 是否强制登录前完成工作量证明 | `false` |
| pow_bits | PoW 难度（SHA-256 前导零位数） | `20` |
| audit_log_path | 操作审计日志 JSONL 文件路径；相对路径以程序运行目录为准 | `audit.log` |
| connection_log_path | 客户端 Bridge、隧道与域名代理连接日志的 JSONL 路径；留空关闭 | `connection.log` |
| allow_user_login | 是否允许普通用户登录。优先使用「用户管理」中创建的账号；`user` + 客户端 VerifyKey 仅为历史兼容方式 | `true` |
| allow_user_vkey_login | 是否允许普通用户继续使用客户端 VKey 登录；关闭后只接受独立用户账号 | `true` |
| allow_user_change_username | 多用户登录后是否允许修改用户名 | `true` |

### 注册与邮箱验证

管理员可在「系统 → 全局参数」的「用户注册」Tab 中统一维护自助注册入口、新账号默认权益、限频和 SMTP。服务端配置的完整源码视图仍可查看并维护相同字段；注册成功后，账号使用独立用户体系保存到 `users.json`。

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| allow_user_register | 是否开放公开注册；关闭时注册页面会跳回登录页，提交接口也拒绝请求 | `false` |
| register_captcha | 注册提交是否必须通过图片验证码 | `true` |
| register_max_clients | 新注册用户默认最大客户端数；`0` 表示不限制 | `0` |
| register_max_tunnels | 新注册用户默认总隧道数，域名反代也计入；`0` 表示不限制 | `0` |
| register_max_conn | 新注册用户全部客户端共享的最大并发连接数；`0` 表示不限制 | `0` |
| register_upload_limit | 新注册用户全部客户端共享的上行带宽，单位 KB/s；`0` 表示不限制 | `0` |
| register_download_limit | 新注册用户全部客户端共享的下行带宽，单位 KB/s；`0` 表示不限制 | `0` |
| register_expire_days | 新注册用户从注册成功时起默认有效天数；`0` 表示永久有效 | `0` |
| register_rate_window | 注册频率统计窗口，单位秒 | `60` |
| register_rate_limit | 同一 IP 或同一用户名在一个窗口内允许提交的注册次数；`0` 表示不限制 | `5` |
| register_ip_rate_limit | 同一来源 IP 在一个窗口内允许成功创建的账号数；`0` 表示不限制 | `3` |
| register_email_verify | 是否要求邮箱和一次性验证码；关闭时为用户名、密码注册 | `false` |
| register_email_code_ttl | 邮箱验证码有效期，单位秒 | `600` |
| register_email_code_cooldown | 同一邮箱或来源 IP 两次发送验证码的最小间隔，单位秒；`0` 表示不限制 | `60` |
| smtp_host | SMTP 服务器主机名；启用邮箱验证时必填 | 空 |
| smtp_port | SMTP 端口；STARTTLS 常用 `587`，隐式 TLS 常用 `465` | `587` |
| smtp_security | `starttls`、`ssl` 或 `none`；`none` 只应用于可信内网中继 | `starttls` |
| smtp_username / smtp_password | SMTP 认证用户名和密码；不需要认证的中继可留空 | 空 |
| smtp_from | 验证码邮件的发件人地址；启用邮箱验证时必填 | 空 |

邮箱地址在 `users.json` 中统一保存为小写，并且同一邮箱只能绑定一个用户。邮件验证码只保存在内存中，有效期结束、验证成功或失败次数达到上限后即失效；发送验证码不会记录验证码原文或 SMTP 密码。

升级不会覆盖已有 `nps.conf` 中的显式注册值。只补齐缺失的 `register_max_conn`、`register_upload_limit`、`register_download_limit`；如果旧配置仍写着 `register_expire_days=7`，管理员需要在「系统 → 全局参数」的「用户注册」Tab 中主动改为 `0` 才能得到默认永久有效。

## 客户端认证与公共密钥

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| public_vkey | 允许 NPC 使用的公共验证密钥；留空关闭。启用后应限制 Bridge 来源并评估审计与配额边界，发布模板中的历史值 `123` 会被自动清空 | 空 |
| auth_time_window | NPC `1.1.29` 及以上安全握手允许的客户端时钟偏差秒数；`0` 关闭时间窗口检查，但仍使用一次性随机挑战 | `300` |

`public_vkey` 适合受控环境中的临时接入，不应替代逐客户端 VKey。
通过公共密钥连接的客户端不会沿用普通客户端的持久化归属关系；其运行数据可能在
服务重启后丢失，后台配额和审计口径也应按公共客户端单独评估。

## IP 登记

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| public_register_key | 对所有启用“仅允许已登记 IP”的资源生效的公共登记口令 | 空 |
| ip_register_max_hours | IP 登记页面允许的默认最长有效期（小时） | `2` |
| ip_register_ip | 独立 IP 登记监听地址；仅 `ip_register_port` 非 0 时生效 | `127.0.0.1` |
| ip_register_port | 独立 IP 登记监听端口；`0` 表示仅使用管理面板 `/ip/register` | `0` |
| ip_register_cert_file / ip_register_key_file | 独立登记监听的 HTTPS 证书与私钥，需同时填写 | 空 |

`allow_x_real_ip` 默认开启（`true`）：前置反向代理**覆盖**传递 `X-Real-IP`（或 `X-Forwarded-For`）时，NPS 优先读取 `X-Real-IP`，无效时回退到 `X-Forwarded-For` 首地址；显式设为 `false` 后，页面直接使用 TCP 对端地址。因此在 Docker + Nginx 部署中，应让 Nginx 覆盖而不是追加这些请求头，才能登记访问者的真实公网 IP。也可以显式关闭全局开关，只让特定入口携带与服务端一致的 `X-NPS-Http-Only` 请求头密钥。`trusted_proxy_ips` 仅是旧配置的兼容字段，不参与当前来源 IP 判断。页面检测到回环/内网地址时会主动提示代理链路问题，避免所有访问者被登记成同一地址。该开关默认开启意味着直连公网且可被任意客户端访问的入口存在请求头伪造风险，此类部署应显式设为 `false`。

## API 鉴权

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| auth_key | 管理员 API 签名密钥，详见 [API 鉴权与兼容](../extend/api.md) | 首次启动随机生成 |
| auth_crypt_key | `auth/getauthkey` 接口的 AES 加密密钥，**必须 16 位** | 首次启动随机生成 |

管理员可选启用 TOTP 双因素登录：执行 `nps -gen2fa` 生成密钥，将输出的 `totp_secret=...` 写入配置并重启服务。登录页会要求输入密码和认证器中的 6 位动态码；清空该配置并重启即可关闭。此设置仅作用于 Web 管理员账号，不改变旧 NPC 或普通客户端账号的登录方式。

## P2P

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| p2p_ip | 服务端 IP，使用 p2p 模式必填 | - |
| p2p_port | P2P 的 UDP 起始端口，共占用连续 6 个端口。若设为 `6000`，旧链路使用 `6000-6002`，新版 NAT observer 使用 `6003-6005` | - |
| p2p_stun_servers | 新版 P2P 使用的 STUN 地址，逗号分隔；留空使用内置列表，`none` 关闭 | 内置列表 |
| p2p_enable_upnp_portmap | 允许 NPC 申请 UPnP 端口映射 | `false` |
| p2p_enable_pcp_portmap | 允许 NPC 申请 PCP 端口映射 | `false` |
| p2p_enable_natpmp_portmap | 允许 NPC 申请 NAT-PMP 端口映射 | `false` |
| p2p_portmap_lease_seconds | 端口映射租约秒数，`0` 使用网关默认值 | `3600` |
| p2p_probe_timeout_ms | NAT 探测超时；`0` 使用内置默认值 | `0` |
| p2p_handshake_timeout_ms | P2P 控制协商超时；`0` 使用内置默认值 | `0` |
| p2p_transport_timeout_ms | 直连传输建立超时；`0` 使用内置默认值 | `0` |

当 `p2p_port=6000` 时，旧版 NPC 使用 `6000-6002` 的 UDP/KCP 通道；新版 NPC（`>=1.1.29`）额外使用 `6003-6005` 的 UDP NAT observer。新旧端口不能只开放一组，否则不同版本的客户端会出现“能连接但 P2P 不通”。新版直连失败会按客户端任务配置自动回退到 Secret 中继；普通 TCP/HTTP 转发不依赖 P2P，P2P 探测失败不会阻塞主转发链路。

### HTTP/3、证书与 ACME

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| http3_proxy_port | 浏览器 HTTP/3/QUIC 域名反代 UDP 端口；必须同时开启 HTTPS 入口和证书 | （空） |
| https_default_cert_file / https_default_key_file | Host 未单独配置证书时的默认证书和私钥 | `conf/server.pem` / `conf/server.key` |
| force_auto_ssl | 非标准端口也允许 Host 自动申请 ACME 证书；标准 80/443 时可不打开 | `false` |
| ssl_path | ACME 证书缓存目录 | `conf/ssl` |
| ssl_email | ACME 联系邮箱 | （空） |
| ssl_ca | ACME 服务名称；`LetsEncrypt` 使用默认目录，`ZeroSSL` 使用 ZeroSSL ACME 目录 | `LetsEncrypt` |
| ssl_zerossl_api | ZeroSSL API 密钥；启动时仅用于换取 EAB 凭据，不写入日志 | （空） |
| ssl_cache_max / ssl_cache_reload / ssl_cache_idle | 证书缓存上限、检查周期（秒）和空闲回收时间（秒） | `0` / `3600` / `60` |

HTTP/3 只处理能在 NPS 终止 TLS 的 Host。TLS 原样透传和后端 TLS 卸载规则继续使用 HTTPS/TCP，不会被 HTTP/3 七层处理。HTTP/3 需要同时启用 HTTPS 入口、配置可用的 `http3_proxy_port`，并在防火墙和云安全组放行 UDP；如果 HTTPS 入口未启用或 UDP 端口绑定失败，管理面板不会生成 HTTP/3 Bridge 命令。证书仍按 SNI 选择。启用 `bridge_http3=true` 时，Bridge QUIC 与浏览器 HTTP/3 可以共用 UDP 端口：协商为 `h3` 且 SNI 不等于 `bridge_host` 的连接进入浏览器 HTTP/3；SNI 命中 `bridge_host` 时交给 Bridge，其他 ALPN 也交给 Bridge。`quic_alpn=h3` 可以使用，但必须为 Bridge 保留独立的 `bridge_host`，不能让浏览器站点和 Bridge 共用同一个 SNI。未配置 `bridge_host` 时，浏览器应使用带 SNI 的站点域名；不带 SNI 的 `h3` 连接会按 Bridge 处理。TLS 原样透传 Host 不会被 HTTP/3 七层反代处理。

## 限制开关

> 发布配置默认启用除 `allow_local_proxy` 外的各项开关；关闭某项后，Web 管理面板会隐藏对应字段。

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| allow_flow_limit | 是否启用客户端流量限制 | `true` |
| flow_limit_type | 客户端历史总流量的配额计算口径：`total` 按上下行实际传输量之和；`actual` 将双向传输量之和再乘 2，因此会更快耗尽同一配额 | `total` |
| allow_rate_limit | 是否启用客户端带宽限制 | `true` |
| allow_tunnel_num_limit | 是否启用客户端最大隧道数限制 | `true` |
| allow_connection_num_limit | 是否启用客户端最大连接数限制 | `true` |
| allow_multi_ip | 是否允许每个隧道监听不同的服务端 IP | `true` |
| allow_local_proxy | 是否允许把隧道目标转发到 nps 所在服务器本地 | `false` |
| allow_user_local | 是否允许普通用户在资源中启用服务端本地代理 | `false` |
| allow_secret_link | 是否允许新版 Secret 访问端发送 Link 元数据（目标、来源、ACK） | `false` |
| allow_secret_local | 是否允许 Secret Link 访问 NPS 所在主机的本地地址 | `false` |
| allow_remote_update | 是否允许管理员从 Web 面板向在线 NPC 下发更新指令 | `true` |
| allow_time_limit | 是否执行客户端和用户的到期时间限制 | `true` |
| allow_ports | 限制可开放的隧道端口范围，例如 `9001-9009,10001,11000-12000`，留空不限制 | （空） |
| system_info_display | 是否在 web 上展示服务器系统监控信息图表 | `true`（默认开启） |

## 运行、DNS 与登录保护

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| timezone | 日志和时间显示使用的 IANA 时区名称，例如 `Asia/Shanghai` | `Asia/Shanghai` |
| dns_server | 服务端新版 P2P/STUN 等组件使用的 DNS 服务器，多个地址用逗号分隔 | `8.8.8.8` |
| login_ban_time | 同一来源 IP 或用户名两次登录请求的最小间隔，单位秒；`0` 不限制间隔 | `5` |
| login_ip_ban_time | IP 维度失败次数的统计窗口，单位秒 | `180` |
| login_user_ban_time | 用户名维度失败次数的统计窗口，单位秒 | `3600` |
| login_max_fail_times | 任一维度在统计窗口内达到该失败次数后拒绝继续登录 | `10` |
| login_max_body | 登录请求体上限，单位字节 | `1024` |
| login_max_skew | 浏览器 RSA 登录载荷允许的客户端时间偏差，单位毫秒；`0` 关闭时间偏差检查，但 nonce 仍必须匹配 | `300000` |

登录保护参数在进程启动时读取。修改后需重启 NPS；若反向代理没有正确覆盖
`X-Real-IP` / `X-Forwarded-For`，所有访问者可能被归入同一个 IP 桶。

### NTP、GeoIP/Geosite 与 ACL

`ntp_server` 和 `ntp_interval` 用于新版 Bridge 握手的时间校准；服务端和 NPC 都可以配置，校时失败时保留上一次成功的偏移。`geoip_path` 和 `geosite_path` 支持文本规则文件以及常见的 GeoIP/Geosite protobuf 数据文件：来源名单支持 `geoip:cn`，混合代理目标规则还支持 `geosite:private`。`acl_max_rules` 限制每张普通用户资源的黑名单、白名单或目标 ACL 列表，默认 `1000`；空行和 `#`、`;` 注释行不计数，全局管理员来源名单不受此限制。`geo_reload_interval` 控制文件变更检查周期，默认 `60` 秒。

GeoIP 用于来源 IP 规则，Geosite 用于混合代理和目标 ACL 的域名匹配。`geoip_path` 和 `geosite_path` 留空时分别使用运行目录下的 `conf/geoip.dat` 与 `conf/geosite.dat`，文件不存在时对应规则保持不可用。数据文件不写入 JSON 数据库；文件出现或替换后，NPS 会按刷新周期自动重新加载并编译运行中的策略。

管理操作审计日志记录登录、用户、客户端、隧道、Host 和全局配置等控制面变更，不记录密码、验证密钥、API Token 原文、证书私钥或代理请求体。客户端连接日志记录 Bridge 上下线、认证失败及代理会话的协议、来源、目标、耗时和双向字节数。日志按 JSONL 追加写入，文件权限为仅服务账号可读写；管理员可查询全部记录，普通用户只能查询所属客户端记录。

客户端连接历史在客户端详情的「时间信息」中展示，仅记录 NPS 收到的 Bridge 主控制连接上线和确认离线时间，不记录请求明细。记录保存在客户端数据中，并随 `flow_store_interval` 配置的客户端数据持久化周期写盘；因此意外断电或进程崩溃时，最近一次变更可能尚未落盘。旧版本客户端无需升级即可使用。

## debug / pprof

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| pprof_ip | debug pprof 监听 IP | - |
| pprof_port | debug pprof 监听端口 | - |
