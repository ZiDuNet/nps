# 隧道模式

NPS 支持多种代理模式。普通 TCP/UDP/HTTP 代理需要占用公网端口；HTTP(S) 域名代理共用 `http_proxy_port` 和 `https_proxy_port`；P2P 和 Secret 通过密钥建立本地访问入口。

## 模式总览

| 模式 | 代码值 | 场景 | 是否占用公网端口 |
|------|--------|------|----------------|
| TCP 隧道 | `tcp` | SSH、远程桌面、数据库 | 是 |
| UDP 隧道 | `udp` | DNS、游戏、VoIP | 是 |
| HTTP 正向代理 | `httpProxy` | 通过 HTTP 代理访问内网 | 是 |
| SOCKS5 | `socks5` | 代理访问多种内网资源 | 是 |
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

SOCKS5 的账号密码不在「新增 SOCKS5 隧道」页面单独设置，而是在该隧道所属客户端的「认证配置」里设置：

```text
客户端 -> 新增/编辑客户端 -> 认证配置 -> Basic 认证用户名 / Basic 认证密码
```

如果客户端的 Basic 认证用户名和密码都为空，SOCKS5 会允许无认证连接。公网开放 SOCKS5 时，建议必须设置账号密码，并按需配合黑名单、白名单或防火墙限制来源 IP。

同一个客户端下的 HTTP 正向代理、SOCKS5 代理和域名代理会复用这组认证信息。

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
- 自定义证书路径
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
  -password=secret-ssh -local_type=secret -local_port=2000
```

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

访问端示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=<VerifyKey> \
  -password=p2p-ssh -target=10.0.0.2:22 -local_port=2000
```

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

## 配额说明

普通隧道和 Host 域名规则都会计入隧道数量：

- 客户端 `MaxTunnelNum` 限制该客户端下的普通隧道和 Host 规则总数。
- 用户 `MaxTunnelNum` 限制该用户所有客户端下的普通隧道和 Host 规则总数。
