# 服务端配置文件

配置文件路径：`<nps 可执行文件目录>/conf/nps.conf`


> 首次启动时若 `conf/nps.conf` 不存在会**自动生成默认配置**。用户名默认为 `admin`，`web_password`、`auth_key`、`auth_crypt_key` 会随机生成并打印到终端。升级时会把新版模板中缺失的有效配置项追加到已有 `nps.conf`，不改已有值（包括显式空值）、注释和自定义项；仅已知的不安全占位密钥会按原有安全策略轮换。兼容上游 `nps2` 配置时，`bridge_tcp_port`、`bridge_kcp_port`、`bridge_tls_port` 和对应桥接 IP 会自动补写为当前版本使用的 `bridge_port`、`tls_bridge_port`、`bridge_ip`，原字段保留不删除。修改通过同目录临时文件替换，并保留原文件权限。升级前仍建议备份配置。

发布包中的已知占位值会在启动时自动轮换；显式填写的自定义值和空值不会被覆盖。生产环境建议将 Web 面板限制在管理网段、启用 HTTPS，并按最小权限开放端口。

## 通用配置

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| bridge_type | 客户端与服务端连接方式 `tcp` 或 `kcp` | `tcp` |
| bridge_ip | 桥接监听 IP | `0.0.0.0` |
| bridge_port | 服务端客户端通信端口 | `8024` |
| tls_enable | 是否开启 TLS。开启后客户端可通过 `-tls_enable=true` 连接 `tls_bridge_port` | `true` |
| tls_bridge_port | TLS 桥接端口，默认 `8025`。**与 `bridge_port` 并存**：客户端可分别用 `bridge_port`（明文）或 `tls_bridge_port`（TLS）接入 | `8025` |
| disconnect_timeout | 未收到 mux 心跳回包的最大次数；每次检查间隔为 5 秒，默认 `60`（即约 5 分钟） | `60` |
| flow_store_interval | 流量数据持久化间隔，单位分钟；留空或 `0` 关闭定时持久化 | `1` |
| client_history_limit | 客户端详情中保留的最近连接会话数；`0` 关闭，最大 `100` | `5` |
| log_level | 日志级别：当前格式支持 `0`~`7`；兼容上游文字值 `trace`、`debug`、`info`、`warn`、`error`、`fatal`、`panic`、`off` | `6` |
| log_path | 日志文件路径；`-log_path` 命令行参数优先，归档保存在同一目录 | `nps.log` |
| log_max_size | 单个日志文件大小上限（MB）；`0` 关闭大小轮转 | `2` |
| log_max_files | 最多保留的轮转归档数量；`0` 不限制 | `10` |
| log_max_days | 归档保留天数；`0` 关闭按时间清理 | `7` |
| log_compress | 是否将轮转归档压缩为 gzip；活动日志保持纯文本 | `false` |
| log_rotate_daily | 日期变化时是否轮转日志 | `true` |
| ip_limit | 是否限制 IP 访问，`true` / `false` / 忽略 | - |

服务安装/运行模式使用支持大小、日期轮转及保留策略的文件日志，压缩在后台日志工作线程执行；交互式前台运行仍写入控制台。升级时缺少的日志配置会自动追加到 `nps.conf`，不会覆盖现有值。


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
| web_open_ssl | web 管理是否启用 https；远程直连时应开启 | `false` |
| web_cert_file | web 管理 https 证书路径 | `conf/server.pem` |
| web_key_file | web 管理 https 私钥路径 | `conf/server.key` |
| open_captcha | 登录是否开启验证码校验 | `false` |
| force_pow | 是否强制登录前完成工作量证明 | `false` |
| pow_bits | PoW 难度（SHA-256 前导零位数） | `20` |
| audit_log_path | 操作审计日志 JSONL 文件路径；相对路径以程序运行目录为准 | `audit.log` |
| allow_user_login | 是否允许普通用户登录。优先使用「用户管理」中创建的账号；`user` + 客户端 VerifyKey 仅为历史兼容方式 | `true` |
| allow_user_register | 是否允许从登录页注册账号 | `false` |
| allow_user_change_username | 多用户登录后是否允许修改用户名 | `true` |

## API 鉴权

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| auth_key | web API 鉴权密钥，详见 [Web API](../extend/api.md) | 首次启动随机生成 |
| auth_crypt_key | `auth/getauthkey` 接口的 AES 加密密钥，**必须 16 位** | 首次启动随机生成 |

管理员可选启用 TOTP 双因素登录：执行 `nps -gen2fa` 生成密钥，将输出的 `totp_secret=...` 写入配置并重启服务。登录页会要求输入密码和认证器中的 6 位动态码；清空该配置并重启即可关闭。此设置仅作用于 Web 管理员账号，不改变旧 NPC 或普通客户端账号的登录方式。

## P2P

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| p2p_ip | 服务端 IP，使用 p2p 模式必填 | - |
| p2p_port | p2p 模式开启的 UDP 起始端口。若设为 `6000`，请额外开放 `6001`、`6002` | - |

## 限制开关

> 发布配置默认启用除 `allow_local_proxy` 外的各项开关；关闭某项后，Web 管理面板会隐藏对应字段。

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| allow_flow_limit | 是否启用客户端流量限制 | `true` |
| allow_rate_limit | 是否启用客户端带宽限制 | `true` |
| allow_tunnel_num_limit | 是否启用客户端最大隧道数限制 | `true` |
| allow_connection_num_limit | 是否启用客户端最大连接数限制 | `true` |
| allow_multi_ip | 是否允许每个隧道监听不同的服务端 IP | `true` |
| allow_local_proxy | 是否允许把隧道目标转发到 nps 所在服务器本地 | `false` |
| allow_ports | 限制可开放的隧道端口范围，例如 `9001-9009,10001,11000-12000`，留空不限制 | （空） |
| system_info_display | 是否在 web 上展示服务器系统监控信息图表 | `true`（默认开启） |

管理操作审计日志记录登录、用户、客户端、隧道、Host 和全局配置等控制面变更，不记录密码、验证密钥、API Token 原文、证书私钥或代理请求体。日志按 JSONL 追加写入，文件权限为仅服务账号可读写；管理员可在 Web 面板的「操作审计」页面查询。

客户端连接历史在客户端详情的「时间信息」中展示，仅记录 NPS 收到的 Bridge 主控制连接上线和确认离线时间，不记录请求明细。记录保存在客户端数据中，并随 `flow_store_interval` 配置的客户端数据持久化周期写盘；因此意外断电或进程崩溃时，最近一次变更可能尚未落盘。旧版本客户端无需升级即可使用。

## debug / pprof

| 名称 | 含义 | 默认值 |
| --- | --- | --- |
| pprof_ip | debug pprof 监听 IP | - |
| pprof_port | debug pprof 监听端口 | - |
