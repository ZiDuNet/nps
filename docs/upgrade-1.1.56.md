# NPS 1.1.56 升级说明

v1.1.56 在面板里加入服务端一键升级：点击顶栏版本徽标打开「版本与升级」弹窗，
管理员可以直接把当前服务端升级到最新正式版，不需要登录服务器执行菜单。

## 新增功能

### 面板一键升级

1. 顶栏显示当前版本；检测到新版本时徽标会高亮“有新版本 vX.Y.Z”。
2. 点击徽标打开弹窗，显示当前版本、最新版本、发布说明链接和升级能力。
3. 管理员点击「立即升级」后，服务端依次执行：
   - 下载当前平台对应的 `*_server.tar.gz`；
   - 用 `checksums.txt` 校验 SHA-256；
   - 备份当前程序为 `<程序>.pre-web-update`；
   - 原子替换程序文件；
   - `syscall.Exec` 原地重启到新版本（PID 不变）。
4. 面板轮询升级阶段（下载校验 → 替换文件 → 重启），检测到版本变化后自动刷新。

### 能力检测

以下情况会直接禁用「立即升级」并说明原因，不会开始下载：

| 场景 | 原因 |
| --- | --- |
| Windows | 运行中的 exe 无法替换，请用菜单或手动升级 |
| `go run` / 临时构建 | 程序位于临时目录，替换后可能被清理 |
| 程序目录不可写 | 需要以有权限的账号运行 NPS |

`Docker` 会显示额外提示：升级只替换当前容器内的程序，重建容器后会回到镜像版本；
建议同时在宿主机更新镜像。

## 升级前

1. 备份整个 `conf` 目录；运行中的 SQLite 需连同 `nps.db-wal` 一起备份。
2. 确认现有 NPC 版本无需升级：1.1.56 不修改 Bridge 握手、控制消息或数据通道。

## 升级方式

### 方式一：面板一键升级（推荐）

登录面板，点击顶栏版本徽标，然后点击「立即升级」。

### 方式二：菜单升级

```bash
./nps -server
# 选择 [3] 更新 NPS
```

### 方式三：手动替换

```bash
cd /tmp
curl -fL -o nps156.tar.gz \
  https://github.com/ZiDuNet/nps/releases/download/v1.1.56/linux_amd64_server.tar.gz
tar -xzf nps156.tar.gz

TARGET=$(systemctl cat Nps | sed -n 's/^ExecStart=//p' | awk '{print $1}')
sudo systemctl stop Nps
sudo cp -a "$TARGET" "$TARGET.pre-1.1.56"
sudo install -m 0755 ./nps "$TARGET"
sudo systemctl start Nps
```

## 升级后检查

1. `./nps -version`、面板 `GET /api/v1/` 和 `npsctl version` 显示 `1.1.56`。
2. 点击顶栏版本徽标，弹窗显示“当前已是最新版本”，管理员按钮隐藏。
3. 确认登录、旧 NPC 上线、TCP/UDP、域名反代和配额统计正常。

## 失败与回滚

- 下载或校验失败：程序不会被替换，任务显示失败原因，可直接重试。
- 替换成功但自动重启失败：任务提示手动重启；旧程序保存在
  `<程序>.pre-web-update`，可复制回原路径恢复。
- 新版本成功启动后会自动删除 `.pre-web-update`，无需人工清理。

## 兼容性

- 只新增面板接口和页面交互；Bridge 握手、NPC 控制消息、代理协议、`/api/v1`
  字段和 SQLite schema 均不变，旧 NPC 可继续连接。
- 1.1.49 至 1.1.55 的配置和数据可直接升级，不执行迁移。
