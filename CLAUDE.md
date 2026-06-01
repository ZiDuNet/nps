# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

NPS（内网穿透代理服务器）是一个轻量级、功能强大的内网穿透工具。本项目基于原版 nps 0.26.10 二次开发，当前版本 0.26.33。支持 TCP/UDP 流量转发、HTTP(S) 代理、SOCKS5 代理、P2P 穿透等。

## 构建命令

```bash
# 构建服务端和客户端
go build cmd/nps/nps.go
go build cmd/npc/npc.go

# 运行测试
go test ./...

# 运行单个测试包（含覆盖率）
go test -failfast -race -coverpkg=./... -covermode=atomic -coverprofile=coverage.txt ./lib/... -run TestXXX -timeout=2m

# 格式化
gofmt -w -s <file>

# 交叉编译示例
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" ./cmd/npc/npc.go
```

模块路径：`ehang.io/nps`，Go 版本要求：1.24+

## 架构

项目采用 C/S 架构，由服务端（NPS）和客户端（NPC）组成，通过 Bridge 层通信。

### 核心组件

```
cmd/nps/nps.go       → 服务端入口，初始化配置/Web路由/Bridge，支持系统服务管理
cmd/npc/npc.go       → 客户端入口，支持命令行和交互式两种启动方式
cmd/npc/npc-gui/     → 基于 Wails 的 GUI 客户端（需 WebView2）
cmd/npc/sdk.go       → 客户端 SDK（构建为 .dll/.so）

bridge/bridge.go     → 服务端 Bridge 层：管理客户端连接、隧道复用、心跳检测、配置下发
client/client.go     → 客户端核心（TRPClient）：建立主连接和隧道连接，处理流量转发
client/control.go    → 客户端连接建立（NewConn）
client/local.go      → 本地服务（P2P/Secret 模式）

server/server.go     → 服务端任务管理：启停隧道、客户端管理、流量统计
server/proxy/        → 代理服务实现
  base.go            → Service 接口定义 + BaseServer 基类
  tcp.go             → TCP 隧道代理（含负载均衡）
  http.go / https.go → HTTP/HTTPS 反向代理（域名解析）
  socks5.go          → SOCKS5 代理
  udp.go             → UDP 代理
  websocket.go       → WebSocket 代理
  p2p.go             → P2P 穿透服务
  transport.go       → Proxy Protocol 支持（传递真实 IP）

lib/nps_mux/         → 自定义多路复用库（基于连接的 Mux 协议）
lib/rate/            → 限速器（令牌桶）
lib/crypt/           → TLS 证书管理、加密工具
lib/conn/            → 连接封装（Snappy 压缩、自定义协议）
lib/file/            → 数据持久化层（JSON 文件存储）
  file.go            → JsonDb：sync.Map 为内存存储，定时持久化到 JSON 文件
  obj.go             → 数据模型：Client, Tunnel, Host, Flow, Target
  db.go              → DbUtils：CRUD 操作
lib/config/          → 客户端配置文件解析
lib/common/          → 通用工具（连接池、字节操作、平台检测）

web/                  → Web 管理面板
  embed.go           → 静态文件嵌入可执行文件（go:embed），启动时按需释放
  routers/router.go  → Beego 路由（Index/Login/Client/Auth/Global 控制器）
  controllers/       → Beego 控制器（认证、客户端管理、隧道管理、全局设置）
```

### 数据流

1. **控制面**：客户端通过 `WORK_MAIN` 标志建立信号连接，服务端 Bridge 验证 vkey 后保持长连接（心跳+健康检查）
2. **数据面**：客户端通过 `WORK_CHAN` 标志建立隧道连接，经 nps_mux 多路复用后，服务端通过 `SendLinkInfo` 下发连接请求
3. **存储**：所有配置（clients/hosts/tasks/global）存储在 JSON 文件中，通过 `sync.Map` 内存缓存，定时持久化

### 隧道模式

Service 接口有两种实现路径：
- **TunnelModeServer**（tcp/file/socks5/httpProxy/tcpTrans）：监听端口 → 收到连接 → 通过 Bridge 发送到客户端 → 客户端连接本地目标 → 双向转发
- **HttpServer**（httpHostServer）：基于域名的 HTTP/HTTPS 反向代理，根据 Host 头路由到不同客户端

### 配置

- 服务端配置：`conf/nps.conf`（INI 格式，通过 beego 加载）
- 客户端无配置文件模式：直接通过命令行参数或交互式输入启动
- 首次启动自动生成随机密码的默认配置
