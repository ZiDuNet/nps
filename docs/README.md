# NPS 文档

> 轻量级、高性能、功能完整的内网穿透代理服务器。

当前版本：**v1.1.2**。本版本支持独立用户体系、一个用户管理多个客户端、用户级隧道配额，以及从旧客户端登录体系自动迁移。

## 适用场景

- 将内网 Web 服务通过域名暴露到公网。
- 将 SSH、远程桌面、数据库等 TCP 服务映射到公网端口。
- 通过 SOCKS5/HTTP 代理访问内网资源。
- 为多个客户、团队或设备分配独立账号，并集中管理客户端。
- 使用 Docker 快速部署服务端和客户端。

## 核心能力

- 支持 TCP、UDP、HTTP(S) 反向代理、HTTP 正向代理、SOCKS5、P2P、Secret、文件访问。
- Web 管理面板支持客户端、隧道、域名解析、用户、全局配置等管理能力。
- 管理员可创建用户，并把多个客户端分配给同一个用户。
- 支持客户端级和用户级隧道数量限制，Host 域名规则也计入隧道配额。
- 支持客户端到期、用户到期、IP 白名单/黑名单、验证码、带宽/流量/连接数限制。
- 数据继续使用 JSON 文件持久化，便于轻量部署和备份。

## 推荐阅读顺序

1. [安装部署](install.md)
2. [快速开始](start.md)
3. [运行命令速查](run.md)
4. [用户体系](user.md)
5. [隧道模式](tunnel.md)
6. [服务端配置](server_config.md)
7. [客户端配置](client_config.md)
8. [Docker 部署](docker.md)
9. [GUI 客户端](gui.md)
10. [升级迁移](migrate.md)

## 项目链接

- [GitHub](https://github.com/ZiDuNet/nps)
- [Docker Hub - nps](https://hub.docker.com/r/wushuo98/nps)
- [Docker Hub - npc](https://hub.docker.com/r/wushuo98/npc)
