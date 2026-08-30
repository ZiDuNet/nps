# 宝塔面板部署

## 安装宝塔面板

前往 [宝塔面板官网](https://www.bt.cn/new/download.html) 安装，推荐版本 9.2.0+。

登录面板，点击左侧 **Docker**，如提示未安装 Docker/Docker Compose，根据引导安装。

## Docker Compose 部署 NPS

在宝塔面板的 Docker 管理中，使用 Docker Compose 方式部署：

1. 创建目录 `/www/docker/nps`
2. 创建 `docker-compose.yml`：

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

3. 启动：`docker compose up -d`

## 配置

配置文件在 `/www/docker/nps/conf/nps.conf`，首次启动会自动生成。

Web 管理端口默认 `8081`。首次生成配置时查看日志获取管理员账号 `admin` 和随机管理员密码；已有配置请以 `conf/nps.conf` 为准：

```bash
docker logs nps | head -20
```

**注意：** 如果 80/443 端口被占用，修改 `nps.conf` 中的 `http_proxy_port` 和 `https_proxy_port`。

## 安装 NPS 客户端

客户端同样可以通过 Docker 运行：

```bash
docker run -d --name npc \
  wushuo98/npc \
  -server=your-ip:8024 -vkey=your-key
```

推荐使用无配置文件模式，所有配置在服务端 Web 面板管理。
