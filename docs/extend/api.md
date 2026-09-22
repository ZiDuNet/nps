# Web API 鉴权

在 `nps.conf` 中配置 `auth_key` 即可启用 API 鉴权（首次启动自动生成）。

## 鉴权方式

管理员签名 API 的每个请求需附带两个参数：

| 参数 | 说明 |
| --- | --- |
| `auth_key` | `md5(配置文件中的 auth_key + 当前时间戳)` |
| `timestamp` | 当前 unix 时间戳（秒） |

时间戳有效范围为 **20 秒**，每次请求须重新生成。

## 普通用户 API Token

管理员可以为每个普通用户生成独立的 Bearer Token。Token 只在生成接口的响应中显示一次，服务端仅保存 SHA-256 哈希；重新生成会立即使旧 Token 失效。普通用户 Token 不具备管理员权限，只能访问该用户拥有的客户端、隧道和 Host 规则。

管理员使用现有签名 API 调用：

```bash
ts=$(curl -s http://127.0.0.1:8081/auth/gettime/ | sed 's/.*"time":\([0-9]*\).*/\1/')
sign=$(echo -n "your_auth_key${ts}" | md5sum | awk '{print $1}')
curl -s -X POST "http://127.0.0.1:8081/user/regenerateapikey/" \
  -d "auth_key=${sign}&timestamp=${ts}&id=7"
```

响应中的 `token` 形如 `npsu_...`，之后在请求头中携带：

```bash
curl -s -X POST "http://127.0.0.1:8081/client/list/" \
  -H "Authorization: Bearer npsu_REPLACE_WITH_TOKEN" \
  -d "offset=0&limit=20"
```

Python：

```python
import requests

host = "http://127.0.0.1:8081"
token = "npsu_REPLACE_WITH_TOKEN"
r = requests.post(
    f"{host}/client/list/",
    headers={"Authorization": f"Bearer {token}"},
    data={"offset": 0, "limit": 20},
)
print(r.json())
```

JavaScript：

```javascript
const host = "http://127.0.0.1:8081";
const token = "npsu_REPLACE_WITH_TOKEN";

const response = await fetch(`${host}/client/list/`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/x-www-form-urlencoded",
  },
  body: new URLSearchParams({ offset: "0", limit: "20" }),
});
console.log(await response.json());
```

所有写操作仍必须使用 `POST`。也支持 `api_token` 查询参数以兼容无法设置请求头的客户端，但 URL 可能进入访问日志，生产环境应优先使用 `Authorization`。

### 普通用户自助管理 Token

普通用户登录面板后，在左侧账号区域打开「API 接入」即可查看状态、生成/重新生成或撤销自己的 Token。普通用户不需要填写用户 ID，服务端会从当前登录会话或当前 Bearer Token 绑定账号；提交其他用户 ID 会被拒绝。Token 只在生成成功的响应中显示一次，刷新页面后无法恢复，只能重新生成。

撤销当前用户 Token：

```bash
curl -s -X POST "http://127.0.0.1:8081/user/revokeapikey/" \
  -H "Authorization: Bearer npsu_REPLACE_WITH_TOKEN"
```

查询当前用户 Token 是否已启用：

```bash
curl -s "http://127.0.0.1:8081/user/apikeystatus/" \
  -H "Authorization: Bearer npsu_REPLACE_WITH_TOKEN"
```

管理员仍可在「用户管理」中生成、重新生成或撤销任意普通用户 Token。管理员只能看到「未配置 / 已启用」状态，不能查看已生成的 Token 原文。

## 资源拓扑大屏快捷地址

仪表盘的「资源拓扑」支持为当前账号生成独立的只读大屏密钥。管理员生成的快捷地址展示全局资源；普通用户生成的快捷地址只展示自己名下客户端、隧道和 Host 规则。该密钥与登录密码、普通用户 API Token 分开管理，不具备创建、修改、删除资源的权限。

生成后得到类似下面的地址：

```text
https://nps.example.com/overview#access_key=npsd_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

密钥位于 URL 的 `#` 片段中，浏览器不会把片段发送给 HTTP 服务器。快捷地址可用于无人值守大屏，支持自定义密钥；留空时由系统随机生成。服务端保存 SHA-256 哈希用于校验，同时保存使用 `auth_crypt_key`（缺失时回退 `auth_key`）加密的密钥副本，因此当前已登录且有权限的账号可以再次查看和打开快捷地址，服务端 JSON 中不会出现明文。密钥默认不设置过期时间，但管理员或账号本人可以随时撤销/重新生成；账号停用或到期时，其大屏密钥立即失效。

如果更换用于加密的 `auth_crypt_key`，旧密钥副本将无法解密；此时重新生成一次大屏密钥即可恢复查看。

请把快捷地址视为密码，仅通过 HTTPS 或可信内网分享。密钥仅用于读取拓扑数据，不能调用管理 API。

## 获取服务端时间戳

由于客户端与服务端时间可能不一致，可先获取服务端时间：

```
GET /auth/gettime/
```

返回：
```json
{"time": 1717654321}
```

> 此接口无需鉴权。

## 获取服务端 auth_key（加密）

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

## 接入示例

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

## 详细接口清单

- [Web API 接口文档](webapi.html)
