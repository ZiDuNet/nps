# 对外 API v1 与历史 Web API

新集成只使用 `/api/v1`。本页以 `/api/v1` 为权威接口说明；旧
`/auth/*`、`/client/*`、`/index/*`、`/user/*`、`/global/*` 继续为现有
页面、NPC 和旧调用方保持兼容，清单见[历史 Web API](extend/webapi.md)。

API 文档固定发布在：
[https://zidunet.github.io/nps/api.html](https://zidunet.github.io/nps/api.html)。

## `/api/v1` 约定

`/api/v1` 有四条固定约定：

1. 只接受 API 凭据，不使用浏览器登录 Session。管理员使用
   `auth_key + timestamp` 签名，普通用户使用
   `Authorization: Bearer npsu_...`；
2. 管理员可使用 `nps.conf` 的 `auth_key`，也可使用「系统 → API 接入」
   生成的等价网页管理密钥 `npsa_...`；签名都按
   `md5(密钥原文 + timestamp)` 计算；
3. 成功与失败统一返回 `status`、`code`、`msg`，失败额外返回
   `error_code`，每个响应都带 `request_id`；
4. 请求体支持 `application/json`，也兼容表单与查询参数。

管理员签名时间戳为 Unix 秒，服务端允许约 20 秒误差。普通用户 Token 不携带
时间戳。Token 可在 API 接入页再次查看；查询参数传递 Token 仅用于无法设置
请求头的兼容客户端，并应只走 HTTPS。

详细鉴权、Token 生命周期和历史接口迁移见[API 鉴权](extend/api.md)。

## 公开接口

公开接口不需要凭据：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1` | 版本、能力与资源发现 |
| `GET` | `/api/v1/time` | 服务端 Unix 时间戳，用于签名对时 |
| `GET` | `/api/v1/authkey` | AES-CBC 加密后的 `auth_key` |
| `POST` | `/api/v1/ip-register` | 使用新版登记密钥绑定当前访问来源 IP，有效期内供“仅允许已登记 IP”资源校验 |

其余接口都需要凭据：

| 资源 | 路径 | 方法 |
| --- | --- | --- |
| 概览 | `/api/v1/status`、`/api/v1/status/live`、`/api/v1/overview` | `GET` |
| 客户端 | `/api/v1/clients` | `GET`、`POST` |
| 客户端 | `/api/v1/clients/{id}` | `GET`、`PATCH`、`PUT`、`DELETE` |
| 客户端 | `/api/v1/clients/{id}/status`、`/upgrade` | `POST` |
| 客户端 | `/api/v1/clients/{id}/update-check` | `GET` |
| 隧道 | `/api/v1/tunnels` | `GET`、`POST` |
| 隧道 | `/api/v1/tunnels/{id}` | `GET`、`PATCH`、`PUT`、`DELETE` |
| 隧道 | `/api/v1/tunnels/{id}/start`、`/stop`、`/copy` | `POST` |
| 域名反代 | `/api/v1/hosts` | `GET`、`POST` |
| 域名反代 | `/api/v1/hosts/{id}` | `GET`、`PATCH`、`PUT`、`DELETE` |
| 域名反代 | `/api/v1/hosts/{id}/start`、`/stop` | `POST` |
| 域名反代 | `/api/v1/hosts/check`、`/diagnose` | `POST` |
| 用户注册 | `/api/v1/users/register` | `POST`（管理员） |
| 用户 | `/api/v1/users` | `GET`、`POST`（管理员） |
| 用户 | `/api/v1/users/{id}` | `GET`、`PATCH`、`PUT`、`DELETE`（管理员） |
| 用户 | `/api/v1/users/{id}/status`、`/password` | `POST` |
| 用户 Token | `/api/v1/users/{id}/token`、`/token/revoke` | `POST` |
| 用户 Token | `/api/v1/users/{id}/token/status` | `GET` |
| 全局参数 | `/api/v1/global/config` | `GET`、`POST`（管理员） |
| 全局参数 | `/api/v1/global/config/restart` | `POST`（管理员） |
| Bridge 证书 | `/api/v1/global/bridge-certificate` | `GET`、`POST`（管理员） |
| Bridge 证书 | `/api/v1/global/bridge-certificate/check` | `POST`（管理员） |
| Geo 数据 | `/api/v1/global/geo`、`/geo/update` | `GET`、`POST`（管理员） |
| 登录保护 | `/api/v1/global/banlist`、`/unban`、`/unbanall`、`/banclean` | `GET`、`POST`（管理员） |
| IP 登记 | `/api/v1/global/ip-register`、`/revoke`、`/clean`、`/clear` | `GET`、`POST`（管理员或普通用户 Token） |
| 审计 | `/api/v1/audit` | `GET`、`POST`（管理员查看全部；普通用户仅查看所属资源） |
| 连接日志 | `/api/v1/audit/connections` | `GET`、`POST`（管理员查看全部；普通用户仅查看所属客户端） |

审计和连接日志使用相同的分页参数。连接日志支持
`kind=bridge|tunnel|host`、`result=success|failure`、
`bridge_transport=tcp|tls|kcp|ws|wss|quic|http3` 和 `search`。
普通用户凭据不能通过筛选参数扩大资源范围。连接日志记录 Bridge 上下线、
认证失败，以及隧道和域名反代的来源、目标、耗时与双向字节数；不会保存
Basic 密码、API Token、验证密钥、请求头或代理正文。

### Bridge TLS 证书接口

这三个接口只允许管理员 API 凭据调用，用于维护 `nps.conf` 中现有的一组
`bridge_cert_file`、`bridge_key_file`。当前 Bridge 的 TCP TLS、WSS、
QUIC/HTTP/3 与加密内层链路共用这套证书，不支持多证书列表。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/global/bridge-certificate` | 返回运行证书和配置证书的状态、到期时间、SAN、指纹及是否需要重启；不返回私钥内容 |
| `POST` | `/api/v1/global/bridge-certificate/check` | 只校验提交的证书与私钥，不写入配置或文件 |
| `POST` | `/api/v1/global/bridge-certificate` | 校验并保存 `nps.conf` 中的证书、私钥路径 |

校验请求处理成功时顶层 `status` 仍为 `1`，证书是否有效以
`data.valid` 和 `data.message` 为准；保存接口遇到无效证书时返回
`status=0`，且不会修改 `nps.conf`。

保存和校验接口支持表单或 JSON 参数：

| 参数 | 说明 |
| --- | --- |
| `mode` | `path`、`upload`、`paste` 或 `clear`；保存接口默认 `path` |
| `cert` | `path` 模式为证书链路径；`upload`/`paste` 模式为 PEM 文本或 Base64 编码的证书链 |
| `key` | `path` 模式为私钥路径；`upload`/`paste` 模式为 PEM 文本或 Base64 编码的私钥 |
| `hostname` | 可选。填写域名或 IP 时额外校验证书 SAN；留空只校验证书对、有效期和格式 |

`upload` 与 `paste` 的提交内容会安全写入
`conf/bridgessl/bridge_server.pem`、`conf/bridgessl/bridge_server.key`，接口响应与
`nps.conf` 只使用相对路径。`mode=clear` 会删除两个配置键，重启后回到临时
自签名证书。保存成功后 `restart_required=true`，可再调用
`POST /api/v1/global/config/restart`，或手动重启 NPS；不会热加载证书。

示例：

```bash
ts=$(date +%s)
sign=$(printf '%s' "your_auth_key${ts}" | md5sum | awk '{print $1}')
curl -s "http://127.0.0.1:8081/api/v1/global/bridge-certificate?auth_key=${sign}&timestamp=${ts}"

curl -s -X POST "http://127.0.0.1:8081/api/v1/global/bridge-certificate?auth_key=${sign}&timestamp=${ts}" \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "path",
    "cert": "conf/bridgessl/bridge_server.pem",
    "key": "conf/bridgessl/bridge_server.key",
    "hostname": "bridge.example.com"
  }'
```

统一成功信封：

```json
{
  "status": 1,
  "code": "ok",
  "msg": "ok",
  "data": {},
  "request_id": "..."
}
```

统一失败信封：

```json
{
  "status": 0,
  "code": "error",
  "error_code": "operation_failed",
  "msg": "错误说明",
  "request_id": "..."
}
```

列表接口的 `rows`、`total`、`meta` 位于顶层，`meta` 与旧接口一致，包含
`offset`、`limit`、`page`、`page_size`、`has_more`。未传分页参数时默认
`limit=20`，最大 `500`；也可以使用 `page` 与 `page_size`。

管理员签名示例：

```bash
ts=$(date +%s)
sign=$(printf '%s' "your_auth_key${ts}" | md5sum | awk '{print $1}')
curl -s "http://127.0.0.1:8081/api/v1/clients?auth_key=${sign}&timestamp=${ts}&limit=20"
```

普通用户 Token 示例：

```bash
curl -s "http://127.0.0.1:8081/api/v1/clients?limit=20" \
  -H "Authorization: Bearer npsu_REPLACE_WITH_TOKEN"
```

JSON 请求示例：

```bash
curl -s -X POST "http://127.0.0.1:8081/api/v1/clients?auth_key=${sign}&timestamp=${ts}" \
  -H "Content-Type: application/json" \
  -d '{"remark":"office","flow_limit":1024,"compress":true}'
```

### 实时带宽与连接数

```http
GET /api/v1/status/live
```

该接口用于高频轮询，只读取内存计数器，不采集 CPU、内存和网卡信息。管理员
得到全部用户和客户端，普通用户 Token 只会得到自己名下的资源。`uploadRate`、
`downloadRate` 单位为字节/秒；`clients[].connections` 是该客户端当前连接数，
`users[].connections` 是该用户全部客户端共享的连接计数。用户共享带宽由该用户
所有客户端共同占用，只有一个客户端时它可以跑满用户的配置总额度；P2P 直连
不计入该额度。速率空闲超过两个采样窗口后返回 `0`。

普通用户调用 `/api/v1` 时，客户端、隧道、Host、统计、审计与客户端连接
日志仍由服务端按 `UserId` 授权范围过滤；用户、全局参数等管理资源只允许
管理员凭据访问。API 密钥与登录 Session 相互独立，浏览器 Cookie 不能替代
本接口要求的凭据。

### 管理员创建注册用户

```http
POST /api/v1/users/register
```

该接口仅允许管理员凭据调用，不受公开注册开关 `allow_user_register` 影响。
请求体可以使用 JSON，也可以使用表单或查询参数。

| 参数 | 说明 |
| --- | --- |
| `username` | 必填，最多 64 个字符，不能与现有用户或配置文件管理员用户名重复 |
| `password` | 必填，至少 6 个字符；API 调用方应通过 HTTPS 传输 |
| `email` | 可选；填写后必须格式有效，且不区分大小写全局唯一 |
| `remark` | 可选备注 |
| `max_client` / `max_client_num` | 客户端配额；省略时使用 `register_max_clients`，`0` 表示不限 |
| `max_tunnel` / `max_tunnel_num` | 隧道总配额；省略时使用 `register_max_tunnels`，`0` 表示不限 |
| `max_conn` / `max_connection` | 该用户全部客户端共享的最大并发连接数；省略时使用 `register_max_conn`，`0` 表示不限 |
| `upload_limit` / `upload_rate` | 该用户全部客户端共享的上行带宽，单位 KB/s；省略时使用 `register_upload_limit`，`0` 表示不限 |
| `download_limit` / `download_rate` | 该用户全部客户端共享的下行带宽，单位 KB/s；省略时使用 `register_download_limit`，`0` 表示不限 |
| `expire_days` | 从请求成功时起有效天数；`0` 表示永久 |
| `expire_time` | 指定绝对到期时间，格式同用户编辑页；填写后优先于 `expire_days` |

有效期取值优先级为 `expire_time`、`expire_days`、`register_expire_days`。
两者都省略时使用服务端默认值，当前模板默认 `0`（永久有效）。

```bash
ts=$(date +%s)
sign=$(printf '%s' "your_auth_key${ts}" | md5sum | awk '{print $1}')
curl -s -X POST \
  "http://127.0.0.1:8081/api/v1/users/register?auth_key=${sign}&timestamp=${ts}" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "alice",
    "password": "replace-with-a-strong-password",
    "email": "alice@example.com",
    "remark": "API registered",
    "max_client": 1,
    "max_tunnel": 2,
    "max_conn": 50,
    "upload_limit": 1024,
    "download_limit": 2048,
    "expire_days": 7
  }'
```

成功响应的 `data` 为用户列表行，包含用户 ID、配额、并发连接数、共享上下行带宽、到期时间和资源计数。

### IP 登记接口的权限范围

IP 登记接口可以由管理员签名或普通用户 Bearer Token 调用，但两种凭据看到的
数据范围不同：

- 管理员可以查看、撤销和清理所有客户端的登记，也可以管理 `AnyClient`
  公共登记桶；
- 普通用户 Token 只能查看、撤销、清理和清空 Token 所属用户名下客户端的
  登记，不能查看或操作其他用户及公共登记桶；
- 服务端会按数据库中的客户端归属实时过滤，不依赖请求参数中的
  `client_id`。即使手工构造其他客户端的 `client_id`，撤销请求也会返回
  `403`；
- 该权限与 Web 面板中的「系统 → IP 登记」一致，隐藏按钮不是权限边界，
  后端仍会再次校验归属。

## 历史 Web API（兼容）

以下内容描述旧版 `/client/*`、`/user/*`、`/index/*` 和 `/global/*` 管理
接口。它们继续为现有 Web 页面、NPC 和旧调用方工作，但新集成不应再使用。
完整兼容清单与历史响应格式见[历史 Web API 清单](extend/webapi.md)。

### 历史通用返回

普通操作返回：

```json
{
  "status": 1,
  "msg": "save success"
}
```

表格接口返回：

```json
{
  "rows": [],
  "total": 0
}
```

## 历史客户端接口

### 获取客户端列表

```http
POST /client/list
```

参数：

| 参数 | 说明 |
|------|------|
| `search` | 搜索关键字 |
| `sort` | 排序字段 |
| `order` | `asc` 或 `desc` |
| `offset` | 起始偏移 |
| `limit` | 数量 |

响应在保留历史客户端字段的同时增加以下连接信息：

| 字段 | 说明 |
|------|------|
| `ConnectionProtocol` | 当前承载新转发连接的协议标签，例如 `TCP`、`TCP + TLS`、`WS`、`QUIC`、`HTTP/3` |
| `ConnectionProtocols` | 同一 VKey 当前在线且完整建立的全部协议，按运行时发现顺序返回 |
| `ConnectionCommands` | 为当前客户端生成的全部可复制命令；只包含已启用、正在监听且允许展示的入口 |
| `bridgeEndpoints` | 当前实际监听的全部 Bridge 入口元数据，含 `key`、`protocol`、`address`、`path`、`alpn`、`main`、`usesUdp`、`viaHttp3` |

`ConnectionCommands` 每项包含 `Key`、`Label`、`Protocol`、`Transport`、`Address`、`Path`、`ALPN`、`TLS`、`Main`、`RequiresUDP`、`ViaHTTP3`、`NormalCommand`、`QuickCommand` 和 `QuickFormat`。`QuickFormat=v1` 表示历史格式，`v2` 表示 JSON 格式；v2 新增 Bridge 入口、Path、ALPN、TLS Server Name 和证书指纹字段。HTTP/3 只有在 HTTPS 入口与 UDP 监听实际启动后才返回。

### 获取单个客户端

```http
POST /client/getclient
```

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

单条查询返回与列表相同的协议状态和 `ConnectionCommands` 字段。

### 新增客户端

```http
POST /client/add
```

| 参数 | 说明 |
|------|------|
| `remark` | 备注 |
| `vkey` | 连接密钥，留空自动生成 |
| `user_id` | 所属用户 ID |
| `u` | 代理认证用户名 |
| `p` | 代理认证密码 |
| `compress` | 是否压缩 |
| `crypt` | 是否加密 |
| `config_conn_allow` | 是否允许配置文件模式连接 |
| `rate_limit` | 带宽限制，单位 KB/s；`0` 或留空表示不限速 |
| `flow_limit` | 流量限制，MB |
| `max_conn` | 最大连接数 |
| `max_tunnel` | 客户端最大隧道数 |
| `expire_time` | 到期时间 |
| `blackiplist` | 黑名单列表 |
| `whiteiplist` | 白名单列表 |
| `iplistpriority` | 黑白名单冲突策略 |
| `registered_only` | 是否仅允许已登记 IP |
| `register_key` | 客户端 IP 登记密钥，留空禁用该客户端的独立登记密钥 |
| `register_max_hours` | 客户端允许登记的最长有效小时数，`0` 使用服务端默认值 |

普通用户调用此接口时，客户端会自动归属当前登录用户，`vkey`、`user_id` 和客户端级配额等管理字段不会从请求体采纳。普通用户可设置备注、Basic 认证（`u`/`p`）、压缩、加密、黑白名单和新版 IP 登记参数；创建前会原子校验用户的 `MaxClientNum`。管理员可通过 `user_id` 指定归属用户。

### 修改客户端

```http
POST /client/edit
```

包含新增客户端参数，并额外需要：

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

编辑时客户端 ID 只用于定位记录，不能修改。普通用户只能编辑自己客户端的备注、Basic 认证、压缩、加密、访问控制和新版 IP 登记参数；密码留空表示保持原值。管理员可修改归属、配额、验证密钥、旧版 Web 登录和独立到期时间，但客户端到期时间不能晚于所属用户到期时间。

### 公开 IP 登记

```http
POST /api/v1/ip-register
Content-Type: application/json

{"key":"客户端登记密钥","hours":2}
```

也接受 `application/x-www-form-urlencoded` 请求体。登记密钥只从请求体读取，不接受查询参数；登记地址始终由服务端按 TCP 对端地址或受信任反代配置识别，客户端不能提交任意 IP。`hours` 超过服务端或客户端允许上限时按上限处理。登记仅保存在内存中，NPS 重启后失效。

### 删除客户端

```http
POST /client/del
```

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

## 历史用户接口

### 获取用户列表

```http
POST /user/list
```

### 新增用户

```http
POST /user/add
```

| 参数 | 说明 |
|------|------|
| `username` | 用户名 |
| `password` | 密码 |
| `email` | 可选邮箱；不区分大小写且全局唯一 |
| `remark` | 备注 |
| `max_tunnel` | 用户最大隧道数 |
| `max_client` | 用户最大客户端数，`0` 为不限制。管理员分配和用户自建客户端共用此配额。 |
| `expire_time` | 到期时间 |

### 修改用户

```http
POST /user/edit
```

| 参数 | 说明 |
|------|------|
| `id` | 用户 ID |
| `username` | 用户名 |
| `password` | 密码 |
| `email` | 可选邮箱；不区分大小写且全局唯一 |
| `remark` | 备注 |
| `max_tunnel` | 用户最大隧道数 |
| `max_client` | 用户最大客户端数，`0` 为不限制；不能调低到当前已分配数量以下。 |
| `expire_time` | 到期时间 |

### 修改登录密码

```http
POST /user/changepassword
```

管理员提交 `id`、`new_password` 和 `confirm_password` 可重置指定普通用户密码。普通用户不需要提交 `id`（也可提交自己的 ID），必须同时提交 `current_password`、`new_password` 和 `confirm_password`，且不能修改其他用户；密码至少 6 个字符。该接口不会返回或回显旧密码。

### 删除用户

```http
POST /user/del
```

删除用户不会删除客户端，但会停用并解绑其名下客户端（`UserId` 置空）；管理员重新分配后才能恢复使用。

## 历史隧道接口

### 获取隧道列表

```http
POST /index/gettunnel
```

| 参数 | 说明 |
|------|------|
| `client_id` | 客户端 ID |
| `type` | `tcp`、`udp`、`httpProxy`、`socks5`、`secret`、`p2p`、`file` |
| `search` | 搜索关键字 |
| `offset` | 起始偏移 |
| `limit` | 数量 |

### 新增隧道

```http
POST /index/add
```

| 参数 | 说明 |
|------|------|
| `type` | 隧道类型 |
| `remark` | 备注 |
| `port` | 服务端端口，留空自动生成 |
| `server_ip` | 服务端监听 IP |
| `target` | 内网目标 |
| `client_id` | 客户端 ID |
| `password` | Secret/P2P 密钥 |
| `proxy_auth` | 混合代理独立 Basic 认证，每行 `用户名:密码`；配置后覆盖所属客户端的 Basic 认证 |
| `local_path` | 文件访问本地路径 |
| `strip_pre` | 文件访问路径前缀 |
| `local_proxy` | 是否代理到服务端本地 |
| `proto_version` | 协议版本 |
| `upload_limit` | 资源上行限速，单位 KB/s；`0` 或留空表示不限速 |
| `download_limit` | 资源下行限速，单位 KB/s；`0` 或留空表示不限速 |
| `health_check_type` | 健康检查类型：`http` 或 `tcp`；留空目标时关闭 |
| `health_check_target` | 健康检查目标，多个 `host:port` 可用逗号或换行分隔 |
| `health_check_timeout` | 单次检查超时秒数，默认 2，最大 300 |
| `health_max_failed` | 连续失败次数，默认 3，最大 100 |
| `health_check_interval` | 检查间隔秒数，默认 10，最大 3600 |
| `health_http_url` | HTTP 检查路径，默认 `/` |

### 修改、删除、启动、停止

```http
POST /index/edit
POST /index/del
POST /index/start
POST /index/stop
```

这些接口均使用 `id` 指定隧道。

修改隧道时可继续提交新增接口中的 `upload_limit`、`download_limit` 和健康检查字段；缺少这些字段时保留原值，兼容旧调用方。

隧道列表和单条查询在保留历史 `Tunnel` 字段的同时，增加 `AccessHost`、`AccessAddr`、`AccessAddress` 和 `AccessURL`。例如服务地址为 `https://nps.example.com:8081/panel`、隧道端口为 `10022` 时，返回 `AccessHost=nps.example.com`、`AccessAddr=nps.example.com:10022`、`AccessURL=tcp://nps.example.com:10022`。这些字段只用于展示访问入口，不会改变隧道监听端口。

列表和单条查询还会返回 `UploadLimit`、`DownloadLimit`、`UploadRate`、`DownloadRate`。前两个是配置值，单位 KB/s，`0` 表示不限速；后两个是最近一秒的近似字节速率，空闲时为 `0`。

混合代理的独立认证只返回布尔字段 `ProxyAuthConfigured`，不会返回
`proxy_auth` 明文。修改时省略或留空 `proxy_auth` 会保留原值；提交新的账号
集合会整体替换；提交 `clear_proxy_auth=true` 会清除独立认证并回退到客户端
Basic。

## 历史 Host 域名接口

### 获取 Host 列表

```http
POST /index/hostlist
```

### 新增 Host

```http
POST /index/addhost
```

| 参数 | 说明 |
|------|------|
| `remark` | 备注 |
| `host` | 域名 |
| `scheme` | `all`、`http`、`https` |
| `location` | URL 路由，空值默认为 `/` |
| `client_id` | 客户端 ID |
| `target` | 内网目标 |
| `proxy_protocol` | Host 后端 PROXY Protocol：`0` 关闭、`1` v1、`2` v2。只有后端明确支持时才应开启 |
| `header` | 请求 Header 修改 |
| `hostchange` | 请求 Host 修改 |
| `path_rewrite` | 后端请求路径重写规则，例如 `/api => /v1/api` |
| `response_header` | 响应 Header 修改规则；代理 framing Header 不可修改 |
| `redirect_url` | 307 重定向地址，支持 `{path}` |
| `auto_cors` | 是否追加通用 CORS 响应 Header |
| `compat_mode` | 是否使用标准 HTTP 兼容代理链路 |
| `cert_mode` | 自定义域名证书录入方式：`upload` 或 `paste` |
| `cert` | 上传或粘贴模式的证书链 Base64 内容，也可直接提交 PEM 文本 |
| `key` | 上传或粘贴模式的私钥 Base64 内容，也可直接提交 PEM 文本 |
| `cert_file_path` | 历史证书路径；编辑时只能保持原值，不能改成新路径 |
| `key_file_path` | 历史私钥路径；编辑时只能保持原值，不能改成新路径 |
| `AutoHttps` | 是否自动 HTTPS |
| `upload_limit` | 资源上行限速，单位 KB/s；`0` 或留空表示不限速 |
| `download_limit` | 资源下行限速，单位 KB/s；`0` 或留空表示不限速 |
| `health_check_type` | 健康检查类型：`http` 或 `tcp` |
| `health_check_target` | 健康检查目标，多个 `host:port` 可用逗号或换行分隔 |
| `health_check_timeout` | 单次检查超时秒数，默认 2，最大 300 |
| `health_max_failed` | 连续失败次数，默认 3，最大 100 |
| `health_check_interval` | 检查间隔秒数，默认 10，最大 3600 |
| `health_http_url` | HTTP 检查路径，默认 `/` |

平台域名使用 `domain_mode=platform`、`platform_domain_id` 和 `platform_prefix`；此时证书路径由管理员维护的平台泛域名锁定。`/api/v1` 的 Host 业务字段与下方历史 Host 字段表一致，调用时应使用 `/api/v1/hosts` 路径；实时查重和规则诊断分别使用 `POST /api/v1/hosts/check` 与 `POST /api/v1/hosts/diagnose`。完整说明见[平台泛域名与规则诊断](extend/platform-domain.md)。

通过 `POST /api/v1/hosts` 新增或通过 `PATCH` / `PUT /api/v1/hosts/{id}` 修改自定义域名时，证书只接受 `cert_mode=upload` 或 `cert_mode=paste`，并提交 `cert` 和 `key`。接口不会读取调用方新提交的服务器文件路径：提交 `cert_mode=path` 或新的 `cert_file_path` / `key_file_path` 会返回 `400` 和 `operation_failed`。历史路径证书在编辑时可省略全部证书字段并原样保留，不会因修改其他字段而丢失；Host 查询响应不会返回服务器证书或私钥路径。仅使用 HTTP 或平台域名时可不提交 Host 级证书。

Host 列表与单条查询返回规则 `Id`、`Scheme` 以及 `HttpPort` / `HttpsPort`。当入口使用标准 80/443 时端口字段为空；非标准端口以 `:端口` 返回，例如 `:30111`，便于 API 调用方分别组装 HTTP 和 HTTPS 访问地址。TCP 隧道的访问主机名来自全局「服务地址」中的主机部分，协议、路径和配置端口会被剥离，隧道自身的服务端口由规则单独提供。

Host 列表和单条查询同时返回 `UploadLimit`、`DownloadLimit`、`UploadRate`、`DownloadRate`。限速单位为 KB/s，当前速率是近似的一秒采样值，空闲时为 `0`。

### 修改、删除、启动、停止 Host

```http
POST /index/edithost
POST /index/delhost
POST /index/hoststart
POST /index/hoststop
```

这些接口均使用 `id` 指定 Host。
