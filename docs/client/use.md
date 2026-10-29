# 客户端使用

`npc` 运行在内网设备上，主动连接公网 NPS。常规部署推荐无配置文件模式：
客户端、隧道和域名规则都由 NPS Web 面板管理，NPC 只保存服务端地址和 VKey。

命令参数、配置文件字段、交互菜单和系统服务的完整口径见
[客户端配置与启动](../client_config.md)与
[NPC 配置文件参考](config-file.md)。

## 最短连接流程

1. 在 Web 面板「客户端」新增客户端，取得 `VerifyKey`。
2. 展开客户端详情，选择实际启用的 Bridge 入口。
3. 复制该入口的普通命令或快捷启动命令。
4. 在内网主机运行 NPC。

TCP 示例：

```bash
./npc -server=<NPS地址>:8024 -vkey=<VerifyKey>
```

TCP + TLS 示例：

```bash
./npc -server=<NPS地址>:8025 -vkey=<VerifyKey> -tls_enable=true
```

也可直接运行 `./npc`，粘贴 Web 面板生成的快捷启动命令。

## Bridge 协议

当前 NPC 支持：

| 面板入口 | 命令行要点 | 网络要求 |
| --- | --- | --- |
| TCP | `-type=tcp` | TCP |
| TCP + TLS | `-tls_enable=true` | TCP，使用 Bridge TLS 端口 |
| KCP | `-type=kcp` | UDP |
| WS | `-type=ws -bridge_path=/ws` | TCP 与 WebSocket Upgrade |
| WSS | `-type=wss -bridge_path=/ws` | TCP、TLS 与 WebSocket Upgrade |
| QUIC | `-type=quic -quic_alpn=nps` | UDP |
| HTTP/3 Bridge | `-type=quic -quic_alpn=<ALPN>` | HTTP/3 UDP 共用入口，需 `bridge_http3=true` |

只应使用 Web 面板实际展示、服务端实际监听的入口。`bridge_*_show=false`
只是隐藏命令，不会关闭监听。

快捷启动命令是 Base64：

- TCP 和 TCP + TLS 使用历史 v1 格式，旧 NPC 继续兼容。
- KCP、WS、WSS、QUIC 和 HTTP/3 使用 v2 JSON，需要 NPC/GUI `1.1.37`
  或更高版本。

TLS 客户端默认校验证书。自签名证书应使用 `-tls_fingerprint`；CA 证书链
应使用 `-tls_ca_file`，必要时配合 `-tls_server_name`。生产环境不要启用
`-tls_insecure_skip_verify=true`。

## 交互菜单与系统服务

在没有参数且未找到本地配置时，直接运行 `npc` 会进入菜单。菜单支持注册、
启动、停止、卸载、查看状态、查看日志、远程更新和直接粘贴快捷启动命令。

交互菜单注册的系统服务按协议区分：

```text
nps-client-<vkey>
nps-client-<vkey>-tls
nps-client-<vkey>-kcp
nps-client-<vkey>-ws
nps-client-<vkey>-wss
nps-client-<vkey>-quic
nps-client-<vkey>-http3
```

普通 TCP 保留无后缀的历史服务名。同一 VKey 安装多个协议时，服务名互不
覆盖；启动、停止或卸载时只填 VKey 且存在多个匹配项，程序会要求选择完整
服务名。

命令行安装仍可注册通用 `Npc` 服务：

```bash
sudo ./npc install -server=<NPS地址>:8024 -vkey=<VerifyKey>
sudo npc start
```

Windows 请在管理员 CMD 或 PowerShell 中执行 `npc.exe install`、`start`、
`stop` 和 `uninstall`。安装后程序使用稳定安装路径，不依赖当前解压目录。

## 删除客户端后的行为

管理员在 Web 面板删除客户端后，新版 NPS 会向在线 NPC 下发“注册已删除”
指令。NPC 停止被删除的连接和重连；旧版 NPC 不认识该指令，会继续尝试连接，
直到手动停止或升级。

系统服务模式只停止受影响的那条连接。仅当该进程内最后一条连接也因删除而
结束时，NPC 才尽力停止本机服务进程，但不会卸载服务注册。同一进程同时连接
多个 NPS 或使用多个协议时，删除其中一个客户端不会中断其他连接。

## GUI

桌面用户可使用 [NPC GUI 客户端](../gui.md)。GUI 在程序内直接调用客户端
核心，不是简单启动外部 `npc` 进程；它支持粘贴快捷命令、手动选择协议、查看
状态和用户级开机自启动。

## 日志与更新

系统服务日志按服务分别保存：

| 系统 | 默认位置 |
| --- | --- |
| Linux/macOS | `/var/log/npc-<vkey>[-协议].log` |
| Windows | `npc.exe` 所在目录 |

手动更新：

```bash
./npc update
./npc update-force
```

更新程序从 GitHub 最新正式 Release 下载当前平台产物，并校验 `checksums.txt`。
服务端也可在管理员允许时下发远程更新。更新需要当前 NPC 安装目录和可执行
文件具备写权限；系统服务通常应使用 `sudo` 或管理员终端执行。
