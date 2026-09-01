# NPC 配置文件参考

配置文件模式适合批量静态部署、离线配置，或将规则与基础设施代码一起管理的场景。

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
| `conn_type` | Bridge 类型，支持 `tcp` 和 `kcp`；兼容旧键名 `tp`。 |
| `auto_reconnection` | 控制连接断开后自动重连。长期运行建议设为 `true`。 |
| `proxy_url` | 通过 HTTP 或 SOCKS5 出站代理连接 NPS，例如 `socks5://user:pass@127.0.0.1:1080`。 |
| `compress` | 对 Bridge 数据启用 Snappy 压缩。文本或低带宽场景可考虑开启，数据本身已压缩或 CPU 较弱时通常关闭。 |
| `crypt` | 启用项目内置加密。跨不可信网络优先使用 TLS Bridge，不要将它当作 TLS 的替代品。 |
| `tls_enable` | 改连服务端 `tls_bridge_port`，默认通常为 `8025`。 |
| `tls_ca_file` | 信任服务端证书的 CA 文件路径。 |
| `tls_server_name` | TLS SNI 和证书名称校验值；连接地址与证书名称不同时填写。 |
| `tls_fingerprint` | 服务端证书 SHA-256 指纹，适合自签名证书。 |
| `tls_insecure_skip_verify` | 显式跳过证书校验，仅用于旧部署兼容，生产环境不要开启。 |
| `basic_username` / `basic_password` | HTTP 正向代理、SOCKS5 和域名代理共用的 Basic 认证账号。 |
| `web_username` / `web_password` | 此客户端的 Web 登录账号；仅在普通用户登录场景使用。 |
| `max_conn` | 此客户端最大数据连接数。 |
| `rate_limit` | 此客户端带宽上限，单位 KiB/s。 |
| `flow_limit` | 此客户端累计流量上限，单位 MiB，入口与出口相加计算。 |
| `disconnect_timeout` | 未收到心跳回包的最大检查次数；检查间隔为 5 秒，默认 `60`，约为 5 分钟。 |
| `pprof_addr` | 可选调试监听地址，例如 `127.0.0.1:9999`。只允许在受控网络使用。 |
| `remark` | 客户端显示备注。 |

服务端是否执行 `max_conn`、`rate_limit` 和 `flow_limit`，取决于对应的 `allow_*` 开关。具体配额语义见[运行说明](../extend/description.md#流量、带宽和连接数)。

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

## 健康检查

健康检查节名称必须以 `health` 开头。它只在配置文件模式下生效，可将不健康的后端临时从目标池移除：

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
