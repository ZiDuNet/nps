# Docker 部署

推荐用 Docker Compose 部署 NPS 服务端，并把容器内 `/conf` 挂载到宿主机。这个目录保存配置和 JSON 数据，升级、重建容器都要保留。

## NPS 服务端

```bash
mkdir -p /opt/nps/conf
cd /opt/nps
```

创建 `docker-compose.yml`：

```yaml
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
```

启动：

```bash
docker compose up -d
docker logs nps --tail=50
```

首次启动会在日志中输出管理员账号 `admin` 和随机管理员密码。Web 面板默认地址：

```text
http://<服务器IP>:8081
```

注意：源码默认 `web_port = 8081`。如果你把 `conf/nps.conf` 改成 `8080`，Compose 也要改成 `"8080:8080"`。

## NPC 客户端

NPC 也可以用 Docker 运行。镜像入口就是 `npc`，启动参数和物理机二进制一致。本项目保留了上游 `Dockerfile.npc` 的用法，并用于发布 `wushuo98/npc` 镜像。

普通 Bridge：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS Bridge：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

如果内网服务在宿主机上，容器里的 `127.0.0.1` 不是宿主机。Linux 可按需使用 `--network host`；Windows/macOS Docker Desktop 需要使用 Docker 提供的宿主机访问地址。

## 只开放必要端口

如果只使用 Web 面板和 TCP 隧道，不需要映射 80/443：

```yaml
ports:
  - "8081:8081"
  - "8024:8024"
  - "9001:9001"
```

如果使用域名代理，需要映射 `http_proxy_port` 和 `https_proxy_port`，默认是 80 和 443。

如果使用 TLS Bridge，需要映射 `tls_bridge_port`，默认是 8025。

## 配置和数据

容器内 `/conf` 包含：

- `nps.conf`
- `clients.json`
- `users.json`
- `tasks.json`
- `hosts.json`
- `global.json`
- TLS 证书等配置文件

升级前建议备份挂载目录：

```bash
cp -a /opt/nps/conf /opt/nps/conf.bak.$(date +%Y%m%d%H%M%S)
```

## 更新镜像

```bash
cd /opt/nps
docker compose pull
docker compose up -d
```

## 镜像平台

Docker Hub：

- 服务端：[wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- 客户端：[wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

当前 Docker CI 构建的平台：

- `linux/amd64`
- `linux/arm/v7`
- `linux/arm64`

这里只构建 Linux 容器镜像。Windows、macOS、FreeBSD 等平台使用 Release 中的二进制包。
