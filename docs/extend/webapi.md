# Web API

> 除 `AuthController` 和 `LoginController` 外，所有接口均需通过鉴权，详见 [API 鉴权说明](api.html)。

## 通用响应格式

| 响应类型 | 格式 |
| --- | --- |
| 成功 | `{"status": 1, "msg": "success"}` |
| 成功（含 id） | `{"status": 1, "msg": "success", "id": 123}` |
| 失败 | `{"status": 0, "msg": "error message"}` |
| 列表 | `{"rows": [...], "total": 100}` |

> 单个查询接口（`GetClient`、`GetOneTunnel`、`GetHost`）返回 `{"code": 1, "data": {...}}` 或 `{"code": 0}`。

---

## Client 客户端管理

### 客户端列表

```
POST /client/list/
```

| 参数 | 含义 |
| --- | --- |
| search | 搜索关键词 |
| sort | 排序字段 |
| order | asc 正序 / desc 倒序 |
| offset | 分页起始 |
| limit | 每页条数 |

返回 `AjaxTable` 格式，额外含 `ip`、`bridgeType`、`bridgePort` 字段。

---

### 获取单个客户端

```
POST /client/getclient/
```

| 参数 | 含义 |
| --- | --- |
| id | 客户端 id |

---

### 添加客户端

```
POST /client/add/
```

| 参数 | 含义 |
| --- | --- |
| remark | 备注 |
| vkey | 客户端验证密钥 |
| u | basic 权限认证用户名 |
| p | basic 权限认证密码 |
| compress | 是否压缩传输，`true` / `false` |
| crypt | 是否加密传输，`true` / `false` |
| config_conn_allow | 是否允许客户端以配置文件模式连接，`true` / `false` |
| rate_limit | 带宽限制，单位 KB/s，留空不限制 |
| flow_limit | 流量限制，单位 M，留空不限制 |
| max_conn | 最大连接数，留空不限制 |
| max_tunnel | 最大隧道数，留空不限制 |
| user_id | 所属用户 ID（仅管理员可指定；普通用户由服务端自动绑定当前账号） |
| web_username | web 用户登录用户名 |
| web_password | web 用户登录密码 |
| blackiplist | 黑名单 IP 列表，`\r\n` 分隔 |
| ipwhite | 是否开启 IP 白名单，`true` / `false` |
| ipwhitepass | IP 白名单授权密码 |
| ipwhitelist | 白名单 IP 列表，`\r\n` 分隔 |
| expire_time | 到期时间，留空表示永不过期。支持格式：`2006-01-02 15:04:05`、`2006-01-02 15:04`、`2006-01-02T15:04:05`、`2006-01-02T15:04`、`2006-01-02` |

普通用户调用新增接口时，服务端忽略请求中的 `vkey`、`user_id`、客户端级配额、`web_username`、`web_password` 和 `expire_time`，并自动将新客户端绑定到当前用户。普通用户可以提交 `remark`、`u`/`p`（Basic 认证）、`compress`、`crypt`、`ipwhite`、`ipwhitepass`、`ipwhitelist` 和 `blackiplist`；服务端会原子校验 `MaxClientNum`，达到上限时返回错误。

---

### 修改客户端

```
POST /client/edit/
```

| 参数 | 含义 |
| --- | --- |
| id | 要修改的客户端 id |
| remark | 备注 |
| vkey | 客户端验证密钥（仅管理员可修改） |
| u | basic 权限认证用户名 |
| p | basic 权限认证密码 |
| compress | 是否压缩传输，`true` / `false` |
| crypt | 是否加密传输，`true` / `false` |
| config_conn_allow | 是否允许客户端以配置文件模式连接，`true` / `false` |
| rate_limit | 带宽限制，单位 KB/s（仅管理员可修改） |
| flow_limit | 流量限制，单位 M（仅管理员可修改） |
| max_conn | 最大连接数（仅管理员可修改） |
| max_tunnel | 最大隧道数（仅管理员可修改） |
| web_username | web 用户登录用户名 |
| web_password | web 用户登录密码 |
| blackiplist | 黑名单 IP 列表，`\r\n` 分隔 |
| ipwhite | 是否开启 IP 白名单，`true` / `false` |
| ipwhitepass | IP 白名单授权密码 |
| ipwhitelist | 白名单 IP 列表，`\r\n` 分隔 |
| expire_time | 到期时间，格式同新增接口 |

编辑接口中的 `id` 只用于定位客户端，客户端 ID 本身不可修改。普通用户只能修改备注、Basic 认证、压缩、加密和 IP 访问控制；将 `p` 留空表示保留现有 Basic 密码。管理员可以调整归属、配额、验证密钥、旧版 Web 登录和客户端独立到期时间，但独立到期时间不得晚于所属用户到期时间。

---

### 更改客户端状态

```
POST /client/changestatus/
```

| 参数 | 含义 |
| --- | --- |
| id | 客户端 id |
| status | `true` 启用 / `false` 禁用 |

---

### 删除客户端

```
POST /client/del/
```

| 参数 | 含义 |
| --- | --- |
| id | 要删除的客户端 id |

---

## User 用户管理

用户接口仅供管理员调用。普通用户账号归属 `users.json`，与客户端的历史 Web 登录字段不同。

### 用户列表

```
POST /user/list/
```

| 参数 | 含义 |
| --- | --- |
| search | 用户名或备注关键词 |
| sort | 排序字段 |
| order | `asc` 或 `desc` |
| offset | 分页起始 |
| limit | 每页数量 |

### 添加用户

```
POST /user/add/
```

| 参数 | 含义 |
| --- | --- |
| username | 登录用户名，必须唯一 |
| password | 登录密码 |
| remark | 管理备注 |
| max_tunnel | 该用户全部客户端可创建的最大隧道数，`0` 为不限制 |
| max_client | 该用户可拥有的最大客户端数，`0` 为不限制；管理员分配和用户自建共用 |
| expire_time | 到期时间；留空表示永不过期 |

### 修改用户

```
POST /user/edit/
```

| 参数 | 含义 |
| --- | --- |
| id | 用户 ID |
| username | 登录用户名 |
| password | 新密码；留空时保留原密码 |
| remark | 管理备注 |
| max_tunnel | 最大隧道数，`0` 为不限制 |
| max_client | 最大客户端数，`0` 为不限制；不能调低到当前已分配客户端数量以下 |
| expire_time | 到期时间；留空表示永不过期 |

### 修改登录密码

```
POST /user/changepassword/
```

管理员提交 `id`、`new_password` 和 `confirm_password` 可重置指定普通用户密码。普通用户不需要提交 `id`（也可提交自己的 ID），必须同时提交 `current_password`、`new_password` 和 `confirm_password`，且不能修改其他用户；密码至少 6 个字符。接口不会返回或回显旧密码。

### 更改用户状态与删除

```
POST /user/changestatus/
POST /user/del/
```

| 接口 | 参数 | 含义 |
| --- | --- | --- |
| `/user/changestatus/` | `id`、`status` | 启用或停用用户。停用时会撤销其名下客户端的在线连接。 |
| `/user/del/` | `id` | 删除用户，并停用、解绑其名下客户端；管理员重新分配后才能恢复使用。 |

---

## Index 隧道管理

### 隧道列表

```
POST /index/gettunnel/
```

| 参数 | 含义 |
| --- | --- |
| client_id | 客户端 id |
| type | 隧道类型：`tcp`、`udp`、`httpProxy`、`socks5`、`secret`、`p2p`、`file` |
| search | 搜索关键词 |
| sort | 排序字段 |
| order | asc 正序 / desc 倒序 |
| offset | 分页起始 |
| limit | 每页条数 |

---

### 获取单条隧道

```
POST /index/getonetunnel/
```

| 参数 | 含义 |
| --- | --- |
| id | 隧道 id |

---

### 添加隧道

```
POST /index/add/
```

| 参数 | 含义 |
| --- | --- |
| client_id | 客户端 id |
| type | 隧道类型：`tcp`、`udp`、`httpProxy`、`socks5`、`secret`、`p2p`、`file` |
| remark | 备注 |
| port | 服务端端口（端口为 0 或留空时自动分配） |
| server_ip | 绑定的服务端 IP（多 IP 场景） |
| target | 内网目标，格式 `ip:端口` |
| local_proxy | 是否转发到 nps 服务器本地，`true` / `false` |
| password | 隧道密码（secret 模式） |
| local_path | 本地文件路径（file 模式） |
| strip_pre | URL 前缀去除（仅 `file` 模式） |
| proto_version | 协议版本 |

---

### 复制隧道

```
POST /index/copy/
```

| 参数 | 含义 |
| --- | --- |
| id | 要复制的源隧道 id |

复制后自动分配新端口和新 id，其他配置沿用源隧道。

---

### 修改隧道

```
POST /index/edit/
```

| 参数 | 含义 |
| --- | --- |
| id | 隧道 id |
| client_id | 客户端 id |
| type | 隧道类型 |
| port | 服务端端口 |
| server_ip | 绑定的服务端 IP |
| target | 内网目标 |
| local_proxy | 是否转发到 nps 服务器本地 |
| remark | 备注 |
| password | 隧道密码 |
| local_path | 本地文件路径 |
| strip_pre | URL 前缀去除（仅 `file` 模式） |
| proto_version | 协议版本 |

---

### 停止隧道

```
POST /index/stop/
```

| 参数 | 含义 |
| --- | --- |
| id | 隧道 id |

---

### 启动隧道

```
POST /index/start/
```

| 参数 | 含义 |
| --- | --- |
| id | 隧道 id |

---

### 删除隧道

```
POST /index/del/
```

| 参数 | 含义 |
| --- | --- |
| id | 隧道 id |

---

## Host 域名解析管理

### 域名列表

```
POST /index/hostlist/
```

| 参数 | 含义 |
| --- | --- |
| client_id | 客户端 id |
| search | 搜索关键词（域名/备注） |
| offset | 分页起始 |
| limit | 每页条数 |

---

### 获取单条域名解析

```
POST /index/gethost/
```

| 参数 | 含义 |
| --- | --- |
| id | 域名解析 id |

---

### 添加域名解析

```
POST /index/addhost/
```

| 参数 | 含义 |
| --- | --- |
| client_id | 客户端 id |
| remark | 备注 |
| host | 自定义模式下的完整域名；平台模式下由服务端根据前缀生成 |
| domain_mode | 域名来源：`custom`（默认）或 `platform` |
| platform_domain_id | 平台泛域名的稳定 ID；`domain_mode=platform` 时必填 |
| platform_prefix | 平台子域名前缀；`domain_mode=platform` 时必填，保存时全局查重 |
| scheme | 协议类型：`all`、`http`、`https` |
| location | URL 路由，留空不限制 |
| target | 内网目标，格式 `ip:端口` |
| local_proxy | 是否转发到 nps 服务器本地，`true` / `false` |
| header | 自定义 request header |
| hostchange | 修改 request host |
| key_file_path | 自定义域名的 HTTPS 证书私钥文本或路径；平台模式下忽略并由服务端锁定 |
| cert_file_path | 自定义域名的 HTTPS 证书文件文本或路径；平台模式下忽略并由服务端锁定 |
| AutoHttps | 是否自动 HTTPS（仅 scheme 非 `http` 时生效） |

---

### 修改域名解析

```
POST /index/edithost/
```

| 参数 | 含义 |
| --- | --- |
| id | 域名解析 id |
| client_id | 客户端 id |
| remark | 备注 |
| host | 自定义模式下的完整域名；平台模式下由服务端根据前缀生成 |
| domain_mode | 域名来源：`custom`（默认）或 `platform` |
| platform_domain_id | 平台泛域名的稳定 ID；`domain_mode=platform` 时必填 |
| platform_prefix | 平台子域名前缀；`domain_mode=platform` 时必填，保存时全局查重 |
| scheme | 协议类型 |
| location | URL 路由 |
| target | 内网目标 |
| local_proxy | 是否转发到 nps 服务器本地 |
| header | 自定义 request header |
| hostchange | 修改 request host |
| key_file_path | 自定义域名的 HTTPS 证书私钥文本或路径；平台模式下忽略并由服务端锁定 |
| cert_file_path | 自定义域名的 HTTPS 证书文件文本或路径；平台模式下忽略并由服务端锁定 |
| AutoHttps | 是否自动 HTTPS |

```
POST /index/hoststop/
```

| 参数 | 含义 |
| --- | --- |
| id | 域名解析 id |

---

### 启动域名解析

```
POST /index/hoststart/
```

| 参数 | 含义 |
| --- | --- |
| id | 域名解析 id |

---

### 删除域名解析

```
POST /index/delhost/
```

| 参数 | 含义 |
| --- | --- |
| id | 域名解析 id |

---

### 检查平台子域名是否可用

```
POST /index/platformhostavailable/
```

| 参数 | 含义 |
| --- | --- |
| platform_domain_id | 管理员配置的平台泛域名 ID |
| platform_prefix | 待检查的子域名前缀 |
| id | 编辑已有 Host 时传当前 Host ID；新增时省略 |

成功响应会包含 `available` 和拼接后的 `host`。该接口仅用于表单即时提示；新增和编辑接口仍会在服务端最终校验，不能依赖即时检查避免并发重复。

---

### 域名规则诊断

```
GET  /index/hostdiagnose/
POST /index/hostdiagnose/
```

`GET` 打开管理面板的诊断页；`POST` 返回路由诊断结果，不会向内网目标发起连接。

| 参数 | 含义 |
| --- | --- |
| host | 实际收到的 Host，不要包含协议或路径 |
| path | 请求路径，必须以 `/` 开头；留空按 `/` 处理 |
| scheme | `http` 或 `https` |

响应 `data` 中的 `matched` 表示是否命中可用规则；命中时 `rule` 包含规则、所属客户端和预览目标，未命中时 `reason` 说明 Host、协议、路径、停用状态或目标配置的原因。普通用户只会得到自己有权访问的规则信息。

---

## Global 全局设置

### 查看全局设置

```
GET /global/index/
```

返回全局黑名单 IP 列表、服务端 URL 和管理员维护的平台泛域名状态。全局设置仅管理员可访问。

---

### 保存全局设置

```
POST /global/save/
```

| 参数 | 含义 |
| --- | --- |
| globalBlackIpList | 全局黑名单 IP 列表，`\r\n` 分隔 |
| serverUrl | 服务端访问地址（用于更正显示 IP） |
| platform_domains | 平台域名数组的 JSON。每项为 `ID`、`Wildcard`（`*.example.com`）、`CertFilePath`、`KeyFilePath`；证书和私钥可同时留空（该项仅支持 HTTP），但不能只填一项。新增项可省略 `ID`，系统会生成。已被 Host 引用的项不可删除或修改 `Wildcard`，但可更新证书路径。 |

---
