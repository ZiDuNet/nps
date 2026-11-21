# NPS 1.1.49 升级说明

v1.1.49 正式将 SQLite 作为业务数据权威存储。客户端、用户、隧道、Host、全局参数和运行期业务配置将写入 `conf/nps.db`；Bridge 握手、NPC 控制消息和既有 `/api/v1` 字段不变，旧 NPC 无需为本次数据迁移同步升级。

## 升级前准备

1. 安排维护窗口并停止 NPS。
2. 完整备份运行目录中的 `conf`。Docker 部署必须备份包含 `/conf` 的宿主机挂载目录。
3. 确认磁盘至少有容纳一份完整 `conf` 备份和 SQLite 数据库的空间。
4. 确认准备使用同一份 v1.1.49 `nps` 二进制完成网关和启动，不要混用旧版本。

```bash
sudo systemctl stop nps
cp -a /etc/nps/conf "/etc/nps/conf.before-1.1.49.$(date +%Y%m%d-%H%M%S)"
```

## JSON 到 SQLite

1. 使用 v1.1.49 二进制执行一次网关整形：

   ```bash
   ./nps -conf_path=/etc/nps -json-gateway
   ```

2. 确认 `conf/.json-gateway` 的版本为 `1.1.49`，且覆盖现有客户端、用户、隧道、Host 和全局配置文件。
3. 使用同一个二进制正常启动 NPS。
4. 首次导入会在提交数据库前生成同级 `conf.backup.YYYYMMDD-HHMMSS`，随后校验实体 ID、归属和路由冲突，并在单个事务中提交。
5. 检查 `conf/migration-report.json`；只有 `status` 为 `success` 才继续验收。
6. 验证管理员/普通用户登录、NPC 上线、TCP/UDP 转发、域名反代、证书、用户共享带宽、最大连接数、到期和停用状态。
7. 至少执行一次客户端或用户级联删除，确认关联隧道与 Host 不残留。

若缺少网关标记、标记来自旧版本、记录损坏或校验冲突，服务会拒绝启动并保留原 JSON，不会创建 SQLite。此时从备份恢复，修复数据后重新执行网关；不要手工修改或伪造 `.json-gateway`。

## Docker

从旧 JSON 版本升级时，先备份整个挂载目录，再使用 1.1.49 容器执行一次网关：

```bash
cd /opt/nps
cp -a conf "conf.before-1.1.49.$(date +%Y%m%d%H%M%S)"
docker compose pull
docker compose stop nps
docker run --rm \
  -v /opt/nps/conf:/conf \
  wushuo98/nps:1.1.49 /nps -json-gateway
docker compose up -d
```

之后按原来的 Compose 配置运行。容器内运行根目录是 `/`，因此网关命令不要添加 `-conf_path=/conf`；服务启动后检查 `/opt/nps/conf/migration-report.json` 的 `status` 为 `success`。

全新安装可以直接使用完整目录挂载：

```bash
docker run -d --name nps \
  -v /opt/nps/conf:/conf \
  -p 80:80 -p 443:443 -p 8024:8024 -p 8025:8025 \
  wushuo98/nps:1.1.49
```

不要只挂载 `nps.conf`、`nps.db` 或单个 JSON 文件。运行中复制 SQLite 文件必须同时考虑 `nps.db-wal` 和 `nps.db-shm`；最稳妥的一致性备份仍是停服后复制整个 `conf`。数据库已经由 1.1.49 或更高版本创建后，后续升级不需要重复执行 JSON 网关。程序自动生成的 `conf.backup.*` 位于容器根目录，不在宿主机挂载中；Docker 回滚应使用升级前在宿主机保存的 `conf.before-1.1.49.*`。

## 回滚

要精确回到升级前，停止 NPS 并恢复对应的 `conf.backup.*` 或升级前手工备份。该方式会丢弃升级后的修改。

要保留升级后 SQLite 数据并回到 JSON 版本：

```bash
./nps -conf_path=/etc/nps -export-json=/var/backups/nps-json
```

检查导出根目录的 `migration-report.json.status` 为 `success`，再使用导出目录中的完整 `conf` 启动仍支持 JSON 的目标版本。不要直接把旧 JSON 写回原 SQLite 运行目录。

## 验收清单

- `GET /api/v1/info`、面板和 `npsctl version` 均显示 `1.1.49`。
- `conf/nps.db`、WAL、SHM 仅服务账号可读，磁盘空间充足。
- `conf/migration-report.json` 为成功状态，实体计数与源数据一致。
- 旧 NPC 可继续连接；需要新版管理能力时再按客户端发布说明升级 NPC。
- 已完成一次停服备份、一次启动导入和一次 `-export-json` 演练。

完整存储设计、文件权限与维护命令见 [SQLite 数据存储与升级](server/sqlite-storage.md)。
