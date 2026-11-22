# NPS 服务端

NPS 是一款轻量级、高性能的内网穿透代理服务器，支持 TCP、UDP、HTTP(S) 反向代理、HTTP 正向代理、SOCKS5、P2P、Secret 和文件访问。

`latest` 始终指向最近一次正式 Release；正式 Release 同时推送版本号标签（例如 `1.1.52`）和 `latest`，普通 `master` 提交不会更新 Docker 标签。服务端软件更新提示以 GitHub Releases 正式版本为准，与 Docker 标签无关。

## 主要特性

- Web 管理面板
- 独立用户体系，一个用户可管理多个客户端
- 用户级和客户端级隧道配额
- HTTP/HTTPS 域名代理
- TLS Bridge 加密连接
- IP 白名单/黑名单、验证码、限速、限流
- SQLite 持久化，停服后可备份整个数据目录

## 快速启动

```bash
docker run -d --name nps \
  -p 80:80 -p 443:443 \
  -p 8024:8024 -p 8025:8025 \
  -p 127.0.0.1:8081:8081 \
  -e NPS_WEB_IP=0.0.0.0 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps:latest
```

查看首次启动账号和密码：

```bash
docker logs nps | head -20
```

Web 面板（宿主机回环地址，生产环境请置于 HTTPS 反向代理之后）：

```text
http://127.0.0.1:8081
```

默认管理员账号为 `admin`，密码首次启动随机生成。

## 端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 反向代理 |
| 443 | HTTPS 反向代理 |
| 8024 | Bridge TCP |
| 8025 | Bridge TLS |
| 8081 | Web 管理面板 |

## 数据目录

请持久化整个 `/conf`。`nps.conf` 保存启动参数，`nps.db` 保存客户端、用户、隧道、域名规则和业务设置。不要只复制运行中的 `nps.db`。全新安装不需要迁移。升级旧 JSON 时请先停掉容器并备份挂载目录，按实际镜像版本选择步骤，见[部署和升级文档](https://github.com/ZiDuNet/nps/blob/master/docs/docker.md)。

项目地址：https://github.com/ZiDuNet/nps
