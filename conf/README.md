# 配置文件说明

## INI 配置

- `nps.conf`：服务端启动配置。全新安装会自动初始化；管理密码、`auth_key` 和 `auth_crypt_key` 会随机生成。发布包中的 `CHANGE_ME` 占位值以及历史固定弱默认值会在启动时轮换，显式留空的可选鉴权项保持不变，公共客户端密钥默认关闭。升级时保留已有启动配置、自定义项及权限；支持自动迁移的新版确认业务配置入库后，才会移除对应业务键和明确废弃的键，原始 `nps.conf` 保存在迁移归档里。由 Web 面板移除的可选监听、证书、日志和调试配置不会在下次启动时被重新添加。
- `npc.conf`：客户端完整配置示例，不会由服务端或 NPC 自动生成。使用配置文件模式时，请复制并按需修改它，再通过 `./npc -config=conf/npc.conf` 启动；也可以直接使用 `-server` 和 `-vkey` 参数启动。
- `multi_account.conf`：SOCKS5 等多账号代理认证文件，每行使用 `用户名=密码`。它不会自动复制，`npc.conf` 中的 `multi_account` 路径必须指向实际文件。

全新安装只初始化 `nps.conf`、SQLite 及正常服务所需文件，不执行旧数据迁移，也不生成迁移包或报告。`npc.conf` 和 `multi_account.conf` 是客户端示例，不会被服务端释放或改写；Docker 部署请挂载整个 `/conf`。

管理员可以在 Web 面板「系统 → 全局参数」的「服务端配置」Tab 中维护当前运行目录下的 `nps.conf`。可视化页面按功能分类展示非证书、非注册配置；证书、私钥和 ACME 字段统一放在「证书设置」Tab，注册入口、新用户默认权益、限频和 SMTP 放在「用户注册」Tab，完整 `nps.conf` 仍可在源码视图中查看和维护。可选监听、证书、密钥、日志归档和调试项关闭后会从有效配置中移除。密码、API 密钥和私钥不会回显，保存后必须重启 NPS 才会应用。历史版本或自定义键会只读展示并在保存时保留。

Bridge 全协议共用一套 TLS 证书。管理员可在「系统 → 全局参数」的「证书设置」Tab 中通过路径、上传或粘贴方式维护 `bridge_cert_file`、`bridge_key_file`；上传/粘贴文件写入 `conf/bridgessl`，最终配置仍只保存在 `nps.conf`，保存后需重启 NPS 才会生效。平台泛域名和 HTTPS 默认证书、管理面板 HTTPS、IP 登记 HTTPS 也在同一 Tab 中维护。平台泛域名使用上传/粘贴时写入 `conf/certs/platform_<ID>.pem` 和 `conf/certs/platform_<ID>.key`；使用路径模式时不会复制文件。

客户端详情中的各协议连接命令由 `bridge_*_show` 控制是否展示，由 `bridge_*_show_ip`、`bridge_*_show_port` 和 `bridge_show_path` 覆盖公网地址、端口与 WS 路径。这些字段只影响命令文本，不改变服务端监听；`bridge_*_show=false` 会作为显式配置保留，升级时不会被自动恢复为 `true`。

## SQLite 数据与 JSON 迁移格式

从 v1.1.49 起，`conf/nps.db` 是权威业务数据存储；运行中一致性备份要考虑 `nps.db-wal` 和 `nps.db-shm`，`nps.db.lock` 是可重建的单进程锁。`nps.conf` 保存监听、端口、TLS、程序日志等启动参数，注册策略、SMTP、HTTP 超时、审计与连接日志路径、PoW/TOTP 等业务设置在数据库的 `settings` 表。

支持自动迁移的新版仅在原目录有旧业务 JSON、尚未建库时生成 `conf/backups/pre-sqlite-*.tar.gz` 及 `conf/migration-report.json`。归档校验和入库成功后，只清理已备份且确认属于本次迁移的旧文件；证书、密钥、用量历史、日志、未知文件和独立备份保留。已发布的 v1.1.49、v1.1.50 和 v1.1.51 首次从 JSON 建库行为不同，完整升级、备份和回滚见 [SQLite 数据存储与升级](../docs/server/sqlite-storage.md)。

下列 JSON 文件只用于旧版首次导入、迁移归档或 `-export-json` 回滚。JSON 导出按旧版兼容格式把带宽写为 KB/s、流量配额写为 MB；SQLite 内部使用 bps 和明确的三态配额。不要手工把旧 JSON 覆盖到运行中的 SQLite 目录。

- `clients.json`：客户端列表。常用字段：`Id` 客户端编号、`VerifyKey` 验证密钥、`Remark` 备注、`Status` 是否允许连接、`RateLimit` 速率上限（KB/s）、`MaxConn` 最大连接数、`MaxTunnelNum` 最大隧道数、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单优先级及黑白名单、`RegisterKey` IP 登记密钥、`RegisterMaxHours` 登记最长有效期、`RegisteredOnly` 是否仅允许已登记 IP、`ExpireTime` 到期时间。登记记录本身只存在内存中，不写入 JSON。
- `tasks.json`：端口隧道列表。常用字段：`Id` 编号、`Port` 公网端口、`ServerIp` 监听 IP、`Mode` 代理模式、`Client.Id` 所属客户端、`Target.TargetStr` 内网目标、`Status` 是否启用、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单、`RegisteredOnly` 是否只允许已登记来源、`HealthCheckType` 健康检查类型、`UploadLimit`/`DownloadLimit` 规则上下行限速（KB/s）。旧数据缺少限速字段时按 `0`（不限速）处理。
- `hosts.json`：HTTP/HTTPS 主机列表。常用字段：`Host` 域名、`Scheme` 协议、`Location` URL 路径、`Target.TargetStr` 内网目标、`CertFilePath`/`KeyFilePath` 证书和私钥、`AutoHttps` 是否自动 HTTPS、`HeaderChange` 请求头改写、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单、`RegisteredOnly` 是否只允许已登记来源、`UploadLimit`/`DownloadLimit` 规则上下行限速（KB/s）。
- `global.json`：全局运行数据。`BlackIpList`/`WhiteIpList` 是全局 IP 黑白名单，`ServerUrl` 是客户端连接服务端时使用的地址，`PlatformDomains` 是管理员维护的平台泛域名池；每项包含稳定 ID、`*.example.com` 泛域名和对应证书/私钥路径。已被 Host 引用的平台域名不能删除或改名，证书路径更新会同步关联 Host。
- `users.json`：旧版面板用户数据。`MaxClientNum`、`MaxTunnelNum` 分别是客户端与总隧道配额，`MaxConn`、`UploadLimit`、`DownloadLimit` 分别是全部所属客户端共享的并发连接数及上下行带宽（KB/s）；旧格式使用 `0` 表示不限制。密码字段为敏感数据，请限制文件权限。

旧格式用 `*#*` 分隔多条记录，不要删除或替换分隔符。
