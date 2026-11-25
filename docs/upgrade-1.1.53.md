# NPS 1.1.53 升级说明

v1.1.53 是兼容性补丁版本，包含两项内容：修复域名反代（Host）长响应被
keep-alive 读超时截断的问题，以及把「全局参数」和「服务端配置」按用途拆分
为多个 Tab。数据库、协议和配置目录结构均无变化。

## 变更内容

### 修复：域名反代长响应被截断

1.1.52 及更早在转发 Host 请求后，会立即给访客侧连接设一个 10 秒读超时，
用来等待同一 keep-alive 连接上的下一个请求。分块视频流、非 SSE 长连接和
大文件下载在传输期间不会产生下一个请求，因此会在 10 秒时被截断；当请求带
`Connection: close`（Nginx 反代上游的常见配置）时，关闭前还会再等待 5 秒，
表现为**固定的 15 秒断流**。

1.1.53 改为只有在上游响应完整写回访客之后，才开始计空闲读超时。响应进行中
不再受该超时约束，SSE、gRPC、`multipart/x-mixed-replace` 之外的流式响应同样
受益。上传与下载的停滞超时、总时长上限、后端连接与响应头超时等设置行为不变。

### 调整：配置页面按用途分组

- 「系统 → 全局参数 → 全局参数」拆为「基础与容量」「访问控制」「使用说明」
  「Geo 数据」四个 Tab，深链为 `#global-basic`、`#global-access`、
  `#global-guide`、`#global-geo`；旧链接 `#usage-guide-settings` 继续可用。
- 「服务端配置」拆为「入口与转发」「Bridge 与客户端」「日志与监控」
  「账号与接入」「运行与安全」和「`nps.conf` 启动项」六个 Tab，深链为
  `#config-entry`、`#config-bridge`、`#config-logs`、`#config-access`、
  `#config-runtime`、`#config-conf`。
- 前五个 Tab 只展示 SQLite 业务配置，最后一项集中展示启动参数、兼容/自定义
  字段和完整源码。字段卡片标注来源（SQLite 业务配置 / `nps.conf` 启动配置）
  和生效方式（立即生效 / 需重启）。

本次只调整页面信息架构，字段名称、默认值和保存目标均未改变。

## 升级前

1. 选择维护窗口并停止旧 NPS，避免同时运行两个服务进程。
2. 备份整个 `conf` 目录；运行中的 SQLite 需要连同 `nps.db-wal` 一起备份。
3. 确认现有 NPC 版本无需升级。1.1.53 不修改 Bridge 握手、控制消息或数据通道。

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

也可以使用菜单：`./nps -server` → 更新客户端/服务端。菜单会读取系统服务注册的
真实程序路径，下载校验后替换并重启，失败时回滚到 `.pre-update` 中的旧程序。

## Docker 升级

```bash
docker compose stop nps
sudo cp -a /path/to/conf "/path/to/conf.manual-backup.$(date +%Y%m%d-%H%M%S)"
docker compose pull
docker compose up -d
docker logs nps --tail=200
```

`/conf` 必须挂载整个目录。1.1.53 不执行迁移、不生成迁移归档，已有 SQLite 直接使用。

## 升级后检查

1. `./nps -version`、面板 `GET /api/v1/` 和 `npsctl version` 显示 `1.1.53`。
2. 打开「系统 → 全局参数」，确认两个页面按 Tab 分组，深链可定位到对应分类。
3. 用一个持续 60 秒以上的分块响应（视频流、SSE 之外的长连接或大文件下载）
   验证 Host 转发不再在 10/15 秒被截断：

```bash
curl -sS -m 60 -o /dev/null \
  -w 'code=%{http_code} bytes=%{size_download} time=%{time_total}\n' \
  'https://<你的 Host 域名>/<长响应路径>'
```

4. 确认登录、旧 NPC 上线、TCP/UDP、域名反代和配额统计正常。

## 失败与回滚

- 服务未能启动：用升级前的二进制和备份的 `conf` 目录回滚，确认版本与数据一致后再排查。
- 菜单升级失败：保留的 `.pre-update` 旧程序和原服务会被自动恢复；若回滚落盘失败，
  备份文件会保留，按提示手工恢复。
- 本次不涉及数据格式变更，回滚不需要导出/导入 JSON。

## 兼容性

- Bridge 握手、NPC 控制消息、快捷命令、代理协议和既有 `/api/v1` 字段不变，旧 NPC
  可继续连接。
- SQLite schema 版本不变，`nps.conf` 的启动参数键和默认值不变。
- 已发布的 1.1.49 至 1.1.52 配置和数据可直接升级，无需迁移步骤。
