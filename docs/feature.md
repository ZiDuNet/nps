# 功能一览

## 安全功能

### TLS 加密传输

客户端与服务端之间支持 TLS 加密。在 `nps.conf` 中设置 `tls_enable=true`，开启后自动在 `tls_bridge_port`（默认 8025）监听。

客户端连接：
```bash
./npc -server=ip:8025 -vkey=key -tls_enable=true
```

### IP 白名单与黑名单

- **IP 白名单**：在客户端配置中添加允许访问的 IP，或通过 IP 授权页面让用户自行添加
- **IP 黑名单**：全局黑名单，在 `nps.conf` 中配置，防止恶意扫描
- **IP 限制访问**：设置 `ip_limit=true` 后，仅注册 IP 可访问代理端口

### 站点保护

域名代理模式支持 HTTP Basic Auth 认证，在 Web 面板设置用户名和密码。

### Web 管理保护

同一 IP 连续登录失败超过 10 次，将在 1 分钟内禁止该 IP 再次尝试。

### 登录验证码

在 `nps.conf` 中设置 `open_captcha=true` 开启登录验证码。

## 流量与带宽控制

### 流量限制

按客户端设置流量总量限制（单位 MB），达到后拒绝服务。需在 `nps.conf` 设置 `allow_flow_limit=true`。

### 带宽限制

按客户端设置带宽限制（单位 KB/s）。需在 `nps.conf` 设置 `allow_rate_limit=true`。

### 连接数限制

按客户端设置最大连接数。需在 `nps.conf` 设置 `allow_connection_num_limit=true`。

### 隧道数限制

按客户端限制隧道数量。需在 `nps.conf` 设置 `allow_tunnel_num_limit=true`。

## 代理功能

### 负载均衡

TCP 隧道和域名代理支持负载均衡，内网目标填写多个地址（逗号分隔），自动轮询。

### 端口复用

`bridge_port`、`http_proxy_port`、`https_proxy_port`、`web_port` 可设置为同一端口，自动识别协议。

### Proxy Protocol

TCP 隧道支持 Proxy Protocol 协议传递真实客户端 IP。

### 端口白名单

限制可开启的端口范围：
```ini
allow_ports=9001-9009,10001,11000-12000
```

### 代理到服务端本地

设置 `allow_local_proxy=true`，可将请求转发到 NPS 服务器本地的服务。

## 域名代理功能

### 自定义 Header

支持新增或修改请求 Header。

### Host 修改

修改请求中的 Host 字段，适配内网站点。

### URL 路由

同一域名根据 URL 路径转发到不同内网服务。

### 泛解析

支持 `*.proxy.com` 格式的泛域名解析。

### 自动 HTTPS

自动将 HTTP 请求 301 跳转到 HTTPS。

### HTTPS 证书管理

在 Web 面板为每个域名单独上传证书，或指定证书文件路径。系统自动识别。

## 系统功能

### 流量数据持久化

设置 `flow_store_interval` 定时保存流量数据到磁盘。

### 系统信息监控

设置 `system_info_display=true`，Web 面板展示 CPU、内存、网络、连接数等实时图表。

### 多用户支持

- `allow_user_login=true`：允许多用户登录
- `allow_user_register=true`：允许用户注册

### 获取用户真实 IP

设置 `http_add_origin_header=true`，通过 `X-Forwarded-For` 和 `X-Real-IP` 获取。

### 热更新

Web 面板修改的配置实时生效，无需重启。

### 环境变量渲染

客户端支持环境变量替换配置文件中的参数：

```bash
export NPC_SERVER_ADDR=1.1.1.1:8024
export NPC_SERVER_VKEY=xxxxx
./npc  # 自动使用环境变量
```

### 健康检查

配置文件模式支持多节点健康检查，失败自动移除目标，恢复后自动加回。

### KCP 协议

设置 `bridge_type=kcp` 可启用 UDP 协议传输，降低延迟（适合专线/内网）。

### 断线重连

配置文件模式：
```ini
[common]
auto_reconnection=true
```

无配置文件模式默认自动重连。

### 通过代理连接

客户端可通过 SOCKS5/HTTP 代理连接服务端：
```bash
./npc -server=ip:8024 -vkey=key -proxy=socks5://user:pass@proxy:port
```
