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

Linux 需要桌面环境和 WebKitGTK 运行库。源码构建时使用 `webkit2_41` 标签，对应依赖是 `libgtk-3-dev` 和 `libwebkit2gtk-4.1-dev`。

## 添加连接

### 使用快捷启动命令

1. 在 NPS Web 管理面板打开「客户端」页面。
2. 展开客户端详情。
3. 复制「快捷启动命令」或「TLS 快捷启动命令」。
4. 打开 NPC GUI，粘贴命令并添加。
5. 点击连接。

快捷启动命令是 Base64，解码后的格式为：

```text
nps:<名称>|<服务端地址:端口>|<VerifyKey>|<是否TLS>
```

例如 TLS 快捷启动命令会把地址指向 `tls_bridge_port`，并把最后一段设为 `true`。

### 手动添加

手动添加需要填写：

- 名称：本地显示名称。
- 服务端地址：例如 `1.1.1.1:8024`，TLS 时通常是 `1.1.1.1:8025`。
- `VerifyKey`：Web 面板中客户端的连接密钥。
- 是否启用 TLS：必须和服务端地址对应。

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
npc-client-<vkey>.log
```

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
GOOS=linux GOARCH=amd64 WAILS_TAGS=webkit2_41 wails3 build --tags npcgui
```

`wails.json` 已指定前端包管理器为 Yarn，不要混用 npm 生成 `package-lock.json`。CI 会由 Wails 3 按配置自动执行以下前端步骤：

```bash
yarn install --frozen-lockfile
yarn build
```

更多构建说明见 [构建发布](build.md)。
