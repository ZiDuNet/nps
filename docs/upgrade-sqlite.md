# SQLite 升级说明

## 版本选择

- **v1.1.52 及后续版本**：旧 JSON 在 NPS 正常启动时自动导入。全新安装直接初始化精简的 `nps.conf` 和 SQLite，不执行迁移、不留迁移备份或报告；已有 SQLite 不重复导入。
- **已发布的 v1.1.49、v1.1.50、v1.1.51**：首次从 JSON 导入仍须先运行 `-json-gateway`，见 [v1.1.49 升级说明](upgrade-1.1.49.md)、[v1.1.50 升级说明](upgrade-1.1.50.md)和 [v1.1.51 升级说明](upgrade-1.1.51.md)。

SQLite 迁移仅影响服务端数据存储，不变更 Bridge 握手、NPC 配置或代理协议。现有 NPC 无需跟随升级。

## 使用支持自动迁移的新版升级

1. 通知用户维护窗口，停止 NPS。
2. 独立备份整个 `conf`，不要只复制 `nps.conf` 或活跃的 `nps.db`。
3. 替换 NPS 二进制或镜像，使用原配置目录正常启动。程序先生成并验证 `conf/backups/pre-sqlite-*.tar.gz`，再导入业务数据和设置。
4. 检查启动日志、归档和 `conf/migration-report.json`：`status=success`，实体及设置数量符合预期。已备份并确认的旧文件会被清理；`nps.conf` 留下启动参数，证书、密钥、历史用量和日志不受影响。
5. 验证登录、NPC、实际使用的代理协议、用户配额及停用状态。Docker 发布必须挂载整个 `/conf`。

若在入库前失败，修复报告中的问题后用原文件重试；若数据库已提交，仅收尾失败，保留数据库和同一份归档，检查权限或空间后重启。不要删除数据库，也不要用旧 JSON 覆盖它。

如果上次升级中断留下了空 `nps.db`，而目录中仍有旧 JSON，新版会停止启动并保留两份数据。先停服备份整个 `conf`，再在独立目录从旧 JSON 验证恢复，不要直接覆盖原目录。

## 回滚

精确回滚可使用独立停服备份，或核对自动迁移的 `conf/backups/pre-sqlite-*.tar.gz` 后在单独目录恢复。回滚到升级前不会保留升级后的修改。

需要保留升级后 SQLite 数据并回到 JSON 版本时，停止 NPS 后执行：

```bash
./nps -conf_path=/etc/nps -export-json=/var/backups/nps-json
```

新版导出目录含旧单位 JSON、合并 SQLite 设置的 `nps.conf` 和导出报告；不包含作为 SQLite 单位凭证的 `.json-gateway`。若要用 v1.1.49、v1.1.50 或 v1.1.51 从该导出目录重新导入，仍须先对这份导出执行它们的网关。导出失败时保留原库，检查导出目录下的报告，不要用半成品覆盖生产目录。

完整说明见 [SQLite 数据存储与升级](server/sqlite-storage.md)。
