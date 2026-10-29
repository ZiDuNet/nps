# NPC GUI 客户端

这是基于 Wails 的 NPS 桌面客户端。它不是外部 `npc` 进程的壳，而是在程序内直接调用客户端核心逻辑连接 NPS 服务端。

## 运行环境

| 系统 | 注意事项 |
|------|----------|
| Windows | 需要 WebView2 Runtime；Windows 10/11 通常已内置 |
| macOS | 使用对应 `darwin/amd64` 或 `darwin/arm64` 构建 |
| Linux | 需要桌面环境和 GTK4/WebKitGTK 6 运行库 |

## 快捷启动命令

Web 管理面板生成的 TCP/TCP + TLS 快捷启动命令是 Base64，v1 解码后格式为：

```text
nps:<名称>|<地址:端口>|<密钥>|<是否TLS>
```

KCP、WS、WSS、QUIC 和 HTTP/3 使用 v2 JSON 快捷命令。GUI 同时兼容
v1 与 v2；v2 需要 NPC/GUI `1.1.37` 或更高版本。

也可以在 GUI 中手动填写：

- 名称
- 服务端地址，例如 `1.1.1.1:8024`
- `VerifyKey`
- Bridge 协议，以及 WS/WSS 路径、QUIC ALPN、TLS Server Name、
  证书指纹或自定义 CA

## 配置和日志

配置存储：

- Windows：`%APPDATA%\npc\npc_data.json`
- Linux：`~/.config/npc/npc_data.json`
- macOS：`~/Library/Application Support/npc/npc_data.json`

日志目录：

- Windows：`%APPDATA%\npc\logs`
- Linux：`~/.config/npc/logs`
- macOS：`~/Library/Application Support/npc/logs`

每个客户端会生成独立日志文件；同一 VKey 的不同协议会分别记录：

```text
npc-client-<地址>-<vkey>[-协议].log
```

## 开发

前置要求：

- Go 1.26.0（以 `cmd/npc/npc-gui/go.mod` 为准）
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
sudo apt-get install build-essential libgtk-4-dev libwebkitgtk-6.0-dev \
  libsoup-3.0-dev libjavascriptcoregtk-6.0-dev \
  libayatana-appindicator3-dev librsvg2-dev patchelf pkg-config
cd cmd/npc/npc-gui
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 wails3 build --tags npcgui
```

Wails 3 当前使用 GTK4/WebKitGTK 6 默认构建链。

`wails.json` 已指定前端包管理器为 Yarn，不要混用 npm 生成 `package-lock.json`。
