# NPC Docker 部署

NPC 是主动连接 NPS 的客户端容器。普通 TCP、KCP、TLS Bridge 隧道不需要给 NPC 映射公网端口；容器只需要能够访问 NPS 的 Bridge 地址，以及能够访问 `npc.conf` 中填写的内网目标。

镜像地址：[`wushuo98/npc`](https://hub.docker.com/r/wushuo98/npc)。本文同时介绍命令行模式、配置文件模式和 Docker Compose 模式。

## 选择启动方式

| 方式 | 适用场景 | 配置来源 |
| --- | --- | --- |
| 命令行模式 | 只连接一个或少量客户端，规则由 Web 面板管理 | `-server`、`-vkey` 等参数 |
| 配置文件模式 | 批量部署、静态规则、健康检查、端口范围映射 | 挂载的 `npc.conf` |
| Compose 模式 | 需要自动重启、固定挂载和可重复部署 | `docker-compose.yml` + `npc.conf` |

常规部署推荐使用命令行模式，客户端、隧道和 Host 规则统一在 NPS Web 面板维护。只有需要把规则纳入配置管理，或使用配置文件专属能力时，才使用 `npc.conf`。

## 最小启动：命令行模式

先在 NPS Web 面板创建客户端，复制该客户端的 `VerifyKey`，然后启动容器：

```bash
docker pull wushuo98/npc:1.1.34

docker run -d \
  --name npc \
  --restart unless-stopped \
  wushuo98/npc:1.1.34 \
  -server=<NPS地址>:8024 \
  -vkey=<VerifyKey>
```

使用 TLS Bridge 时，连接服务端的 TLS Bridge 端口并启用 TLS：

```bash
docker run -d \
  --name npc \
  --restart unless-stopped \
  wushuo98/npc:1.1.34 \
  -server=<NPS地址>:8025 \
  -vkey=<VerifyKey> \
  -tls_enable=true
```

多个客户端可以在同一个进程中用英文逗号分隔：

```bash
docker run -d \
  --name npc \
  --restart unless-stopped \
  wushuo98/npc:1.1.34 \
  -server=<NPS地址>:8024 \
  -vkey=<VerifyKey1>,<VerifyKey2>
```

`latest` 始终指向最近一次正式 Release，不会跟随普通 `master` 提交更新；生产环境建议固定使用版本号，升级时明确执行 `docker pull` 和容器重建。

## 使用 `npc.conf`

NPC 镜像不会自动生成配置文件。仓库中的 [`conf/npc.conf`](https://github.com/ZiDuNet/nps/blob/master/conf/npc.conf) 是参考模板，需要复制到宿主机后再修改。

### 1. 准备配置目录

```bash
mkdir -p /opt/npc/conf
cp conf/npc.conf /opt/npc/conf/npc.conf
```

至少修改 `[common]` 中的服务端地址和验证密钥：

```ini
[common]
server_addr=nps.example.com:8024
conn_type=tcp
vkey=replace-with-verify-key
auto_reconnection=true
```

完整字段和所有规则示例见 [NPC 配置文件参考](config-file.md)。

### 2. 挂载配置并启动

```bash
docker run -d \
  --name npc \
  --restart unless-stopped \
  -v /opt/npc/conf:/conf:ro \
  wushuo98/npc:1.1.34 \
  -config=/conf/npc.conf
```

配置文件模式下，规则节中的 `target_addr` 是从 NPC 容器网络命名空间访问的地址。例如：

```ini
[tcp_ssh]
mode=tcp
target_addr=192.168.14.20:22
server_port=10022
```

不要把容器内的 `127.0.0.1` 误认为宿主机地址；它只指向 NPC 容器自身。

## Docker Compose

创建 `/opt/npc/docker-compose.yml`：

```yaml
services:
  npc:
    image: wushuo98/npc:1.1.34
    container_name: npc
    restart: unless-stopped
    volumes:
      - ./conf:/conf:ro
    command: ["-config=/conf/npc.conf"]
```

准备 `/opt/npc/conf/npc.conf` 后启动：

```bash
cd /opt/npc
docker compose up -d
docker compose logs -f npc
```

NPC 只发起出站连接时不需要 `ports`。如果配置了 P2P/Secret 本地访问端，或其他需要在宿主机监听的本地端口，再显式增加端口映射。

## 容器网络与内网目标

### Linux：目标服务在宿主机上

如果 `target_addr` 是宿主机上的 `127.0.0.1:端口`，可使用 host 网络，让 NPC 和宿主机共享网络命名空间：

```bash
docker run -d \
  --name npc \
  --restart unless-stopped \
  --network host \
  -v /opt/npc/conf:/conf:ro \
  wushuo98/npc:1.1.34 \
  -config=/conf/npc.conf
```

Compose 写法：

```yaml
services:
  npc:
    image: wushuo98/npc:1.1.34
    container_name: npc
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./conf:/conf:ro
    command: ["-config=/conf/npc.conf"]
```

使用 `network_mode: host` 时不要再配置 `ports`。该模式主要适用于 Linux；Windows/macOS Docker Desktop 应使用宿主机可达地址（例如 Docker 提供的宿主机网关地址），不要直接照搬 `127.0.0.1`。

### 普通 Bridge 网络

如果目标服务在局域网其他设备上，直接填写 NPC 容器可以访问的局域网地址即可，例如 `192.168.14.20:8080`。同时确认：

- NPC 容器到目标地址的路由和防火墙允许访问。
- NPS 地址是 NPC 容器可以访问的公网或内网地址。
- 只给 NPS 放行必要的 Bridge 端口（通常为 `8024` 或 `8025`）。

## TLS、CA 和其他挂载文件

自签名证书或自定义 CA 需要把证书文件挂载进容器，并在 `npc.conf` 中使用容器内的绝对路径：

```yaml
services:
  npc:
    image: wushuo98/npc:1.1.34
    restart: unless-stopped
    volumes:
      - ./conf:/conf:ro
      - ./certs:/certs:ro
    command: ["-config=/conf/npc.conf"]
```

```ini
[common]
server_addr=nps.example.com:8025
vkey=replace-with-verify-key
tls_enable=true
tls_ca_file=/certs/nps-ca.pem
tls_server_name=nps.example.com
```

如果使用 SOCKS5 多账号认证或文件访问，也要将对应文件/目录挂载到容器内，并在配置中使用容器内路径：

```yaml
volumes:
  - ./conf:/conf:ro
  - ./share:/srv/share:ro
```

```ini
[socks_outbound]
mode=socks5
server_port=19009
multi_account=/conf/multi_account.conf

[file_share]
mode=file
server_port=19008
local_path=/srv/share
```

## 日志、状态与排查

NPC 镜像基于 `scratch`，容器内没有 `sh`、`curl` 等诊断命令。请使用 Docker 日志和宿主机工具排查：

```bash
docker ps --filter name=npc
docker logs --tail=100 npc
docker logs -f npc
docker inspect npc --format '{{.State.Status}} {{.State.ExitCode}}'
```

常见问题：

| 现象 | 优先检查 |
| --- | --- |
| 容器不断重启 | `docker logs npc`，重点看配置文件路径、地址和验证密钥 |
| `connection refused` | NPS 的 `bridge_port`/`tls_bridge_port`、安全组、防火墙和 DNS |
| 验证失败 | Web 面板中的 `VerifyKey` 是否属于当前客户端，是否误用了旧密钥 |
| 隧道已连接但访问失败 | `target_addr` 是否从容器网络可达，是否把宿主机服务误写成 `127.0.0.1` |
| TLS 握手失败 | `tls_enable`、端口、CA 路径、SNI 和证书指纹是否匹配 |
| 修改配置不生效 | 配置以只读挂载后，需要重建或重启容器 |

修改挂载的配置后执行：

```bash
docker compose up -d --force-recreate
```

## 升级与回滚

使用 Compose 时，先备份配置，再拉取目标版本并重建：

```bash
cd /opt/npc
cp -a conf conf.bak.$(date +%Y%m%d%H%M%S)
docker compose pull
docker compose up -d
```

回滚时把 `image` 改回上一个版本号，例如 `wushuo98/npc:1.1.26`，再执行 `docker compose up -d`。NPC 容器升级不会修改挂载的 `npc.conf`。

## 相关文档

- [客户端配置与启动](../client_config.md)
- [NPC 配置文件参考](config-file.md)
- [客户端使用](use.md)
- [服务端 Docker 部署](../docker.md)
