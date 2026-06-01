# GUI 客户端

基于 Wails 开发的桌面客户端（Windows），需要 WebView2 运行时。

## 下载

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载 `npc-gui-windows-amd64.zip`。

## 使用方式

### 方式一：快捷启动命令（推荐）

1. 在 Web 面板客户端列表展开详情，复制快捷启动命令
2. 打开 GUI 客户端，粘贴快捷启动命令
3. 点击连接

### 方式二：手动添加

1. 打开 GUI 客户端
2. 填写服务端地址（`ip:port`）和连接密钥
3. 点击连接

## 功能

- 系统托盘图标，最小化到托盘
- 连接状态实时显示
- 支持管理多个连接配置
- 开机自启动

## 配置存储

连接配置自动保存到：
- Windows: `%APPDATA%\npc\npc_data.json`

## 快捷命令格式

快捷命令为 Base64 编码，解码格式：
```
nps:名称|地址:端口|密钥|是否TLS
```
