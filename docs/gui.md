# GUI 客户端

GUI 客户端基于 Wails 开发，适合 Windows/macOS/Linux 桌面环境管理和启动 `npc` 连接。

## 下载

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应平台的 GUI 压缩包。

Windows 需要 WebView2 Runtime。Windows 10/11 通常已内置；如果启动失败，请先安装 Microsoft Edge WebView2 Runtime。

## 使用方式

### 快捷启动命令

1. 在 Web 管理面板打开客户端详情。
2. 复制快捷启动命令。
3. 打开 GUI 客户端并粘贴。
4. 点击连接。

快捷启动命令可包含 TLS 参数，GUI 会按命令内容连接普通 Bridge 或 TLS Bridge。

### 手动添加

填写：

- 名称
- 服务端地址（如 `1.1.1.1:8024`）
- `VerifyKey`
- 是否启用 TLS

保存后点击连接。

## 功能

- 管理多个连接配置。
- 显示连接状态。
- 支持系统托盘。
- 支持最小化到托盘。
- 支持开机自启动（依赖操作系统授权）。

## 配置文件

连接配置会保存到用户目录：

```text
Windows: %APPDATA%\npc\npc_data.json
```

## 从源码构建

```bash
cd cmd/npc/npc-gui/frontend
corepack enable
corepack prepare yarn@1.22.22 --activate
yarn install --frozen-lockfile
yarn build

cd ..
wails build
```

构建要求见[构建发布](build.md)。
