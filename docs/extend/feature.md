# 功能与能力边界

本页按当前实现列出可用功能、配置入口和使用边界。完整的端到端操作可直接参考[使用示例](example.md)；服务端参数见[服务端配置](../server_config.md)，NPC 参数与配置文件格式见[客户端配置](../client_config.md)。

## 功能索引

| 目标 | 阅读入口 |
| --- | --- |
| 选择 TCP、UDP、HTTP(S)、SOCKS5、Secret、P2P 或文件访问 | [代理模式](#代理模式)与[隧道模式](../tunnel.md) |
| 配置域名、泛域名、路径路由、Host/Header、证书或非标准端口 | [域名代理与路由](domain-proxy.md) |
| 为多用户提供平台子域名、热更新证书或诊断路由命中 | [平台域名、证书热更新与规则诊断](platform-domain.md) |
| 配置 Basic 认证、白名单、黑名单、端口限制、用户和客户端配额 | [访问控制与配额](access-control.md) |
| 打开 TLS Bridge、KCP、压缩/加密、多路复用或健康检查 | [服务端增强功能](../server/nps_extend.md)与[NPC 配置文件参考](../client/config-file.md) |
| 查看真实 IP、流量、带宽、连接状态、日志、pprof 和排查顺序 | [运行说明](description.md) |
| 使用 Web API 自动化客户端、隧道和 Host 管理 | [API 鉴权](api.md)与[API 清单](webapi.md) |

## 代理模式

| 模式 | 适用场景 | 配置入口 |
| --- | --- | --- |
| TCP | SSH、RDP、数据库、任意 TCP 服务 | Web 面板「隧道」或 NPC 配置文件 `mode=tcp` |
| UDP | DNS、游戏、VoIP 等 UDP 服务 | Web 面板「隧道」或 `mode=udp` |
| HTTP(S) 域名代理 | 网站、Webhook、接口回调 | Web 面板「域名」；共用 `http_proxy_port` / `https_proxy_port` |
| HTTP 正向代理 | 通过内网客户端访问 HTTP 资源 | `mode=httpProxy` |
| SOCKS5 | 通用代理访问内网资源 | `mode=socks5` |
| Secret 私密代理 | 不固定占用公网端口的受密码保护连接 | `mode=secret`，访问端以 `-local_type=secret` 启动 NPC |
| P2P | 尝试让访问端与内网端直连 | `mode=p2p`，需要 `p2p_ip`、`p2p_port` 和 NAT 条件 |
| 文件访问 | 将客户端本地目录通过 HTTP 暴露 | `mode=file`，配置 `local_path` 与 `strip_pre` |

各模式的端口、命令和限制说明见[隧道模式](../tunnel.md)。普通 TCP/UDP/HTTP/SOCKS5/文件隧道占用服务端端口；域名代理使用统一 HTTP(S) 入口；Secret 与 P2P 在访问端本地监听端口。

## 隧道创建与复制

在 Web 面板新增普通隧道时，服务端端口留空或填 `0` 会由 NPS 在允许范围内自动选择未占用端口。若设置了 `allow_ports`，自动分配同样遵守该白名单。

隧道列表提供「复制」操作。复制会保留所属客户端、模式、目标、备注和访问控制相关设置，并分配新的隧道 ID 与服务端端口；复制后仍应确认端口开放策略、目标可达性和密钥是否符合预期。

### 列表搜索与筛选

客户端、普通隧道和域名规则列表的工具栏支持备注等关键字模糊搜索。管理员还可以按所属用户筛选，或选择「未分配」查看尚未关联用户的客户端及其资源；筛选会同时影响总数、分页和列表结果。普通用户只看到自己名下资源，不显示管理员专用的用户筛选项。列设置可将次要字段收起，展开每行详情即可查看完整配置。

## 域名代理能力

在 Web 面板「域名」新增 Host 规则，或在 NPC 配置文件中配置 `host` 和 `target_addr`。每条规则可以设置：

- **精确域名与泛域名**：`app.example.com` 或 `*.example.com`。DNS 仍要将相应域名解析到 NPS 公网 IP。
- **平台泛域名池**：管理员可在全局参数维护多个 `*.example.com`，按需绑定证书；用户选择后以唯一前缀创建子域名，平台证书在服务端锁定，无证书项仅支持 HTTP。DNS、续期和诊断见[平台域名说明](platform-domain.md)。
- **协议选择**：`http`、`https` 或 `all`。HTTP 与 HTTPS 监听端口由 `http_proxy_port` 和 `https_proxy_port` 决定；非 80/443 端口需要在浏览器 URL 中显式写出。
- **路径路由**：同一个域名可按 `location` 匹配不同路径，例如 `/api` 和 `/static`。
- **多目标轮询**：目标地址每行一个，NPS 按顺序轮换，可用于简单负载均衡。需要按存活状态摘除节点时，配合[健康检查](#健康检查)。
- **Host 重写和自定义请求 Header**：用于后端虚拟主机、上游鉴权或兼容已有应用；Header 每行 `名称: 值`。
- **Basic 认证**：在所属客户端的「认证配置」中设置用户名和密码，域名代理、HTTP 正向代理与 SOCKS5 共用该组凭据。
- **HTTPS 终结或透传**：可上传/引用证书与私钥，或者让内网服务自行处理 TLS。自动 HTTPS 会将 HTTP 请求重定向到对应 HTTPS 端口。
- **真实来源 Header**：`http_add_origin_header=true` 时，NPS 向后端添加 `X-Forwarded-For` 与 `X-Real-IP`。代理链路和信任边界见[部署与运行说明](description.md#获取访问者真实-ip)。

### HTTP 缓存

对适合缓存的 HTTP(S) 域名代理可开启：

```ini
http_cache=true
http_cache_length=100
```

缓存保存在 NPS 内存中，按域名和请求路径复用响应。它不替代 CDN，也不会自动识别应用的完整缓存语义；动态页面、带身份态响应或需要严格缓存控制的服务应保持关闭。`http_cache_length=0` 表示不限制缓存条目数，应谨慎使用以免占满内存。

### 代理到服务端本地

默认情况下，域名规则和隧道的目标由所绑定的 NPC 连接。若目标服务运行在 **NPS 服务器本机**，需要满足两个条件：

```ini
# nps.conf
allow_local_proxy=true
```

并在对应的隧道或 Host 规则中勾选「转发到本地」。此时 `127.0.0.1:8081` 指向 NPS 的网络命名空间；NPS 使用 Docker `network_mode: host` 时，等同于宿主机回环地址。未勾选该规则选项时，即使全局开关为 `true`，目标仍由 NPC 连接。

该功能允许规则创建者让 NPS 主动访问服务端可达地址，只应向可信管理员开放，并用防火墙、权限和网段隔离保护管理面板。

## 传输、健康检查与配置文件

### 压缩、加密与 TLS Bridge

客户端可在面板或 NPC 配置文件的 `[common]` 中设置：

```ini
compress=true
crypt=true
```

这会影响客户端与 NPS 的代理数据传输。需要使用标准 TLS 保护 Bridge 时，在服务端启用 `tls_enable=true`，客户端改连 `tls_bridge_port` 并传入 `-tls_enable=true`；自签名证书建议设置 `-tls_fingerprint` 或 `-tls_ca_file`，不要在生产环境使用 `-tls_insecure_skip_verify=true`。

Bridge 控制连接支持 `tcp` 与 `kcp`。KCP 需要服务端 `bridge_type=kcp`，客户端对应使用 `-type=kcp` 或 `conn_type=kcp`；它占用 UDP Bridge 端口，部署时要放行 UDP。

### 多路复用

NPS 的数据连接默认使用多路复用，无需单独开启。高并发时优先检查操作系统的文件描述符、监听队列、带宽和连接数限制，参见[部署与运行说明](description.md#linux-连接数限制)。

### PROXY Protocol

TCP 隧道可在 Web 面板中选择 PROXY Protocol V1 或 V2。开启后，NPC 在连接本地 TCP 后端前写入来源地址头，后端（例如 Nginx、HAProxy）必须显式启用对应协议解析。

它只适用于支持 PROXY Protocol 的 TCP 后端；未支持的服务会把该头当成业务数据，从而导致协议错误。该选项当前由 Web 面板的 TCP 隧道提供，配置文件模式请以发布模板和实际版本为准。

### 环境变量模板

NPC 读取配置文件前会以环境变量渲染 Go 模板占位符，适合容器和批量部署：

```ini
[common]
server_addr={{.NPC_SERVER_ADDR}}
vkey={{.NPC_SERVER_VKEY}}

[web]
host={{.NPC_WEB_HOST}}
target_addr={{.NPC_WEB_TARGET}}
```

```bash
export NPC_SERVER_ADDR=1.1.1.1:8024
export NPC_SERVER_VKEY=replace-with-verify-key
./npc -config=conf/npc.conf
```

无配置文件模式也可使用 `NPC_SERVER_ADDR` 与 `NPC_SERVER_VKEY`。模板缺少对应环境变量时会渲染为空，启动前应验证最终配置和日志。

### 健康检查

NPC 配置文件模式支持 HTTP 与 TCP 健康检查。健康节名称必须以 `health` 开头：

```ini
[health_api]
health_check_timeout=2
health_check_max_failed=3
health_check_interval=10
health_http_url=/healthz
health_check_type=http
health_check_target=127.0.0.1:8081,127.0.0.1:8082
```

- `http`：向 `http://<target><health_http_url>` 发起请求，返回 `200` 视为健康。
- `tcp`：能在超时时间内建立 TCP 连接视为健康。
- 连续失败达到 `health_check_max_failed` 后，NPS 会从该客户端的目标池移除该目标；检查恢复后会重新加入。

### 端口范围映射与指定监听 IP

NPC 配置文件模式支持 TCP/UDP 端口范围映射。`server_port` 与 `target_port` 必须一一对应；只有范围映射时可用 `target_ip` 指定非本机目标：

```ini
[batch_tcp]
mode=tcp
server_port=9001-9003
target_port=8001-8003
target_ip=10.1.50.2
```

服务端设置 `allow_multi_ip=true` 后，规则可指定 `server_ip`，从多网卡中的特定地址监听。用 `allow_ports` 可限制允许创建的服务端端口：

```ini
allow_ports=9001-9009,10001,11000-12000
```

### 端口复用

NPS 支持将 Bridge、HTTP(S) 或 Web 管理端口复用在同一监听端口，由协议和 `web_host` 区分。它适合端口数受到严格限制的环境，但会增加排障和反向代理配置难度；新部署建议优先使用独立端口，确认需求后再设计复用方案。

## 资源、访问与账号控制

### 客户端与用户配额

- 客户端可配置带宽、流量、最大连接数和最大隧道数。
- 普通用户可拥有多个客户端；用户级 `MaxClientNum` 限制其名下客户端数量，管理员分配和普通用户自建共用该配额，`0` 表示不限制。
- 用户级 `MaxTunnelNum` 统计其所有客户端下的普通隧道与 Host 规则；管理员和普通用户创建隧道时都执行同一配额校验。
- 客户端到期时间不能晚于所属用户到期时间；客户端留空时跟随用户，到期后客户端及其隧道/Host 会被停用。
- 客户端或用户到期、停用后，其代理资源会被停止或拒绝新连接。
- 详细的对象关系、权限和迁移影响见[用户体系](../user.md)与[部署与运行说明](description.md#流量、带宽和连接数)。

### IP 白名单、黑名单与访问授权

- 全局黑名单在「全局设置」中维护，命中的来源会被所有入口拒绝。
- 客户端黑名单只作用于该客户端名下的隧道和 Host 规则。
- 客户端白名单可直接维护允许地址，也可配置 IP 授权密码，让访问者通过授权页登记当前公网 IP。
- `ip_limit=true` 时，只有已注册的来源 IP 才能访问代理。可用 `npc register -server=<NPS>:<bridge-port> -vkey=<key> -time=2` 注册临时授权；单位为小时。

白名单、黑名单和 IP 注册都依赖 NPS 实际看到的来源地址。若 NPS 位于反向代理或四层负载均衡之后，先确认源地址透传方式，再启用严格的 IP 策略。

### 认证与 API

- 管理员账号在 `nps.conf` 中；普通用户、客户端级 Web 登录账号保存在 JSON 数据文件中。
- HTTP 正向代理、SOCKS5 与域名代理使用客户端的 Basic 认证配置。
- Web API 使用 `auth_key` 与时间戳签名；见[API 鉴权](api.md)和[API 清单](webapi.md)。
- 登录验证码通过 `open_captcha=true` 开启，登录尝试限制和安全部署建议见[部署与运行说明](description.md#web-管理保护)。

## 运维能力

- **JSON 持久化**：客户端、隧道、域名、用户和全局配置保存在 `conf/`，方便备份、迁移和恢复。
- **系统服务**：NPS/NPC 支持安装、启动、停止、重启和卸载服务；命令见[运行命令速查](../run.md)。
- **Docker 与多架构镜像**：服务端、客户端可容器化运行；注意容器的网络命名空间和端口发布，详见[Docker 部署](../docker.md)。
- **GUI 客户端**：提供 Wails GUI，用于管理连接、日志、状态和更新，见[GUI 客户端](../gui.md)。
- **日志和 pprof**：`log_level`/`log_path` 控制日志；`pprof_ip` 与 `pprof_port` 仅应在受控网络临时开启。
- **流量持久化**：`flow_store_interval` 按分钟将流量写入配置数据；设置为空或 `0` 关闭定时持久化。

## 当前不支持或需要谨慎使用的能力

下列项目不应当作已支持功能部署：

- QUIC Bridge。
- WebSocket 作为 Bridge 传输。HTTP Host 代理可转发业务层 WebSocket，但这不是 WebSocket Bridge。
- mTLS/客户端证书双向校验。
- 完整的 `nps.conf` 热重载；端口、监听器和 TLS/P2P 启停都需要重启 NPS。
- NPC 的 `reload` 子命令。修改 NPC 配置文件后应重启对应 NPC 进程或系统服务。

部署前请以本仓库版本、[服务端配置](../server_config.md)和实际启动日志为准，不要直接套用其他 NPS 分支或旧版文档中的默认值。
