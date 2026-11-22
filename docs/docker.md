# Docker 部署

推荐用 Docker Compose 部署 NPS 服务端，并把容器内 `/conf` 挂载到宿主机。从 v1.1.49 起，`nps.db`、WAL、SHM、单进程锁和启动配置都在这个目录，升级、重建容器必须保留整个目录。

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
```

启动：

```bash
docker compose up -d
docker logs nps --tail=50
```

全新安装会创建 `/conf/nps.conf` 和 `/conf/nps.db`，不需要执行旧数据迁移，也不会生成迁移备份。首次启动会在日志中输出管理员账号 `admin` 和随机管理员密码。容器内面板通过 `NPS_WEB_IP` 绑定，宿主机仅回环发布；请在宿主机本地访问或通过 HTTPS 反向代理转发：

```text
http://127.0.0.1:8081
```

注意：源码默认 `web_ip = 127.0.0.1`、`web_port = 8081`。Docker 示例通过 `NPS_WEB_IP=0.0.0.0` 让容器内的反向代理可达，但仍只把宿主机端口发布到回环地址。如果你把 `web_port` 改成 `8080`，Compose 也要改成 `"127.0.0.1:8080:8080"`。

## NPC 客户端

NPC 也可以用 Docker 运行。镜像入口就是 `npc`，启动参数和物理机二进制一致；正式构建会发布 `wushuo98/npc` 镜像。

完整的 NPC Docker 命令行、`npc.conf` 挂载、Compose、TLS CA、网络模式、日志排查和升级说明见：[NPC Docker 部署](client/docker.md)。

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
- `nps.db`
- `nps.db-wal` / `nps.db-shm`（运行中）
- `nps.db.lock`
- `backups/pre-sqlite-*.tar.gz` 和 `migration-report.json`（仅支持自动迁移的新版首次导入旧 JSON 时生成）
- TLS 证书等配置文件

升级前先停止容器，再备份整个挂载目录，示例见下节。不要只复制运行中的 `nps.db`，因为 WAL 可能仍含未合并的数据。停服执行 `nps -export-json=<独立目录>` 可另存 JSON 回滚数据，但它不替代包含证书、密钥等文件的完整备份。详见 [SQLite 数据存储与升级](server/sqlite-storage.md)。

## 更新镜像

`v1.1.52` 及后续版本包含 JSON → SQLite 自动迁移。停掉旧容器后正常启动即可导入旧 JSON。若固定使用已发布的 `1.1.49`、`1.1.50` 或 `1.1.51` 镜像，首次从旧 JSON 建库应按[对应版本的升级说明](upgrade-sqlite.md#版本选择)先执行 JSON 网关，而不是直接套用下列流程；已经迁移到 SQLite 的卷可直接升级。

```bash
cd /opt/nps
docker compose pull
docker compose stop nps
cp -a conf "conf.before-upgrade.$(date +%Y%m%d%H%M%S)"
docker compose up -d
docker compose logs --tail=100 nps
```

自动迁移会先在 `/conf/backups/` 生成并验证 `pre-sqlite-*.tar.gz`。用上面的绑定挂载时，归档在宿主机 `/opt/nps/conf/backups/`，不会随容器重建而消失。归档含密码、密钥和原始配置，请限制访问。导入后检查 `/opt/nps/conf/migration-report.json` 的 `status` 为 `success`，并核对实体数量。全新安装、已有数据库的正常更新不需要这项迁移检查。

不要给容器附加 `-conf_path=/conf`：镜像内的运行根目录是 `/`，默认已读取 `/conf`。升级失败时保留现有文件和归档，先检查[迁移说明](server/sqlite-storage.md#启动迁移)，不要删除数据库再强行导入。

## 镜像平台

Docker Hub：

- 服务端：[wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- 客户端：[wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

标签约定：普通代码推送只执行 CI，不会更新 Docker 镜像。只有正式 GitHub Release（推送与源码版本一致的 `vX.Y.Z` 标签）才会构建多架构镜像，并同时推送版本号标签（如 `1.1.49`）和 `latest`。因此 `latest` 表示最近一次正式发布，生产环境也可以固定使用版本号。NPC/NPS 的软件内在线更新独立读取 GitHub Releases 的正式版本号和校验文件，不依赖 Docker 标签。

当前 Docker CI 构建的平台：

- `linux/amd64`
- `linux/arm/v7`
- `linux/arm64`

这里只构建 Linux 容器镜像。Windows、macOS、FreeBSD 等平台使用 Release 中的二进制包。
