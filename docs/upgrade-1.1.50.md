# NPS 1.1.50 升级说明

v1.1.50 是 v1.1.49 的发布可靠性补丁，不包含数据库结构或 Bridge 协议变更。

## 修复内容

`v1.1.49` 的 Release 工作流在构建 Linux MIPS 产物时失败。SQLite 驱动原先
由公共 `lib/store` 匿名注册，NPC 即使不访问数据库，也会因导入存储类型而编译
`modernc.org/libc`。该模块没有 MIPS 实现，构建器报告 `build constraints
exclude all Go files`，导致 GitHub Release、fnOS FPK、Android APK、GUI 和
校验文件无法统一上传。

v1.1.50 将 SQLite 驱动注册拆成服务端专用入口：

- NPS 服务端显式加载 SQLite 驱动。
- NPC 和 NPC SDK 不再编译 SQLite/`modernc.org/libc`。
- Linux MIPS NPC 继续保留；无法使用 SQLite 权威存储的 Linux MIPS server
  不再进入正式发布矩阵。
- CI 在提交阶段交叉编译全部正式服务端架构和全部历史 NPC 架构，并阻止 NPC
  重新引入 SQLite 依赖。

正式服务端和 NPC 架构为：

- Linux：`amd64`、`386`、`armv5`、`armv6`、`armv7`、`arm64`
- FreeBSD：`386`、`amd64`、`arm`
- macOS：`amd64`、`arm64`
- Windows：`386`、`amd64`、`arm64`

此外继续发布 Linux MIPS `mips`、`mipsle`、`mips64`、`mips64le` 的 NPC
客户端。已部署的旧 MIPS NPC 可直接使用原 Bridge 协议连接新服务端。

## 从 1.1.49 升级

1. 按既有 SQLite 流程备份整个 `conf` 目录。
2. 替换为 `v1.1.50` 的服务端或 NPC 二进制。
3. 直接启动即可。已经由 1.1.49 创建 SQLite 数据库的环境不需要再次执行
   `-json-gateway`。

Windows manager 菜单、系统服务和 GUI 的在线更新可在新 Release 完整生成后
升级到 1.1.50。

## 从 1.1.48 及更早版本升级

仍须先使用 1.1.49 或更高版本的 `-json-gateway` 处理旧 JSON，再正常启动
完成 SQLite 首次导入。完整命令、WAL/SHM 备份和 Docker 操作见
[SQLite 数据存储与升级](server/sqlite-storage.md)。

## 回滚

v1.1.50 未改变 SQLite schema。可由同一 `conf` 目录直接回退到 v1.1.49；
若要回退到 JSON 数据版本，使用当前二进制执行 `-export-json` 生成完整回滚
目录，不要只复制运行中的 `nps.db`。

## 验收

- 面板 `GET /api/v1/` 与 `npsctl version` 显示 `1.1.50`；设置 `web_base_url`
  时，公开探针路径需添加其前缀。
- Release 资产包含受支持服务端平台的 `*_server.tar.gz`、全部客户端的
  `*_client.tar.gz`、`npsctl_*`、`android_client.apk`、fnOS FPK、GUI ZIP
  和 `checksums.txt`。
- 不再提供 Linux MIPS server；v1.1.48 及更早版本中的 MIPS server 资产仍
  保留在各自历史 Release。
