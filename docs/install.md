# 安装

## 下载安装包

前往 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应系统版本。

服务端和客户端是单独的二进制文件。

## Docker 安装（推荐）

### docker-compose 一键部署

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

```bash
docker compose up -d
```

### 手动 Docker 运行

```bash
# 服务端
docker run -d --name nps \
  -p 80:80 -p 443:443 -p 8024:8024 -p 8025:8025 -p 8080:8080 \
  -v /path/to/conf:/conf \
  wushuo98/nps

# 客户端
docker run -d --name npc \
  wushuo98/npc \
  -server=your-ip:8024 -vkey=your-key
```

Docker Hub:
- 服务端: [wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- 客户端: [wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

## 从源码编译

需要 Go 1.24+

```bash
# 克隆仓库
git clone https://github.com/ZiDuNet/nps.git
cd nps

# 编译服务端
go build cmd/nps/nps.go

# 编译客户端
go build cmd/npc/npc.go

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
```
