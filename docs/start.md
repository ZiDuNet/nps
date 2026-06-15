# 快速开始

本章带你完成一次完整的内网穿透：部署服务端、登录管理面板、创建客户端、启动客户端、添加隧道。

## 1. 部署服务端

### Docker 部署

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
      - "8080:8080"
    volumes:
      - ./conf:/conf
EOF

cd /opt/nps
docker compose up -d
docker logs nps | head -20
```

首次启动会自动生成 `conf/nps.conf`，并在日志中打印随机管理员账号和密码。

### 二进制运行

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应平台的服务端压缩包，解压后执行：

```bash
./nps
```

安装为系统服务：

```bash
sudo ./nps install
sudo nps start
```

Windows 下请使用管理员 CMD：

```cmd
nps.exe install
nps.exe start
```

## 2. 登录 Web 管理面板

浏览器打开：

```text
http://<服务器IP>:8080
```

使用首次启动打印的管理员账号和密码登录。

管理员拥有全部权限，包括用户管理、客户端管理、隧道和域名管理、全局设置。普通用户只能看到管理员分配给自己的客户端和相关资源。

## 3. 创建用户和客户端

推荐管理方式：

1. 管理员进入「用户管理」，创建普通用户。
2. 进入「客户端」页面，创建客户端，并选择所属用户。
3. 一个用户可以关联多个客户端。
4. 用户登录后，可以看到自己名下的多个客户端，并管理对应隧道和域名规则。

如果你只自己使用，也可以不创建普通用户，所有客户端由管理员直接管理。

## 4. 启动客户端

### 无配置文件模式

在 Web 面板客户端详情中复制 `VerifyKey`，然后运行：

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

### 交互式模式

直接运行 `npc` 或双击 `npc.exe`，按菜单输入快捷启动命令。

菜单支持：

- `1` 注册系统服务
- `2` 卸载系统服务
- `3` 启动系统服务
- `4` 停止系统服务
- `5` 更新客户端
- `0` 退出

### Docker 客户端

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

## 5. 添加隧道

以 TCP 映射内网 SSH 为例：

1. 在 Web 面板进入对应客户端。
2. 点击「隧道」并新增 TCP 隧道。
3. 服务端端口填写 `9001`。
4. 内网目标填写 `127.0.0.1:22` 或内网主机地址。
5. 保存后访问：

```bash
ssh -p 9001 user@<服务器IP>
```

更多模式见[隧道模式](tunnel.md)。

## 默认端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 反向代理入口 |
| 443 | HTTPS 反向代理入口 |
| 8024 | Bridge TCP，客户端连接 |
| 8025 | Bridge TLS，客户端加密连接 |
| 8080 | Web 管理面板 |

只开放你实际使用的端口即可。例如只使用 Web 面板和 TCP 隧道时，可只开放 `8080`、`8024` 和对应隧道端口。
