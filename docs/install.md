# 安装部署

本项目实际有 3 个可运行组件：

| 组件 | 程序 | 运行位置 | 主要作用 |
|------|------|----------|----------|
| NPS 服务端 | `nps` | 公网服务器、VPS、云主机、Docker 容器 | 提供 Web 管理面板、Bridge 连接入口、隧道监听端口和域名代理入口 |
| NPC 命令行客户端 | `npc` | 内网机器、路由器、NAS、Windows 主机、Docker 容器 | 连接 NPS，把内网服务转发到服务端 |
| NPC GUI 客户端 | `npc-gui` | Windows/macOS/Linux 桌面系统 | 图形化管理并启动 NPC 连接，适合桌面用户 |

通常只需要在公网服务器部署 1 个 `nps`，然后在每台内网设备上运行 `npc` 或 NPC GUI。

## 选择运行方式

| 方式 | 适合场景 | 配置和数据位置 | 启停方式 |
|------|----------|----------------|----------|
| Docker 运行 NPS | 推荐新部署、方便升级回滚 | 宿主机挂载目录，例如 `/opt/nps/conf`，容器内是 `/conf` | `docker compose up -d` |
| 物理机直接运行 NPS | 临时测试、手动启动 | 当前程序目录下的 `conf` | `./nps` 或 `nps.exe` |
| 物理机系统服务运行 NPS | 长期运行、开机自启 | Linux：`/etc/nps/conf`；Windows：`C:\Program Files\nps\conf` | `nps start`、`nps stop` |
| 命令行运行 NPC | 服务器、NAS、路由器、无桌面环境 | 无配置文件模式不落本地配置 | `./npc -server=... -vkey=...` |
| 系统服务运行 NPC | 内网机器长期在线 | 服务参数写入系统服务，日志写入文件 | `npc install` 或交互式菜单 |
| NPC GUI | 桌面环境手动管理连接 | 用户配置目录下的 `npc/npc_data.json` | 在 GUI 内点击连接/断开 |

## 端口准备

源码默认配置以 `conf/nps.conf` 和内置 `defaultNpsConf` 为准：

| 端口 | 配置项 | 默认值 | 用途 |
|------|--------|--------|------|
| 80 | `http_proxy_port` | `80` | HTTP 域名代理入口 |
| 443 | `https_proxy_port` | `443` | HTTPS 域名代理入口 |
| 8024 | `bridge_port` | `8024` | NPC 普通 TCP 连接入口 |
| 8025 | `tls_bridge_port` | `8025` | NPC TLS 连接入口 |
| 8081 | `web_port` | `8081` | Web 管理面板 |

只开放实际使用的端口即可。例如只使用 Web 面板和 TCP 隧道，通常需要放行 `8081`、`8024` 和你创建的隧道端口。使用域名代理时再放行 80/443。

## NPS 服务端：Docker 部署

Docker 部署必须挂载 `/conf`。`clients.json`、`users.json`、`tasks.json`、`hosts.json` 和 `nps.conf` 都在这里，容器重建后是否丢数据就看这个目录有没有保留。

```bash
mkdir -p /opt/nps/conf
cat > /opt/nps/docker-compose.yml << 'EOF'
version: '3.8'
services:
  nps:
    image: wushuo98/nps:latest
    container_name: nps
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "8024:8024"
      - "8025:8025"
      - "8081:8081"
    volumes:
      - ./conf:/conf
EOF

cd /opt/nps
docker compose up -d
docker logs nps --tail=50
```

首次启动如果 `/opt/nps/conf/nps.conf` 不存在，程序会自动生成配置，并在日志中打印：

- Web 管理员账号：默认 `admin`
- Web 管理员密码：随机生成
- `auth_key`
- `auth_crypt_key`

浏览器访问：

```text
http://<服务器IP>:8081
```

如果你把 `web_port` 改成其他端口，Compose 里的端口映射也要一起改。

## NPS 服务端：Linux/macOS 直接运行

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应平台的服务端包，例如：

- Linux x86_64：`linux_amd64_server.tar.gz`
- Linux arm64：`linux_arm64_server.tar.gz`
- macOS Intel：`darwin_amd64_server.tar.gz`
- macOS Apple Silicon：`darwin_arm64_server.tar.gz`

解压并运行：

```bash
tar -zxvf linux_amd64_server.tar.gz
chmod +x nps
./nps
```

直接运行时，配置目录是当前程序目录下的 `conf`。首次启动会自动生成 `conf/nps.conf`，并打印随机管理员密码。

## NPS 服务端：Linux 系统服务

在 `nps` 所在目录执行：

```bash
sudo ./nps install
sudo nps start
```

这组命令仍然可用，是非交互安装方式，适合脚本化部署。

常用管理命令：

```bash
sudo nps stop
sudo nps restart
sudo nps uninstall
sudo nps update
```

上面的 `install`、`start`、`stop`、`restart`、`uninstall`、`update` 都是非交互命令。安装命令没有废弃，仍然适合脚本化部署和服务器运维。

安装为系统服务后，配置目录不再使用当前目录，而是：

```text
/etc/nps/conf/nps.conf
```

日志默认写入：

```text
/var/log/nps.log
```

如果启动失败，先停止服务，再直接运行 `nps` 查看终端错误。

也可以进入服务端交互菜单：

```bash
./nps -server
```

交互菜单包含：

- `1` 安装 NPS，并立即尝试启动服务
- `2` 删除/卸载 NPS
- `3` 更新 NPS
- `4` 查看状态
- `5` 启动 NPS
- `6` 停止 NPS
- `7` 重启 NPS
- `0` 退出

这个菜单不是替代安装命令，而是把常用服务操作集中到一个入口里。已经熟悉命令行的场景，继续使用 `./nps install` 即可。

注意：`./nps install` 会安装到系统默认目录，Linux 默认配置目录是 `/etc/nps/conf`。`./nps -server` 菜单里的「安装 NPS」会以当前程序目录作为配置目录，并把 `-conf_path=<当前程序目录>` 写入服务参数。

## NPS 服务端：Windows 运行

下载 `windows_amd64_server.tar.gz` 或 `windows_386_server.tar.gz`，解压后会得到 `nps.exe`。

直接运行：

```cmd
nps.exe
```

直接运行时，配置目录是 `nps.exe` 所在目录下的 `conf`。

安装系统服务需要使用管理员 CMD 或 PowerShell：

```cmd
nps.exe install
nps.exe start
```

常用管理命令：

```cmd
nps.exe stop
nps.exe restart
nps.exe uninstall
nps.exe update
```

安装为系统服务后，配置目录是：

```text
C:\Program Files\nps\conf\nps.conf
```

## NPC 命令行客户端：无配置文件模式

推荐普通用户使用无配置文件模式。客户端、隧道和域名规则都在 NPS Web 面板维护，NPC 只需要 `server` 和 `vkey`。

先在 Web 管理面板创建客户端，复制该客户端的 `VerifyKey`，然后在内网机器运行：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

启用 TLS Bridge：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

一个 `npc` 进程可以连接多个客户端，多个 `vkey` 用英文逗号分隔：

```bash
./npc -server=<服务器IP>:8024 -vkey=key1,key2,key3
```

Windows 下命令相同，只是程序名为 `npc.exe`：

```cmd
npc.exe -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

## NPC 命令行客户端：系统服务

命令行安装仍然可用：

```bash
sudo ./npc install -server=<服务器IP>:8024 -vkey=<VerifyKey>
sudo npc start
```

这是非交互安装方式，适合只绑定一个 `server` 和一个 `vkey` 的客户端。安装后会注册通用 `Npc` 服务。

直接执行 `npc` 或双击 `npc.exe`，如果没有传入 `-server`、`-vkey`，并且没有找到本地配置文件，会进入交互式菜单。源码中菜单提供：

- `1` 注册系统服务
- `2` 删除/卸载系统服务
- `3` 启动系统服务
- `4` 停止系统服务
- `5` 更新客户端
- `6` 查看已注册服务列表
- `7` 查看服务状态
- `0` 退出

交互菜单不是安装命令本身。它是客户端管理入口，适合从 Web 面板复制快捷启动命令后注册、删除、启动、停止或查看客户端服务。菜单也支持直接粘贴 Web 面板客户端列表里生成的「快捷启动命令」；如果选择 `1` 注册系统服务，建议粘贴快捷启动命令。服务名按 `vkey` 生成，格式为：

```text
nps-client-<vkey>
```

菜单里的 `2` 会先停止服务再卸载服务。菜单里的 `6` 会扫描当前系统中已经注册的 `nps-client-*` 服务，并显示状态；菜单里的 `7` 用于查看指定 `vkey` 对应的服务状态。

Linux/macOS 日志默认在 `/var/log/npc.log`，Windows 日志默认在 `npc.exe` 所在目录。

## NPC 命令行客户端：Docker 运行

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

如果内网服务在宿主机上，注意容器网络和宿主机网络不是一回事。Linux 可以按需使用 `--network host`，Windows/macOS Docker Desktop 需要使用对应的宿主机访问地址。

## NPC GUI 客户端

NPC GUI 是 Wails 桌面程序。它不是简单启动外部 `npc` 进程，而是在程序内直接调用客户端核心逻辑连接 NPS。

GUI 适合桌面系统：

- Windows：需要 WebView2 Runtime，Windows 10/11 通常已内置。
- macOS：下载对应 `darwin_amd64` 或 `darwin_arm64` GUI 包。
- Linux：需要桌面环境和 WebKitGTK 运行库，发布包目前以 `linux_amd64` 为主。

GUI 支持两种添加方式：

- 粘贴 Web 面板生成的「快捷启动命令」。
- 手动填写名称、服务端地址、`VerifyKey` 和是否启用 TLS。

GUI 配置和日志保存在当前用户目录：

| 系统 | 配置文件 | 日志目录 |
|------|----------|----------|
| Windows | `%APPDATA%\npc\npc_data.json` | `%APPDATA%\npc\logs` |
| macOS | `~/Library/Application Support/npc/npc_data.json` | `~/Library/Application Support/npc/logs` |
| Linux | `~/.config/npc/npc_data.json` | `~/.config/npc/logs` |

开机自启动也是用户级的：

- Windows：写入当前用户注册表 `Software\Microsoft\Windows\CurrentVersion\Run`。
- macOS：写入 `~/Library/LaunchAgents/com.nps.client.plist`。
- Linux：写入 `~/.config/autostart/nps-client.desktop` 或 `$XDG_CONFIG_HOME/autostart/nps-client.desktop`。

## 升级和备份

升级前先备份配置和 JSON 数据。

Docker：

```bash
cp -a /opt/nps/conf /opt/nps/conf.bak.$(date +%Y%m%d%H%M%S)
cd /opt/nps
docker compose pull
docker compose up -d
```

物理机服务端：

```bash
sudo nps stop
sudo cp -a /etc/nps/conf /etc/nps/conf.bak.$(date +%Y%m%d%H%M%S)
# 替换 nps 可执行文件
sudo nps start
```

Windows 服务端建议先复制 `C:\Program Files\nps\conf` 作为备份，再替换 `nps.exe`。

## 常见检查

- Web 面板打不开：检查 `web_port`，默认是 `8081`，并确认安全组/防火墙放行。
- NPC 连不上：检查 `bridge_port` 或 `tls_bridge_port`，确认 `VerifyKey` 属于正确客户端。
- Docker 重建后数据没了：检查是否挂载了 `/conf`，以及宿主机目录是不是同一个。
- TLS 连接失败：服务端 `tls_enable=true` 时，客户端使用 `-tls_enable=true` 并连接 `tls_bridge_port`。
- 域名代理不通：域名必须解析到 NPS 服务端公网 IP，并放行 80/443 或你配置的 HTTP/HTTPS 代理端口。

更多内容：

- [快速开始](start.md)
- [运行命令速查](run.md)
- [服务端配置](server_config.md)
- [客户端配置](client_config.md)
- [GUI 客户端](gui.md)
- [Docker 部署](docker.md)
- [升级迁移](migrate.md)
