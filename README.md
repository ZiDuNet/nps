# NPS <sub>v1.0.0</sub>

![](https://img.shields.io/github/v/tag/ehang-io/nps.svg) ![](https://img.shields.io/github/stars/ehang-io/nps.svg) ![](https://img.shields.io/github/forks/ehang-io/nps.svg)

NPS 是一款轻量级、高性能、功能强大的**内网穿透**代理服务器。支持 **TCP/UDP 流量转发、HTTP(S) 反向代理、SOCKS5 代理、P2P 穿透**等，并配备现代化 Web 管理面板。

基于原版 nps 0.26.10 二次开发，修复了大量 bug，优化了性能和安全性，并重新设计了 Web UI。

## 特性

- **协议全面** — TCP、UDP、HTTP(S)、SOCKS5、P2P、Secret、文件访问
- **跨平台** — Linux / Windows / macOS / ARM / 群晖，支持一键安装为系统服务
- **Web 管理** — 现代化 UI，支持明暗主题切换，实时流量和网速监控
- **安全增强** — 首次启动随机生成密码、IP 白名单/黑名单、验证码、限速限流
- **域名代理** — 自定义 Header、404 页面、Host 修改、URL 路由、泛解析、自动 HTTPS
- **TLS 加密** — 支持客户端与服务端之间 TLS 加密通信
- **Docker 部署** — 多平台镜像（amd64/arm/arm64），一键启动
- **GUI 客户端** — 基于 Wails 的桌面客户端（Windows）

## 快速开始

### 服务端

```bash
# 直接运行
./nps

# 安装为系统服务
./nps -server        # 交互式引导安装/卸载

# Docker 部署
docker run -d --name nps -p 80:80 -p 443:443 -p 8024:8024 -p 8080:8080 -v /path/to/conf:/conf wushuo98/nps
```

启动后访问 `http://ip:8080` 进入 Web 管理面板，首次启动会在终端打印随机生成的用户名和密码。

### 客户端

```bash
# 直接运行（交互式）
./npc

# 命令行模式
./npc -server=your-ip:8024 -vkey=your-key

# TLS 模式
./npc -server=your-ip:8025 -vkey=your-key -tls_enable=true

# Docker
docker run -d --name npc wushuo98/npc -server=your-ip:8024 -vkey=your-key
```

> 推荐使用无配置文件模式启动客户端（删除 npc 目录下的 `conf` 文件夹），所有配置在服务端 Web 面板管理。

## 隧道模式

| 模式 | 说明 | 使用场景 |
|------|------|---------|
| TCP | TCP 端口转发，支持负载均衡 | SSH、远程桌面、数据库 |
| UDP | UDP 端口转发 | DNS、游戏、内网 UDP 服务 |
| HTTP(S) | 基于域名的反向代理 | 微信开发、小程序、Web 站点 |
| SOCKS5 | SOCKS5 代理 | 内网资源访问 |
| P2P | 点对点穿透 | 直连内网设备 |
| Secret | 私密代理 | 安全的临时连接 |
| 文件 | 内网文件访问 | 文件浏览与下载 |

## 构建从源码

```bash
# 需要 Go 1.24+
go build cmd/nps/nps.go     # 服务端
go build cmd/npc/npc.go     # 客户端

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" ./cmd/nps/nps.go
```

## Docker Hub

- **NPS 服务端**: [wushuo98/nps](https://hub.docker.com/r/wushuo98/nps)
- **NPC 客户端**: [wushuo98/npc](https://hub.docker.com/r/wushuo98/npc)

## 宝塔面板

详见 [宝塔面板 Docker 部署指南](docs/bt.md)

## 文档

- [完整文档](https://ehang.io/nps/documents)
- [新特性文档](https://dqg9t9eulqq.feishu.cn/wiki/FmVVwDcEGiTZxekYJl5ccuFanlg)

## 更新日志

- **2026-06-02 v1.0.0**
  - **版本号升级至 1.0.0**，标志项目进入稳定阶段
  - **Web UI 现代化改造**：
    - 全新现代化 CSS 主题（modern.css），支持明暗主题一键切换
    - 登录页面重新设计，双栏布局，左侧功能介绍 + 右侧登录表单
    - 侧边栏菜单分组优化，增加「隧道管理」「系统」分组标题
    - 按钮、表格、表单、卡片、统计面板全面美化（圆角、渐变、阴影、动画）
    - ECharts 图表自适应明暗主题色
  - **Bug 修复**（33 项）：
    - 修复 mux 连接池 goroutine 泄漏、内存泄漏
    - 修复 HTTP/SOCKS5/P2P 代理服务连接泄漏
    - 修复登录验证码绕过漏洞、密码强度校验
    - 修复客户端列表网速/连接数不显示（Rate nil 安全检查）
    - 修复限速零值导致令牌桶永久阻塞
  - **性能优化**（16 项内存泄漏修复）：
    - HTTP/HTTPS/SOCKS5 代理服务 response body 未关闭
    - mux 连接 map 未清理、rate limiter 未释放
    - 客户端断开后资源未回收
  - **安全加固**：
    - 登录验证码绕过漏洞修复
    - 注册密码强度校验（最少 6 位）
    - 首次启动随机生成管理密码

- **2026-05-23 v0.26.33**
  - 配置文件自动生成，首次启动随机密码
  - Web 静态文件嵌入可执行文件，部署无需拷贝 web 目录
  - 修复：限速导致隧道中断、关闭隧道端口仍可用、TCP 负载均衡异常、实时网速不显示等

- **2026-03-27 v0.26.32** — 修复客户端注册参数、HTTPS 反向代理 bug
- **2026-03-23 v0.26.31** — 新增域名解析开关、TCP 隧道 Basic 认证
- **2026-03-12 v0.26.30** — 修复 HTTP WebSocket、域名解析端口跳转、自动 HTTPS
- **2026-01-24 v0.26.29** — 新增 GUI 桌面客户端（Wails）
- **2025-12-06 v0.26.28** — 全局参数新增服务地址配置、IP 授权优化、限速器重构
- **2025-11-05 v0.26.27** — 客户端 IP 白名单、移除压缩/加密功能
- **2025-08-15 v0.26.26** — 修复 Windows TLS 服务、HTTPS 自动跳转
- **2025-05-28 v0.26.25** — 新增 `nps -server` 服务管理命令、TLS 快捷启动命令
- **2025-04-16 v0.26.24** — 隧道复制功能、修复私密代理连接崩溃
- **2025-04-11 v0.26.23** — TCP 隧道 Proxy Protocol、协程增长优化

<details>
<summary>查看更早的更新日志</summary>

- **2025-01-23 v0.26.22** — 客户端日志按 vkey 分文件、HTTPS 证书支持文件路径
- **2025-01-07 v0.26.21** — 快捷启动命令、交互式客户端启动、vkey 缩短至 10 位
- **2024-11-07 v0.26.20** — 客户端创建时间、限速单位统一、隧道列表排序
- **2024-06-01 v0.26.19** — Go 1.22、自动 HTTPS、多隧道 ID 启动
- **2024-02-27 v0.26.18** — TLS Bridge 端口支持
- **2024-01-31 v0.26.17** — TLS 流量加密
- **2023-06-01 v0.26.16** — HTTPS 流量统计修复、全局黑名单 IP、客户端在线时间
- **2023-02-24 v0.26.15** — 指定配置文件路径 `-conf_path`
- **2022-12-30 v0.26.14** — API 鉴权漏洞修复
- **2022-12-19** — 丢包崩溃修复、自动端口分配
- **2022-10-30** — 客户端黑名单 IP、系统服务注册还原
- **2022-10-27** — 登录验证码
- **2022-10-24** — HTTP WebSocket 支持
- **2022-10-21** — HTTP 实时流量统计
- **2022-10-19** — TCP 实时流量统计

</details>

## 许可证

GPL-3.0
