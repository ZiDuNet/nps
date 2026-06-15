# NPS 服务端

NPS 是一款轻量级、高性能的内网穿透代理服务器，支持 TCP、UDP、HTTP(S) 反向代理、HTTP 正向代理、SOCKS5、P2P、Secret 和文件访问。

## 主要特性

- Web 管理面板
- 独立用户体系，一个用户可管理多个客户端
- 用户级和客户端级隧道配额
- HTTP/HTTPS 域名代理
- TLS Bridge 加密连接
- IP 白名单/黑名单、验证码、限速、限流
- JSON 文件持久化，轻量易备份

## 快速启动

```bash
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8025:8025 \
  -p 8080:8080 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps:latest
```

查看首次启动账号密码：

```bash
docker logs nps | head -20
```

Web 面板：

```text
http://<服务器IP>:8080
```

## 端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 反向代理 |
| 443 | HTTPS 反向代理 |
| 8024 | Bridge TCP |
| 8025 | Bridge TLS |
| 8080 | Web 管理面板 |

## 数据目录

请挂载 `/conf`，其中包含 `nps.conf`、`clients.json`、`users.json`、`tasks.json`、`hosts.json` 等配置和数据文件。

项目地址：https://github.com/ZiDuNet/nps
