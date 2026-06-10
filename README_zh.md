# NPS <sub>v1.1.0</sub>

[![](https://img.shields.io/github/v/release/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/releases)
[![](https://img.shields.io/github/stars/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/stargazers)
[![](https://img.shields.io/github/forks/ZiDuNet/nps.svg)](https://github.com/ZiDuNet/nps/network/members)
[![](https://img.shields.io/docker/pulls/wushuo98/nps.svg)](https://hub.docker.com/r/wushuo98/nps)

NPS 是一款轻量级、高性能、功能强大的**内网穿透**代理服务器。支持 **TCP/UDP 流量转发、HTTP(S) 反向代理、SOCKS5 代理、P2P 穿透**等，并配备现代化 Web 管理面板。

基于原版 nps 0.26.10 二次开发，修复了大量 bug，优化了性能和安全性，并重新设计了 Web UI。

## 使用场景

| 场景 | 推荐模式 | 说明 |
|------|----------|------|
| 微信公众号/小程序开发 | HTTP(S) | 域名代理，将内网服务暴露到外网 |
| SSH 远程连接 | TCP | 端口转发，映射内网机器 22 端口 |
| 内网 DNS/游戏服务器 | UDP | UDP 端口转发 |
| 内网资源全面访问 | SOCKS5 | 代理访问，如同 VPN |
| 安全临时连接 | Secret | 私密代理，一次性连接 |
| 点对点直连 | P2P | 中继协助建立直连 |

## 特性

- 🚀 **协议全面** — TCP、UDP、HTTP(S)、SOCKS5、P2P、Secret、文件访问
- 🖥️ **跨平台** — Linux / Windows / macOS / ARM / 群晖，支持一键安装为系统服务
- 🎨 **Web 管理** — 现代化 UI，支持明暗主题切换，实时流量和网速监控
- 🔒 **安全增强** — 首次启动随机密码、IP 白名单/黑名单、验证码、限速限流
- 🌐 **域名代理** — 自定义 Header、404 页面、Host 修改、URL 路由、泛解析、自动 HTTPS
- 🔐 **TLS 加密** — 客户端与服务端之间 TLS 加密通信
- 📦 **Docker 部署** — 多平台镜像（amd64/arm/arm64），一键启动
- 💻 **GUI 客户端** — 基于 Wails 的桌面客户端（Windows）

## 快速开始

### 服务端

```bash
# 直接运行
./nps

# 安装为系统服务（交互式）
./nps -server

# Docker 部署
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8080:8080 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps
```

启动后访问 `http://<服务器IP>:8080` 进入 Web 管理面板。首次启动会在终端打印随机生成的用户名和密码。

<details>
<summary>📋 端口说明</summary>

| 端口 | 用途 |
|------|------|
| 80 | HTTP 反向代理入口 |
| 443 | HTTPS 反向代理入口 |
| 8024 | Bridge TCP（客户端连接） |
| 8025 | Bridge TLS（加密连接） |
| 8080 | Web 管理面板 |

</details>

### 客户端

```bash
# 交互式运行（推荐）
./npc

# 命令行模式
./npc -server=<IP>:8024 -vkey=<密钥>

# TLS 加密模式
./npc -server=<IP>:8025 -vkey=<密钥> -tls_enable=true

# Docker
docker run -d --name npc \
  wushuo98/npc -server=<IP>:8024 -vkey=<密钥>
```

> 💡 **推荐无配置文件模式**：删除 npc 目录下的 `conf` 文件夹，所有配置在服务端 Web 面板管理。

## 队道模式

| 模式 | 说明 | 典型用途 |
|------|------|---------|
| **TCP** | TCP 端口转发，支持负载均衡 | SSH、远程桌面、数据库 |
| **UDP** | UDP 端口转发 | DNS、游戏、VoIP |
| **HTTP(S)** | 基于域名的反向代理 | 微信开发、Web 站点 |
| **SOCKS5** | SOCKS5 代理 | 内网资源全面访问 |
| **P2P** | 点对点穿透 | 直连内网设备 |
| **Secret** | 私密代理 | 安全的临时连接 |
| **文件** | 内网文件访问 | 文件浏览与下载 |

## 项目结构

```
nps/
├── cmd/
│   ├── nps/           # 服务端入口
│   ├── npc/           # 客户端入口
│   └── npc/npc-gui/   # Wails GUI 客户端
├── bridge/            # Bridge 层（连接管理、隧道复用）
├── server/            # 服务端核心（代理模式实现）
├── client/            # 客户端核心
├── lib/               # 公共库
│   ├── file/          # 数据模型 + JSON 持久化
│   ├── conn/          # 连接协议封装
│   ├── nps_mux/       # 多路复用库
│   ├── rate/          # 限速器
│   └── crypt/         # TLS 证书管理
├── web/               # Web 管理面板（Beego）
├── conf/              # 配置文件 + 数据存储
├── docs/              # 文档网站（Docsify）
├── build.sh           # 跨平台构建脚本
├── Makefile           # 构建/测试/CI
└── Dockerfile.*       # Docker 构建
```

## 构建从源码

需要 Go 1.24+

```bash
# 快速构建
go build cmd/nps/nps.go    # 服务端
go build cmd/npc/npc.go    # 客户端

# Makefile（推荐）
make build                 # 构建 nps + npc
make test                  # 带竞态检测和覆盖率测试
make lint                  # golangci-lint

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" ./cmd/npc/npc.go

# GUI 客户端（需安装 Wails）
cd cmd/npc/npc-gui && wails build
```

## Docker

### Docker Hub

- **服务端**: [wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- **客户端**: [wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

### docker-compose 一键部署

```bash
git clone https://github.com/ZiDuNet/nps.git
cd nps
docker-compose up -d
```

## 宝塔面板

详见 [宝塔面板 Docker 部署指南](docs/bt.md)

## 文档

- 📖 [完整文档](https://ehang.io/nps/documents)
- 🚀 [快速开始](docs/start.md)
- ⚙️ [服务端配置](docs/server_config.md)
- 📱 [客户端配置](docs/client_config.md)
- 🔧 [隧道详解](docs/tunnel.md)
- 🐳 [Docker 部署](docs/docker.md)
- 🖥️ [GUI 客户端](docs/gui.md)

## 更新日志

详见 [CHANGELOG.md](CHANGELOG.md)

### 近期更新

- **v1.1.0** (2026-06-10) — Web UI 现代化改造、Bug 修复、安全加固
- **v1.0.0** (2026-05) — 二次开发基线、流量统计修复、Web UI 重设计

## 贡献

欢迎提交 Issue 和 Pull Request！

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add amazing feature'`)
4. 推送分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

## 许可证

[GPL-3.0](LICENSE)

## 致谢

基于 [ehang-io/nps](https://github.com/ehang-io/nps) 二次开发，感谢原作者的贡献。