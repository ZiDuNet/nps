# 历史 Web API 兼容清单

本页只记录旧版管理接口的路径和兼容约定。新集成请使用
[对外 API v1](../api.md)。

> 本页列出的历史管理接口中，除 `AuthController` 和 `LoginController` 的公开
> 辅助接口外，均需通过鉴权，详见[API 鉴权与兼容说明](api.md)。管理员使用
> 时间戳签名；普通用户可以使用自己的 Bearer Token，服务端会按用户自动过滤
> 资源。新版 `/api/v1` 的公开发现、对时和 IP 登记端点不受此句限制。

> **对外集成请优先使用 `/api/v1`**：统一 `status`/`code`/`msg`/`error_code`
> 信封、`request_id`、JSON 请求体和 `page`/`page_size` 分页。它只接受 API
> 凭据，不使用浏览器 Session；旧 `/client/*`、`/index/*`、`/user/*`、
> `/global/*` 与 `/auth/*` 保持原样。完整清单和示例见
> [对外 API v1](../api.md) 与 [API 鉴权与兼容说明](api.md)。

## 通用响应格式

| 响应类型 | 格式 |
| --- | --- |
| 成功 | `{"status": 1, "msg": "success"}` |
| 成功（含 id） | `{"status": 1, "msg": "success", "id": 123}` |
| 失败 | `{"status": 0, "msg": "error message"}` |
| 列表 | `{"rows": [...], "total": 100}` |

> 单个查询接口（`GetClient`、`GetOneTunnel`、`GetHost`）返回
> `{"code": 1, "status": 1, "msg": "ok", "data": {...}}`，失败时返回
> `{"code": 0, "status": 0, "msg": "错误说明"}`。旧的 `code`/`data` 字段
> 继续保留，新增的 `status`/`msg` 与其他管理接口保持一致。

对时和密钥发现仍保留 `/auth/gettime`、`/auth/getauthkey`，标准信封版本
统一使用 `/api/v1/time`、`/api/v1/authkey`。IP 登记只保留新版
`POST /api/v1/ip-register`，旧 `/auth/ipwhiteauth` 和
`/api/v1/ipwhiteauth` 已删除，见 [API 鉴权与兼容说明](api.md)。

所有由管理控制器返回的写操作响应会额外包含 `code` 和 `request_id` 字段；原有的
`status`、`msg`、`id` 字段继续保留，旧版调用方无需修改。`code` 当前为 `ok` 或
`error`，`request_id` 与响应头 `X-Request-ID` 一致，可用于把接口错误和服务端日志对应起来。

列表响应仍保持 `rows`、`total` 顶层字段，同时增加 `meta`：

```json
{
  "rows": [],
  "total": 0,
  "meta": {
    "offset": 0,
    "limit": 20,
    "page": 1,
    "page_size": 20,
    "has_more": false
  },
  "code": "ok",
  "request_id": "..."
}
```

`meta` 是附加信息，不改变旧版表格组件读取 `rows` 和 `total` 的方式。

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

返回 `AjaxTable` 格式，额外含 `ip`、`bridgeType`、`bridgePort`、`bridgeEndpoints` 字段。

客户端行新增：

| 字段 | 含义 |
| --- | --- |
| ConnectionProtocol | 当前承载新转发连接的协议标签 |
| ConnectionProtocols | 同一 VKey 当前在线的全部协议 |
| ConnectionCommands | 当前客户端可复制的普通命令和快捷命令 |

`ConnectionCommands` 只包含实际启用、正在监听且 `bridge_*_show=true` 的入口。每项提供 `Key`、`Label`、`Protocol`、`Transport`、`Address`、`Path`、`ALPN`、`NormalCommand`、`QuickCommand` 和 `QuickFormat`。TCP/TCP + TLS 快捷命令保持 v1，其他入口使用 v2；`HTTP/3` 仅在 HTTP/3 UDP 监听实际运行后出现。

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
| registered_only | 是否仅允许已登记 IP，`true` / `false` |
| register_key | 客户端 IP 登记密钥 |
| register_max_hours | 登记最长有效小时数，`0` 使用服务端默认值 |
| expire_time | 到期时间，留空表示永不过期。支持格式：`2006-01-02 15:04:05`、`2006-01-02 15:04`、`2006-01-02T15:04:05`、`2006-01-02T15:04`、`2006-01-02` |

普通用户调用新增接口时，服务端忽略请求中的 `vkey`、`user_id` 和客户端级配额，并自动将新客户端绑定到当前用户。普通用户可以提交 `remark`、`u`/`p`（Basic 认证）、`compress`、`crypt`、黑白名单以及 `registered_only`、`register_key`、`register_max_hours`；服务端会原子校验 `MaxClientNum`，达到上限时返回错误。

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
| registered_only | 是否仅允许已登记 IP，`true` / `false` |
| register_key | 客户端 IP 登记密钥 |
| register_max_hours | 登记最长有效小时数 |
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

管理员可以删除任意客户端；普通用户只能删除当前账号名下的客户端。删除成功后，该客户端的普通隧道、Host 域名规则和在线连接会一并清理，客户端配额随即释放。旧版按单个客户端登录的会话不能调用此接口执行自助删除。

---

## User 用户管理

用户列表、创建、编辑和删除等管理接口仅供管理员调用。普通用户账号归属 `users.json`，与客户端的历史 Web 登录字段不同；Token 自助接口允许普通用户只操作自己的凭据。

### 生成普通用户 API Token

```
POST /user/regenerateapikey/
```

管理员提交 `id`，响应返回新生成的 `token`。服务端同时保存 Token 哈希和加密密文，授权账号之后仍可通过状态接口再次查看；重新生成会使旧 Token 立即失效。Token 使用 `Authorization: Bearer <token>` 调用其他接口时，只能看到和管理该用户名下的客户端、隧道及 Host 规则；不会获得用户管理、全局参数或其他用户数据权限。普通用户也可以在自己的已授权会话中调用该接口轮换自己的 Token（API Token 调用时不需要提交 `id`）。

### 撤销普通用户 API Token

```
POST /user/revokeapikey/
```

管理员提交 `id` 撤销指定用户 Token；普通用户不提交 `id` 时撤销自己的 Token。撤销后原 Token 立即失效，接口不会返回 Token 原文。

### 查询当前用户 Token 状态

```
GET /user/apikeystatus/
```

普通用户可查询自己的 Token 是否已启用并取得可查看的原文；管理员可以提交 `id` 查询指定用户状态。由旧版本创建、没有密文字段的 Token 只返回 `enabled=true`，需要重新生成后才能再次查看。响应示例：

```json
{"status":1,"enabled":true,"masked":"npsu_••••••••","token":"npsu_REPLACE_WITH_TOKEN"}
```

### 资源拓扑大屏密钥

大屏快捷地址由登录后的面板操作生成，不使用 `auth_key` 签名 API。相关接口如下：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/overview/accesskey/` | 查询当前账号是否已配置大屏密钥，不返回原文 |
| `POST` | `/overview/accesskey/` | 生成或重新生成密钥；可选提交 `key` 自定义密钥，响应只显示一次 `key` 与 `url` |
| `POST` | `/overview/accesskey/revoke/` | 撤销当前账号大屏密钥 |

`url` 是带 `#access_key=...` 片段的快捷地址；生成后和之后已授权的状态查询都会返回 `key` 与 `url`，但只返回给当前管理员或当前普通用户。大屏数据接口 `/overview/data/` 只接受当前登录会话或 `Authorization: Bearer npsd_...`，并按密钥所属账号实时过滤资源。

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
| max_conn | 该用户全部客户端共享的最大并发连接数，`0` 为不限制；P2P 直连不计入 |
| upload_limit | 该用户全部客户端共享的上行带宽，单位 KB/s，`0` 为不限制 |
| download_limit | 该用户全部客户端共享的下行带宽，单位 KB/s，`0` 为不限制 |
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
| max_conn | 用户级最大并发连接数，`0` 为不限制 |
| upload_limit | 用户级共享上行带宽，单位 KB/s，`0` 为不限制 |
| download_limit | 用户级共享下行带宽，单位 KB/s，`0` 为不限制 |
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
| type | 隧道类型：`tcp`、`udp`、`httpProxy`、`socks5`、`mixProxy`、`secret`、`p2p`、`file` |
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
| type | 隧道类型：`tcp`、`udp`、`httpProxy`、`socks5`、`mixProxy`、`secret`、`p2p`、`file` |
| remark | 备注 |
| port | 服务端端口（端口为 0 或留空时自动分配） |
| server_ip | 绑定的服务端 IP（多 IP 场景） |
| target | 内网目标，格式 `ip:端口` |
| local_proxy | 是否转发到 nps 服务器本地，`true` / `false` |
| password | 隧道密码（secret 模式） |
| proxy_auth | `mixProxy` 独立 Basic 认证，每行 `用户名:密码`；配置后覆盖所属客户端的 Basic 认证 |
| local_path | 本地文件路径（file 模式） |
| strip_pre | URL 前缀去除（仅 `file` 模式） |
| proto_version | 协议版本 |
| upload_limit | 资源上行限速，单位 KB/s；`0` 或留空表示不限速 |
| download_limit | 资源下行限速，单位 KB/s；`0` 或留空表示不限速 |
| enable_http | `mixProxy` 是否启用 HTTP 正向代理；新增混合代理默认启用 |
| enable_socks5 | `mixProxy` 是否启用 SOCKS5；新增混合代理默认启用 |
| dest_acl_mode | `mixProxy` 目标 ACL：`0` 关闭、`1` 白名单、`2` 黑名单 |
| dest_acl_rules | `mixProxy` 目标规则，逐行填写主机名、通配域名、IP/CIDR、`full:`、`domain:`、`keyword:`、`geoip:` 或 `geosite:`；`geosite:` 匹配主机名，`geoip:` 只匹配目标本身为 IP/CIDR 的地址 |

隧道列表和详情只返回 `ProxyAuthConfigured`，不会返回 `proxy_auth` 明文。
修改时留空保留原账号，提交新账号会整体替换；提交
`clear_proxy_auth=true` 可清除独立认证并回退到客户端 Basic。

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
| enable_http / enable_socks5 | `mixProxy` 协议开关；省略时保留原值 |
| proxy_auth / clear_proxy_auth | 独立认证账号集合；留空保留原值，`clear_proxy_auth=true` 清除并回退客户端 Basic |
| dest_acl_mode / dest_acl_rules | `mixProxy` 目标 ACL 配置；省略时保留原值 |

编辑时可通过 `client_id` 将隧道迁移到当前账号有权限的另一个客户端；省略时沿用当前客户端，兼容旧调用方。目标客户端和用户配额会在迁移前校验。

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
| filter_client_id | 管理员列表筛选客户端；`0` 表示全部。已有 `client_id` 固定作用域优先于此筛选 |
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
| proxy_protocol | Host 后端 PROXY Protocol：`0` 关闭、`1` v1、`2` v2；后端必须显式支持 |
| header | 自定义 request header |
| hostchange | 修改 request host |
| path_rewrite | 路径重写规则，例如 `/api => /v1/api` |
| response_header | 响应 Header 修改规则；`-Name` 删除，代理 framing Header 不可修改 |
| redirect_url | 307 重定向地址，支持 `{path}` 占位符 |
| auto_cors | 是否追加通用 CORS 响应 Header |
| compat_mode | 是否使用标准 HTTP 兼容代理链路 |
| key_file_path | 历史自定义域名的私钥路径；仅允许原值保持不变，不能通过接口改成新路径 |
| cert_file_path | 历史自定义域名的证书路径；仅允许原值保持不变，不能通过接口改成新路径 |
| cert_mode | 自定义域名证书录入方式：`upload` 或 `paste`；省略且不提交新材料时保留已保存证书 |
| cert | 上传或粘贴模式的证书链 Base64 内容，也可直接提交 PEM 文本 |
| key | 上传或粘贴模式的私钥 Base64 内容，也可直接提交 PEM 文本 |
| AutoHttps | 是否自动 HTTPS（仅 scheme 非 `http` 时生效） |
| upload_limit | 资源上行限速，单位 KB/s；`0` 或留空表示不限速 |
| download_limit | 资源下行限速，单位 KB/s；`0` 或留空表示不限速 |

域名来源有且只能有一种：`custom` 必须提供 `host`，且不能同时提交 `platform_domain_id` / `platform_prefix`；`platform` 必须同时提供平台域名 ID 和前缀，且不能同时提交自定义 `host`。为兼容旧版客户端，未提交 `domain_mode` 时会按旧字段推断模式；新集成应始终明确传入 `domain_mode`。协议只接受 `http`、`https`、`all`（历史请求省略时按 `all` 处理）；内网目标每行一个 `host:port` 地址。

创建和更新会在数据层再次做原子唯一性、路由冲突和字段校验。失败时不会留下内存或 `hosts.json` 记录；Host ID 由递增序列分配，不保证无间断。列表和单条查询响应包含 `Id`、`Scheme`、`HttpPort`、`HttpsPort` 等字段；端口为空表示标准端口 80/443，非空时是带冒号的端口后缀，例如 `:30111`。因此消费方可分别用协议和端口组装 HTTP/HTTPS 访问地址，无需依赖页面模板配置。

Host 列表和单条查询同时返回 `UploadLimit`、`DownloadLimit`、`UploadRate`、`DownloadRate`。限速单位为 KB/s，当前速率是近似的一秒采样值，空闲时为 `0`。

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
| proxy_protocol | Host 后端 PROXY Protocol：`0` 关闭、`1` v1、`2` v2；省略时保留原值 |
| header | 自定义 request header |
| hostchange | 修改 request host |
| path_rewrite | 路径重写规则，例如 `/api => /v1/api` |
| response_header | 响应 Header 修改规则；`-Name` 删除，代理 framing Header 不可修改 |
| redirect_url | 307 重定向地址，支持 `{path}` 占位符 |
| auto_cors | 是否追加通用 CORS 响应 Header |
| compat_mode | 是否使用标准 HTTP 兼容代理链路 |
| key_file_path | 历史自定义域名的私钥路径；仅允许原值保持不变，不能通过接口改成新路径 |
| cert_file_path | 历史自定义域名的证书路径；仅允许原值保持不变，不能通过接口改成新路径 |
| cert_mode | 自定义域名证书录入方式：`upload` 或 `paste`；省略且不提交新材料时保留已保存证书 |
| cert | 上传或粘贴模式的证书链 Base64 内容，也可直接提交 PEM 文本 |
| key | 上传或粘贴模式的私钥 Base64 内容，也可直接提交 PEM 文本 |
| AutoHttps | 是否自动 HTTPS |
| upload_limit | 资源上行限速，单位 KB/s；`0` 或留空表示不限速 |
| download_limit | 资源下行限速，单位 KB/s；`0` 或留空表示不限速 |

编辑时可传 `client_id` 将规则迁移到该账号有权限的客户端；省略时沿用当前客户端，兼容旧调用方。平台/自定义域名字段互斥规则与新增接口相同。

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

## Global 全局参数

### 查看全局参数

```
GET /global/index/
```

返回全局黑白名单、服务端 URL、连接与带宽容量、使用说明和平台泛域名配置。平台域名证书通过独立的证书设置页面维护。该接口仅管理员可访问。

---

### 保存全局参数

```
POST /global/save/
```

| 参数 | 含义 |
| --- | --- |
| globalBlackIpList | 全局黑名单 IP 列表，`\r\n` 分隔 |
| serverUrl | 服务端访问地址（用于更正显示 IP） |
| platform_domains | 旧版兼容参数。新页面使用 `POST /global/certificate/platform-domains/`；该接口在省略此参数时保留现有平台域名池，提交 JSON 数组时按旧语义更新。 |

---

### 证书设置页面

```
GET /global/certificate/
```

仅管理员可访问。页面集中展示 Bridge TLS 证书、平台泛域名与证书、HTTPS 默认证书与 ACME、管理面板 HTTPS、IP 登记 HTTPS，以及 Bridge TLS 入口配置。完整 `nps.conf` 仍可通过服务端配置的源码视图查看和维护。

---

### 保存证书相关 nps.conf 字段

```
POST /global/certificate/config/
```

仅管理员可调用。参数使用服务端配置页面相同的 `config_<key>` 和
`config_enabled_<key>` 形式，但接口只接受证书设置页拥有的字段：
`https_default_cert_file`、`https_default_key_file`、`force_auto_ssl`、
`ssl_path`、`ssl_email`、`ssl_ca`、`ssl_zerossl_api`、`ssl_cache_max`、
`ssl_cache_reload`、`ssl_cache_idle`、`web_open_ssl`、`web_cert_file`、
`web_key_file`、`ip_register_cert_file`、`ip_register_key_file`、
`tls_enable` 和 `tls_bridge_port`。保存会保留注释、自定义项及其他未提交配置；
成功响应中的 `restart_required` 表示是否需要重启 NPS。

---

### 保存平台泛域名与证书

```
POST /global/certificate/platform-domains/
```

仅管理员可调用。`platform_domains` 为 JSON 数组，每项包含 `ID`、
`Wildcard`、`CertFilePath`、`KeyFilePath`，并可通过 `CertMode`、
`CertData`、`KeyData` 提交上传或粘贴的证书材料。只更新
`conf/global.json` 中的平台域名池，不修改全局访问名单、服务地址或使用说明。

---

### 查看 Bridge TLS 证书

```
GET /global/bridgecertificate/
```

仅管理员可调用。返回当前运行证书与 `nps.conf` 配置证书的状态、有效期、
剩余天数、SAN、密钥算法、链条数和 SHA-256 指纹，并标记证书变更是否需要
重启；不会返回私钥内容。

---

### 校验 Bridge TLS 证书

```
POST /global/bridgecertificate/check/
```

仅管理员可调用。参数与 `/api/v1/global/bridge-certificate/check` 相同：
`mode` 为 `path`、`upload` 或 `paste`，`cert`、`key` 为路径或证书材料，
`hostname` 可选用于校验证书覆盖的域名/IP。该接口只校验，不写文件或配置。

---

### 保存 Bridge TLS 证书

```
POST /global/bridgecertificate/
```

仅管理员可调用。`mode=path` 时保存 `cert`、`key` 路径；`mode=upload` 或
`paste` 时把材料写入 `conf/bridgessl/bridge_server.pem` 和
`conf/bridgessl/bridge_server.key` 后保存路径；`mode=clear` 删除两个配置键。
保存只会更新 `nps.conf` 中现有的 `bridge_cert_file`、`bridge_key_file`，
保留其他字段、注释和自定义项，保存后需要重启 NPS。标准 `/api/v1` 说明见
[对外 API v1](../api.md#bridge-tls-证书接口)。

---
