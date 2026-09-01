# 快速开始

本章带你完成一次最常见的内网穿透：部署 NPS 服务端、登录 Web 面板、创建用户和客户端、启动 NPC、添加 TCP 隧道。

如果你还没有决定用 Docker、物理机二进制还是系统服务部署，请先看 [安装部署](install.md)。

## 1. 部署 NPS 服务端

### Docker 方式

```bash
mkdir -p /opt/nps/conf
cat > /opt/nps/docker-compose.yml << 'EOF'
version: '3.8'
services:
  nps:
    image: wushuo98/nps:latest
    container_name: nps
    restart: unless-stopped
    environment:
      NPS_WEB_IP: 0.0.0.0
    ports:
      - "80:80"
      - "443:443"
      - "8024:8024"
      - "8025:8025"
      - "127.0.0.1:8081:8081"
    volumes:
      - ./conf:/conf
EOF

cd /opt/nps
docker compose up -d
docker logs nps --tail=50
```

### 物理机二进制方式

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应平台的 `server` 包，解压后运行：

```bash
./nps
```

Linux 安装为系统服务：

```bash
sudo ./nps install
sudo nps start
```

Windows 使用管理员 CMD 或 PowerShell：

```cmd
nps.exe install
nps.exe start
```

首次启动会自动生成 `conf/nps.conf`，并在日志或终端中打印随机管理员密码。

## 2. 登录 Web 管理面板

浏览器打开：

```text
http://127.0.0.1:8081
```

使用首次启动打印的管理员账号和密码登录。管理员账号默认是 `admin`，密码首次启动随机生成。

管理员拥有全部权限，包括用户管理、客户端管理、隧道和域名管理、全局设置。普通用户只能看到管理员分配给自己的客户端和相关资源。

## 3. 创建用户和客户端

推荐管理方式：

1. 管理员进入「用户管理」，创建普通用户。
2. 进入「客户端」页面，创建客户端，并选择所属用户。
3. 一个用户可以关联多个客户端。
4. 用户登录后，可以看到自己名下的多个客户端，并管理对应隧道和域名规则。

如果只是个人使用，也可以不创建普通用户，所有客户端由管理员直接管理。

## 4. 启动 NPC 客户端

### 命令行 NPC

在 Web 面板客户端详情中复制 `VerifyKey`，然后在内网机器运行：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

启用 TLS：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

`-vkey` 支持多个密钥，用英文逗号分隔：

```bash
./npc -server=<服务器IP>:8024 -vkey=key1,key2,key3
```

Windows 使用 `npc.exe`：

```cmd
npc.exe -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

### NPC GUI

桌面用户可以使用 NPC GUI：

1. 在 Web 面板客户端详情中复制「快捷启动命令」。
2. 打开 NPC GUI。
3. 粘贴快捷启动命令并添加连接。
4. 点击连接。

GUI 不需要额外安装命令行 `npc`。它内置客户端连接逻辑。

### Docker NPC

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

## 5. 添加 TCP 隧道

以映射内网 SSH 为例：

1. 在 Web 面板进入对应客户端。
2. 点击「隧道」并新增 TCP 隧道。
3. 服务端端口填写 `9001`。
4. 内网目标填写 `127.0.0.1:22` 或内网主机地址。
5. 保存后访问：

```bash
ssh -p 9001 user@<服务器IP>
```

更多模式见 [隧道模式](tunnel.md)。

## 默认端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 反向代理入口 |
| 443 | HTTPS 反向代理入口 |
| 8024 | Bridge TCP，客户端连接 |
| 8025 | Bridge TLS，客户端加密连接 |
| 8081 | Web 管理面板 |

只开放实际使用的端口即可。例如只使用 Web 面板和 TCP 隧道时，可只开放 `8081`、`8024` 和对应隧道端口。
