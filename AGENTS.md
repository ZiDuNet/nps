# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## 项目概述

NPS（内网穿透代理服务器）是一个轻量级、高性能的内网穿透工具，当前版本 1.1.0。基于原版 nps 0.26.10 二次开发。模块路径 `ehang.io/nps`，Go 1.24+。

## 构建命令

```bash
# 快速构建
go build cmd/nps/nps.go          # 服务端
go build cmd/npc/npc.go          # 客户端

# Makefile（推荐）
make build                       # 构建 nps + npc
make test                        # 带竞态检测和覆盖率的测试
make lint                        # golangci-lint + misspell
make ci                          # 完整 CI：build + test + lint + go-mod-tidy

# 单个测试
go test -failfast -race -coverpkg=./... -covermode=atomic -coverprofile=coverage.txt ./lib/... -run TestXXX -timeout=2m

# 格式化
gofmt -w -s <file>

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" ./cmd/npc/npc.go

# GUI 客户端（Wails）
cd cmd/npc/npc-gui && wails dev   # 开发
cd cmd/npc/npc-gui && wails build # 构建

# Docker
docker build -f Dockerfile.nps -t nps .
docker build -f Dockerfile.npc -t npc .
```

## 架构

C/S 架构，服务端（NPS）和客户端（NPC）通过 Bridge 层通信。服务端支持多种代理模式。

### 通信协议

连接建立流程（`bridge/bridge.go` → `cliProcess`）：
1. 客户端发送 3 字节测试标志 + 版本字符串
2. 服务端回复版本哈希
3. 客户端发送 32 字节 vKey（验证密钥）
4. 验证成功后发送 4 字节连接类型标志，分发到对应处理器

连接类型常量（定义在 `lib/common/const.go`）：
- `WORK_MAIN`（main）→ 信号/控制连接，长连接 + 心跳
- `WORK_CHAN`（chan）→ 数据隧道，经 nps_mux 多路复用
- `WORK_CONFIG`（conf）→ 配置交换（增删主机/隧道、查询状态）
- `WORK_REGISTER`（rgst）→ IP 注册
- `WORK_SECRET`（sert）→ 私密代理连接
- `WORK_FILE`（file）→ 文件传输通道
- `WORK_P2P`（p2pm）→ P2P 中继

结构化消息的线协议格式（`lib/conn/conn.go` → `SendInfo`）：
```
+--------+--------+---------+
| 4-byte | 4-byte | content |
|  flag  |  len   | (JSON)  |
+--------+--------+---------+
```

### 核心数据流

1. **控制面**：客户端 `WORK_MAIN` 连接 → Bridge 验证 vkey → 保持长连接（ping 心跳每 5 秒，3 次失败踢出）
2. **数据面**：客户端 `WORK_CHAN` 连接 → nps_mux 多路复用 → 服务端 `SendLinkInfo` 下发连接请求 → 客户端连本地目标 → 双向转发
3. **存储**：所有数据（clients/tasks/hosts）存在 JSON 文件中，`sync.Map` 内存缓存，定时持久化。`lib/file/` 中 `JsonDb` 负责读写

### 代理模式

Service 接口（`server/proxy/base.go`）：`Start() error` + `Close() error`。`BaseServer` 提供公共逻辑（流量统计、认证、限速、`DealClient` 双向转发）。

`server/server.go` → `NewMode` 根据模式字符串创建对应实现：
- **TunnelModeServer**（tcp/file/httpProxy/tcpTrans）→ 监听端口 → 收连接 → Bridge 转发到客户端 → 本地目标 → 双向转发
- **HttpServer**（httpHostServer）→ 基于域名的 HTTP/HTTPS 反向代理，Host 头路由
- **Sock5ModeServer** → SOCKS5 代理
- **UdpModeServer** → UDP 转发
- **P2P** → 点对点穿透中继

### 数据模型（`lib/file/obj.go`）

- `Client`：客户端（Id, VerifyKey, RateLimit, Flow, MaxConn, NowConn, WebUserName/Password, IpWhite, BlackIpList）
- `Tunnel`：隧道（Id, Port, Mode, Client, Target, Health 健康检查配置）
- `Host`：HTTP 虚拟主机（Host 域名, Location, Scheme, CertFilePath, AutoHttps）
- `Target`：负载均衡后端（TargetStr 换行分隔多目标，`GetRandomTarget()` 轮询选择）
- `Flow`：流量计量（ExportFlow, InletFlow, FlowLimit）
- `Glob`：全局设置（BlackIpList, ServerUrl）

### Web 管理面板

Beego 框架，路由在 `web/routers/router.go`。AutoRouter 映射：`ClientController.Add` → `/client/add`。

控制器（`web/controllers/`）：IndexController（仪表盘）、LoginController（认证+验证码）、ClientController（客户端 CRUD）、AuthController（IP 白名单授权）、GlobalController（全局设置）。

静态文件通过 `go:embed` 嵌入可执行文件（`web/embed.go`），启动时按需释放到临时目录。

### 配置

- 服务端：`conf/nps.conf`（INI 格式，beego 加载），首次启动自动生成随机密码
- 客户端：无需配置文件，通过命令行参数或交互式输入启动
- 关键端口：Bridge TCP 8024、Bridge TLS 8025、Web 面板 8080、HTTP 代理 80、HTTPS 代理 443

### 测试模式

5 个测试文件，分布在 `lib/config/`、`lib/conn/`、`lib/nps_mux/`、`lib/pmux/`、`server/proxy/`。使用 `net.Pipe()` 模拟连接、`t.TempDir()` 管理临时配置、`t.Cleanup()` 恢复全局状态。

### 目录概览

```
bridge/              → Bridge 层：客户端连接管理、隧道复用、心跳
client/              → NPC 客户端核心（TRPClient、连接建立、本地服务）
cmd/nps/             → 服务端入口（系统服务管理）
cmd/npc/             → 客户端入口（CLI + 交互式）
cmd/npc/npc-gui/     → Wails GUI 客户端（Go + Vue.js）
cmd/npc/sdk.go       → 客户端 SDK（构建为 .dll/.so）
server/proxy/        → 代理模式实现（tcp/udp/socks5/http/https/p2p/websocket）
server/server.go     → 服务端生命周期（启停隧道、AddTask、NewMode）
lib/file/            → 数据模型 + JSON 持久化（JsonDb, DbUtils）
lib/conn/            → 连接协议封装（Conn, Link, SnappyConn, 线协议）
lib/nps_mux/         → 自定义多路复用库
lib/rate/            → 令牌桶限速器
lib/crypt/           → TLS 证书管理
lib/config/          → 客户端配置文件解析
lib/common/          → 常量、连接池、字节操作
web/                 → Web 管理面板（Beego MVC）
conf/                → 默认配置 + JSON 数据 + SSL 证书
build.sh             → 跨平台发布构建脚本（15+ 平台）
Makefile             → build/test/lint/ci
Dockerfile.nps/.npc  → 多阶段 Docker 构建
docker-compose.yml   → 服务端一键部署
```
