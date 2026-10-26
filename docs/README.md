# NPS 文档

> 轻量级、高性能、功能完整的内网穿透代理服务器。

当前版本：**v1.1.34**。本版本调整管理面板列表与表单排版：域名反代列表移除「认证」列，改为在访问入口前显示 Basic 认证锁图标；域名反代与 TCP 隧道的展开详情统一为紧凑卡片布局；全局参数「平台泛域名」输入区改为单列并限制输入宽度。同时修复域名反代新增/编辑页在健康检查目标为空时保存被浏览器校验拦截的问题。

## 适用场景

- 将内网 Web 服务通过域名暴露到公网。
- 将 SSH、远程桌面、数据库等 TCP 服务映射到公网端口。
- 通过 SOCKS5/HTTP 代理访问内网资源。
- 为多个客户、团队或设备分配独立账号，并集中管理客户端。
- 使用 Docker 快速部署服务端和客户端。

## 核心能力

- 支持 TCP、UDP、HTTP(S) 反向代理、HTTP / SOCKS 混合代理、P2P、Secret、文件访问。混合代理在一个公网端口上按需开启 HTTP 和 SOCKS5，旧版独立 HTTP/SOCKS 记录继续兼容。
- Web 管理面板支持客户端、隧道、域名解析、用户、全局配置等管理能力；管理员可在「全局参数 → 服务端配置」按功能分组维护 `nps.conf`，可选配置关闭后不会写入有效配置文件，敏感值脱敏，保存后支持确认重启。仪表盘可进入按当前账号授权范围筛选的实时资源拓扑页，提供浅色/深色主题、中文/英文切换、响应式布局和键盘可访问交互。资源拓扑默认每 60 秒刷新，可在页面上自定义 5–3600 秒的刷新间隔，设置仅保存在当前浏览器；管理员和普通用户均可生成独立的只读大屏快捷地址。
- 管理员可创建用户，并把多个客户端分配给同一个用户；普通用户也可在管理员设置的客户端配额内自助创建客户端。
- 支持客户端级和用户级隧道数量限制，Host 域名规则也计入隧道配额；管理员分配与用户自建客户端共用用户客户端配额。
- 用户与客户端分别支持到期时间：客户端到期不得晚于用户到期，客户端留空时跟随用户；用户未设置到期时，客户端可独立设置或永不过期。
- 管理员可维护多个平台泛域名及其证书，用户可安全创建唯一子域名；证书外部续期后，新 TLS 连接自动使用有效的新文件。
- 支持客户端到期、用户到期、IP 白名单/黑名单、验证码、带宽/流量/连接数限制。
- 数据继续使用 JSON 文件持久化，便于轻量部署和备份。
- Bridge 支持 TCP、KCP、WebSocket、WSS 和可选 QUIC；KCP、WS、WSS、QUIC 可通过独立端口启用。TLS Bridge 仍使用独立 TLS 端口。现有同端口复用只覆盖受支持的 Bridge、HTTP(S) 和管理面板协议路由。
- 客户端支持压缩/加密传输选项、SOCKS5 出站代理、P2P/Secret 本地访问端和 GUI 快捷启动命令。

## 推荐阅读顺序

1. [安装与部署](install/)
2. [快速开始](start.md)
3. [运行命令速查](run.md)
4. [用户体系](user.md)
5. [隧道模式](tunnel.md)
6. [系统架构](architecture.md)
7. [服务端配置文件参考](server/server_config.md)
8. [客户端配置](client_config.md)
9. [Docker 部署](docker.md)
10. [GUI 客户端](gui.md)
11. [升级迁移](migrate.md)
12. [对外 API v1](api.md)
13. [1.1.34 本地升级说明](upgrade-1.1.34.md)
14. [1.1.33 本地升级说明](upgrade-1.1.33.md)
15. [1.1.32 本地升级说明](upgrade-1.1.32.md)
16. [1.1.31 本地升级说明](upgrade-1.1.31.md)
17. [1.1.30 本地升级说明](upgrade-1.1.30.md)
18. [功能与兼容矩阵](feature-matrix.md)

## 文档目录

### 快速上手

- [安装与部署](install/)
- [完整部署参考](install.md)
- [快速开始](start.md)
- [运行命令速查](run.md)
- [使用示例](extend/example.md)
- [隧道模式](tunnel.md)

### 服务端

- [服务端介绍](introduction.md)
- [服务端使用](server/nps_use.md)
- [服务端配置文件参考](server/server_config.md)
- [部署安全与参数速查](server_config.md)
- [服务端增强功能](server/nps_extend.md)
- [Docker 部署](docker.md)
- [宝塔面板部署](bt.md)

### 客户端

- [客户端配置与启动](client_config.md)
- [配置文件参考](client/config-file.md)
- [NPC Docker 部署](client/docker.md)
- [客户端使用](client/use.md)
- [客户端增强功能](client/npc_extend.md)
- [GUI 客户端](gui.md)
- [NPC SDK](client/npc_sdk.md)

### 扩展功能

- [功能概览与能力边界](extend/feature.md)
- [域名代理与路由](extend/domain-proxy.md)
- [平台泛域名、证书热更新与规则诊断](extend/platform-domain.md)
- [访问控制与配额](extend/access-control.md)
- [运行说明与排查](extend/description.md)
- [对外 API v1](api.md)
- [Web API 鉴权](extend/api.md)
- [Web API 清单](extend/webapi.md)

### 项目与社区

- [用户体系](user.md)
- [系统架构](architecture.md)
- [升级迁移](migrate.md)
- [1.1.34 本地升级说明](upgrade-1.1.34.md)
- [1.1.33 本地升级说明](upgrade-1.1.33.md)
- [1.1.32 本地升级说明](upgrade-1.1.32.md)
- [1.1.31 本地升级说明](upgrade-1.1.31.md)
- [1.1.30 本地升级说明](upgrade-1.1.30.md)
- [功能与兼容矩阵](feature-matrix.md)
- [构建发布](build.md)
- [本项目更新日志](changelog.md)
- [FAQ](faq.md)
- [贡献](contribute.md)
- [交流](discuss.md)
- [捐助](donate.md)
- [致谢](thanks.md)

## 项目链接

- [GitHub](https://github.com/ZiDuNet/nps)
- [Docker Hub - nps](https://hub.docker.com/r/wushuo98/nps)
- [Docker Hub - npc](https://hub.docker.com/r/wushuo98/npc)
