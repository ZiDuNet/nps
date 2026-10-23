# NPS 1.1.29 本地升级与项目差异说明

本文面向从 `1.1.28` 升级到 `1.1.29` 的部署，也记录当前 ZiDuNet/nps 与本地 `nps2` 源码的核对结果。

## 1. 版本范围

- 当前项目：`ZiDuNet/nps`，版本 `1.1.29`，协议核心版本仍为 `0.26.0`。
- 升级基线：`1.1.28`。
- 对比项目：`/home/wushuo/桌面/nps2`，当前检出分支为 `0.34.0`，工作树包含 `v0.34.10` 之后的提交；因此它代表 nps2 的开发线，不等同于一个可直接替换的兼容补丁。

## 2. 1.1.29 的功能变化

### 2.1 资源级上下行限速

普通隧道和 Host 域名规则现在可以分别设置：

- `upload_limit`：访问者 → NPS → 内网服务，单位为 KB/s（代码按 1024 字节计算）。
- `download_limit`：内网服务 → NPS → 访问者，单位为 KB/s（代码按 1024 字节计算）。
- `0` 或留空表示不限速。
- 同一资源的所有连接共享该方向的额度；它会与客户端级、服务端全局限速叠加。
- 列表中的当前速率是近似的一秒采样值，用于观察趋势，不是精确计费值。
- P2P 成功直连时数据不经过 NPS 服务端，因此不受服务端资源级限速约束。

限速已覆盖 TCP、UDP、SOCKS5、HTTP 代理、HTTP/HTTPS 域名代理、WebSocket、Secret、文件访问和透明转发。

### 2.2 Web 配置的后端健康检查

隧道和 Host 规则可以在 Web 页面配置后端健康检查，服务端会在资源变化后刷新客户端配置，并向支持新控制消息的 NPC 下发：

- 检查类型：`http`（要求返回 HTTP 200）或 `tcp`（在超时内建立 TCP 连接）。
- 检查目标：多个 `host:port`，可使用逗号或换行分隔。
- 单次超时：默认 2 秒，最大 300 秒。
- 连续失败次数：默认 3，最大 100；达到次数后暂时摘除目标，恢复后重新加入。
- 检查间隔：默认 10 秒，最大 3600 秒。
- HTTP 检查路径：默认 `/`，例如 `/healthz`。

健康配置先经过原子校验和持久化；旧版 NPC 不识别该控制消息时不会收到新帧，继续使用旧协议和本地配置，不会因为服务端升级而断开。

### 2.3 API 和访问元数据

- TCP 隧道接口补充 `AccessHost`、`AccessAddr`、`AccessAddress`、`AccessURL`，不再只有监听端口。
- Host 接口补充健康检查字段。
- 隧道与 Host 列表、单条查询固定返回：`UploadLimit`、`DownloadLimit`、`UploadRate`、`DownloadRate`。

### 2.4 配置文件自动迁移

服务端启动时会把当前 `conf/nps.conf` 与内置模板对齐：

- 只追加缺失的有效字段，不覆盖已有值、注释、自定义字段、显式空值和文件权限。
- 同目录临时文件原子替换，重复启动幂等。
- 兼容上游字段：`bridge_tcp_port`、`bridge_kcp_port`、`bridge_tls_port` 会映射到当前项目的 `bridge_port`、`tls_bridge_port`；原字段会保留，便于回退或人工核对。
- 旧版 `tasks.json`、`hosts.json` 没有限速字段时按不限速读取，保存后才会写入新字段。

### 2.5 本地启动修复

修复 Web Cookie 会话的 AES 密钥长度错误。此前首次启动在 Beego 初始化 Cookie Provider 时可能出现 `crypto/aes: invalid key size 64` 并退出；1.1.29 使用有效的 AES-256 Cookie 密钥格式，首次启动和重启均可正常绑定 Web 监听端口。

## 3. 从 1.1.28 升级时的影响

### 3.1 对服务端

1. 建议先备份 `conf/`，至少包含 `nps.conf`、`clients.json`、`tasks.json`、`hosts.json`、`global.json`、`users.json` 和 `instance.key`（如果已经生成）。
2. 替换服务端程序后正常启动即可触发配置迁移，不需要手工重写 `nps.conf`。
3. 新字段默认关闭或为 `0`，不改变现有资源的转发速度和路由行为。
4. Web 面板的会话密钥存放在 `conf/instance.key`。升级时应保留此文件，否则现有浏览器会话会失效，需要重新登录；这不会影响客户端 vkey。
5. 如果使用旧版 `bridge_*` 配置，迁移后仍可保留；实际运行以当前项目的 `bridge_port` / `tls_bridge_port` 为准。
6. 升级后建议完整重启一次，而不是只刷新 Web 页面，以便重新载入所有资源、健康检查和限速桶。

### 3.2 对 NPC 客户端

- 旧 NPC 可以继续连接 1.1.29 服务端，原有 TCP/KCP/TLS Bridge 和隧道协议保持兼容。
- Web 页面下发的健康检查只有 `1.1.29` 及以上 NPC 会接收；旧 NPC 会保持原来的控制流和本地 `[health_*]` 配置。
- 资源级限速由 NPS 服务端转发链路执行，NPC 不需要升级才能继续使用旧隧道；升级 NPC 后才能使用新的健康配置同步。
- 远程升级链路仍遵循既有最低版本要求；升级前应确认客户端服务账户对安装目录和日志目录有写权限。

### 3.3 对数据和接口调用方

- 缺少 `UploadLimit` / `DownloadLimit` 的旧 JSON 按 `0` 处理，不需要批量迁移数据。
- 修改接口缺少新字段时保留旧值，旧调用方无需立刻改造。
- 新接口调用方应以 `status` 和返回 ID 为准，并在需要强一致时重新读取列表核对。
- 当前速率是运行时采样数据，服务重启后重新建立，不应作为计费依据。

## 4. Web 页面新增和变化的字段

### 4.1 隧道新增/编辑页

| 页面分组 | 字段 | 说明 |
| --- | --- | --- |
| 资源限速 | 上行限速（KB/s） | 访问者到内网服务；`0` 表示不限速 |
| 资源限速 | 下行限速（KB/s） | 内网服务到访问者；`0` 表示不限速 |
| 后端健康检查 | 检查类型 | TCP 连接或 HTTP 200 |
| 后端健康检查 | 检查目标 | 多个 `host:port`，逗号或换行分隔 |
| 后端健康检查 | 单次超时（秒） | 默认 2，最大 300 |
| 后端健康检查 | 连续失败次数 | 默认 3，最大 100 |
| 后端健康检查 | 检查间隔（秒） | 默认 10，最大 3600 |
| 后端健康检查 | HTTP 检查路径 | 例如 `/healthz`；仅 HTTP 检查使用 |

健康检查字段只在支持的 TCP/资源模式下显示；限速字段按资源类型显示，表单切换模式时不会把隐藏字段误提交。

### 4.2 域名解析（Host）新增/编辑页

- 上行限速（KB/s）和下行限速（KB/s）。
- 后端健康检查的全部字段：类型、目标、超时、连续失败次数、间隔、HTTP 路径。
- 目标为空表示关闭健康检查；目标池中的地址会按健康状态参与轮询。
- 列表和详情可以看到资源 ID、访问地址、协议/端口、健康配置和当前方向速率。
- 平台域名、自定义域名、CORS、兼容模式、Basic 认证等既有字段保持 1.1.28 行为。

### 4.3 列表与仪表盘

- 隧道和 Host 详情显示方向限速及当前速率。
- TCP 接口和列表显示可直接使用的访问地址，不再让前端自行猜测端口。
- 管理员和普通用户继续按既有权限范围查看资源；普通用户不会因为新增字段看到其他用户的资源。

## 5. 本地启动与健康冒烟检查

### 5.1 使用独立配置目录

不要直接拿生产 `conf/` 做启动验证。准备一个包含 `conf/nps.conf` 的临时目录，并将端口改为未占用端口，例如：

```ini
bridge_port=18024
tls_bridge_port=18025
http_proxy_port=18080
https_proxy_port=18443
web_ip=127.0.0.1
web_port=18081
```

### 5.2 编译并启动

```bash
go build -o /tmp/nps-local-1.1.29/nps ./cmd/nps
/tmp/nps-local-1.1.29/nps -version
/tmp/nps-local-1.1.29/nps -conf_path=/tmp/nps-local-1.1.29
```

`-conf_path` 指向运行目录，而不是直接指向 `nps.conf` 文件。服务端会在该目录下读取或生成 `conf/nps.conf`、JSON 数据和 Web 资源。

### 5.3 验证监听与 Web 面板

```bash
ss -ltn '( sport = :18024 or sport = :18025 or sport = :18080 or sport = :18443 or sport = :18081 )'
curl -i http://127.0.0.1:18081/
curl -i http://127.0.0.1:18081/login/index
```

预期结果：

- `18024`：Bridge TCP；
- `18025`：Bridge TLS；
- `18080` / `18443`：HTTP/HTTPS Host 入口；
- `18081`：Web 管理面板；
- `/` 返回登录跳转，`/login/index` 返回 `200` 登录页；
- 日志中不出现 `crypto/aes: invalid key size 64`、端口占用或配置迁移失败。

## 6. 当前项目与 nps2 的差异

### 6.1 总体定位

| 维度 | 当前 ZiDuNet/nps | `/home/wushuo/桌面/nps2` | 结论 |
| --- | --- | --- | --- |
| 版本线 | `1.1.29`，协议核心 `0.26.0` | `v0.34.10` 开发线，协议版本按索引协商 | 不是同一版本号体系，不能直接覆盖二进制 |
| Web 架构 | Beego 服务端模板 + ZUI 运维控制台 | Gin/Viper 服务层 + `web/api`、`web/service`、新 UI shell | nps2 的 Web 内核已重构，不能只复制页面 |
| Go 与依赖 | Go 1.24，保留 Beego 兼容层和旧版依赖 | Go 1.27，使用 Gin、Viper、zerolog、quic-go、netx、byterate 等 | nps2 的依赖升级跨度大，直接合并风险高 |
| 数据存储 | 兼容原版 JSON 文件，并增加用户、审计、平台域名等字段 | 仍有兼容层，但配置和服务模型更复杂 | 数据文件不能未经备份直接互换 |

### 6.2 当前项目已经具备、nps2 不是同一实现的能力

- 独立用户体系、用户级客户端/隧道配额、普通用户资源隔离。
- 平台泛域名池、平台域名/自定义域名校验、证书热更新。
- 资源拓扑大屏、审计日志、按用户 API Token、实时流量调试。
- NPC CLI/GUI/服务端触发的远程升级，以及面向本项目 Docker/Release 的版本流程。
- 兼容原版 Web 页面和旧 JSON 数据的渐进式迁移。
- 1.1.29 新增的 Web 健康检查同步、资源级上下行限速和 TCP/Host 访问元数据。

这些属于 ZiDuNet 产品层能力，不能因为 nps2 的包名不同就假定已经包含或可直接替换。

### 6.3 nps2 已有、当前项目尚未等价实现的能力

#### Bridge 和协议

- 独立的 TCP、KCP、TLS、QUIC、WS、WSS Bridge 端口与路径配置。
- `bridge_select_mode`、`bridge_host`、`bridge_path` 等多入口选择能力。
- `secure_mode`、`auth_time_window`、HMAC 时间窗口和防重放缓存。
- 更细的协议版本索引与能力门槛；旧客户端兼容由 `secure_mode=false` 明确控制。

当前项目仍使用原版核心 `0.26.0` 握手模型，配置中主要是 `bridge_type`、`bridge_port` 和独立 TLS Bridge 端口；不能把 nps2 的 QUIC/WS/WSS 参数直接写进当前 `nps.conf`。

#### HTTP/HTTPS 和域名代理

- HTTP/3 入口（`http3_proxy_port`）。
- TLS 直通和 TLS 终止的完整配置链路。
- ACME/CertMagic 自动证书、默认/多域名证书选择、证书缓存检查。
- `http_proxy_response_timeout`、`x_nps_http_only`、更细的反向代理协议控制。

当前项目已经支持 HTTP/HTTPS Host、WebSocket、CORS、兼容模式、路径/响应头处理和证书热更新，但没有 nps2 的 HTTP/3、ACME 自动申请和多传输 Bridge 体系。

#### P2P 与网络

- nps2 在 `0.34.8` 以后使用 holepunch 重写 P2P，支持 IPv4/IPv6、后台重试和失败时 Secret 中继回落。
- `p2p_ip` 可以是域名或多地址，并会综合客户端观测地址生成候选列表。
- nps2 还支持客户端 `local_ip`、`local_ip_forward` 和更细的出口地址控制。

当前项目保留原版 P2P 中继/打洞实现；与 nps2 新 P2P 协议不保证互通，不能只升级服务端一侧。

#### ACL、流量和运维

- nps2 的三层 IP 黑白名单支持优先级、CIDR、IPv4 通配、`geoip:`、`geosite:`、规则条数限制和 IP 登记页面。
- nps2 支持全局转发连接数限制、全局带宽限制、`flow_limit_type=actual`、Geo 数据下载和网页更新服务端。
- nps2 列表支持批量操作、自动刷新、客户端连接历史、延迟检测和更细的服务状态控制。

当前项目已有 IP 黑白名单、客户端/用户配额、仪表盘监控、审计和远程升级，但尚未等价提供 nps2 的 Geo ACL、HTTP/3、全局实际消耗计量、批量操作和延迟探测全套能力。

### 6.4 配置键差异的重点

以下 nps2 配置在当前项目中没有同名等价实现，不能直接照抄：

```text
secure_mode          auth_time_window       bridge_tcp_port
bridge_kcp_port      bridge_tls_port        bridge_quic_port
bridge_ws_port       bridge_wss_port        bridge_path
bridge_host           bridge_select_mode     http3_proxy_port
http_proxy_response_timeout                 force_auto_ssl
ssl_path              ssl_cache_reload       allow_user_local
allow_secret_link     allow_secret_local     flow_limit_type
geoip_path            geosite_path           acl_max_rules
trusted_proxy_ips     allow_x_real_ip
```

当前项目的下列配置是 ZiDuNet 侧的扩展或兼容字段：

```text
audit_log_path        instance_key_file      web_base_url
http_proxy_access_port                      https_proxy_access_port
show_http_proxy_port  log_rotate_daily       tls_enable
tls_bridge_port       bridge_type
```

### 6.5 是否建议直接合并 nps2

不建议把 nps2 当作一次普通的上游合并。原因是：

1. 协议握手、版本协商、P2P 和 Bridge 传输都发生了结构性变化。
2. Web 层从 Beego MVC 迁移到 Gin/API/service/UI shell，现有用户、权限和页面定制无法无冲突保留。
3. 配置键、默认值、依赖版本和数据模型均不完全一致。
4. nps2 的 P2P、secure mode 和 QUIC/WS/WSS 需要成套升级服务端与 NPC，不能只替换其中一个组件。

当前更稳妥的策略是：继续以 ZiDuNet/nps 为产品主线；从 nps2 选择性评估 Geo ACL、全局连接/带宽限制、批量操作、延迟检测和 HTTP/3 等独立功能，逐项做兼容设计和回归测试。

## 7. 回滚建议

如果升级后需要回滚：

1. 停止 1.1.29 服务端。
2. 恢复上一版本二进制和备份的 `conf/`。
3. 保留 `tasks.json` / `hosts.json` 中新增字段也可以，1.1.28 会忽略未知字段；如需完全还原，再恢复原始 JSON。
4. 恢复 `instance.key` 可以让旧浏览器会话继续工作；不恢复也只会要求重新登录。

回滚前不要把 nps2 的配置文件、Geo 数据或 P2P 状态目录直接混入当前项目运行目录。
