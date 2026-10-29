# API 鉴权与兼容说明

新集成统一使用 [`/api/v1`](../api.md)。本页说明凭据、Token 生命周期和
旧 Web API 的兼容边界；完整端点、响应信封和分页规则以
[对外 API v1](../api.md)为准。

## 管理员凭据

服务端接受两套等价的管理员 API 密钥：

1. `nps.conf` 中的 `auth_key`（首次启动自动生成）；
2. 「系统 → API 接入」生成的网页管理密钥，前缀为 `npsa_`。

每个请求携带：

| 参数 | 说明 |
| --- | --- |
| `auth_key` | `md5(管理员密钥原文 + timestamp)` |
| `timestamp` | 当前 Unix 时间戳（秒） |

服务端允许约 **20 秒**时间偏差，每次请求都应重新计算。两套管理员密钥权限
等价，均可覆盖全部功能和用户管理。网页管理密钥可在验证登录密码后再次查看。

```bash
ts=$(date +%s)
sign=$(printf '%s' "your_auth_key${ts}" | md5sum | awk '{print $1}')
curl -s "http://127.0.0.1:8081/api/v1/clients?auth_key=${sign}&timestamp=${ts}"
```

## 普通用户 Token

普通用户使用 `npsu_` 前缀的 Bearer Token：

```http
Authorization: Bearer npsu_<token>
```

```bash
curl -s "http://127.0.0.1:8081/api/v1/clients?limit=20" \
  -H "Authorization: Bearer npsu_REPLACE_WITH_TOKEN"
```

普通用户 Token 不携带时间戳，也不能访问管理员的用户管理、全局参数、Geo、
登录保护和管理员密钥接口。服务端会按 Token 所属用户过滤客户端、隧道、
Host、统计、审计和连接日志。

管理员可在「用户管理」为用户生成、查看或撤销 Token；普通用户可在
「系统 → API 接入」自助管理。Token 同时保存哈希和加密密文，因此当前版本
生成的 Token 可再次查看；旧版本只保存哈希的 Token 无法恢复，需要重新生成。

## 接口边界

- `/api/v1` 只接受 API 凭据，不接受浏览器 Session Cookie。
- 管理员签名或普通用户 Token 在进入业务控制器前校验，失败返回 JSON
  `401/403`，不会跳转登录页。
- Bridge TLS 证书只允许管理员调用
  `/api/v1/global/bridge-certificate`；保存后需重启 NPS。
- 旧 `/auth/*`、`/client/*`、`/index/*`、`/user/*`、`/global/*` 继续兼容
  Web 页面、NPC 和既有调用方，清单见[历史 Web API](webapi.md)。

证书输入、响应字段和示例见
[Bridge TLS 证书接口](../api.md#bridge-tls-证书接口)。

## 资源拓扑大屏快捷地址

仪表盘的「资源拓扑」支持为当前账号生成独立的只读大屏密钥。管理员生成的快捷地址展示全局资源；普通用户生成的快捷地址只展示自己名下客户端、隧道和 Host 规则。该密钥与登录密码、普通用户 API Token 分开管理，不具备创建、修改、删除资源的权限。

生成后得到类似下面的地址：

```text
https://nps.example.com/overview#access_key=npsd_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

密钥位于 URL 的 `#` 片段中，浏览器不会把片段发送给 HTTP 服务器。快捷地址可用于无人值守大屏，支持自定义密钥；留空时由系统随机生成。服务端保存 SHA-256 哈希用于校验，同时保存使用 `auth_crypt_key`（缺失时回退 `auth_key`）加密的密钥副本，因此当前已登录且有权限的账号可以再次查看和打开快捷地址，服务端 JSON 中不会出现明文。密钥默认不设置过期时间，但管理员或账号本人可以随时撤销/重新生成；账号停用或到期时，其大屏密钥立即失效。

如果更换用于加密的 `auth_crypt_key`，旧密钥副本将无法解密；此时重新生成一次大屏密钥即可恢复查看。

请把快捷地址视为密码，仅通过 HTTPS 或可信内网分享。密钥仅用于读取拓扑数据，不能调用管理 API。

## 历史兼容接口

以下路径和示例仅用于维护已有集成。新集成请使用 `/api/v1` 的
`/time`、`/authkey` 与标准响应信封。

### 获取服务端时间戳

由于客户端与服务端时间可能不一致，可先获取服务端时间：

```
GET /auth/gettime/
```

返回：
```json
{"time": 1717654321}
```

> 此接口无需鉴权。

### 标准格式（`/api/v1`）

同一组公共能力在对外 API 中统一使用
`status`/`code`/`msg`/`data`/`request_id` 信封。
旧 `/auth/*` 路径与返回格式继续保留，现有调用方无需改动：

| 接口 | 对外 API | 说明 |
| --- | --- | --- |
| `GET /auth/gettime/` | `GET /api/v1/time` | 服务端时间戳 |
| `GET /auth/getauthkey/` | `GET /api/v1/authkey` | 加密后的 `auth_key` |
| - | `POST /api/v1/ip-register` | 新版限时 IP 登记；密钥只放请求体 |

`/api/v1/time` 返回：

```json
{"status": 1, "code": "ok", "msg": "ok", "data": {"time": 1717654321}, "request_id": "..."}
```

`/api/v1/authkey` 返回：

```json
{
  "status": 1,
  "code": "ok",
  "msg": "ok",
  "data": {"crypt_auth_key": "…", "crypt_type": "aes cbc"},
  "request_id": "..."
}
```

失败时返回
`{"status": 0, "code": "error", "error_code": "...", "msg": "错误说明", "request_id": "..."}`
并携带对应的 HTTP 状态码。

### 获取服务端 auth_key（加密）

```
GET /auth/getauthkey/
```

返回经 AES-CBC 加密后的 `auth_key`（hex 编码）。

> 此接口无需鉴权。需确保 `nps.conf` 中 `auth_crypt_key` 为 **16 位**。

解密参数：
- 算法：AES-128-CBC
- 密钥：`auth_crypt_key`（16 字节）
- IV：与密钥相同
- 填充：PKCS5Padding
- 密文编码：hex

---

### 历史接口接入示例

::: tabs

@tab curl

```bash
# 1. 获取服务端时间戳
ts=$(curl -s http://127.0.0.1:8081/auth/gettime/ | sed 's/.*"time":\([0-9]*\).*/\1/')

# 2. 计算签名（Linux）
sign=$(echo -n "your_auth_key${ts}" | md5sum | awk '{print $1}')
# 或 macOS:
# sign=$(echo -n "your_auth_key${ts}" | md5)

# 3. 调用接口
curl -s -X POST "http://127.0.0.1:8081/client/list/" \
  -d "auth_key=${sign}&timestamp=${ts}&search=&order=asc&offset=0&limit=10"
```

@tab Python

```python
import hashlib, requests

host = "http://127.0.0.1:8081"
auth_key = "your_auth_key"

ts = requests.get(f"{host}/auth/gettime/").json()["time"]
sign = hashlib.md5(f"{auth_key}{ts}".encode()).hexdigest()

r = requests.post(f"{host}/client/list/", data={
    "auth_key": sign, "timestamp": ts,
    "search": "", "order": "asc", "offset": 0, "limit": 10
})
print(r.json())
```

@tab JavaScript

```javascript
const crypto = require("crypto");

const host = "http://127.0.0.1:8081";
const authKey = "your_auth_key";

(async () => {
  const ts = (await (await fetch(`${host}/auth/gettime/`)).json()).time;
  const sign = crypto.createHash("md5").update(`${authKey}${ts}`).digest("hex");

  const r = await fetch(`${host}/client/list/`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ auth_key: sign, timestamp: ts, search: "", order: "asc", offset: 0, limit: 10 }).toString(),
  });
  console.log(await r.json());
})();
```

:::

---

## 兼容接口清单

- [历史 Web API 清单](webapi.md)
