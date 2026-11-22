# API 鉴权与兼容说明

新集成统一使用 [`/api/v1`](../api.md)。本页说明凭据、Token 生命周期和
旧 Web API 的兼容边界；完整端点、响应信封和分页规则以
[对外 API v1](../api.md)为准。

## 管理员凭据

服务端接受两套等价的管理员 API 密钥：

1. `nps.conf` 中的 `auth_key`（首次启动自动生成）；
2. 「系统 → 账户管理」生成的网页管理密钥，前缀为 `npsa_`。

新版本 `/api/v1` 默认使用 `NPS-HMAC` 请求头签名。签名覆盖 HTTP 方法、路径、完整
查询串、请求体摘要、时间戳、随机 nonce 和密钥类型。查询串按 Go `url.Values.Encode()`
规则排序和编码，所有参数（包括名为 `auth_key` 或 `timestamp` 的业务参数）都参与签名；
旧式 MD5 凭据参数在 HMAC 请求中没有特殊含义。格式非法或有歧义的查询串会被拒绝。
不要在 URL 中传 Token 或签名。

直接集成时，签名可按以下参考实现生成：

```python
import base64, hashlib, hmac, os, time
from urllib.parse import quote_from_bytes, unquote_to_bytes, urlencode, urlsplit

def go_escaped_path(path):
    # Match Go URL.EscapedPath: preserve a valid RawPath verbatim; if it
    # contains literal unsafe characters, escape the decoded path instead.
    if not path:
        return "/"
    safe = b"/!$&'()*+,-./:;=@_~"
    raw = path.encode("utf-8")
    i = 0
    raw_is_valid = True
    while i < len(raw):
        if raw[i] == ord("%"):
            if i + 2 >= len(raw):
                raw_is_valid = False
                break
            try:
                int(raw[i + 1:i + 3], 16)
            except ValueError:
                raw_is_valid = False
                break
            i += 3
            continue
        char = raw[i]
        if char >= 128 or not (char in safe or ord("0") <= char <= ord("9") or
                                ord("A") <= char <= ord("Z") or
                                ord("a") <= char <= ord("z")):
            raw_is_valid = False
            break
        i += 1
    if raw_is_valid:
        return path
    return quote_from_bytes(unquote_to_bytes(path), safe=safe.decode("ascii"))

def admin_hmac_headers(method, url, body, secret, key_id="auth_key"):
    parts = urlsplit(url)
    # Match Go url.ParseQuery: reject malformed percent escapes, unescaped
    # semicolons and invalid UTF-8 instead of signing a partial interpretation.
    raw_pairs = []
    if parts.query:
        for item in parts.query.split("&"):
            key, separator, value = item.partition("=")
            if not separator:
                value = ""
            raw_pairs.append((key.replace("+", " "), value.replace("+", " ")))

    def strict_unquote(value):
        raw = bytearray()
        index = 0
        while index < len(value):
            if value[index] == "%":
                if index + 2 >= len(value):
                    raise ValueError("malformed query escape")
                try:
                    raw.append(int(value[index + 1:index + 3], 16))
                except ValueError as error:
                    raise ValueError("malformed query escape") from error
                index += 3
            else:
                raw.extend(value[index].encode("utf-8"))
                index += 1
        return bytes(raw)

    def go_query_escape(value):
        # Go QueryEscape works on bytes, leaves only -_.~ unescaped and uses +
        # for spaces. Python's quote_from_bytes follows the same byte model.
        return quote_from_bytes(value, safe="-_.~").replace("%20", "+")

    values = {}
    for raw_key, raw_value in raw_pairs:
        if ";" in raw_key or ";" in raw_value:
            raise ValueError("unescaped semicolon in query")
        key, value = strict_unquote(raw_key), strict_unquote(raw_value)
        values.setdefault(key, []).append(value)
    pairs = [(key, value) for key in sorted(values)
             for value in values[key]]
    query = "&".join(go_query_escape(key) + "=" + go_query_escape(value)
                     for key, value in pairs)
    path = go_escaped_path(parts.path)
    timestamp = str(int(time.time()))
    nonce = base64.urlsafe_b64encode(os.urandom(24)).rstrip(b"=").decode()
    body_hash = hashlib.sha256(body or b"").hexdigest()
    canonical = "\n".join((method.upper(), path, query, body_hash,
                           timestamp, nonce, key_id))
    signature = hmac.new(secret.encode(), canonical.encode(), hashlib.sha256).hexdigest()
    return {
        "Authorization": "NPS-HMAC " + signature,
        "X-NPS-Timestamp": timestamp,
        "X-NPS-Nonce": nonce,
        "X-NPS-Key-Id": key_id,
    }
```

默认时间窗口为 60 秒，可通过 `api_hmac_time_window` 配置（范围 1–3600 秒）。
nonce 去重缓存只存在于单个 NPS 进程内；多副本间不会共享。若反向代理把请求分发
到多个副本，REST HMAC 不具备跨副本防重放保证。只有部署共享 nonce 存储才能提供
跨副本去重；粘滞路由只能降低正常请求被分发到不同副本的概率，不能作为防重放保证。
MCP 的 stateless 会话仅表示 MCP 不依赖会话 ID，不会让 REST nonce 缓存变成共享状态。

CLI 的 `--key-id auto|auth_key|managed_admin` 用于选择密钥类型。`auto` 按
`npsa_` 前缀识别网页管理密钥；如果 `auth_key` 本身碰巧以 `npsa_` 开头，必须显式
指定 `--key-id auth_key`。该 CLI 选项随 v1.1.51 发布；已发布的
v1.1.50 `npsctl` 不包含该选项。

旧版 `md5(管理员密钥 + timestamp)` 查询签名不再是默认方式。兼容旧调用方时，可临时在
`nps.conf` 设置 `api_legacy_md5_enabled=true`。仅允许注册的 `/api/v1` 受保护 API 路由和
历史 Web 管理路由；公开探针、登录/注册页面、未知路由和不支持的方法不会因此获得 API
身份。请求中的签名会出现在 URL/access log，且协议没有 nonce、防重放能力。迁移完成后
应关闭兼容开关。不要在新集成或示例中使用该旧协议。

### 旧式签名（仅迁移兼容）

旧 `/client/*`、`/index/*` 等历史管理 API 无法使用 `/api/v1` 的 HMAC 协议。旧版
MD5 示例仅供迁移仍依赖这些路由的调用方参考，要求显式启用上述兼容开关。它会把签名
暴露在 URL 日志中，仅应在 HTTPS 下短期迁移使用。

`/api/v1` 新集成推荐使用 `npsctl --key`，它会自动生成 HMAC 请求头；也可以按上方
Python helper 直接签名。

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

### 旧式 MD5 管理员签名（仅迁移兼容）

以下 `auth_key=md5(管理员密钥原文 + timestamp)` 查询参数属于已弃用的历史
签名，并非普通用户 Token，也不是当前 `/api/v1` 管理员调用的推荐方式。它只在
`api_legacy_md5_enabled=true` 时接受，时间戳误差窗口为 20 秒。签名在 URL 中，
可能进入反向代理、Web 服务器和监控系统的访问日志；该协议没有 nonce，窗口内可重放。
迁移到 `NPS-HMAC` 或用户 Bearer Token 后，应保持兼容开关关闭。

管理员可在「用户管理」为用户生成、查看或撤销 Token；普通用户可在
「系统 → 账户管理」自助管理。Token 同时保存哈希和加密密文，因此当前版本
生成的 Token 可再次查看；旧版本只保存哈希的 Token 无法恢复，需要重新生成。

## 接口边界

- `/api/v1` 只接受 API 凭据，不接受浏览器 Session Cookie。
- 管理员签名或普通用户 Token 在进入业务控制器前校验，失败返回 JSON
  `401/403`，不会跳转登录页。
- Bridge TLS 证书只允许管理员调用
  `/api/v1/global/bridge-certificate`；保存会同步校验证书并热加载到后续 TLS
  握手，热加载失败时继续使用上一份有效证书。
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

> 以下示例使用旧式 MD5 查询签名，只在 `api_legacy_md5_enabled=true` 时可用；签名会
> 进入 URL/access log，且可重放。仅用于迁移旧历史 Web API 集成。新代码请使用
> `/api/v1` 的 HMAC 请求头或用户 Bearer Token。

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
