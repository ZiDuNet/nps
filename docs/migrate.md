# 升级迁移

本章说明从旧版本升级到 v1.1.x 后的数据变化和备份建议。

## 升级前备份

升级前请备份整个 `conf` 目录：

```bash
cp -a conf conf.bak.$(date +%Y%m%d%H%M%S)
```

Docker 部署时备份挂载目录，例如：

```bash
cp -a /opt/nps/conf /opt/nps/conf.bak.$(date +%Y%m%d%H%M%S)
```

## 数据文件

NPS 仍使用 JSON 文件持久化，默认位于 `conf` 目录。

| 文件 | 说明 |
|------|------|
| `nps.conf` | 服务端配置，包含管理员账号、端口、功能开关 |
| `clients.json` | 客户端数据 |
| `users.json` | 普通用户数据 |
| `tasks.json` | 普通隧道数据 |
| `hosts.json` | HTTP/HTTPS 域名规则 |
| `global.json` | 全局设置 |

每个对象文件使用 `*#*` 分隔多条 JSON 记录，这是当前项目的既有格式。

## 用户自动迁移与旧客户端恢复

v1.1.1 已新增 `users.json`。服务启动时会自动执行一次兼容迁移；升级到 v1.1.2 及后续 v1.1.x 时会继续保留该文件和已有用户关系：

1. 扫描所有客户端。
2. 如果客户端的 `UserId` 指向现有用户，保留该归属。
3. 如果 `UserId` 已失效，但旧 `WebUserName` / `WebPassword` 与现有用户完全匹配，修复为该用户的 ID。
4. 如果客户端没有 `UserId`，同样会优先复用完全匹配的现有用户。
5. 仅当整个 `users.json` 不存在时，才会依据旧客户端凭据创建新 `User`；文件已经存在时不会因旧字段复活被删除的账号。
6. 保存 `users.json` 和更新后的 `clients.json`。

冲突处理：

- 同名同密码：合并为一个用户。
- 同名不同密码：新用户名为 `原用户名_客户端ID`。

迁移失败不会影响主流程，旧客户端登录仍保留兼容。

如果升级时整个 `users.json` 因为未挂载数据卷而缺失，服务会仅依据仍保留
`WebUserName` / `WebPassword` 的历史客户端，恢复对应的启用用户并写回新的
`users.json`。这是为了让旧版 NPC 无需重新配置即可恢复连接。

该恢复只会在 `users.json` **完全不存在** 时执行：文件存在但用户被删除、停用、到期
或到期时间格式不正确时，服务仍会拒绝该客户端连接，不会自行重新启用被撤销的账号。

### `nps.conf` 自动兼容迁移

服务启动时会把内置模板中缺少的有效配置项追加到现有 `conf/nps.conf`，保留原有
值（包括显式空值）、注释、自定义项和文件权限。来自上游 `nps2` 的配置也可以直接
升级：`bridge_tcp_port` / `bridge_kcp_port` 会补写当前版本使用的 `bridge_port`，
`bridge_tls_port` 会补写为 `tls_bridge_port`，`bridge_tcp_ip` / `bridge_kcp_ip`
会补写为 `bridge_ip`。旧字段不会删除；若当前版本字段已经存在，则当前版本字段
始终优先。迁移采用同目录临时文件原子替换，重复启动是幂等的。

上游 `nps2` 可能同时配置 `bridge_tcp_port` 和 `bridge_kcp_port`；迁移时会根据
`bridge_type` 选择对应端口写入 `bridge_port`，若指定类型的端口未配置则回退到另一个
可用端口。上游文字型 `log_level`（`trace`、`debug`、`info`、`warn`、`error`、
`fatal`、`panic`、`off`）也可以直接使用；原配置值保留，NPS 启动时按当前日志级别
转换规则解释，不会将 `trace` 错误地解析成最低日志级别。

资源级限速字段也兼容旧数据：旧版 `tasks.json` 或 `hosts.json` 没有
`UploadLimit`/`DownloadLimit` 时按 `0`（不限速）读取；升级后的 Web 面板保存时才会
写入新字段。NPC 配置文件中的规则节可以新增 `upload_limit` 和 `download_limit`，旧版
NPC 不认识这两个字段时会忽略它们，原有连接协议和目标配置不受影响。

## 升级步骤

### 二进制部署

```bash
# 停止旧服务
nps stop

# 备份 conf
cp -a conf conf.bak.$(date +%Y%m%d%H%M%S)

# 替换 nps/npc 二进制
# 启动服务
nps start
```

### Docker 部署

```bash
cd /opt/nps
docker compose pull
docker compose up -d
docker logs nps --tail=100
```

## 升级后检查

1. 登录管理员面板。
2. 打开「用户管理」，确认旧客户端账号是否已迁移为用户。
3. 打开「客户端」，确认客户端显示了所属用户。
4. 使用普通用户账号登录，确认只能看到分配给自己的客户端。
5. 新增一个普通隧道或 Host 规则，确认配额逻辑符合预期。
6. 若旧 NPC 仍提示验证失败，检查容器挂载的 `/conf` 是否同时保留了 `clients.json`
   和 `users.json`，并查看服务端日志中的具体拒绝原因。

## 回滚

如果需要回滚：

1. 停止当前服务。
2. 恢复旧二进制。
3. 恢复备份的 `conf` 目录。
4. 启动旧服务。

`users.json` 是 v1.1.1 新增文件，旧版本不会使用它。升级时不要删除该文件。
