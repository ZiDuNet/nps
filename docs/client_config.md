# 客户端配置

推荐使用无配置文件模式，所有配置在服务端 Web 面板管理。

如果需要配置文件模式，以下为完整说明。

## 无配置文件模式

```bash
./npc -server=ip:8024 -vkey=密钥
```

参数：

| 参数 | 含义 |
|------|------|
| -server | 服务端地址:端口 |
| -vkey | 连接密钥 |
| -tls_enable | 启用 TLS（连接 8025 端口时） |
| -type | 连接类型（tcp/kcp），默认 tcp |
| -proxy | 通过代理连接（socks5/http） |
| -disconnect_timeout | 超时断开时间（单位 5s），默认 60 |
| -log_level | 日志级别（0-7） |

## 配置文件模式

```bash
./npc -config=npc.conf
```

配置文件示例：[npc.conf](https://github.com/ZiDuNet/nps/tree/master/conf/npc.conf)

### 全局配置

```ini
[common]
server_addr=1.1.1.1:8024
conn_type=tcp
vkey=your-key
auto_reconnection=true
```

| 项 | 含义 |
|------|------|
| server_addr | 服务端地址 |
| conn_type | tcp 或 kcp |
| vkey | 连接密钥 |
| username | SOCKS5/HTTP 认证用户名（可选） |
| password | SOCKS5/HTTP 认证密码（可选） |
| rate_limit | 速度限制 KB/S（可选） |
| flow_limit | 流量限制 MB（可选） |
| max_conn | 最大连接数（可选） |

### TCP 隧道

```ini
[tcp]
mode=tcp
target_addr=127.0.0.1:8080
server_port=9001
```

### UDP 隧道

```ini
[udp]
mode=udp
target_addr=127.0.0.1:8080
server_port=9002
```

### HTTP 代理

```ini
[http]
mode=httpProxy
server_port=9003
```

### SOCKS5 代理

```ini
[socks5]
mode=socks5
server_port=9004
```

### 私密代理

```ini
[secret_ssh]
mode=secret
password=密钥
target_addr=10.1.50.2:22
```

### P2P 代理

```ini
[p2p_ssh]
mode=p2p
password=密钥
target_addr=10.2.50.2:22
```

### 文件访问

```ini
[file]
mode=file
server_port=9100
local_path=/tmp/
strip_pre=/web/
```

### 健康检查

```ini
[health_check]
health_check_timeout=1
health_check_max_failed=3
health_check_interval=1
health_check_type=http
health_http_url=/
health_check_target=127.0.0.1:8080
```

### 端口范围映射

```ini
[tcp]
mode=tcp
server_port=9001-9009,10001
target_port=8001-8009,10002
target_ip=10.1.50.2   # 可选，默认 127.0.0.1
```

## 群晖支持

在 Releases 中下载 `.spk` 套件安装。
