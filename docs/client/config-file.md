# NPC 配置文件参考

配置文件模式适合批量静态部署、离线配置，或将规则与基础设施代码一起管理的场景。

NPC 不会自动生成或覆盖配置文件；仓库中的 `conf/npc.conf` 是完整参考模板。只有服务端首次启动时会自动生成 `conf/nps.conf`。`multi_account` 指向的账号文件也需要由部署者自行创建，路径按 NPC 当前工作目录解析。

~~~bash
./npc -config=conf/npc.conf
~~~

一个文件包含一个 `[common]` 公共连接节，以及任意数量的规则节。节名称用于备注，必须唯一。NPC 会在建立到 NPS 的控制连接后注册其中的规则。

> 常规的 Web 面板集中管理只需使用快捷启动命令。不要同时把同一条规则长期维护在面板和 NPC 配置文件两处，以免排查时无法确认实际来源。

## 最小可用示例

~~~ini
[common]
server_addr=nps.example.com:8024
conn_type=tcp
vkey=replace-with-verify-key
auto_reconnection=true

[tcp_ssh]
mode=tcp
target_addr=127.0.0.1:22
server_port=9001
~~~

这里的 `127.0.0.1` 指向 NPC 进程所在主机或容器，而不是 NPS 服务器。Docker 中如目标位于宿主机，需使用宿主机可达地址或按需使用 host 网络。

## `[common]` 公共连接配置

| 配置项 | 说明 |
| --- | --- |
| `server_addr` | NPS Bridge 地址，格式为 `主机:端口`；兼容旧键名 `server`。 |
| `vkey` | 客户端验证密钥。 |
| `conn_type` | Bridge 类型，支持 `tcp`、`kcp`、`ws`、`wss` 和 `quic`；兼容旧键名 `tp`。WS/WSS/QUIC 需要服务端启用对应独立监听。 |
| `bridge_path` | WS/WSS Bridge 路径，必须与服务端 `bridge_path` 相同，默认 `/ws`。 |
| `quic_alpn` | QUIC Bridge 的 ALPN，必须与服务端 `quic_alpn` 相同，默认 `nps`。 |
| `auto_reconnection` | 控制连接断开后自动重连。长期运行建议设为 `true`。 |
| `proxy_url` | 通过 HTTP/HTTPS CONNECT、SOCKS4/4a、SOCKS5/SOCKS5H 出站代理连接 NPS，例如 `socks5h://user:pass@127.0.0.1:1080`；留空时可使用 `ALL_PROXY`，并按 `NO_PROXY` 绕过。 |
| `compress` | 对 Bridge 数据启用 Snappy 压缩。文本或低带宽场景可考虑开启，数据本身已压缩或 CPU 较弱时通常关闭。 |
| `crypt` | 启用项目内置加密。跨不可信网络优先使用 TLS Bridge，不要将它当作 TLS 的替代品。 |
| `tls_enable` | 改连服务端 `tls_bridge_port`，默认通常为 `8025`。 |
| `tls_ca_file` | 信任服务端证书的 CA 文件路径。 |
| `tls_server_name` | TLS SNI 和证书名称校验值；连接地址与证书名称不同时填写。 |
| `tls_fingerprint` | 服务端证书 SHA-256 指纹，适合自签名证书。 |
| `tls_insecure_skip_verify` | 显式跳过证书校验，仅用于旧部署兼容，生产环境不要开启。 |
| `basic_username` / `basic_password` | HTTP 正向代理、SOCKS5 和域名代理共用的 Basic 认证账号。 |
| `web_username` / `web_password` | 旧版客户端 Web 登录兼容账号。当前面板应优先使用客户端的“所属用户”；配置文件模式只有在需要兼容旧客户端账号登录时才填写这两个字段，二者必须成对出现。 |
| `max_conn` | 此客户端最大数据连接数。 |
| `rate_limit` | 此客户端带宽上限，单位 KiB/s。 |
| `flow_limit` | 此客户端累计流量上限，单位 MiB，入口与出口相加计算。 |
| `disconnect_timeout` | 未收到心跳回包的最大检查次数；检查间隔为 5 秒，默认 `60`，约为 5 分钟。 |
| `local_ip` / `local_ip_forward` | 指定 NPC 出站绑定地址；启用 `local_ip_forward=true` 后，对公网 IP/域名目标的 TCP、UDP、SOCKS5 UDP 转发也使用该地址，私网目标保持系统路由。不填写时由系统自动探测或由内核选择。 |
| `dns_server` | NPC 目标解析和 NAT 探测使用的 DNS 服务器，多个地址用逗号分隔；未配置时使用操作系统解析器。 |
| `ntp_server` / `ntp_interval` | NPC 校时服务器和周期（分钟）；留空关闭，避免新握手因设备时钟偏差失败。 |
| `fallback_secret` | P2P 直连失败后是否回退到同任务 Secret 中继。 |
| `secret_link` | 是否向已允许 `allow_secret_link` 的服务端发送 Link 元数据；旧服务端或服务端未开启时保持关闭。 |
| `disable_remote_update` | 设为 `true` 后拒绝服务端下发的远程更新指令。 |
| `pprof_addr` | 可选调试监听地址，例如 `127.0.0.1:9999`。只允许在受控网络使用。 |
| `remark` | 客户端显示备注。 |

服务端是否执行 `max_conn`、`rate_limit` 和 `flow_limit`，取决于对应的 `allow_*` 开关。具体配额语义见[运行说明](../extend/description.md#流量、带宽和连接数)。

`upload_limit` 和 `download_limit` 写在隧道或 Host 规则节中，而不是 `[common]`。同一条规则的所有连接共享对应方向的令牌桶；它们与客户端级 `rate_limit`、服务端全局带宽限制叠加生效。限速器只在 NPS 服务端转发链路上生效，P2P 成功直连时不经过 NPS，因此不受服务端资源限速影响。

## HTTP(S) 域名规则

域名规则包含 `host`，不需要 `mode`：

~~~ini
[web_api]
host=api.example.com
target_addr=127.0.0.1:3000,127.0.0.1:3001
scheme=all
location=/api
host_change=api.internal.example
header_X-Environment=production
header_X-Proxy-Source=nps
~~~

| 配置项 | 说明 |
| --- | --- |
| `host` | 精确域名或泛域名，例如 `app.example.com`、`*.example.com`。DNS 必须解析到 NPS。 |
| `target_addr` | 后端目标，多个地址以英文逗号分隔。 |
| `scheme` | `http`、`https` 或 `all`。 |
| `location` | 路径匹配前缀，例如 `/api`。 |
| `host_change` | 转发给后端时替换 HTTP Host。 |
| `header_名称` | 新增或替换请求 Header，例如 `header_X-Env=prod`。 |
| `response_名称` | 新增或替换后端响应 Header，例如 `response_Cache-Control=no-cache`；不改变响应正文。 |
| `proxy_protocol` | 后端支持时传递 PROXY Protocol：`0` 关闭、`1` v1、`2` v2。普通 HTTP 服务不要开启。 |
| `path_rewrite` | 每行一条路径替换规则，例如 `/api => /v1/api`；查询参数保持不变。 |
| `redirect_url` | 可选 307 重定向地址，`{path}` 会替换为原始请求路径和查询参数。填写后该 Host 不再转发到后端。 |
| `auto_cors` | 是否自动追加宽松 CORS 响应头；不会代替后端处理 OPTIONS 预检。 |
| `compat_mode` | 是否让普通 HTTP 请求走标准反向代理兼容链路，适用于 SSE、流式响应或复杂 Header。 |
| `multi_account` | Host 独立 Basic 认证账号文件；每行 `用户名=密码`，路径按 NPC 当前工作目录解析。该规则认证优先于客户端公共 Basic 账号。 |
| `upload_limit` | 此 Host 规则上行限速，单位 KiB/s；访问者到内网服务，`0` 或留空表示不限速。 |
| `download_limit` | 此 Host 规则下行限速，单位 KiB/s；内网服务到访问者，`0` 或留空表示不限速。 |

`proxy_protocol` 同时接受本项目旧配置中的 `proto_version=V1` / `proto_version=V2` 写法。省略时保持关闭，因此旧配置的转发行为不变。只有当后端明确配置为接收 PROXY Protocol 时才启用，否则后端通常会把 PROXY 头当作错误的 HTTP 数据。

配置文件只描述路由规则。HTTP/HTTPS 监听端口、证书和自动 HTTPS 仍由 NPS 服务端配置控制。DNS、非 80/443 端口和证书流程见[隧道模式](../tunnel.md#http-s-域名代理)。

## TCP 与 UDP 隧道

~~~ini
[tcp_rdp]
mode=tcp
target_addr=192.168.10.20:3389
server_port=13389

[udp_dns]
mode=udp
target_addr=127.0.0.1:53
server_port=5353
~~~

| 配置项 | 说明 |
| --- | --- |
| `mode` | `tcp` 或 `udp`。 |
| `server_port` | NPS 对公网监听的端口；云安全组和系统防火墙也必须放行。 |
| `target_addr` | NPC 所在网络可访问的目标地址。 |
| `server_ip` | 可选。服务端开启 `allow_multi_ip=true` 后，将规则绑定到指定 NPS 网卡地址。 |
| `upload_limit` | 此隧道上行限速，单位 KiB/s；访问者到内网服务，`0` 或留空表示不限速。 |
| `download_limit` | 此隧道下行限速，单位 KiB/s；内网服务到访问者，`0` 或留空表示不限速。 |
| `proxy_protocol` | TCP 隧道可配置 `0`（关闭）、`1`（v1）或 `2`（v2）；只有目标服务显式支持时才开启。兼容旧键 `proto_version=V1` / `V2`。 |

## HTTP 正向代理、SOCKS5 与文件访问

~~~ini
[http_forward]
mode=httpProxy
server_port=18080

[socks_outbound]
mode=socks5
server_port=1080
multi_account=conf/socks-users.conf

[file_share]
mode=file
server_port=19008
local_path=/srv/share
strip_pre=/files/
~~~

HTTP/SOCKS5 默认使用 `[common]` 的 `basic_username` 和 `basic_password`。若 SOCKS5 配置了 `multi_account`，账号文件一行一个 `用户名=密码`，用于多账号认证。文件服务中，访问 `http://<NPS>:19008/files/` 会由 NPC 从 `/srv/share` 提供内容。公网开放这些服务前必须设置认证并限制来源。

## Secret 与 P2P

~~~ini
[secret_ssh]
mode=secret
password=replace-with-secret
target_addr=127.0.0.1:22

[p2p_ssh]
mode=p2p
password=replace-with-p2p-secret
target_addr=127.0.0.1:22
~~~

Secret 和 P2P 都需要一个访问端 NPC 在本地开端口。可以通过命令行启动，也可在同一配置文件中增加不含 `mode` 的本地访问节：

~~~ini
[secret_access]
local_port=2001
password=replace-with-secret

[p2p_access]
local_port=2002
password=replace-with-p2p-secret
target_addr=127.0.0.1:22
~~~

节名称必须以 `secret` 或 `p2p` 开头，NPC 才会把它识别为本地访问端。Secret/P2P 的命令和 NAT 限制见[客户端配置](../client_config.md#p2p-和-secret-本地访问端)。

### P2P 版本兼容

NPC `1.1.28` 及以下继续使用历史 UDP/KCP P2P 握手；NPC `1.1.29` 及以上使用带时间校验的安全 Bridge 握手和现代 NAT 打洞。服务端同时保留两条路径，不会向旧 NPC 下发新控制帧。现代直连失败时，只有 `fallback_secret=true` 且服务端对应 Secret 任务存在、并允许 Secret 访问时才会回退；普通 TCP、HTTP、WebSocket 和文件转发不依赖 P2P。

现代 P2P 需要服务端开放 `p2p_port` 对应的旧版 `+0..+2` UDP 端口和新版 `+3..+5` UDP observer 端口。UPnP、PCP、NAT-PMP 默认关闭，只有明确打开服务端开关并且局域网网关支持时才会申请端口映射。映射失败不会阻塞普通转发。

## 健康检查

健康检查节名称必须以 `health` 开头。配置文件模式下可将不健康的后端临时从目标池移除；Web 面板为隧道或域名规则保存的健康检查也会通过兼容的新控制消息下发到 NPC。旧版 NPC 会忽略这部分 Web 配置并继续使用本地配置，不会影响普通隧道连接：

~~~ini
[health_api]
health_check_timeout=2
health_check_max_failed=3
health_check_interval=10
health_http_url=/healthz
health_check_type=http
health_check_target=127.0.0.1:8080,127.0.0.1:8082
~~~

| 配置项 | 说明 |
| --- | --- |
| `health_check_type` | `http` 或 `tcp`。HTTP 返回 200、或 TCP 在超时内建立连接，视为健康。 |
| `health_check_target` | 多个目标使用英文逗号分隔。 |
| `health_http_url` | HTTP 检查路径。 |
| `health_check_timeout` | 单次检查超时秒数。 |
| `health_check_max_failed` | 连续失败次数达到该值后摘除目标。 |
| `health_check_interval` | 检查间隔秒数。 |

## 端口范围映射

~~~ini
[batch_tcp]
mode=tcp
server_port=9001-9003,9010
target_port=8001-8003,8010
target_ip=192.168.10.20
~~~

`server_port` 与 `target_port` 必须一一对应。仅在范围映射时使用 `target_ip`；不填写时目标默认是 NPC 本地回环地址。服务端还可用 `allow_ports` 限制允许开放的端口范围。

## 环境变量模板

NPC 在解析配置文件前会执行 Go 模板渲染，可以将环境变量放入规则：

~~~ini
[common]
server_addr={{.NPC_SERVER_ADDR}}
vkey={{.NPC_SERVER_VKEY}}

[web]
host={{.NPC_WEB_HOST}}
target_addr={{.NPC_WEB_TARGET}}
~~~

~~~bash
export NPC_SERVER_ADDR=nps.example.com:8024
export NPC_SERVER_VKEY=replace-with-verify-key
./npc -config=conf/npc.conf
~~~

缺失的环境变量会被渲染为空。容器或自动化部署前应确认实际环境变量和启动日志，避免把空地址或空验证密钥带入生产。

## 参考模板

仓库中的 [conf/npc.conf](https://github.com/ZiDuNet/nps/blob/master/conf/npc.conf) 保留了当前版本的完整示例。升级时请以它和[服务端配置](../server_config.md)为准，避免直接复制其他 NPS 分支的旧字段。
