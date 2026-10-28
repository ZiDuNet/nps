# NPC 客户端

NPC 是 NPS 的客户端程序，用于连接 NPS 服务端并把内网服务转发到公网。

NPC 可以直接容器化运行，镜像入口为 `/npc`，命令行参数和物理机二进制一致。

`latest` 始终指向最近一次正式 Release；正式 Release 同时推送版本号标签（例如 `1.1.36`）和 `latest`。NPC 在线升级以 GitHub Releases 正式版本为准，与 Docker 标签无关。

## 快速启动

```bash
docker run -d --name npc wushuo98/npc:latest \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS 模式：

```bash
docker run -d --name npc wushuo98/npc:latest \
  -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

## 使用配置文件

NPC 不会在容器内自动生成 `npc.conf`。先从仓库模板复制一份并修改 `[common]`：

```bash
mkdir -p /opt/npc/conf
cp conf/npc.conf /opt/npc/conf/npc.conf
```

然后只读挂载配置启动：

```bash
docker run -d \
  --name npc \
  --restart unless-stopped \
  -v /opt/npc/conf:/conf:ro \
  wushuo98/npc:latest \
  -config=/conf/npc.conf
```

配置文件完整字段和规则示例见项目文档：[NPC 配置文件参考](https://zidunet.github.io/nps/client/config-file.html)。Docker 专项说明（Compose、`--network host`、自定义 CA、日志和升级）见：[NPC Docker 部署](https://zidunet.github.io/nps/client/docker.html)。

NPC 镜像基于 `scratch`，容器内没有 `sh` 或 `curl`；请使用 `docker logs npc` 查看日志。修改挂载配置后需要重启或重建容器。

## 常用参数

| 参数 | 说明 |
|------|------|
| `-server` | 服务端 Bridge 地址 |
| `-vkey` | 客户端连接密钥，多个用英文逗号分隔 |
| `-tls_enable` | 启用 TLS |
| `-type` | 连接类型，`tcp` 或 `kcp` |
| `-proxy` | 通过 HTTP/SOCKS5 代理连接服务端 |

推荐在服务端 Web 面板管理客户端和隧道，客户端使用无配置文件模式连接。

项目地址：https://github.com/ZiDuNet/nps
