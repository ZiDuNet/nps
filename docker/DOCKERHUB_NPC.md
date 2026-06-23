# NPC 客户端

NPC 是 NPS 的客户端程序，用于连接 NPS 服务端并把内网服务转发到公网。

NPC 可以直接容器化运行，镜像入口为 `/npc`，命令行参数和物理机二进制一致。

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
