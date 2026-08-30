# NPC GUI 客户端

这是基于 Wails 的 NPS 桌面客户端。它不是外部 `npc` 进程的壳，而是在程序内直接调用客户端核心逻辑连接 NPS 服务端。

## 运行环境

| 系统 | 注意事项 |
|------|----------|
| Windows | 需要 WebView2 Runtime；Windows 10/11 通常已内置 |
| macOS | 使用对应 `darwin/amd64` 或 `darwin/arm64` 构建 |
| Linux | 需要桌面环境和 WebKitGTK，构建时使用 `webkit2_41` 标签 |

## 快捷启动命令

Web 管理面板生成的 GUI 快捷启动命令是 Base64，解码后格式为：

```text
nps:<名称>|<地址:端口>|<密钥>|<是否TLS>
```

也可以在 GUI 中手动填写：

- 名称
- 服务端地址，例如 `1.1.1.1:8024`
- `VerifyKey`
- 是否启用 TLS

## 配置和日志

配置存储：

- Windows：`%APPDATA%\npc\npc_data.json`
- Linux：`~/.config/npc/npc_data.json`
- macOS：`~/Library/Application Support/npc/npc_data.json`

日志目录：

- Windows：`%APPDATA%\npc\logs`
- Linux：`~/.config/npc/logs`
- macOS：`~/Library/Application Support/npc/logs`

每个客户端会生成独立日志文件：

```text
npc-client-<vkey>.log
```

## 开发

前置要求：

- Go 1.24+
- Node.js 20+
- Yarn 1.22.22
- Wails v3.0.0-beta.12

构建前端：

```bash
cd cmd/npc/npc-gui/frontend
corepack enable
corepack prepare yarn@1.22.22 --activate
yarn install --frozen-lockfile
yarn build
```

运行开发模式：

```bash
cd cmd/npc/npc-gui
wails3 dev
```

构建桌面程序：

```bash
wails3 build --tags npcgui
```

Linux 构建示例：

```bash
GOOS=linux GOARCH=amd64 WAILS_TAGS=webkit2_41 wails3 build --tags npcgui
```

`wails.json` 已指定前端包管理器为 Yarn，不要混用 npm 生成 `package-lock.json`。
