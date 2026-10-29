# NPC GUI 客户端

NPC GUI 是基于 Wails 的桌面客户端，适合 Windows/macOS/Linux 桌面环境。它用于管理并启动 NPC 连接。

和命令行 `npc` 的区别：

- GUI 不是简单调用外部 `npc` 进程。
- GUI 在程序内直接调用客户端核心逻辑连接 NPS。
- GUI 的连接配置和日志保存在当前用户目录。
- GUI 的开机自启动是用户级，不是系统服务。

## 下载

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载 GUI 压缩包。当前 CI 会构建：

| 系统 | 架构 | 产物 |
|------|------|------|
| Windows | 386 / amd64 / arm64 | `npc-gui-windows-<arch>.zip` |
| macOS | amd64 / arm64 | `npc-gui-darwin-<arch>.zip` |
| Linux | amd64 | `npc-gui-linux-amd64.zip` |

Windows 需要 WebView2 Runtime。Windows 10/11 通常已内置；如果启动失败，请先安装 Microsoft Edge WebView2 Runtime。

Linux 需要桌面环境以及 GTK4/WebKitGTK 6 运行库。源码构建使用 Wails 3
默认 GTK4 构建链，开发依赖为 `libgtk-4-dev`、`libwebkitgtk-6.0-dev`、
`libsoup-3.0-dev` 和 `libjavascriptcoregtk-6.0-dev`。

## 添加连接

### 使用快捷启动命令

1. 在 NPS Web 管理面板打开「客户端」页面。
2. 展开客户端详情。
3. 在连接方式页签中选择 TCP、TCP + TLS、KCP、WS、WSS、QUIC 或 HTTP/3。
4. 复制该入口的快捷启动命令。
5. 打开 NPC GUI，粘贴命令并添加。
6. 点击连接。

快捷启动命令是 Base64。TCP/TCP + TLS 使用历史 v1 格式：

```text
nps:<名称>|<服务端地址:端口>|<VerifyKey>|<是否TLS>
```

KCP、WS、WSS、QUIC 和 HTTP/3 使用 v2 JSON 格式。v2 可包含 `transport`、`bridge_path`、`quic_alpn`、`tls_server_name`、`tls_fingerprint` 等字段，能够无损恢复连接方式。GUI 会同时识别 v1 和 v2；v2 需要 `1.1.37` 或更高版本。面板命令旁的 `v1`/`v2` 标记表示兼容范围。

### 手动添加

手动添加需要填写：

- 名称：本地显示名称。
- 服务端地址：使用 Web 面板当前协议页签中显示的地址和端口。
- `VerifyKey`：Web 面板中客户端的连接密钥。
- Bridge 协议：TCP、TCP + TLS、KCP、WS、WSS 或 QUIC。
- WS/WSS：填写与服务端一致的 Bridge Path，默认 `/ws`。
- QUIC：填写 ALPN，默认 `nps`。
- TLS 类入口：可选填写 TLS Server Name、证书 SHA-256 指纹和自定义 CA 文件。

TLS Server Name、证书固定和“跳过证书校验”等字段只在 TCP + TLS、WSS、QUIC 时显示。“跳过证书校验”仅用于临时排查，生产环境建议固定证书指纹或配置 CA。

## 运行状态

GUI 会为每个连接显示状态：

| 状态 | 含义 |
|------|------|
| `stopped` | 未运行 |
| `connecting` | 正在连接或断线重连 |
| `connected` | 已连接服务端 |

连接断开后，GUI 内部会按客户端逻辑继续重连，直到用户手动停止。

## 配置和日志

配置文件：

| 系统 | 配置文件 |
|------|----------|
| Windows | `%APPDATA%\npc\npc_data.json` |
| macOS | `~/Library/Application Support/npc/npc_data.json` |
| Linux | `~/.config/npc/npc_data.json` |

默认日志目录：

| 系统 | 日志目录 |
|------|----------|
| Windows | `%APPDATA%\npc\logs` |
| macOS | `~/Library/Application Support/npc/logs` |
| Linux | `~/.config/npc/logs` |

每个客户端会有独立日志文件：

```text
npc-client-<地址>-<vkey>[-协议].log
```

同一 VKey 的 TCP、WS、QUIC 等连接会分别记录，不会混写到同一个日志文件。

GUI 设置中如果自定义了日志目录，会优先使用自定义目录。

## 开机自启动

GUI 的开机自启动按当前用户配置：

| 系统 | 实现方式 |
|------|----------|
| Windows | 写入 `Software\Microsoft\Windows\CurrentVersion\Run` |
| macOS | 写入 `~/Library/LaunchAgents/com.nps.client.plist` |
| Linux | 写入 `$XDG_CONFIG_HOME/autostart/nps-client.desktop` 或 `~/.config/autostart/nps-client.desktop` |

这和 `npc install` 安装系统服务不同。GUI 自启动需要用户登录桌面后才会启动。

## 从源码构建

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

构建桌面程序：

```bash
cd cmd/npc/npc-gui
wails3 build --tags npcgui
```

Windows amd64 示例：

```bash
wails3 build --tags npcgui
```

Linux amd64 示例：

```bash
sudo apt-get install build-essential libgtk-4-dev libwebkitgtk-6.0-dev \
  libsoup-3.0-dev libjavascriptcoregtk-6.0-dev \
  libayatana-appindicator3-dev librsvg2-dev patchelf pkg-config
cd cmd/npc/npc-gui
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 wails3 build --tags npcgui
```

`wails.json` 已指定前端包管理器为 Yarn，不要混用 npm 生成 `package-lock.json`。CI 会由 Wails 3 按配置自动执行以下前端步骤：

```bash
yarn install --frozen-lockfile
yarn build
```

更多构建说明见 [构建发布](build.md)。
