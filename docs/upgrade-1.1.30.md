# NPS 1.1.30 升级说明

本文说明从 1.1.29 或更早版本升级到当前工作区版本 1.1.30 时，NPC 网络配置的行为变化和兼容边界。

## 本版本变化

- `dns_server` 现在会真正应用到 NPS/NPC 的域名解析和需要解析目标地址的路径；多个服务器以逗号分隔，按轮询顺序尝试，失败时切换到下一个。未配置时继续使用操作系统解析器。
- `proxy_url`、`-proxy` 支持 HTTP/HTTPS CONNECT、SOCKS4、SOCKS4a、SOCKS5 和 SOCKS5H。未填写显式代理时，NPC 会读取 `ALL_PROXY`/`all_proxy`，并使用 `NO_PROXY`/`no_proxy` 绕过匹配地址。
- `local_ip` 现在会绑定 NPC 对 NPS 的 TCP、KCP、QUIC、WS/WSS 出站连接；启用 `local_ip_forward=true` 后，还会绑定转发到公网 IP/域名目标的 TCP、UDP 和 SOCKS5 UDP 出口。私网目标保持原来的系统路由，避免误改变内网访问路径。
- 显式设置本地地址时，QUIC/KCP 会使用独立 UDP socket，并在会话结束时回收；未设置时保持旧的默认拨号路径。

## 对旧部署的影响

- 不新增服务端端口，不改变 `nps.conf` 的默认值，也不要求现有 NPC 立即升级。服务端仍按 1.1.28/1.1.29 兼容边界处理握手和控制消息。
- 现有 `npc.conf` 中的 `proxy_url`、`local_ip`、`local_ip_forward` 和 `dns_server` 会自动被新客户端识别；不填写的字段不会改变旧行为。
- `local_ip_forward` 默认关闭。只有明确开启后才会影响转发出口，且仅对公网 IP 或域名目标生效。
- `proxy_url` 使用 HTTPS 代理时，NPC 会校验代理证书；自签名代理应通过系统信任链处理，不要通过关闭 TLS 校验规避问题。
- 如果设备不允许服务端下发升级，可在直接启动参数中设置 `-disable_remote_update=true`；配置文件模式继续使用 `[common]` 下的同名字段。

## 配置示例

```ini
[common]
server_addr=nps.example.com:8024
vkey=replace-with-verify-key
conn_type=tcp

# 可选：多个 DNS 服务器，逗号分隔
dns_server=1.1.1.1,8.8.8.8

# 可选：NPC 对外连接使用的本地地址
local_ip=192.168.14.20

# 可选：将同一个地址用于公网目标的转发出口
local_ip_forward=true

# 可选：HTTP、HTTPS、SOCKS4/4a/5 代理
proxy_url=socks5h://user:password@127.0.0.1:1080
```

命令行模式等价参数：

```bash
./npc -server=nps.example.com:8024 -vkey=<VerifyKey> \
  -dns_server=1.1.1.1,8.8.8.8 \
  -local_ip=192.168.14.20 -local_ip_forward=true \
  -proxy=socks5h://user:password@127.0.0.1:1080
```

## 升级与回滚

1. 先备份 NPS 的 `conf/` 目录和 NPC 配置文件。
2. 直接替换 NPC 二进制；服务端不需要因本版本变更而停机。
3. 如使用系统服务，按原服务名重启 NPC，并检查日志中的 DNS、代理和本地出口配置提示。
4. 如果新配置不适合当前网络，删除新增字段或设置 `local_ip_forward=false`，即可回到旧版默认路径；不需要恢复服务端 JSON 数据。

需要重新安装同版本正式包时，可执行 `./npc update-force`；普通升级仍使用 `./npc update`。

本文件描述的是当前工作区的 1.1.30 功能，正式发布前仍需通过项目的完整测试和发布流水线验证。
