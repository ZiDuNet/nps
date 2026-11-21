# SQLite 升级说明

本说明适用于从 JSON 数据存储升级到 v1.1.49 及后续版本。带本版本操作清单的发布说明见 [1.1.49 升级说明](upgrade-1.1.49.md)。

## 兼容边界

- NPC 的 Bridge 握手、控制消息和代理协议不变，不需要为 SQLite 升级 NPC。
- `nps.conf` 中的监听、端口、TLS、日志等启动参数继续有效。
- 客户端、用户、隧道、Host、全局参数和运行期业务配置迁移到 `conf/nps.db`。
- 旧 JSON 必须先由 `1.1.49` 二进制执行 JSON 网关，并具有版本为 `1.1.49`、覆盖全部数据文件的 `conf/.json-gateway` 标记。

## 升级步骤

1. 通知用户维护窗口，停止 NPS。
2. 备份整个 `conf`，不要只复制 `nps.conf`。
3. 使用 `1.1.49` 二进制完成 JSON 网关整形：

   ```bash
   ./nps -conf_path=/etc/nps -json-gateway
   ```

4. 使用同一个 `1.1.49` 二进制正常启动，完成 SQLite 首次导入。
5. 检查 `conf/migration-report.json`；只有 `status=success` 才继续验收。
6. 检查 `conf.backup.YYYYMMDD-HHMMSS` 已完整生成。
7. 验证登录、NPC、TCP/UDP、域名反代、用户级共享带宽、最大连接数、到期和停用状态。
8. 验证用户或客户端删除后，关联隧道与 Host 不再残留。

## 升级后文件

```text
conf/
├── nps.conf
├── nps.db
├── nps.db-wal
├── nps.db-shm
├── nps.db.lock
└── migration-report.json
```

运行中不要只复制 `nps.db`。一致备份应先停止服务，再复制整个 `conf`。

## 回滚

精确回滚可使用升级前自动生成的 `conf.backup.*`，但会丢失升级后的修改。

需要保留升级后 SQLite 数据并回到 JSON 版本时，停止 NPS 后执行：

```bash
./nps -conf_path=/etc/nps -export-json=/var/backups/nps-json
```

导出目录包含旧版 JSON、`.json-gateway`、合并 SQLite 业务配置的 `nps.conf` 和导出报告。若 SQLite 中的用户配额无法被旧 JSON 无损表达，导出会失败并给出具体用户字段。

## 验收失败

- 缺少或旧于 1.1.49 的网关标记：不要直接启动 SQLite，使用 1.1.49 二进制完成整形。
- 导入校验失败：从备份恢复原 JSON，修正冲突记录后重新网关处理。
- 数据库已提交但归档失败：保留 `nps.db` 和迁移报告，检查 `conf` 写权限后再启动，不要删除数据库。
- 导出失败：保留 SQLite 和原备份，报告位于导出根目录；不要用半成品覆盖生产目录。

完整说明见 [SQLite 数据存储与升级](server/sqlite-storage.md)。
