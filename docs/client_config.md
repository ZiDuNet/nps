# 客户端配置

客户端程序为 `npc`。推荐使用无配置文件模式：服务端 Web 面板管理客户端、隧道和域名规则，客户端只负责用 `VerifyKey` 连接服务端。

## 无配置文件模式

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS 模式：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

一个进程可连接多个客户端配置：

```bash
./npc -server=<服务器IP>:8024 -vkey=key1,key2,key3
```

## 环境变量

如果命令行未传 `-server` 或 `-vkey`，客户端会尝试读取：

| 环境变量 | 说明 |
|----------|------|
| `NPC_SERVER_ADDR` | 服务端地址 |
| `NPC_SERVER_VKEY` | 连接密钥 |

## 常用命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-server` | 服务端地址，格式 `ip:port` | 空 |
| `-vkey` | 客户端连接密钥 | 空 |
| `-config` | 配置文件路径 | 默认配置路径 |
| `-type` | 连接类型，`tcp` 或 `kcp` | `tcp` |
| `-tls_enable` | 启用 TLS | `false` |
| `-proxy` | 通过代理连接服务端 | 空 |
| `-disconnect_timeout` | 心跳超时倍数，单位为 5 秒 | `60` |
| `-log` | 日志输出方式，`stdout` 或 `file` | `stdout` |
| `-log_level` | 日志级别，`0` 到 `7` | `7` |
| `-log_path` | 日志文件路径 | 自动选择 |
| `-debug` | 是否输出到控制台 | `true` |
| `-pprof` | pprof 地址，格式 `ip:port` | 空 |
| `-version` | 输出版本号 | `false` |

代理示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -proxy=socks5://user:pass@127.0.0.1:9007
```

## 交互式模式

直接运行 `npc` 或双击 `npc.exe`，进入交互式菜单。可输入 Web 面板复制的快捷启动命令，也可注册/卸载/启动/停止系统服务。

快捷启动命令支持多个，用英文逗号分隔。

## P2P 和 Secret 参数

当传入 `-password` 时，客户端进入本地访问端模式，用于 P2P 或 Secret。

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-password` | P2P/Secret 密钥 | 空 |
| `-target` | P2P 目标地址 | 空 |
| `-local_type` | 本地模式，`p2p` 或 `secret` | `p2p` |
| `-local_port` | 本地监听端口 | `2000` |

Secret 示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -password=secret-key -local_type=secret
```

P2P 示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -password=p2p-key -target=10.0.0.2:22
```

NAT 类型检测：

```bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
```

## 配置文件模式

```bash
./npc -config=npc.conf
```

示例：

```ini
[common]
server_addr=1.1.1.1:8024
conn_type=tcp
vkey=your-key
auto_reconnection=true

[ssh]
mode=tcp
target_addr=127.0.0.1:22
server_port=9001
```

配置文件模式适合批量静态配置或不使用 Web 面板管理的场景。常规部署建议继续使用无配置文件模式。
