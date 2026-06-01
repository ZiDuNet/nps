# NPS <sub>v1.0.0</sub>

![](https://img.shields.io/github/v/tag/ehang-io/nps.svg) ![](https://img.shields.io/github/stars/ehang-io/nps.svg)

NPS 是一款轻量级、高性能、功能强大的**内网穿透**代理服务器。支持 **TCP/UDP 流量转发、HTTP(S) 反向代理、SOCKS5 代理、P2P 穿透**等，并配备现代化 Web 管理面板。

基于原版 nps 0.26.10 二次开发，修复了大量 bug，优化了性能和安全性，并重新设计了 Web UI。

## 使用场景

1. **微信公众号/小程序开发** — 域名代理模式，将内网服务暴露到外网
2. **SSH 远程连接** — TCP 代理模式，映射内网机器端口
3. **内网 DNS/UDP 访问** — UDP 代理模式
4. **HTTP 代理访问内网** — HTTP 代理模式
5. **内网资源全面访问** — SOCKS5 代理模式，如同 VPN

## 特性

- **协议全面** — TCP、UDP、HTTP(S)、SOCKS5、P2P、Secret、文件访问
- **跨平台** — Linux / Windows / macOS / ARM / 群晖，支持一键安装为系统服务
- **Web 管理** — 现代化 UI，支持明暗主题切换，实时流量和网速监控
- **安全增强** — 首次启动随机密码、IP 白名单/黑名单、验证码、限速限流
- **域名代理** — 自定义 Header、404 页面、Host 修改、URL 路由、泛解析、自动 HTTPS
- **TLS 加密** — 客户端与服务端之间 TLS 加密通信
- **Docker 部署** — 多平台镜像（amd64/arm/arm64）
- **GUI 客户端** — 基于 Wails 的桌面客户端

## 快速开始

### 服务端

```bash
./nps                  # 直接运行
./nps -server          # 交互式安装/卸载系统服务

# Docker
docker run -d --name nps \
  -p 80:80 -p 443:443 -p 8024:8024 -p 8080:8080 \
  -v /path/to/conf:/conf yisier1/nps
```

访问 `http://ip:8080` 进入管理面板。

### 客户端

```bash
./npc                                         # 交互式运行
./npc -server=ip:8024 -vkey=key               # 命令行模式
./npc -server=ip:8025 -vkey=key -tls_enable=true  # TLS 模式
```

> 推荐无配置文件模式启动客户端，所有配置在服务端管理。

## 构建从源码

```bash
go build cmd/nps/nps.go    # 服务端
go build cmd/npc/npc.go    # 客户端
```

需要 Go 1.24+。

## Docker Hub

- [yisier1/nps](https://hub.docker.com/r/yisier1/nps) — 服务端
- [yisier1/npc](https://hub.docker.com/r/yisier1/npc) — 客户端

## 文档

- [完整文档](https://ehang.io/nps/documents)

## 许可证

GPL-3.0
