# 客户端配置

客户端程序为 `npc`。它运行在内网设备上，主动连接公网 NPS 服务端。推荐使用无配置文件模式：客户端、隧道和域名规则都在服务端 Web 面板管理，`npc` 只负责连接。

## 运行方式

| 方式 | 适合场景 | 特点 |
|------|----------|------|
| 无配置文件模式 | 常规部署、Web 面板集中管理 | 命令只需要 `-server` 和 `-vkey` |
| 配置文件模式 | 旧版配置、批量静态配置、不依赖 Web 面板 | 读取本地 `npc.conf` |
| 交互式菜单 | Windows 双击、手动注册服务 | 可粘贴快捷启动命令 |
| 系统服务 | 需要长期在线、开机自启 | 后台运行，日志写入文件 |
| Docker | 容器化客户端 | 注意容器网络和内网目标地址 |

## 无配置文件模式

普通 Bridge：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS Bridge：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

TLS 客户端默认校验证书。NPS 首次启动会在日志中打印服务端证书的 SHA-256 指纹；服务端使用自签名证书时，建议固定该指纹：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> \
  -tls_enable=true -tls_fingerprint=<SHA-256指纹>
```

也可以通过 `-tls_ca_file=<CA文件>` 使用 CA 链校验，并按需设置 `-tls_server_name=<证书名称>`。`-tls_insecure_skip_verify=true` 仅用于兼容旧部署，生产环境不要启用。

一个进程可连接多个客户端配置：

```bash
./npc -server=<服务器IP>:8024 -vkey=key1,key2,key3
```

Windows：

```cmd
npc.exe -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

`VerifyKey` 在 NPS Web 面板的「客户端」页面创建，必须和对应客户端匹配。

## 环境变量

如果命令行未传 `-server` 或 `-vkey`，客户端会尝试读取：

| 环境变量 | 说明 |
|----------|------|
| `NPC_SERVER_ADDR` | 服务端地址，例如 `1.1.1.1:8024` |
| `NPC_SERVER_VKEY` | 客户端连接密钥 |

## 常用命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-server` | 服务端地址，格式 `ip:port` | 空 |
| `-vkey` | 客户端连接密钥，多个用英文逗号分隔 | 空 |
| `-config` | 配置文件路径 | 默认配置路径 |
| `-type` | 与服务端连接类型，`tcp` 或 `kcp` | `tcp` |
| `-tls_enable` | 启用 TLS Bridge | `false` |
| `-tls_ca_file` | TLS CA 证书文件 | 空 |
| `-tls_server_name` | TLS SNI/证书名称 | 服务端地址 |
| `-tls_fingerprint` | 服务端证书 SHA-256 指纹 | 空 |
| `-tls_insecure_skip_verify` | 显式关闭 TLS 证书校验（不推荐） | `false` |
| `-proxy` | 通过 SOCKS5 代理连接服务端 | 空 |
| `-disconnect_timeout` | 心跳超时倍数，单位为 5 秒 | `60` |
| `-log` | 日志输出方式，`stdout` 或 `file` | `stdout` |
| `-log_level` | 日志级别，`0` 到 `7` | `7` |
| `-log_path` | 日志文件路径 | 自动选择 |
| `-debug` | 是否输出到控制台；作为系统服务时会自动追加 `-debug=false` | `true` |
| `-pprof` | pprof 地址，格式 `ip:port` | 空 |
| `-version` | 输出版本号 | `false` |

代理示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -proxy=socks5://user:pass@127.0.0.1:9007
```

## 配置文件模式

```bash
./npc -config=npc.conf
```

未指定 `-config` 时，源码默认查找：

- Windows：程序目录下的 `conf/npc.conf`
- Linux/macOS：当前工作目录下的 `conf/npc.conf`

示例：

```ini
[common]
server_addr=1.1.1.1:8024
conn_type=tcp
vkey=your-key
auto_reconnection=true
compress=true
crypt=true

[ssh]
mode=tcp
target_addr=127.0.0.1:22
server_port=9001
```

配置文件模式适合旧版用法或不使用 Web 面板管理的场景。常规部署建议继续使用无配置文件模式。

完整的所有模式、配置项、健康检查、端口范围和环境变量模板请查阅 [NPC 配置文件参考](client/config-file.md)。

### common 传输选项

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `compress` | 使用 Snappy 压缩 Bridge 数据，可降低带宽占用；CPU 较弱时可关闭 | `false` |
| `crypt` | 加密 Bridge 数据，需与服务端/客户端配置保持一致 | `false` |
| `server_addr` | 服务端地址；兼容上游别名 `server` | 空 |
| `conn_type` | 连接类型；兼容上游别名 `tp` | `tcp` |
| `tls_ca_file` | 服务端 CA 证书文件 | 空 |
| `tls_server_name` | TLS SNI/证书名称 | 服务端地址 |
| `tls_fingerprint` | 服务端证书 SHA-256 指纹 | 空 |
| `tls_insecure_skip_verify` | 显式关闭证书校验，仅兼容旧部署 | `false` |

## 交互式菜单

直接运行 `npc` 或双击 `npc.exe`，如果没有 `-server`、`-vkey`，也没有找到配置文件，会进入交互式菜单。

菜单项：

| 输入 | 操作 |
|------|------|
| `1` | 注册系统服务，需要输入快捷启动命令；可一次输入多个，用英文逗号分隔 |
| `2` | 删除/卸载系统服务，需要输入 `vkey`；会先停止服务再卸载，可一次输入多个，用英文逗号分隔 |
| `3` | 启动系统服务，需要输入 `vkey` |
| `4` | 停止系统服务，需要输入 `vkey` |
| `5` | 更新客户端 |
| `6` | 查看已注册的 `nps-client-*` 服务列表，并显示每个服务状态 |
| `7` | 查看指定 `vkey` 对应服务的状态 |
| `0` | 退出 |
| 快捷启动命令 | 不注册服务，直接启动连接；可一次输入多个，用英文逗号分隔 |

也可以直接粘贴 Web 面板生成的「快捷启动命令」。多个快捷启动命令用英文逗号拼接。

快捷启动命令由 Web 面板生成，内容是 Base64。当前 GUI 使用的格式为：

```text
nps:<名称>|<服务端地址:端口>|<VerifyKey>|<是否TLS>[|<TLS证书指纹>]
```

命令行 `npc` 同时兼容旧格式，解析后会转成：

```text
<服务端地址:端口> <VerifyKey> <是否TLS>
```

## 系统服务

命令行安装仍然可用：

```bash
sudo ./npc install -server=<服务器IP>:8024 -vkey=<VerifyKey>
sudo npc start
```

Windows 需要管理员 CMD 或 PowerShell：

```cmd
npc.exe install -server=<服务器IP>:8024 -vkey=<VerifyKey>
npc.exe start
```

命令行安装会注册通用 `Npc` 服务，安装时带上的 `-server`、`-vkey`、`-tls_enable` 等参数会作为服务启动参数。

交互式菜单注册服务时，会按 `vkey` 创建独立服务，服务名格式为：

```text
nps-client-<vkey>
```

交互式菜单里的「删除/卸载系统服务」就是删除这个独立服务。菜单里的「查看已注册服务列表」会扫描当前系统中已经注册的 `nps-client-*` 服务，适合一台机器绑定多个客户端时排查。

日志默认位置：

| 系统 | 日志路径 |
|------|----------|
| Linux/macOS | `/var/log/npc.log`，独立服务会写成 `/var/log/npc-<vkey>.log` |
| Windows | `npc.exe` 所在目录下的 `npc.log` 或 `npc-<vkey>.log` |

## HTTP/SOCKS5 代理认证

HTTP 正向代理和 SOCKS5 代理的账号密码不在「新增隧道」页面设置，而是在「客户端」的认证配置里设置：

1. 进入「客户端」。
2. 新增或编辑客户端。
3. 在「认证配置」里填写「Basic 认证用户名」和「Basic 认证密码」。
4. 保存后，该客户端下的 HTTP 正向代理、SOCKS5 代理以及域名代理会使用这组认证信息。

如果这两个字段都为空，SOCKS5 会允许无认证连接，知道服务端 IP 和端口的人都可以尝试连接。公网开放 SOCKS5 时，建议务必设置账号密码，并配合客户端黑名单、全局黑名单或防火墙限制来源 IP。

## Docker 客户端

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS：

```bash
docker run -d --name npc wushuo98/npc \
  -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

容器里的 `127.0.0.1` 是容器自身，不是宿主机。如果隧道目标在宿主机上，Linux 可按需使用 `--network host`；Windows/macOS Docker Desktop 需要使用 Docker 提供的宿主机访问地址。

## P2P 和 Secret 本地访问端

当传入 `-password` 时，客户端进入本地访问端模式，用于访问 P2P 或 Secret 隧道。

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-password` | P2P/Secret 密钥 | 空 |
| `-target` | P2P 目标地址 | 空 |
| `-local_type` | 本地模式，`p2p` 或 `secret` | `p2p` |
| `-local_port` | 本地监听端口 | `2000` |

Secret 示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -password=secret-key -local_type=secret -local_port=2000
```

P2P 示例：

```bash
./npc -server=1.1.1.1:8024 -vkey=key \
  -password=p2p-key -target=10.0.0.2:22 -local_type=p2p -local_port=2000
```

NAT 类型检测：

```bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
```

P2P 依赖双方 NAT 类型，不保证所有网络都能直连。失败时会回落或需要改用普通 TCP/Secret 方式。
