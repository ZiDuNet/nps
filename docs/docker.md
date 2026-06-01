# Docker 部署

## Docker Compose（推荐）

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
      - "80:80"       # HTTP
      - "443:443"     # HTTPS
      - "8024:8024"   # Bridge TCP
      - "8025:8025"   # Bridge TLS
      - "8080:8080"   # Web 管理面板
    volumes:
      - ./conf:/conf
```

```bash
docker compose up -d
docker logs nps | head -20  # 查看初始密码
```

## 手动 Docker 运行

```bash
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8025:8025 \
  -p 8080:8080 \
  -v /path/to/conf:/conf \
  wushuo98/nps
```

## 客户端 Docker

```bash
docker run -d --name npc \
  wushuo98/npc \
  -server=your-ip:8024 -vkey=your-key
```

TLS 模式：
```bash
docker run -d --name npc \
  wushuo98/npc \
  -server=your-ip:8025 -vkey=your-key -tls_enable=true
```

## Docker Hub

- 服务端：[wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- 客户端：[wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

支持平台：`linux/amd64`、`linux/arm`、`linux/arm64`

## 自定义配置

首次启动会自动生成 `conf/nps.conf`，修改后重启容器生效：

```bash
docker restart nps
```

## 端口说明

只映射实际使用的端口即可。例如只使用 TCP 隧道和 Web 管理：

```yaml
ports:
  - "8024:8024"   # Bridge
  - "8080:8080"   # Web 面板
  - "9001:9001"   # TCP 隧道端口
```
