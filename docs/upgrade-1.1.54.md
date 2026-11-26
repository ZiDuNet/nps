# NPS 1.1.54 升级说明

v1.1.54 是 CLI / MCP / Skill 的功能版本：域名反代工具只创建平台域名，
新增只读平台域名接口，并给 `npsctl` 加上自动更新。服务端代理链路、数据库、
Bridge 协议和配置目录结构均无变化，只替换服务端二进制即可。

## 变更内容

### 新增：平台域名只读接口

`GET /api/v1/platform-domains` 返回管理员在「系统 → 全局参数 → 证书设置」
维护且当前可用的平台泛域名：

```json
{"status":1,"code":"ok","msg":"ok","data":[{"id":"pd_7f3c1a2b","wildcard":"*.example.com","certificate_configured":true}],"request_id":"..."}
```

普通用户 Token 可以调用，否则用户无法得知创建平台域名反代所需的
`platform_domain_id`。响应不包含证书和私钥的服务器路径。

### 调整：CLI / MCP / Skill 只创建平台域名

- `npsctl host create` 使用 `--prefix` 指定单层前缀（留空随机生成 8 位，
  冲突时自动重试），`--platform-domain` 指定泛域名 ID 或 `*.example.com`
  （留空自动选择第一个可用泛域名）。传入 `host` 或 `domain_mode=custom`
  会被拒绝，并提示到 Web 面板创建。
- MCP `nps_host_create` 参数为 `client_id`、`prefix`、`platform_domain_id`、
  `scheme`、`location`、`target`、`remark`，不再接受 `host`。新增只读工具
  `nps_platform_domains`。
- 一个可用平台域名都没有时直接报错，不会降级成自定义域名。
- **Web 面板不变**，仍同时支持自定义域名和平台域名，校验与默认选中逻辑保持原样。

### 新增：npsctl 自动更新

- 每天最多一次后台检查 GitHub 最新正式版，有更新时下载 `checksums.txt` 校验
  SHA256 后静默替换自身，下次调用生效。
- 也可运行 `npsctl self-update` 立即检查。
- 检查或替换失败只打印警告，不改变用户命令的退出码；
  `NPSCTL_NO_UPDATE_CHECK=1` 可完全关闭。
- 更新源为 GitHub Release，内网无外网出口时自动检查会静默失败，不影响使用。

## 升级前

1. 备份整个 `conf` 目录；运行中的 SQLite 需连同 `nps.db-wal` 一起备份。
2. 确认现有 NPC 版本无需升级：1.1.54 不修改 Bridge 握手、控制消息或数据通道。

```bash
sudo nps stop
sudo cp -a /etc/nps/conf "/etc/nps/conf.manual-backup.$(date +%Y%m%d-%H%M%S)"
```

## 二进制升级

```bash
sudo systemctl stop Nps
sudo install -m 0755 ./nps /usr/local/bin/nps
sudo systemctl start Nps
sudo journalctl -u Nps -n 200 --no-pager
```

也可以使用菜单：`./nps -server` → 更新服务端。菜单会读取系统服务注册的真实
程序路径，下载校验后替换并重启，失败时回滚到 `.pre-update` 中的旧程序。

## Docker 升级

```bash
docker compose stop nps
sudo cp -a /path/to/conf "/path/to/conf.manual-backup.$(date +%Y%m%d-%H%M%S)"
docker compose pull
docker compose up -d
docker logs nps --tail=200
```

`/conf` 必须挂载整个目录。1.1.54 不执行迁移、不生成迁移归档，已有 SQLite 直接使用。

## 升级后检查

1. `./nps -version`、面板 `GET /api/v1/` 和 `npsctl version` 显示 `1.1.54`。
2. 用普通用户 Token 调用平台域名接口，确认只返回 ID、泛域名和证书状态：

   ```bash
   npsctl --server https://nps.example.com --token npsu_xxx platform-domains
   ```

3. 用一个前缀创建平台域名反代，确认返回 `已创建域名反代 #<id>: <前缀>.<泛域名> → /`：

   ```bash
   npsctl --server https://nps.example.com --token npsu_xxx host create \
     --prefix demo --data '{"client_id":3,"target":"127.0.0.1:8080","scheme":"https"}'
   ```

4. 确认面板「域名反代 → 新增」仍能选择自定义域名并正常保存，行为与 1.1.53 一致。
5. 确认登录、旧 NPC 上线、TCP/UDP、域名反代和配额统计正常。

## 失败与回滚

- 服务未能启动：用升级前的二进制和备份的 `conf` 目录回滚。
- 菜单升级失败：保留的 `.pre-update` 旧程序和原服务会被自动恢复。
- 本次不涉及数据格式变更，回滚不需要导出/导入 JSON。
- `npsctl` 自动更新失败不影响任何命令结果；如需固定版本，设置
  `NPSCTL_NO_UPDATE_CHECK=1` 或直接使用 Release 中的二进制。

## 兼容性

- Bridge 握手、NPC 控制消息、快捷命令、代理协议和既有 `/api/v1` 字段不变，
  旧 NPC 可继续连接。
- SQLite schema 版本不变，`nps.conf` 的启动参数键和默认值不变。
- 已发布的 1.1.49 至 1.1.53 配置和数据可直接升级，无需迁移步骤。
- 直接调用 `POST /api/v1/hosts` 并提交自定义域名的既有集成不受影响；
  只有 MCP `nps_host_create` 的参数收紧了。
