# NPC GUI 客户端

基于 Wails 开发的 NPS 桌面客户端，需要 WebView2 运行时。

## 快捷命令

快捷命令为 Base64 编码，解码格式：
```
nps:名称|地址:端口|密钥|是否TLS
```

## 开发

```bash
cd cmd/npc/npc-gui
yarn install
wails dev
```

前置要求：Go 1.24+、Node.js 16+、Yarn

## 配置存储

- Windows: `%APPDATA%\npc\npc_data.json`
- Linux: `~/.config/npc/npc_data.json`
- macOS: `~/Library/Application Support/npc/npc_data.json`
