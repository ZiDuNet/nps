# 隧道模式

NPS 支持多种代理模式。普通 TCP/UDP/HTTP 代理需要占用公网端口；HTTP(S) 域名代理共用 `http_proxy_port` 和 `https_proxy_port`；P2P 和 Secret 通过密钥建立本地访问入口。

## 模式总览

| 模式 | 代码值 | 场景 | 是否占用公网端口 |
|------|--------|------|----------------|
| TCP 隧道 | `tcp` | SSH、远程桌面、数据库 | 是 |
| UDP 隧道 | `udp` | DNS、游戏、VoIP | 是 |
| HTTP 正向代理 | `httpProxy` | 通过 HTTP 代理访问内网 | 是 |
| SOCKS5 | `socks5` | 代理访问多种内网资源 | 是 |
| 混合代理 | `mixProxy` | HTTP 正向代理和 SOCKS5 共用一个端口 | 是 |
| 域名代理 | Host 规则 | Web 站点、接口回调 | 共用 80/443 |
| Secret | `secret` | 临时安全连接 | 否 |
| P2P | `p2p` | 点对点直连 | 否 |
| 文件访问 | `file` | 浏览客户端本地目录 | 是 |

## TCP 隧道

把服务端端口转发到客户端能访问的内网地址。

示例：将公网 `1.1.1.1:9001` 转发到内网 `127.0.0.1:22`。

```bash
ssh -p 9001 user@1.1.1.1
```

目标地址支持多行，NPS 会轮询选择目标，实现简单负载均衡。

## UDP 隧道

UDP 隧道适合 DNS、游戏、语音等 UDP 服务。配置方式与 TCP 类似，只是协议不同。

示例：将公网 UDP `53` 转发到内网 DNS：

```text
服务端端口：53
目标地址：10.0.0.2:53
```

## HTTP 正向代理

HTTP 正向代理会在服务端开放一个 HTTP 代理端口，浏览器或程序配置代理后即可访问客户端侧内网资源。

```text
模式：httpProxy
服务端端口：9003
```

## SOCKS5

SOCKS5 适合更通用的内网资源访问。可配合 Proxifier、浏览器代理插件或系统代理使用。

```text
模式：socks5
服务端端口：9004
```

HTTP 正向代理和 SOCKS5 可以在「HTTP / SOCKS 混合代理」规则中设置独立的 Basic 认证；规则级认证留空时，才回退到所属客户端的公共 Basic 认证：

```text
新增/编辑 HTTP / SOCKS 混合代理 -> 认证配置 -> 每行填写 用户名:密码
客户端 -> 新增/编辑客户端 -> 认证配置 -> Basic 认证用户名 / Basic 认证密码
```

规则级账号只作用于当前代理端口；没有规则级账号时使用客户端公共 Basic。两者都为空会允许无认证连接。公网开放 SOCKS5 时，务必设置强密码，并按需配合黑名单、白名单或防火墙限制来源 IP。

域名反代优先使用 Host 级 `auth`/独立账号，未配置时才使用所属客户端的公共 Basic。

## 混合代理（HTTP + SOCKS5）

混合代理把 HTTP 正向代理和 SOCKS5 放在同一个服务端 TCP 端口上，客户端根据连接开头的协议握手自动分流。创建规则时可只启用 HTTP、只启用 SOCKS5，或两者都启用；不能两者同时关闭。它不会替换原有的 `httpProxy`、`socks5` 模式，旧规则的监听端口与认证方式保持不变。

混合代理优先使用规则级 Basic 认证；未配置时使用所属客户端的公共 Basic。公网开放前应配置强密码并限制来源 IP。可选的“目标 ACL”用于限制代理最终访问的目标主机：关闭时不增加目标限制；白名单模式只放行匹配项；黑名单模式拒绝匹配项。规则每行一条，支持：

```text
full:api.example.com
domain:example.com
keyword:internal
*.example.net
10.0.0.0/8
```

`full:` 精确匹配主机名；`domain:` 匹配主域名及其子域名；`keyword:` 按主机名子串匹配；通配域名和 IP/CIDR 也可直接填写。目标 ACL 同时应用于 HTTP 代理请求和 SOCKS5 CONNECT；SOCKS5 UDP 会按每个数据报中的实际目标逐个判断。规则还支持 `geosite:` 与 `geoip:`：`geosite:` 按代理请求携带的目标主机名匹配，`geoip:` 只在目标本身是 IP/CIDR 时匹配；NPS 不会为了 GeoIP 规则额外解析域名。

## HTTP(S) 域名代理

域名代理通过 Host 规则维护，不在普通隧道列表中。多个域名共用服务端 `80` 和 `443`。

典型流程：

1. 将域名解析到 NPS 服务端公网 IP。
2. 在 Web 面板新增域名规则。
3. 填写域名、协议、路径、所属客户端、内网目标。
4. 保存后通过域名访问内网 Web 服务。

支持：

- 泛域名，例如 `*.example.com`
- 路径路由，例如 `/api`
- 自定义请求 Header
- Host 重写
- 自定义证书：路径、上传、粘贴三种输入方式，保存前可在线校验证书与私钥是否匹配
- 自动 HTTPS

### 域名、DNS 与非标准端口

域名代理不是 DNS 服务。DNS 的职责是把域名解析到 NPS 公网 IP；NPS 根据请求里的 Host 决定转发到哪条域名规则。

假设 NPS 公网地址为 `203.0.113.10`，HTTP 入口为 `30111`，NPC 连接的目标为 `127.0.0.1:8081`：

1. 在 DNS 控制台为 `app.example.com` 添加 A/AAAA 记录，指向 NPS 公网 IP。泛域名场景可添加 `*.example.com`。
2. 在 `nps.conf` 设置 `http_proxy_port=30111`，重启 NPS。
3. 在 Web 面板创建域名规则：域名为 `app.example.com`，目标为 `127.0.0.1:8081`，并选择对应客户端。
4. 在云安全组、系统防火墙和容器端口策略放行 TCP `30111`。
5. 使用 `http://app.example.com:30111/` 访问。端口不是 80 时必须写在 URL 中。

在 NPS 服务器上可先检查：

~~~bash
curl -i -H 'Host: app.example.com' http://127.0.0.1:30111/
~~~

若本机请求成功、外网失败，问题通常在云安全组、防火墙、宿主机端口监听或 Docker 网络，而不是 DNS。若本机请求也失败，确认 NPS 日志出现 `start http listener, port is 30111`，并检查 Host 规则是否已保存、所属客户端是否在线。

### HTTPS 与反向代理

HTTPS 可由 NPS 终结，也可让内网服务自行处理 TLS。使用非标准 HTTPS 端口时，访问地址同样必须包含端口，例如 `https://app.example.com:30443/`。证书、自动 HTTPS 和 Nginx/Caddy 前置反代配置见[服务端增强功能](server/nps_extend.md)。

当 80/443 被 Nginx、Caddy 或其他服务占用时，NPS 可以监听 `30111`/`30443`，由前置反代转发并保留 Host。此时域名仍解析到前置反代所在地址，NPS 不需要直接占用 80/443。

如需浏览器 HTTP/3，可配置 `http3_proxy_port`（UDP）并保留 HTTPS 证书。HTTP/3 只适用于 NPS 能终止 TLS 的 Host；TLS 原样透传、SSH、RDP、TCP 隧道仍应使用 TCP/HTTPS 或独立 TCP 隧道。前置 Nginx 使用 HTTP/2 不等于后端 NPS 自动支持 HTTP/3，UDP 端口、证书和 SNI 必须同时正确。

#### 将访问者 IP 传给后端（PROXY Protocol）

Host 规则可以选择关闭、PROXY v1 或 PROXY v2。开启后，每个新建的后端 TCP 连接开始时，NPS 会先发送一段 PROXY Protocol 头，携带 NPS 看到的来源地址和后端地址；后端必须明确支持并配置为接收对应版本。未启用的旧规则继续不发送 PROXY 头。

只应对支持 PROXY Protocol 的后端开启。普通 HTTP 服务通常会把这段头当成非法 HTTP 数据，导致请求失败。若后端位于 NPS 本机并通过“本地代理”访问，NPS 同样会发送该头。

### 实时请求调试

隧道列表和域名规则列表的操作列提供「实时请求调试」按钮。打开后，面板只接收**打开窗口之后**发生的流量；关闭、暂停或浏览器断开后会立刻停止捕获，不写入文件和数据库，也不会保留历史记录。

对于 NPS 能看到明文的 HTTP/HTTPS 域名代理，调试器会显示来源地址、NPS 监听地址、实际选中的内网目标、方法、路径、请求头、响应状态、耗时、请求/响应大小，以及请求体和响应体。JSON 会自动格式化，SSE 等文本流会边到达边显示。

- 仅显示 `text/*`、JSON、XML、表单等文本正文；单个请求和响应最多显示前 `256KB`。
- 文件上传、下载、图片、音视频、压缩包和未知二进制内容只显示元信息，不显示正文。
- 请求头和响应头按实时原文展示，包括 `Authorization`、Cookie、API Key 等字段；调试功能仅供受控管理面板使用。
- TLS 原样透传、TCP、UDP、SSH、RDP 等非 HTTP 明文流量无法还原请求体；调试器仅显示连接来源、目标和生命周期信息。
- 调试事件使用有界内存队列。浏览器消费慢时会丢弃调试事件而不是等待，因此不会阻塞实际隧道转发。

## Secret 私密代理

Secret 不固定占用公网端口。服务端保存一个唯一密钥，访问端通过同一个密钥建立本地端口。

服务端创建 Secret 隧道：

```text
模式：secret
密钥：secret-ssh
目标：127.0.0.1:22
```

访问端启动：

```bash
./npc -server=1.1.1.1:8024 -vkey=<VerifyKey> \
  -password=secret-ssh -local_type=secret -target_type=tcp -local_port=2000
```

`target_type` 可选 `tcp`、`udp` 或 `all`（TCP+UDP），用于声明访问内网目标时使用的协议。Web 面板的 Secret / P2P 表单同样提供「目标类型」字段。

- `tcp`：只建立 TCP 连接，与旧版本行为一致。
- `udp`：只转发 UDP 数据报。现代 NPC 使用带长度前缀的数据报帧协议，在 Bridge、Secret 中继和 P2P 直连上都保留报文边界。
- `all`：同一任务同时支持 TCP 与 UDP，访问端本地端口按需同时监听两种协议。

没有 `target_type` 的旧记录默认按 `tcp` 处理；NPC 1.1.28 及以下的旧客户端不发送现代数据报帧，仍按原始流兼容。Secret 访问端需要在 `npc.conf` 或命令行开启 `secret_link`（或使用 UDP 目标类型）时，才会在 TCP 链路上携带 Link 元数据，否则服务端按任务保存的目标地址直接转发。

访问：

```bash
ssh -p 2000 user@127.0.0.1
```

## P2P

P2P 尝试让访问端和客户端直连，服务端只参与协商。是否成功取决于双方 NAT 类型。

服务端需配置：

```ini
p2p_ip=<服务端公网IP>
p2p_port=6000
```

`p2p_port` 是 UDP 起始端口，不是单个端口。设为 `6000` 时需开放
`6000-6005/udp`：旧版 NPC 使用 `6000-6002`，新版 NAT observer 使用
`6003-6005`。新旧客户端共存时应同时放行两组端口。

访问端示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=<VerifyKey> \
  -password=p2p-ssh -target_type=tcp -target=10.0.0.2:22 -local_port=2000 \
  -fallback_secret=true
```

`-fallback_secret` 默认开启：直连打洞失败时自动回退到同任务的 Secret 中继，保证访问端端口始终可用；显式设为 `false` 后，直连失败将直接报错。`-local_type=secret` 的访问端本身就走中继，不受该开关影响。

NAT 检测：

```bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
```

## 文件访问

文件访问用于把客户端本地目录通过 HTTP 暴露出来。配置文件示例：

```ini
[file]
mode=file
server_port=9100
local_path=/tmp/
strip_pre=/web/
```

访问 `http://<服务端IP>:9100/web/` 会映射到客户端 `/tmp/`。

## 内部资源嵌套转发

管理员可将 TCP-capable 隧道或域名反代的目标配置为 NPS 内部路由，不必为下游服务额外打开一个公网端口：

| 目标 | 作用 |
|---|---|
| `tunnel://<隧道ID>` | 把请求送入指定 TCP 隧道的正常访问处理流程，沿用它的来源访问控制、限速和连接统计 |
| `file://<文件通道键>` | 将请求送入同一客户端的文件访问通道；文件通道键由 NPS/NPC 自动生成，API 查询到的目标值可用于配置 |
| `bridge://tcp`、`bridge://tls`、`bridge://ws`、`bridge://wss` | 送入对应 Bridge 传输解码后的握手入口；仍需通过正常的客户端认证和 IP 策略 |
| `bridge://web` | 送入本机 Web 管理面板的 HTTP handler |
| `bridge://ipreg` | 送入独立的 IP 登记页面 handler |

这些路由不创建额外公网监听。它们由服务端进程内虚拟连接承载，并保留原始访问者地址供访问控制和日志使用。`tunnel://` 不能指向自身；为避免递归链和环路，被引用的目标隧道必须是普通 TCP-capable 隧道，不能再以嵌套 URI 作为后端。用户-owned 客户端只能在自己的用户租户内互相引用；未分配的管理端资源只能引用同一个客户端的隧道。`bridge://` 是系统入口，仅管理员可配置，且需要启用全局本地代理权限；普通用户创建或编辑时会被拒绝。

本地代理是 NPS 服务端进程访问内部 handler 的能力，不代表 NPC 本机代理。启用前应按[服务端配置](server/server_config.md)评估安全边界。

## 配额说明

普通隧道和 Host 域名规则都会计入隧道数量：

- 客户端 `MaxTunnelNum` 限制该客户端下的普通隧道和 Host 规则总数。
- 用户 `MaxTunnelNum` 限制该用户所有客户端下的普通隧道和 Host 规则总数。

## 方向限速

普通隧道和 Host 规则都可以单独设置两个方向的速度：

- `upload_limit`：访问者 → NPS → 内网服务，单位 KB/s。
- `download_limit`：内网服务 → NPS → 访问者，单位 KB/s。
- `0` 或留空表示不限速；同一条规则的所有并发连接共享该额度。

Web 面板中的“上行限速（KB/s）”和“下行限速（KB/s）”会写入同名字段。配置文件模式可直接在规则节中填写：

```ini
[tcp_api]
mode=tcp
server_port=9001
target_addr=127.0.0.1:8080
upload_limit=512
download_limit=2048
```

资源级限速会与客户端级 `rate_limit`、服务端全局限速叠加，实际速度取各层限制共同允许的结果。列表中的“当前速率”是一秒采样值，适合观察趋势，不是精确计费数据；服务重启后采样值会重新建立。P2P 成功直连时流量不经过 NPS 服务端，因此不受这组服务端限速影响。
