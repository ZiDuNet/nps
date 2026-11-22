# 升级迁移

本章说明从旧版本升级到 v1.1.x 后的数据变化和备份建议。

> 从 v1.1.49 起，SQLite 是默认业务数据存储。升级前请阅读 [SQLite 数据存储与升级](server/sqlite-storage.md)。从 v1.1.52 起，1.1.47/1.1.48 的旧 JSON 可直接由正常启动流程自动备份、迁移和校验；1.1.49 及以上的 SQLite 数据库不重复导入。全新安装不执行迁移。

当前版本新增自动迁移、菜单使用服务注册真实路径升级和 Docker 首次启动迁移。历史 SQLite
首次升级边界见[1.1.49 升级说明](upgrade-1.1.49.md)；管理员 API 的
HMAC-SHA256 签名边界见[1.1.51 升级说明](upgrade-1.1.51.md)。此前飞牛纯连接重连与日志顺序修复，见
[1.1.47 升级说明](upgrade-1.1.47.md)；更早的飞牛 FPK、服务端配置热更新和持久化 ID 回收边界，见
[1.1.46 升级说明](upgrade-1.1.46.md)；此前登录兼容、日志手动清理和用量历史自动清理边界，见
[1.1.45 升级说明](upgrade-1.1.45.md)。

1.1.46 的持久化 ID 回收行为已由 1.1.49 的“跨重启只增不复用”策略取代；升级后不要期待删除最后一项或重启服务会重置资源 ID。

## 升级前备份

升级前先停止 NPS，再备份整个 `conf` 目录：

```bash
cp -a conf conf.bak.$(date +%Y%m%d%H%M%S)
```

Docker 部署时备份挂载目录，例如：

```bash
cp -a /opt/nps/conf /opt/nps/conf.bak.$(date +%Y%m%d%H%M%S)
```

## 数据文件

当前版本以 `conf/nps.db` 为权威存储。不要只复制运行中的 `nps.db`，尚未合并的数据可能在 `nps.db-wal`；`nps.db-shm` 是共享内存索引，`nps.db.lock` 是可重建的单进程锁。`nps.conf` 保留监听、端口、TLS、程序日志等启动参数；注册策略、SMTP、HTTP 超时、审计及连接日志路径、PoW/TOTP 等业务配置保存在数据库的 `settings` 表。

`clients.json`、`users.json`、`tasks.json`、`hosts.json` 和 `global.json` 现在只用于首次导入源、回滚导出或迁移归档。不要把这些文件重新覆盖到已运行的 SQLite 目录。

新版检测到需要首次导入真实旧 JSON 时，会先将原 `conf` 打包至 `conf/backups/pre-sqlite-*.tar.gz` 并验证，迁移结果写入 `conf/migration-report.json`。全新安装不会生成两者。已由 v1.1.49、v1.1.50、v1.1.51 建库的环境不重新导入，也不会自动删除这些版本留下的历史备份。

## 用户自动迁移与旧客户端恢复

v1.1.1 引入 `users.json`。以下兼容规则仅用于首次从旧 JSON 导入；结果保存在 SQLite，不回写原 JSON：

1. 扫描所有客户端。
2. 如果客户端的 `UserId` 指向现有用户，保留该归属。
3. 如果 `UserId` 已失效，但旧 `WebUserName` / `WebPassword` 与现有用户完全匹配，修复为该用户的 ID。
4. 如果客户端没有 `UserId`，同样会优先复用完全匹配的现有用户。
5. 仅当整个 `users.json` 不存在时，才会依据旧客户端凭据创建新 `User`；文件已经存在时不会因旧字段复活被删除的账号。
6. 将用户和更新后的客户端关系写入 SQLite。

冲突处理：

- 同名同密码：合并为一个用户。
- 同名不同密码：新用户名为 `原用户名_客户端ID`。

如果归属修复或导入校验失败，新版会停止启动并保留原文件。导入成功后仍可按原兼容规则连接旧 NPC。

如果升级时整个 `users.json` 因为未挂载数据卷而缺失，服务会仅依据仍保留
`WebUserName` / `WebPassword` 的历史客户端，在 SQLite 中补齐可恢复的用户；
完整保留旧业务文件才能准确还原原有用户与客户端的归属。

该恢复只会在 `users.json` **完全不存在** 时执行：文件存在但用户被删除、停用、到期
或到期时间格式不正确时，服务仍会拒绝该客户端连接，不会自行重新启用被撤销的账号。

### `nps.conf` 自动兼容迁移

服务启动时会把内置模板中缺少的有效配置项追加到现有 `conf/nps.conf`，保留原有
启动参数值（包括显式空值）、自定义项和文件权限。旧业务键入库后从活动文件移除，原文保存在迁移归档。历史 Bridge 字段名按现有
`bridge_type` 选择：显式 `tcp` 时使用 `bridge_tcp_port` / `bridge_tcp_ip`；显式
`kcp` 时优先使用 `bridge_kcp_port` / `bridge_kcp_ip`。若旧配置没有指定传输，迁移
会从实际存在的历史字段选择主监听值。`bridge_tls_port` 会补写为
`tls_bridge_port`。旧字段不会删除；若当前版本字段已经存在，则当前版本字段始终
优先。迁移采用同目录临时文件原子替换，重复启动是幂等的。

若显式配置 `bridge_type=tcp`，`bridge_kcp_port` 会保留为并行 KCP 监听设置，不会错误地
复制成主 TCP 端口。`log_level` 也接受 `trace`、`debug`、`info`、`warn`、`error`、`fatal`、
`panic`、`off` 等文字别名；原配置值保留，NPS 启动时按当前日志级别转换规则解释，
不会将 `trace` 错误地解析成最低日志级别。

资源级限速字段也兼容旧数据：旧版 `tasks.json` 或 `hosts.json` 没有
`UploadLimit`/`DownloadLimit` 时按 `0`（不限速）读取；升级后的 Web 面板保存时才会
写入新字段。NPC 配置文件中的规则节可以新增 `upload_limit` 和 `download_limit`，旧版
NPC 不认识这两个字段时会忽略它们，原有连接协议和目标配置不受影响。

## 升级步骤

### JSON 到 SQLite（支持自动迁移的新版）

1. 停止旧 NPS，完整备份 `conf`。
2. 替换 NPS，以原配置目录正常启动，不需要手工运行 `-json-gateway`。Docker 必须挂载整个 `/conf`，不能只挂载 `nps.conf` 或单个 `nps.db`。
3. 检查 `conf/backups/pre-sqlite-*.tar.gz` 和 `conf/migration-report.json`，确认 `status=success`，核对客户端、用户、隧道、Host 和设置数量。
4. 验证登录、NPC 连接、TCP/UDP、域名反代、用户共享带宽和最大连接数。

需要保留升级后的业务修改并回滚到 JSON 时，先停服并运行 `nps -conf_path=<运行目录> -export-json=<输出目录>`。新版导出包含旧单位 JSON 和合并了 SQLite 业务配置的 `nps.conf`，不生成 `.json-gateway`。详细边界见 [SQLite 数据存储与升级](server/sqlite-storage.md)。

### NPC 版本边界与无缝切换

`1.1.28` 是旧客户端兼容边界：

- 服务端升级后，`<=1.1.28` NPC 仍使用原有 MD5 Bridge 握手和 UDP/KCP P2P，不需要立即替换客户端。
- `>=1.1.29` NPC 使用时间戳 + Challenge-Response 握手，并可使用现代 P2P、健康检查同步、内网地址上报和远程更新。
- 同一台 NPS 可以同时承载两类 NPC。服务端按客户端在握手中声明的产品版本自动分流，不需要改旧客户端配置。
- 现代 P2P 失败只影响该次 P2P 尝试；配置允许时会回退 Secret，普通 TCP/HTTP 转发和其他客户端不会被阻塞。

### NPC 更新器迁移

本次升级未改变旧 NPC 的登录握手与转发协议。新版更新进度通过能力协商启用，
历史 NPC 不会收到不认识的带载荷消息。旧 Windows 服务或 GUI 如果更新后
不能自行启动，需先手动安装包含更新器修复的客户端一次；仅更新 NPS 不能
改变旧设备内的更新逻辑。之后可使用新版分阶段升级、任务回报和连接恢复。
详细边界及服务共用程序文件的处理见[客户端更新与远程升级](client-update.md)。

### 新增配置的处理

升级启动时会把模板中缺少的必需启动字段追加到现有 `nps.conf`；端口、证书、P2P、ACME、日志和调试类可选字段不会在管理员明确移除后被反复写回。下列功能按需配置，以面板标注的实际保存位置为准：

- `p2p_stun_servers`、`p2p_enable_*_portmap`、`p2p_portmap_lease_seconds` 和三个 P2P 超时字段；
- `http3_proxy_port`、`https_default_*`、`force_auto_ssl` 和 ACME 缓存字段；
- `allow_user_register`、`register_captcha`、`register_max_clients`、`register_max_tunnels`、`register_max_conn`、`register_upload_limit`、`register_download_limit`、`register_expire_days` 和注册限频字段；
- 邮箱验证所需的 `register_email_verify`、验证码有效期/冷却时间及 `smtp_*` 字段；不启用邮箱验证时 SMTP 字段可保持为空；
- `allow_user_local`、`allow_secret_link`、`allow_secret_local`、`allow_remote_update`；
- `ntp_server`、`ntp_interval`、`geoip_path`、`geosite_path`、`geo_reload_interval`、`acl_max_rules`、`dns_server`。

注册相关字段可以在「系统 → 全局参数」的「用户注册」Tab 中维护；其他字段在「服务端配置」Tab 中按分组查看，证书、私钥和 ACME 字段在「证书设置」Tab 中统一维护。运行期业务字段写入 SQLite，不必手工放回 `nps.conf`。保存结果会提示是否需要重启；配置页不会覆盖未知自定义字段，敏感值只显示掩码。升级不要求重写端口和证书路径，原来显式设置的 `register_expire_days` 会导入数据库。

### 二进制部署

```bash
# 停止旧服务
nps stop

# 备份 conf
cp -a conf conf.bak.$(date +%Y%m%d%H%M%S)

# 替换 nps 二进制（SQLite 迁移无需替换 npc）
# 启动服务
nps start
```

### Docker 部署

```bash
cd /opt/nps
docker compose pull
docker compose stop nps
cp -a conf "conf.bak.$(date +%Y%m%d%H%M%S)"
docker compose up -d
docker logs nps --tail=100
```

## 升级后检查

1. 登录管理员面板。
2. 打开「用户管理」，确认旧客户端账号是否已迁移为用户。
3. 打开「客户端」，确认客户端显示了所属用户。
4. 使用普通用户账号登录，确认只能看到分配给自己的客户端。
5. 新增一个普通隧道或 Host 规则，确认配额逻辑符合预期。
6. 若旧 NPC 仍提示验证失败，检查 SQLite 中该客户端及所属用户是否导入并启用；Docker 部署还应确认 `/conf` 挂载的是原目录。查看服务端日志中的具体拒绝原因，不要把旧 JSON 覆盖回运行目录。

## 回滚

如果需要回滚：

1. 停止当前服务。
2. 恢复旧二进制。
3. 在独立目录恢复备份的 `conf`，核对数据后再使用。若此前对 JSON 做过手工网关整形，回到旧 JSON 版需使用整形前的备份。
4. 启动旧服务。

需要保留升级后修改时，先按 [SQLite 回滚说明](server/sqlite-storage.md#回滚)导出；不要让旧版本继续写入原 SQLite 运行目录。
