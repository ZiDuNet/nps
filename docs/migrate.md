# 升级迁移

本章说明从旧版本升级到 v1.1.1 后的数据变化和备份建议。

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

## 用户自动迁移

v1.1.1 新增 `users.json`。服务启动时会自动执行一次兼容迁移：

1. 扫描所有客户端。
2. 如果客户端已有 `UserId`，跳过。
3. 如果客户端存在 `WebUserName` 和 `WebPassword`，创建或复用一个 `User`。
4. 将客户端 `UserId` 指向该用户。
5. 保存 `users.json` 和更新后的 `clients.json`。

冲突处理：

- 同名同密码：合并为一个用户。
- 同名不同密码：新用户名为 `原用户名_客户端ID`。

迁移失败不会影响主流程，旧客户端登录仍保留兼容。

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

## 回滚

如果需要回滚：

1. 停止当前服务。
2. 恢复旧二进制。
3. 恢复备份的 `conf` 目录。
4. 启动旧服务。

`users.json` 是 v1.1.1 新增文件，旧版本不会使用它。
