# Web API

Web API 与 Web 管理面板共用控制器。API 请求需要携带 `auth_key` 和 `timestamp`，或使用已登录的 Session。

## 鉴权

`auth_key` 的计算方式：

```text
md5(nps.conf 中的 auth_key + timestamp)
```

`timestamp` 为当前 Unix 时间戳，服务端允许约 20 秒误差。

示例：

```bash
curl -X POST 'http://127.0.0.1:8081/client/list' \
  -d 'auth_key=<md5值>' \
  -d 'timestamp=<当前时间戳>' \
  -d 'offset=0' \
  -d 'limit=10'
```

获取服务端时间：

```http
POST /auth/gettime
```

获取加密后的 `auth_key`：

```http
POST /auth/getauthkey
```

要求 `auth_crypt_key` 长度为 16 位，返回 AES-CBC 加密结果。

## 通用返回

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

## 客户端接口

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

### 获取单个客户端

```http
POST /client/getclient
```

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

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
| `rate_limit` | 带宽限制，KB/S |
| `flow_limit` | 流量限制，MB |
| `max_conn` | 最大连接数 |
| `max_tunnel` | 客户端最大隧道数 |
| `expire_time` | 到期时间 |
| `ipwhite` | 是否启用 IP 白名单 |
| `ipwhitepass` | IP 白名单授权密码 |
| `ipwhitelist` | IP 白名单列表 |
| `blackiplist` | 黑名单列表 |

普通用户调用此接口时，客户端会自动归属当前登录用户，`vkey`、`user_id`、客户端级配额、旧版 Web 登录凭据和独立到期时间等管理字段不会从请求体采纳。普通用户可设置备注、Basic 认证（`u`/`p`）、压缩、加密、IP 白名单/黑名单及 IP 授权密码；创建前会原子校验用户的 `MaxClientNum`。管理员可通过 `user_id` 指定归属用户。

### 修改客户端

```http
POST /client/edit
```

包含新增客户端参数，并额外需要：

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

编辑时客户端 ID 只用于定位记录，不能修改。普通用户只能编辑自己客户端的备注、Basic 认证、压缩、加密和 IP 访问控制；密码留空表示保持原值。管理员可修改归属、配额、验证密钥、旧版 Web 登录和独立到期时间，但客户端到期时间不能晚于所属用户到期时间。

### 删除客户端

```http
POST /client/del
```

| 参数 | 说明 |
|------|------|
| `id` | 客户端 ID |

## 用户接口

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

## 隧道接口

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
| `local_path` | 文件访问本地路径 |
| `strip_pre` | 文件访问路径前缀 |
| `local_proxy` | 是否代理到服务端本地 |
| `proto_version` | 协议版本 |

### 修改、删除、启动、停止

```http
POST /index/edit
POST /index/del
POST /index/start
POST /index/stop
```

这些接口均使用 `id` 指定隧道。

## Host 域名接口

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
| `header` | 请求 Header 修改 |
| `hostchange` | 请求 Host 修改 |
| `cert_file_path` | 证书路径 |
| `key_file_path` | 私钥路径 |
| `AutoHttps` | 是否自动 HTTPS |

平台域名使用 `domain_mode=platform`、`platform_domain_id` 和 `platform_prefix`；此时证书路径由管理员维护的平台泛域名锁定。完整参数、实时查重和规则诊断接口见[Web API 清单](extend/webapi.md#host-域名解析管理)与[平台泛域名说明](extend/platform-domain.md)。

### 修改、删除、启动、停止 Host

```http
POST /index/edithost
POST /index/delhost
POST /index/hoststart
POST /index/hoststop
```

这些接口均使用 `id` 指定 Host。
