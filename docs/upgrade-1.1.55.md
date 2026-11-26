# NPS 1.1.55 升级说明

v1.1.55 是服务端菜单升级的修复版本。1.1.52 至 1.1.54 通过
`./nps -server` → `[3] 更新 NPS` 升级时，会在新服务已经正常启动后报错并
自动回滚，表现为“升级总是失败”。本版本修复该问题，其他行为不变。

## 问题表现

在 1.1.52 的菜单里选择更新，输出类似：

```text
the latest version is v1.1.54
download package from https://github.com/ZiDuNet/nps/releases/download/v1.1.54/linux_amd64_server.tar.gz
更新失败： 新版本启动或健康检查失败: 新服务版本为 "1.1.54"，期望 "1.1.52"
```

下载、替换、启动实际都成功了，但健康检查把刚启动的 1.1.54 服务和**当前仍在
运行的 1.1.52 菜单二进制**比较，判定为失败，随后停掉新服务、恢复
`.pre-update` 旧程序并重启，所以最终仍是旧版本。

## 修复内容

- `StagedNpsUpdate` 增加 `Version` 字段，取自本次下载的 Release 标签
  （去掉前缀 `v`）。
- 健康检查改为比对目标版本：新服务报告 `1.1.54`、目标是 `1.1.54` 时通过；
  只有新服务没有真正运行到目标版本时才回滚。
- 未提供目标版本时（测试或旧调用路径）仍回退为本地构建版本，保持原语义。

## 升级方式

### 方式一：菜单升级（本版本起恢复正常）

```bash
./nps -server
# 选择 [3] 更新 NPS
```

成功时会输出 `NPS 更新完成并通过健康检查`。

### 方式二：手动替换二进制

```bash
cd /tmp
curl -fL -o nps155.tar.gz \
  https://github.com/ZiDuNet/nps/releases/download/v1.1.55/linux_amd64_server.tar.gz
tar -xzf nps155.tar.gz

TARGET=$(systemctl cat Nps | sed -n 's/^ExecStart=//p' | awk '{print $1}')
sudo systemctl stop Nps
sudo cp -a "$TARGET" "$TARGET.pre-1.1.55"
sudo install -m 0755 ./nps "$TARGET"
sudo systemctl start Nps
```

## 升级后检查

1. `./nps -version`、面板 `GET /api/v1/` 和 `npsctl version` 显示 `1.1.55`。
2. `systemctl status Nps` 为运行中，日志无数据库锁错误。
3. 若之前被回滚过，确认 `.pre-update` 文件已不存在或按需清理。

## 兼容性

- 只影响服务端菜单升级流程，Bridge 握手、NPC 控制消息、快捷命令、代理协议、
  `/api/v1` 字段和 SQLite schema 均不变，旧 NPC 可继续连接。
- 1.1.49 至 1.1.54 的配置和数据可直接升级，不执行迁移。
