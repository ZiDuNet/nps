# 运行命令速查

本页只放运行和管理命令。首次部署、目录说明和注意事项请先看 [安装部署](install.md)。

## NPS 服务端

### 查看版本

```bash
./nps -version
```

### 直接运行

Linux/macOS：

```bash
./nps
```

Windows：

```cmd
nps.exe
```

直接运行时，配置目录是程序所在目录下的 `conf`。新安装的 Web 面板默认仅监听本机：

```text
http://127.0.0.1:8081
```

### 指定配置目录

```bash
./nps -conf_path=/data/nps
```

程序会读取：

```text
/data/nps/conf/nps.conf
```

### 系统服务

安装命令仍然可以使用。`install`、`start`、`stop`、`restart`、`uninstall` 是非交互命令，适合脚本、CI 或运维手册直接执行。

Linux/macOS：

```bash
sudo ./nps install
sudo nps start
sudo nps stop
sudo nps restart
sudo nps uninstall
```

Windows 需要管理员 CMD 或 PowerShell：

```cmd
nps.exe install
nps.exe start
nps.exe stop
nps.exe restart
nps.exe uninstall
```

### 交互菜单

`./nps -server` 不是安装命令本身，而是打开服务端交互管理菜单。菜单内部仍然可以执行安装、卸载、更新、启动、停止等操作。

```bash
./nps -server
```

菜单项：

| 输入 | 操作 |
|------|------|
| `1` | 安装 NPS，并立即尝试启动服务 |
| `2` | 停止并删除/卸载 NPS 服务 |
| `3` | 更新 NPS |
| `4` | 查看 NPS 服务状态 |
| `5` | 启动 NPS 服务 |
| `6` | 停止 NPS 服务 |
| `7` | 重启 NPS 服务 |
| `0` | 退出菜单 |

如果只是安装服务，`./nps install` 仍然是最直接的方式。交互菜单更适合不想记命令时手动管理服务。

注意：命令行安装和交互菜单安装的配置目录行为不同。直接执行 `./nps install` 会安装到系统默认目录，Linux 默认使用 `/etc/nps/conf`。交互菜单的 `1` 会以当前程序目录作为配置目录，并把 `-conf_path=<当前程序目录>` 写入服务参数。

## NPC 命令行客户端

### 查看版本

```bash
./npc -version
```

### 无配置文件模式

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

多个客户端：

```bash
./npc -server=<服务器IP>:8024 -vkey=key1,key2,key3
```

多个服务端：

```bash
./npc -server=nps-a.example.com:8024,nps-b.example.com:8025 \
  -vkey=key-a,key-b -type=tcp,tls
```

通过代理连接服务端：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey> \
  -proxy=socks5://user:pass@127.0.0.1:9007
```

### 配置文件模式

```bash
./npc -config=npc.conf
```

也可以用逗号分隔路径，同时启动多份独立配置：

```bash
./npc -config=/etc/nps/npc-a.conf,/etc/nps/npc-b.conf
```

未指定 `-config` 时，源码默认查找：

- Windows：程序目录下的 `conf/npc.conf`
- Linux/macOS：当前工作目录下的 `conf/npc.conf`

### 交互式菜单

```bash
./npc
```

如果没有传入 `-server`、`-vkey`，并且没有找到本地配置文件，`npc` 会进入交互菜单。菜单也支持直接粘贴 Web 面板生成的快捷启动命令。

菜单项：

| 输入 | 操作 |
|------|------|
| `1` | 注册系统服务，需要输入快捷启动命令；可一次输入多个，用英文逗号分隔 |
| `2` | 删除/卸载系统服务，输入 `vkey` 或完整服务名；会先停止服务再卸载 |
| `3` | 启动系统服务，输入 `vkey` 或完整服务名 |
| `4` | 停止系统服务，输入 `vkey` 或完整服务名 |
| `5` | 更新客户端 |
| `6` | 查看已注册的 `nps-client-*` 服务列表，并显示每个服务状态 |
| `7` | 查看指定 `vkey` 或完整服务名对应的状态 |
| `8` | 实时跟踪指定 NPC 服务日志（输入服务名或 `vkey`，按 `Ctrl+C` 返回） |
| `9` | 重启所有已注册 NPC 服务（需要输入 `y` 确认） |
| `0` | 退出菜单 |
| 快捷启动命令 | 不安装服务，直接启动连接；可一次输入多个，用英文逗号分隔 |

### 系统服务

命令行安装也仍然可以使用，适合明确知道 `server` 和 `vkey` 的场景：

```bash
sudo ./npc install -server=<服务器IP>:8024 -vkey=<VerifyKey>
sudo npc start
```

命令行安装会注册通用 `Npc` 服务，安装参数会作为服务启动参数保存。交互菜单的 `1` 更适合从 Web 面板复制快捷启动命令注册服务。它会按 VKey 和 Bridge 入口创建独立服务：普通 TCP 保留历史名称 `nps-client-<vkey>`，TCP + TLS、KCP、WS、WSS、QUIC、HTTP/3 分别追加 `-tls`、`-kcp`、`-ws`、`-wss`、`-quic`、`-http3`。只填 VKey 且匹配多个服务时，程序会要求选择完整服务名。完整规则见[客户端配置](client_config.md#系统服务)。

Linux 上请使用 `sudo` 执行安装。服务注册后会使用 `/usr/bin/npc` 或 `/usr/local/bin/npc` 的稳定路径，不依赖当前解压目录。

### NAT 类型检测

```bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
```

### P2P/Secret 本地访问端

Secret：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey> \
  -password=<Secret密码> -local_type=secret -local_port=2000
```

P2P：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey> \
  -password=<P2P密码> -target=<目标IP:端口> -local_type=p2p -local_port=2000
```

## NPC GUI 客户端

GUI 没有 `npc` 的命令行参数。启动桌面程序后添加连接：

1. 在 NPS Web 面板进入「客户端」。
2. 展开客户端详情，在「连接方式」中选择实际启用的 Bridge 入口。
3. 复制该入口的普通命令或快捷启动命令。
4. 打开 NPC GUI，粘贴快捷启动命令并添加。

也可以手动填写：

- 名称
- 服务端地址和端口，例如 `<服务器IP>:8024`
- `VerifyKey`
- Bridge 协议；WS/WSS 还需填写路径，QUIC 需填写 ALPN
- TLS 类入口可按需填写 Server Name、证书指纹或自定义 CA

完整字段、快捷命令 v1/v2 兼容范围和日志位置见 [GUI 客户端](gui.md)与
[客户端配置](client_config.md)。

## Docker

NPS 服务端：

```bash
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8025:8025 \
  -p 127.0.0.1:8081:8081 \
  -e NPS_WEB_IP=0.0.0.0 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps
```

宿主机通过 `http://127.0.0.1:8081` 访问管理面板，或在前置 HTTPS 反向代理中
转发该回环端口。

NPC 客户端：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

更多 Docker 说明见 [Docker 部署](docker.md)。
