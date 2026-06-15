# Docker 部署

推荐使用 Docker Compose 部署服务端，并将 `/conf` 挂载到宿主机，方便升级和备份。

## 服务端

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
      - "8080:8080"
    volumes:
      - ./conf:/conf
```

启动：

```bash
docker compose up -d
docker logs nps | head -20
```

首次启动会在日志中输出随机管理员账号和密码。

## 客户端

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS 模式：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

## 只开放必要端口

如果只使用 Web 面板和 TCP 隧道，不需要映射 80/443：

```yaml
ports:
  - "8080:8080"
  - "8024:8024"
  - "9001:9001"
```

如果使用域名代理，需要映射 `http_proxy_port` 和 `https_proxy_port`，默认是 80 和 443。

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

## Docker Hub

- 服务端：[wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- 客户端：[wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

支持平台：`linux/amd64`、`linux/arm`、`linux/arm64`。
