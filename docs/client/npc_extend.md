# 客户端增强功能

本页收录 NPC 运行、诊断和系统服务相关功能。完整的代理配置示例见[NPC 配置文件参考](config-file.md)。

## NAT 类型检测

~~~bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
~~~

该命令输出 NAT 类型和当前公网映射地址，可用于评估 P2P 成功概率。双方均为 Symmetric NAT 时通常无法建立 P2P；其他组合也会受运营商、防火墙和 UDP 放行情况影响，因此不能保证直连成功。

## 配置文件状态与重启

~~~bash
./npc status -config=conf/npc.conf
sudo ./npc restart -config=conf/npc.conf
~~~

`status` 用于读取配置文件中的任务状态。`restart` 操作已注册的系统服务；服务安装时应带上相同的 `-config`、`-server`、`-vkey` 和 TLS 参数。未安装为系统服务的 NPC 进程应由进程管理器、Docker 或手工停止后重新启动。

## 通过代理连接 NPS

内网机器不能直接访问 NPS 时，可让 NPC 通过 HTTP 或 SOCKS5 出站代理建立 Bridge 连接：

~~~bash
./npc -server=nps.example.com:8024 -vkey=replace-with-verify-key \
  -proxy=socks5://user:pass@127.0.0.1:1080
~~~

也可在 `[common]` 中使用：

~~~ini
proxy_url=http://user:pass@127.0.0.1:8080
~~~

代理地址必须是 NPC 所在机器可达的地址。请确认上游代理允许连接 NPS 的 Bridge 端口，并保留 TLS 校验；不要为绕过证书问题而设置 `tls_insecure_skip_verify=true`。

## 日志与诊断

| 参数 | 作用 |
| --- | --- |
| `-log=stdout` | 输出到终端，适合前台排障。 |
| `-log=file` | 输出到日志文件，适合系统服务。 |
| `-log_level=0..7` | 日志等级，数值越高输出越详细。 |
| `-log_path=/path/npc.log` | 指定日志文件路径。 |
| `-pprof=127.0.0.1:9999` | 临时开启 Go pprof；只可绑定回环或管理网段。 |
| `-disconnect_timeout=60` | 设置断线检测超时秒数；网络不稳定时需结合服务端设置一起评估。 |

系统服务日志默认位于 Linux/macOS 的 `/var/log/` 或 Windows 的 NPC 程序目录。按 vkey 创建的独立服务通常使用 `npc-<vkey>.log`。

## 群晖与容器

群晖套件或容器部署的核心原则与普通 NPC 相同：NPS Bridge 地址必须从容器/套件网络可达，目标地址必须从 NPC 所在网络命名空间可达。

- 目标服务在 Docker 宿主机时，Linux 可按需使用 `--network host`。
- 容器内的 `127.0.0.1` 默认是容器自身，不是宿主机。
- 不要把验证密钥直接写进镜像；使用受保护的环境变量、挂载配置文件或密钥管理方式。

更多容器示例见[Docker 部署](../docker.md)。
