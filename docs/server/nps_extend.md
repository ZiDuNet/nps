# 增强功能

## HTTPS 域名代理

NPS 根据域名规则是否绑定证书选择 TLS 处理方式：

| 证书配置 | 行为 | NPS 可获取的信息 |
| --- | --- | --- |
| 已配置证书与私钥 | NPS 终结 TLS，再以 HTTP 转发给内网目标 | 请求头、来源地址和正文 |
| 未配置证书 | TLS 流量由内网目标自行终止 | 只能转发加密连接，不能修改 HTTP 内容 |

## 自动 HTTPS

在「域名反代」的新增或编辑页勾选「自动 HTTPS」后，NPS 会把对应域名的
HTTP 请求以 `301` 重定向到 HTTPS。重定向会使用 `https_proxy_port` 中的端口；
例如 `https_proxy_port=8443` 时会跳转到 `https://<host>:8443/...`。

## HTTPS 证书来源

「域名反代」中的自定义域名只提供上传和粘贴两种证书录入模式：

- **上传模式**：从本机选择证书和私钥文件，由 NPS 安全托管。
- **粘贴模式**：直接粘贴 PEM 内容，由 NPS 安全托管。

两种模式只需填写当前选中的一组，并且必须先通过有效性、私钥匹配和域名覆盖
校验才能保存。管理员仍可在「证书设置」中通过路径维护平台泛域名和 Bridge
证书。历史自定义域名中已经保存的路径证书继续生效并只读回显；外部脚本原子
替换有效证书后，后续 TLS 连接会自动加载，无需重启 NPS。完整说明见
[域名代理与路由](../extend/domain-proxy.md#https证书和自动跳转)。

## TLS 桥接加密

为保护客户端到服务端的 Bridge 流量，可启用 TLS：

```ini
# nps.conf
tls_enable=true
tls_bridge_port=8025      # 与 bridge_port 并存
```

客户端任选一种方式接入：

```shell
npc.exe -server=1.1.1.1:8024 -vkey=xxx                       # 明文桥接（bridge_port）
npc.exe -server=1.1.1.1:8025 -vkey=xxx -tls_enable=true      # TLS 桥接（tls_bridge_port）
```

> 客户端默认会校验证书。使用自签名证书时，请配置 `-tls_fingerprint`、`-tls_ca_file` 或 `-tls_server_name`；`-tls_insecure_skip_verify=true` 仅用于兼容旧部署，生产环境不要开启。

### QUIC Bridge

服务端配置 `bridge_quic_port`（UDP，默认 `0` 关闭）后，NPC 可使用：

```shell
./npc -server=1.1.1.1:8026 -vkey=xxx -type=quic -tls_fingerprint=sha256:...
```

`quic_alpn` 两端必须一致，默认值为 `nps`。QUIC 使用 Bridge 证书完成 TLS 握手，需放行 UDP 端口；它承载 NPS Bridge 字节流，不是浏览器直接访问的 HTTP/3 反向代理。

## 全局参数

管理员进入「系统 → 全局参数」的「全局参数」Tab，可维护保存在
`conf/global.json` 中的业务设置：

| 字段 | 含义 |
| --- | --- |
| 服务地址 | 控制台生成客户端快捷命令、隧道访问地址和域名链接时优先使用的对外主机名；留空时回退到当前访问地址。它不改变实际监听地址 |
| 全局最大连接数 | 所有客户端和代理入口共享的并发连接上限；`0` 表示不限 |
| 全局限速 | 所有转发流量共享的总带宽上限，单位 `KB/s`，并叠加在客户端和资源限速之上；`0` 表示不限 |
| 全局 IP 黑名单 / 白名单 | 支持 IPv4、IPv6、CIDR、IPv4 通配和 `geoip:` 规则，作用于客户端连接、代理入口和 Web 面板。同时配置两张名单时可选择“黑名单优先”或“白名单优先” |
| 使用说明 | 自动识别远程 URL、`conf` 下的 HTML 文件或粘贴的 HTML；远程 URL 和本地文件说明见[使用说明页面配置](../usage-guide.md) |
| Geo 数据 | 显示 `geoip.dat`、`geosite.dat` 的路径、大小、加载状态和版本，并支持在线更新 |

平台泛域名、Bridge/管理面板/默认 HTTPS 证书和 ACME 已集中到
「系统 → 全局参数」的「证书设置」Tab。访问控制规则和配额优先级见
[访问控制与配额](../extend/access-control.md)，字段默认值见
[服务端配置文件参考](server_config.md)。

## 代理到服务端本地

默认情况下，TCP、UDP 和域名规则的目标由所属 NPC 去连接。若要把请求转发到 **运行 NPS 的服务器本机**，先在 `nps.conf` 开启：

~~~ini
allow_local_proxy=true
~~~

重启 NPS 后，在 Web 面板新增或编辑隧道、域名规则时勾选「转发到本地」。两个条件都必须满足：

1. 全局开关为 `true`，否则面板不会保存本地转发标记。
2. 对应规则明确勾选「转发到本地」。

勾选后，目标地址由 NPS 进程连接。例如 NPS 机器上有服务监听 `127.0.0.1:8081`，可创建 Host 规则并将目标设为该地址；访问者请求 NPS 的 HTTP(S) 入口后，会由 NPS 直接转发给本机服务，不经过 NPC。

### Docker 与端口边界

- Linux 上 NPS 使用 `network_mode: host` 时，NPS 容器中的 `127.0.0.1` 就是宿主机回环地址。
- NPS 使用默认 bridge 网络时，容器内的 `127.0.0.1` 只指向容器自身。应改用宿主机可达地址，或将目标服务与 NPS 放入同一 Docker 网络。
- 域名解析只负责把域名指向 NPS 公网 IP，不会替你开放端口。若 HTTP 入口设为 `30111`，访问地址必须写成 `http://example.com:30111/`，并在云安全组、宿主机防火墙和容器网络中放行该端口。
- 在 NPS 主机本地先验证监听：`curl -H 'Host: example.com' http://127.0.0.1:30111/`；成功后再从外网验证。

本地转发让规则创建者可以访问 NPS 主机可达的内部服务。只应授予可信管理员，且应将管理面板限制在管理网络。


## 客户端与资源访问控制

客户端、隧道和 Host 都可以维护黑名单、白名单及冲突策略。规则支持 IPv4、
IPv6、CIDR、IPv4 通配和 `geoip:`；`geosite:` 只用于混合代理的目标 ACL。
需要来源固定、可提前录入时使用静态名单；需要允许访问者自助登记时使用
“仅允许已登记 IP”。

开启“仅允许已登记 IP”后，访问者先到 `/ip/register` 输入登记密钥，将
服务端识别到的公网来源地址登记一段有限时间，再访问目标资源。

登记密钥可使用客户端或资源自身的 `register_key`，也可由管理员配置
`public_register_key`。登记最长有效期由客户端/资源设置和
`ip_register_max_hours` 共同约束；记录只保存在内存中，到期或 NPS 重启后
失效。普通用户只能管理自己客户端下的登记，管理员可管理全部登记。

API 集成使用 `POST /api/v1/ip-register`。旧版“IP 授权密码”、跳转授权页、
持久化 IP 授权列表以及 `/authIp`、`/auth/ipwhiteauth`、
`/api/v1/ipwhiteauth` 均已删除。


## 后端 PROXY Protocol

TCP 隧道和域名反代的编辑页都提供「后端 PROXY Protocol」选项。选择
`PROXY v1` 或 `PROXY v2` 后，NPS 会在每个新后端 TCP 连接开始时发送对应的
PROXY 头，让明确支持该协议的后端获知访问者地址。普通 HTTP 服务不应开启
该选项；只有 Nginx、HAProxy 等后端显式启用 PROXY Protocol 时才应使用。

## Web 登录验证码

`nps.conf` 中：

```ini
open_captcha=true
```

开启后 Web 登录页会要求图形验证码。结合登录失败限频、来源 IP 策略和
HTTPS 反向代理，可以降低暴力破解风险；具体窗口与失败次数见
[Web 管理保护](../extend/description.md#web-管理保护)。

## 关闭代理

如需关闭 HTTP 域名代理，将 `http_proxy_port` 留空；如需关闭 HTTPS 域名代理，
将 `https_proxy_port` 留空。

## 流量数据持久化

服务端默认每 1 分钟持久化一次流量数据。将 `flow_store_interval` 设为空或 `0` 可关闭定时持久化；单位为分钟。

**注意：** NPS 不会持久化通过公共 VKey 连接的客户端。

## 系统信息显示

NPS 支持在 Web 仪表盘显示服务器 CPU、内存、连接和网卡信息。
`system_info_display` 默认开启；如需关闭，可在 `nps.conf` 中设置
`system_info_display=false`。


## 关闭 Web 管理

将 `nps.conf` 中的 `web_port` 设置为空或删除。

## 服务端多用户登录

`allow_user_login` 默认开启。当前优先使用「用户管理」中创建的普通用户账号登录，
账号与密码保存在 `users.json`；登录后可管理归属给该用户的多个客户端，并可在
`MaxClientNum`（`0` 表示不限制）内自行创建客户端。历史客户端仍兼容旧登录方式：
`allow_user_vkey_login=true` 时，配置过 `WebUserName`/`WebPassword` 的客户端使用
该凭据；两项均为空时可使用用户名 `user` 与该客户端 `VerifyKey` 登录。新部署应
关闭旧 VKey 登录并使用独立用户体系，详见[用户体系](../user.md)。

## 用户注册功能

将 `allow_user_register=true` 后登录页会出现注册入口。注册成功后创建独立用户并写入
`users.json`，不会再把面板用户名和密码写入客户端记录。

注册页和管理员 API 均支持以下默认规则：

- `register_max_clients`：新用户默认客户端配额，`0` 表示不限；
- `register_max_tunnels`：新用户默认隧道总配额，域名反代也计入，`0` 表示不限；
- `register_max_conn`：新用户全部客户端共享的并发连接上限，`0` 表示不限；
- `register_upload_limit`：新用户全部客户端共享的上行带宽，单位 KB/s，`0` 表示不限；
- `register_download_limit`：新用户全部客户端共享的下行带宽，单位 KB/s，`0` 表示不限；
- `register_expire_days`：新用户从注册成功时起默认有效天数，默认 `0`（永久有效）；
- `register_captcha`：是否要求图片验证码；
- `register_rate_window`、`register_rate_limit`、`register_ip_rate_limit`：注册限频；
- `register_email_verify`、SMTP 参数和邮箱验证码参数：可选邮箱验证。

公开注册受 `allow_user_register` 控制；管理员通过 `POST /api/v1/users/register`
创建账号时不受该公开注册开关限制。完整字段、SMTP 示例和迁移说明见
[服务端配置](./server_config.md#注册与邮箱验证)。所有注册参数也可在
「系统 → 全局参数」的「用户注册」Tab 中维护。

## 监听指定 IP

NPS 支持每个隧道监听不同的服务端 IP。`nps.conf` 中设置
`allow_multi_ip=true` 后，可在 Web 面板控制，或在 NPC 配置文件中指定：

```ini
server_ip=xxx
```

## 客户端到期时间

在创建 / 修改客户端时可填写「到期时间」（可留空表示跟随所属用户；用户也未设置时表示永不过期）。客户端到期时间不能晚于所属用户到期时间；到期后该客户端会被**自动暂停**，所有隧道停止服务，直到管理员手工延长或清空到期时间并重新启用客户端。用户到期会同时暂停该用户的所有客户端和隧道。
支持格式：`2006-01-02 15:04:05` / `2006-01-02 15:04` / `2006-01-02T15:04:05` / `2006-01-02T15:04` / `2006-01-02`。

## 首次启动随机凭据

为避免默认密码被恶意扫描，**首次启动时**用户名默认为 `admin`，`web_password`、`auth_key`、`auth_crypt_key` 会随机生成，并直接打印到终端：

```text
[INFO] Web login username: admin, password: <xxxxxxxx>
[INFO] auth_key: <xxxxxxxx>
[INFO] auth_crypt_key: <xxxxxxxxxxxxxxxx>
```

请在第一次启动时妥善记录。后续可在 `nps.conf` 中手工修改。



## 与 Nginx 配合

有时需要在云服务器上运行 Nginx，做静态缓存或多服务复用 80/443。
可把 `http_proxy_port` 设为非 80 端口（例如 `8010`），再由 Nginx 反代：

```
server {
    listen 80;
    server_name *.proxy.com;
    location / {
        proxy_set_header Host  $http_host;
        proxy_pass http://127.0.0.1:8010;
    }
}
```

如需 HTTPS 也由 Nginx 终结，将 `https_proxy_port` 留空关闭，由 Nginx
监听 443。下面使用当前推荐的 TLS 配置：

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name *.proxy.com;

    ssl_certificate     /etc/letsencrypt/live/proxy.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/proxy.example.com/privkey.pem;
    ssl_session_timeout 5m;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    location / {
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_pass http://127.0.0.1:8010;
    }
}
```

如果 NPS 前面还有其他可信代理，应只在该链路按实际架构传递真实 IP，并确保
每一层都覆盖而不是追加客户端自带的 `X-Forwarded-For`。

## Web 管理使用 HTTPS

可直接在 `nps.conf` 中设置 `web_open_ssl=true` 并配置 `web_cert_file`、
`web_key_file`；也可以在「系统 → 全局参数」的「证书设置」Tab 中打开
“管理面板 HTTPS”进行维护。证书替换后需重启 NPS。

## Web 使用 Caddy 代理

以下示例把管理面板发布到 `https://nps.example.com/nps`，并保留 `/nps`
路径。`nps.conf` 中同时设置：

```ini
web_base_url=/nps
```

Caddy 2 配置：

```caddyfile
nps.example.com {
    reverse_proxy /nps/* 127.0.0.1:8081
}
```

修改 `web_base_url` 或代理路径后重启 NPS，并清理浏览器缓存。不要再使用
Caddy v1 的 `proxy / ... { transparent }` 语法。
