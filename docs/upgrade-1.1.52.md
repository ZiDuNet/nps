# NPS 1.1.52 升级说明

v1.1.52 修复 SQLite 首次迁移与后续升级链路，使 1.1.47、1.1.48 的旧 JSON 和
1.1.49 至 1.1.51 的 SQLite 都能升级到当前版本。菜单、状态、启动、停止、重启、
安装、卸载和更新命令不再抢占数据库；只有实际运行服务的 NPS 进程会打开数据库。

## 升级矩阵

| 来源 | v1.1.52 的处理 |
| --- | --- |
| 1.1.47、1.1.48 JSON | 正常启动时先在 `conf/backups/` 生成并验证完整迁移归档，再自动整形、校验和导入 SQLite；成功后清理已备份的业务 JSON 与迁移临时文件，精简 `nps.conf`。无需手工运行 `-json-gateway`。 |
| 1.1.49、1.1.50、1.1.51 SQLite | 直接使用 `conf/nps.db`，执行同版本 schema 检查和设置兼容；不重复导入残留 JSON，不删除历史备份。 |
| 全新安装 | 直接创建 `conf/nps.db`、`nps.db.lock` 和精简 `conf/nps.conf`；不生成 `.json-gateway`、迁移报告或迁移归档。 |
| Docker 挂载旧 JSON | 首次启动新镜像时按“旧 JSON”处理；必须挂载整个 `/conf`，备份和数据库都留在该卷内。 |

## 升级前

1. 选择维护窗口并停止旧 NPS。不要同时运行两个服务进程。
2. 备份整个 `conf`。运行中的 SQLite 不能只复制单个 `nps.db`，因为变更还可能位于
   `nps.db-wal`；最稳妥的方式是停服后复制整个目录。
3. 确认磁盘能同时容纳旧 JSON、迁移归档、临时文件和 SQLite。
4. 检查 `conf`、证书、日志和外部 `include` 引用的备份和权限。迁移归档含密钥和历史
   配置，不能放在站点公开目录。

```bash
sudo nps stop
sudo cp -a /etc/nps/conf "/etc/nps/conf.manual-backup.$(date +%Y%m%d-%H%M%S)"
```

## 二进制升级

直接将服务端二进制替换为 v1.1.52，再用原配置目录启动：

```bash
sudo systemctl stop Nps
sudo install -m 0755 ./nps /usr/local/bin/nps
sudo systemctl start Nps
sudo journalctl -u Nps -n 200 --no-pager
```

如果使用菜单安装或更新，`./nps -server` 只展示管理菜单，不会尝试打开数据库。菜单更新
会读取系统服务注册的真实程序路径，完成下载与校验后替换该程序。它不能替代服务账号的
写权限；若当前账户无权替换，保留原服务并改用安装账户手工更新。替换后程序会等待新服务
取得 SQLite 锁，并以新进程实际使用的 `web_ip`、`web_port`、`web_open_ssl` 和
`web_base_url` 请求 `/api/v1/` 校验版本。启动或健康检查失败时会停止新进程、恢复
`.pre-update` 中的旧程序并重新启动旧服务；目录落盘失败同样先尝试恢复，无法确认恢复
落盘时保留回滚备份，不会静默删除。

## Docker 升级

```bash
docker compose stop nps
sudo cp -a /path/to/conf "/path/to/conf.manual-backup.$(date +%Y%m%d-%H%M%S)"
docker compose pull
docker compose up -d
docker logs nps --tail=200
```

必须确认 `/conf` 挂载的是完整旧目录，而不是单独文件。不要为了“清理”而删除
`clients.json`、`users.json`、`tasks.json`、`hosts.json` 或 `global.json`；是否清理由
迁移成功后的备份校验决定。

## 升级后检查

1. `./nps -version`、面板 `GET /api/v1/` 和 `npsctl version` 显示 `1.1.52`。
2. 从旧 JSON 升级时，启动日志包含自动迁移完成信息；检查
   `conf/backups/pre-sqlite-*.tar.gz` 和 `conf/migration-report.json`，确认
   `status=success`，且客户端、用户、隧道、Host 和设置计数符合预期。
3. 确认 `conf/nps.db`、`nps.db-wal` 和 `nps.db-shm` 均可由服务账号读写；`nps.db.lock`
   是可重建锁文件，不是业务数据。
4. 验证登录、旧 NPC 上线、TCP/UDP、域名反代、用户归属、配额和停用状态。
5. 在服务运行中执行 `./nps -server` 进入菜单、查看状态和退出；不应出现“数据库已被
   另一个 NPS 进程占用”。

## 失败与回滚

- 在入库前失败：程序保留原 JSON，不发布半成品数据库。按报告修正磁盘、权限或数据后重试。
- 数据库已提交但收尾失败：保留 SQLite 与迁移归档，重新启动会用原归档重试收尾；不要
  删除 `nps.db` 或用旧 JSON 覆盖它。
- 需要精确回滚时，停止 NPS，在独立目录解压升级前备份或
  `conf/backups/pre-sqlite-*.tar.gz`，用原版本程序验证后再切换。该方式会丢失升级后的
  新修改。
- 需要保留 SQLite 当前业务修改再回到 JSON 时，使用
  `./nps -conf_path=<运行目录> -export-json=<独立输出目录>`，不要直接写回原目录。

## 兼容性

Bridge 握手、NPC 控制消息、快捷命令和既有 `/api/v1` 字段不变；旧 NPC 可继续连接。
SQLite schema 版本仍为 1。1.1.49、1.1.50 的数据库和配置没有 API HMAC 管理项，升级后
继续使用安全的运行时默认值（HMAC 开启、窗口 60 秒、旧 MD5 关闭），不会虚构数据库行。
1.1.51 已发布 HMAC 行项目但尚未把它们作为 SQLite 业务配置管理，因此升级时从未删除的
`nps.conf` 导入并持久化；如果数据库中已有显式 HMAC 行，则数据库值优先。旧 `npsctl`
仍遵循 1.1.51 的兼容与升级边界，不应长期启用旧 MD5 签名。
