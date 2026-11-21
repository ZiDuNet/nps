# SQLite 数据存储与升级

> 自 v1.1.49 起，SQLite 是正式默认业务数据存储。升级生产环境前，必须完整阅读本文并先在副本上演练迁移。

## 版本边界

SQLite 成为客户端、用户、隧道、域名规则、全局资源和运行期业务配置的权威存储。`nps.conf` 继续保存进程启动所需的监听、端口、TLS、日志等参数，不再作为这些业务数据的权威来源。

旧版 JSON 数据不能直接交给 SQLite 版本启动。NPS 只接受已经由 `1.1.49` 二进制 JSON 网关整形、且带有完整 `conf/.json-gateway` 标记的数据。缺少标记、标记版本过旧、覆盖清单不完整或仍含无法识别的记录时，程序会拒绝启动，保留原 JSON，并且不创建 `nps.db`。只有不存在真实业务数据或仅含内置样例的全新安装才会按空系统启动。

## 数据目录

| 路径 | 用途 |
| --- | --- |
| `conf/nps.conf` | 进程启动参数，以及回滚所需的兼容配置 |
| `conf/nps.db` | SQLite 权威数据库 |
| `conf/nps.db-wal` | WAL 日志；运行中可能包含尚未合并的数据 |
| `conf/nps.db-shm` | WAL 共享内存索引 |
| `conf/nps.db.lock` | 单进程实例锁 |
| `conf/migration-report.json` | 最近一次 SQLite 导入或导出报告 |

不要只备份或复制 `nps.db`。运行中的一致性备份必须包含 WAL 和 SHM；最稳妥的方式是先停止 NPS，再复制整个 `conf` 目录。

```bash
sudo nps stop
cp -a /etc/nps/conf "/etc/nps/conf.backup.$(date +%Y%m%d-%H%M%S)"
sudo nps start
```

Unix 上新目录权限为 `0750`，数据库、WAL、SHM 和锁文件权限为 `0600`。Windows 部署必须自行设置等价的 ACL，不能依赖 Unix mode bit。

## 启动迁移

首次启动 SQLite 版本时，程序执行以下步骤：

1. 检查 `conf/.json-gateway` 的版本和文件覆盖清单。
2. 在 `conf` 同级创建 `conf.backup.YYYYMMDD-HHMMSS` 完整备份。
3. 解析并校验客户端、用户、隧道、Host 的数量、ID、归属和跨租户路由冲突。
4. 在一个 SQLite 事务中写入全部实体和迁移元数据。
5. 成功后归档旧 JSON，并写入 `conf/migration-report.json`。

任何校验或事务失败都不会提交不完整数据库。若归档阶段失败但数据库已经提交，程序会保留完整的 `nps.db` 并要求检查 `conf`，避免误删唯一权威副本。

旧数据需要专用网关处理时，先运行：

```bash
./nps -conf_path=/etc/nps -json-gateway
```

该命令完成 JSON 字段、单位和三态限额整形后退出。命令可重复执行；若中途失败，应按提示从备份恢复，不要手工修改 `.json-gateway`。

## 业务配置

首次 SQLite 启动会把下列运行期业务配置从 `nps.conf` 导入 `settings` 表，并生成 `nps.conf.pre-settings.bak.<版本>.<时间>`。此后 Web 面板修改的业务配置直接写入 SQLite。

- 保留在 `nps.conf`：监听地址、端口、TLS 证书路径、日志目标、运行模式等进程启动参数。
- 存入 SQLite：注册策略、用户默认配额、SMTP、HTTP 超时、审计日志、PoW/TOTP、MCP 开关等运行期业务配置。

配置页的“服务端配置”会合并展示两类来源。不能为了回滚而把 SQLite `settings` 表中的值直接复制到裸 `nps.conf`；应使用下方的导出命令。

## 维护命令

执行维护命令前通常必须先停止 NPS，因为 `nps.db.lock` 会阻止第二个进程打开同一数据库。

导出为旧版可读取的 JSON 目录：

```bash
./nps -conf_path=/etc/nps -export-json=/var/backups/nps-json
```

导出内容包括：

- `conf/clients.json`、`users.json`、`tasks.json`、`hosts.json`、`global.json`；
- 合并了 SQLite 业务配置的 `conf/nps.conf`；
- `conf/.json-gateway`；
- 输出根目录的 `migration-report.json`。

导出会把 bps 转回旧版 KB/s。若用户配额中的 `0` 表示“套餐不包含”，旧 JSON 无法无损表达，导出会拒绝并写入失败报告。此时应保留 SQLite 数据，或先在面板中改成旧格式能表达的值。

## 一致性设计

- 用户级共享并发连接和双向共享带宽由运行期状态维护；配置修改不会绕过正在使用的连接。
- 客户端、用户及其隧道/Host 级联删除在一个持久化操作中提交，失败时恢复内存记录。
- 平台泛域名与关联 Host 的证书路径在同一事务内保存。
- 周期持久化按行比较，仅更新发生变化或新增的实体，并删除已不存在的行；不再整表删除后重插。
- 持久化资源 ID 跨重启只增不复用；SQLite 在实体事务中同步保存 ID 高水位，删除最大编号后也不会回退计数器。
- 单进程锁防止两个 NPS 进程同时写同一个数据库。

旧 JSON 多文件后端只保留给迁移、测试和嵌入式调用。它无法把 `clients.json`、`tasks.json`、`hosts.json`、`users.json` 等多个文件作为一次原子事务提交；生产启动只要没有显式处于 JSON 维护流程，就会使用 SQLite 作为权威存储。不要把测试环境中直接构造的 JSON 后端当成生产级回滚目标。

## 回滚

精确回滚到升级前状态时，停止 NPS 并从对应的 `conf.backup.*` 恢复整个目录。该方式会丢失升级后的新修改。

需要回到 JSON 版本但保留 SQLite 当前数据时：

1. 停止 NPS。
2. 执行 `-export-json` 到独立目录。
3. 检查导出的 `migration-report.json.status` 为 `success`。
4. 使用导出的 `conf` 启动仍支持 JSON 的目标版本。
5. 不要同时把旧 JSON 启动结果写回原 SQLite 目录。

## 验收清单

- `conf/migration-report.json` 显示 `status=success`，实体计数与源数据一致。
- `conf/nps.db`、`nps.db-wal`、`nps.db-shm` 所在文件系统有足够空间且仅服务账号可读。
- 全局限流、用户共享带宽、最大连接数、到期时间和停用状态均按预期生效。
- 至少验证一次客户端、用户级联删除，并确认关联隧道和 Host 不残留。
- Docker 已挂载整个 `/conf`，不是只挂载单个 `nps.conf`。
- 已完成一次停服备份和一次 `-export-json` 演练。
