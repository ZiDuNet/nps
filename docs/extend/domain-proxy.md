# 域名代理与路由

NPS 的域名代理按 HTTP 请求中的 `Host` 和可选的 URL 路径匹配规则。DNS 只负责把域名解析到 NPS 公网地址；请求到达 NPS 后才由 Host 规则决定转发目标。需要管理员统一提供泛解析子域名、证书续期热更新或查看路由决策时，阅读[平台泛域名、证书热更新与规则诊断](platform-domain.md)。

## 启用 HTTP 和 HTTPS 入口

在 `conf/nps.conf` 中配置入口端口，然后重启 NPS：

```ini
http_proxy_port=80
https_proxy_port=443
```

80 和 443 被其他服务占用时可以使用任意未占用端口：

```ini
http_proxy_port=30111
https_proxy_port=30443
show_http_proxy_port=true
```

此时访问 URL 必须带端口，例如 `http://app.example.com:30111/`。除 DNS 外，云安全组、宿主机防火墙和 Docker 网络策略都必须放行对应 TCP 端口。

## 创建域名规则

在 Web 面板的「域名」页面新增规则，至少填写：

| 字段 | 说明 |
| --- | --- |
| 域名 | 请求中的 Host，例如 `app.example.com`。 |
| 所属客户端 | 负责连接内网目标的 NPC；勾选本地转发时除外。 |
| 内网目标 | NPC 所在网络可达的 `IP:端口`，例如 `127.0.0.1:8081`。 |
| 协议 | `http`、`https` 或 `all`。 |
| 路径 | 可选；同一域名按路径转发时填写，例如 `/api`。 |

目标可分行填写多个地址，NPS 会轮流选择，用于简单负载均衡。需要按健康状态摘除目标时，使用 [NPC 配置文件参考](../client/config-file.md#健康检查)中的健康检查。

### 自定义域名与平台域名

管理员可在「全局参数」预置多个平台泛域名，例如 `*.example.com`。新增 Host 时可选择：

- **自定义域名**：填写完整域名、自己完成 DNS 解析，并按需填写自己的证书或私钥。
- **平台域名**：从管理员提供的泛域名中选择，填写一个唯一前缀。系统默认生成可修改的 8 位字母数字前缀，并锁定平台证书路径，普通用户不会接触服务器文件路径。

平台域名的 DNS 泛解析、前缀限制、证书状态、引用保护和兼容性见[平台域名说明](platform-domain.md)。

## DNS、泛域名和非标准端口

精确域名可在 DNS 中添加 A/AAAA 记录：

```text
app.example.com -> NPS 公网 IP
```

泛域名可添加一条通配记录，并在 NPS 使用通配 Host 规则：

```text
*.example.com -> NPS 公网 IP
```

```text
NPS Host: *.example.com
```

DNS 不会替代端口开放，也不会隐藏非 80/443 端口。即使 `*.example.com` 已正确解析，`http_proxy_port=30111` 时仍需访问 `http://sub.example.com:30111/`。

## 路径路由、Host 重写和 Header

同一域名可用路径区分目标，例如：

| Host | 路径 | 目标 |
| --- | --- | --- |
| `app.example.com` | `/api` | `127.0.0.1:8081` |
| `app.example.com` | `/static` | `127.0.0.1:8082` |

域名规则还可配置：

- **请求 Host**：将发给后端的 `Host` 改为后端虚拟主机需要的值。
- **自定义请求 Header**：每行使用 `名称: 值`，例如 `X-Environment: production`。
- **真实来源 Header**：启用 `http_add_origin_header=true` 后，NPS 会向后端追加 `X-Forwarded-For` 与 `X-Real-IP`。后端只能信任来自 NPS 或受控反向代理的这些 Header。
- **Basic 认证**：在对应客户端的认证配置中设置用户名和密码，可为域名、HTTP 正向代理和 SOCKS5 增加访问保护。

## HTTPS、证书和自动跳转

域名规则可直接粘贴证书/私钥，或填写 NPS 机器上的证书文件路径。上传证书时 NPS 终结 TLS，后端可以读取 HTTP Header；未上传证书时由内网服务处理 TLS，NPS 只转发加密流量。使用文件路径时，外部脚本原子替换有效证书和私钥后，新的 TLS 连接会自动加载；错误或不完整的新文件会回退到最近有效证书，不需要重启 NPS。详细续期脚本和限制见[证书热更新](platform-domain.md#证书续期与热更新)。

勾选「自动 HTTPS」后，HTTP 请求会 301 跳转到对应 HTTPS 地址。若 `https_proxy_port` 不是 443，跳转 URL 也会带上该端口。

当 Nginx 或 Caddy 负责公网 80/443 时，可让它保留 `Host` 并反代到 NPS 的内部 HTTP 端口。示例和注意事项见[服务端增强功能](../server/nps_extend.md#与-nginx-配合)。

## 转发到 NPS 服务器本机

若后端服务实际运行在 NPS 服务器上，而不是 NPC 所在内网，需要在 `nps.conf` 中开启：

```ini
allow_local_proxy=true
```

重启 NPS 后，在对应 Host 规则勾选「转发到本地」。两项缺一不可。NPS 使用 Docker `network_mode: host` 时，规则目标 `127.0.0.1:8081` 指向宿主机回环；默认 bridge 网络中则只指向 NPS 容器本身。

本地转发让规则创建者访问 NPS 主机可达的服务，只应开放给可信管理员。

## 未匹配规则与 404 页面

没有可用 Host 规则、所属客户端离线或目标不可达时，NPS 会返回代理错误页面。当前项目将默认页面编译进 `nps` 二进制，源码位置为 `web/static/page/error.html`。

如需定制默认 404 内容，请修改该源码文件并重新构建、替换 NPS 二进制；仅修改运行目录下释放出的 `web/static/page/error.html` 不会改变已运行进程使用的嵌入内容。页面不应包含密钥、内网地址、调试堆栈或管理入口信息。

## 排查顺序

1. 先在「域名」的“规则诊断”中输入实际 Host、路径和协议，确认命中的 Host 规则和选择的内网目标。
2. 检查启动日志是否有 `start http listener, port is <端口>`。
3. 从 NPS 机器本地带 Host 头验证：

   ```shell
   curl -i -H 'Host: app.example.com' http://127.0.0.1:30111/
   ```

4. 确认对应客户端在线、未到期，Host 规则已启用且目标地址从 NPC 网络可达。
5. 确认 DNS 指向 NPS 公网 IP，外部访问 URL 包含非标准端口。
6. 检查云安全组、系统防火墙、Docker 端口发布和前置反向代理。

更多代理模式、Secret 和 P2P 的说明见[隧道模式](../tunnel.md)。
