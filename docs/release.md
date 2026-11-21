# NPS 发版规范

本文是 NPS Pro 的正式发版清单。发版提交必须从 `master` 产生，开发分支
（如 `sqlite-dev`、`nps-lite`）在未完成评审和测试前不得直接打正式 tag。

## 版本号规则

- 版本号唯一来源：`lib/version/version.go` 的 `VERSION`。
- 正式 tag 必须等于 `v{VERSION}`，例如源码为 `1.1.50` 时 tag 只能是
  `v1.1.50`。
- tag 必须指向当前 `origin/master` 的提交；`release.yml` 的
  `validate_release` 会强制校验这两点。
- 补丁版本只递增最后一位；不要复用或移动已发布 tag，回滚请发新补丁版本。

## 发版前必须同步的版本位置

| 位置 | 要求 |
| --- | --- |
| `lib/version/version.go` | `VERSION` 已更新 |
| `cmd/npc/npc-gui/wails.json` | `info.productVersion` 同步 |
| `cmd/npc/npc-gui/frontend/package.json` | `version` 同步 |
| `cmd/npc/npc-gui/build/darwin/Info.plist` | `CFBundleVersion` 与 `CFBundleShortVersionString` 同步 |
| `cmd/npc/npc-gui/build/windows/info.json` | `file_version` 与 `ProductVersion` 同步 |
| `cmd/npc/npc-gui/build/windows/wails.exe.manifest` | 程序集版本同步 |
| `cmd/npc/npc-gui/build/config.yml` | 源版本同步，避免下次生成资产时回退 |
| `gui/npc/AndroidManifest.xml` | `versionName` 同步，`versionCode` 单调递增 |
| `packaging/fnos/manifest` | `version` 同步；打包脚本仍会在构建时覆盖 |
| `cmd/npsctl/main.go` | 从 `lib/version` 读取，禁止另写常量 |
| `CHANGELOG.md` | 顶部存在 `## [x.y.z]` 段落，供 Release Notes 使用 |
| `README.md` / `README_zh.md` | 标题版本号与「近期更新」条目 |
| `docs/README.md` | 当前版本、文档口径与升级说明导航 |
| `docs/changelog.md` / `docs/changelog/index.md` | 顶部版本条目 |
| `docs/upgrade-x.y.z.md` | 新增本版本升级说明 |

功能或行为变化时还要检查：

- `docs/api.md`：`/api/v1` 字段、权限或错误码有变化时。
- `docs/mcp.md`：MCP 工具、认证、会话或客户端配置有变化时。
- `docs/usage-guide.md`：面板菜单、按钮或操作流程有变化时。
- `docs/server/sqlite-storage.md`、`docs/upgrade-sqlite.md`：数据存储、迁移、备份、WAL、导出或回滚行为有变化时。
- `README*.md` 与 `docs/screenshots/`：界面截图需要更新时。
- `web/skills/nps/SKILL.md` 和 `web/skills/nps/scripts/npc-help.sh`：CLI、Skill、NPC
  部署方式有变化时。
- 文档中的 Bridge 端口必须写明来自面板/API 的 `bridgeEndpoints`；只有明确
  说明“默认值”时才可提 `8024` / `8025`。
- MCP 文档必须明确 `type: "http"`、无状态会话、管理员/用户凭据矩阵，以及
  REST 与 MCP 审计都按当前账号范围过滤。

## 本地验证

在干净的发版工作区执行：

```bash
export GOPROXY=https://goproxy.cn,direct
go build ./...
bash scripts/check-release-targets.sh
go test ./...
go test -race ./bridge ./client ./cmd/npc ./cmd/nps \
  ./lib/config ./lib/conn ./lib/file ./lib/fnos ./lib/install \
  ./lib/connectionlog ./lib/jsonlog ./server ./server/connection \
  ./server/proxy ./server/tool ./server/usage ./web/...
node --test web/tests/*.test.cjs
```

涉及脚本时额外执行 `bash -n scripts/*.sh web/skills/nps/scripts/*.sh`；
有 `shellcheck` 时一并运行。涉及文档站点时执行 `docs` 目录下的构建命令。

CLI 冒烟至少覆盖：

- `npsctl version` 与源码版本一致。
- `npsctl clients --limit 5` 输出 `rows` / `total`，不再返回 `data:null`。
- 无 Token 时返回凭据错误；错误 Token 时非零退出。
- `npsctl client get` 缺 id 时先报参数错误。
- `npsctl clients -h`、未知尾随参数、凭据冲突和非法 `--server` 均按预期处理。
- `npsctl mcp` 生成的片段包含 `type`，且不误导为 SSE。
- `bash -n web/skills/nps/scripts/npc-help.sh`，并检查帮助内容没有把默认
  端口写成所有部署的固定端口。

## 发版步骤

1. 确认当前分支为 `master`，工作区只包含本次发版改动。
2. 完成上面的版本位置、文档和本地验证。
3. 提交：`release: vX.Y.Z <摘要>`。
4. 推送 `master`，等 CI 通过。
5. 打 tag：`git tag -a vX.Y.Z -m "NPS vX.Y.Z"`。
6. 推送 tag：`git push origin vX.Y.Z`（Release workflow 由 tag 触发）。
7. 用 `gh run watch` 盯完 `Release` workflow，失败立即定位，不要重复打 tag。

## 发布资产检查

`Release` 至少应包含：

- `*_client.tar.gz`、`*_server.tar.gz`、`npc_sdk.tar.gz`、
  `config-examples.tar.gz`
- `npsctl_linux_amd64`、`npsctl_linux_arm64`、`npsctl_darwin_*`、
  `npsctl_windows_*.exe` 等各平台二进制
- `nps-skill.tar.gz`
- `android_client.apk`、fnOS `*.fpk`、GUI `*.zip`
- `checksums.txt`（覆盖 tar.gz、apk、zip、fpk 与 `npsctl_*`）

发布后用 `gh release view vX.Y.Z --json assets` 核对关键资产，并实际测试：

```bash
curl -fsSL https://github.com/ZiDuNet/nps/releases/latest/download/nps-skill.tar.gz | tar -tz
curl -fsSL https://github.com/ZiDuNet/nps/releases/latest/download/npsctl_linux_amd64 -o /tmp/npsctl
chmod +x /tmp/npsctl && /tmp/npsctl version
```

Docker 用户还要确认 `nps` / `npc` 的版本标签和 `latest` 指向新版本。

## 发布后文档检查

- GitHub 首页 README 标题显示新版本号。
- `docs/README.md`、`docs/changelog*` 顶部为新版本。
- Release Notes 来自 `CHANGELOG.md` 对应段落，且包含 Full Changelog 链接。
- 面板 `/api/v1/info` 与 `npsctl version` 一致。

## 常见失败

- `Release tag must equal vX.Y.Z`：源码版本与 tag 不一致。
- `CHANGELOG.md has no section for vX.Y.Z`：缺少对应版本段落。
- `tag must point to the current origin/master commit`：tag 打在了旧提交或
  非 master 提交上。
- 资产 404：检查 `build.assets.sh` 是否生成、`upload-artifact` 是否包含、
  `publish_release.files` 是否匹配；裸二进制需单列 `npsctl_*`。
- 跨架构构建失败：先运行 `bash scripts/check-release-targets.sh`。SQLite
  驱动不支持 Linux MIPS server；MIPS NPC 必须保持不依赖 SQLite，不要通过
  复制旧资产或伪造版本号绕过。
- Node.js 20 deprecation 警告目前不阻塞发布，但升级 action 版本时应一并处理。

## 红线

- 不在带有其他开发分支未提交改动的工作区直接切分支发版。
- 不把其他 Agent / 开发分支的未提交文件混入发版提交。
- 不用 `workflow_dispatch` 代替 tag 发布，除非只是验证流水线。
- 不移动、不删除已发布 tag；需要修复时递增版本重新发布。
