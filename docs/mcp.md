# NPS Pro MCP 接入

NPS Pro 内置一个 [Model Context Protocol](https://modelcontextprotocol.io) 服务端，
位于 **`/mcp`**，供 AI Agent（Claude、Codex、Cherry Studio 等）通过 Bearer Token
调用平台的只读与维护能力。

该端点实现 **Streamable HTTP**，不是旧式 SSE 端点。客户端配置应使用
`"type": "http"`，不要改成 `"type": "sse"`。

## 启用与关闭

`mcp_enabled` 默认开启，可在「系统 → 全局参数 → 服务端配置 → 账号与接入」的
“Web API 认证”中修改。该值保存在 SQLite，保存后按字段提示生效；关闭后
`/mcp` 返回 404。

## 认证

`/mcp` 复用平台的 API 凭据，使用标准 `Authorization: Bearer <token>` 头：

| 凭据 | 前缀 | `npsctl` 用法 | MCP 直连 | 权限 |
| --- | --- | --- | --- | --- |
| 用户 API Token | `npsu_` | `--token` / `NPS_TOKEN` | 是 | 当前账号名下的客户端、隧道、域名反代与本人范围审计 |
| Web 生成的管理员 API 密钥 | `npsa_` | `--key` / `NPS_KEY`，自动时间戳签名 | 是 | 全局 |
| `nps.conf` 中的 `auth_key` | 无固定前缀 | `--key` / `NPS_KEY`，HMAC 时间戳签名；前缀碰撞时指定 `--key-id auth_key` | 否 | 全局 |

Token 在「接入与安全」页面生成；生成后仍可再次查看，重新生成会立即使旧值失效。
管理员 API 密钥可直接作为 `/mcp` Bearer Token，也可作为 `npsctl --key`
的签名密钥。`nps.conf` 中的 `auth_key` 只能用于 `npsctl --key` 时间戳签名，
不能直接作为 `/mcp` Bearer Token。

Agent 配置中的 `Authorization` 是可直接使用的 Bearer 凭据。部分 MCP 客户端会将其
明文保存在本机配置文件中；应限制该文件的操作系统访问权限，不要提交到代码仓库、
同步到不可信位置或截图分享。优先使用权限受限的用户 Token；只有确实需要全局管理
能力时才配置 `npsa_` 管理员密钥。怀疑泄漏时立即在「接入与安全」撤销/重新生成密钥，
并同步更新 Agent 配置。

注意：v1.1.50 及更早的 `npsctl` 使用旧 MD5 查询签名，不包含 HMAC 和
`--key-id`。本文 HMAC 用法要求服务端和 CLI 均升级到 v1.1.51 或更新版本。
如必须分阶段升级，可在「服务端配置 → 账号与接入」的“Web API 认证”中临时开启
“兼容旧版 MD5 API 签名”（`api_legacy_md5_enabled`）；升级 CLI 并验证 HMAC 后
务必关闭该兼容项。
兼容期旧签名会进入 URL/access log 且不防重放，不应长期启用。

AI Agent 接入优先使用用户 API Token：它只管理当前账号拥有的客户端、隧道和域名反代，
审计也仅限本人范围。管理员 API 密钥在 MCP 中拥有全局权限，能管理所有用户的这些资源，
并读取全局用户列表、全量审计与脱敏全局配置。管理员页面生成的 MCP 示例默认不会填入
此全局密钥；复制时只有在明确确认其全局权限后才会使用。MCP 不提供用户管理或全局配置写入工具。

## 客户端配置

将以下片段写入你的 MCP 客户端配置（例如 `claude_desktop_config.json` 的
`mcpServers`，或 Codex 的 MCP 配置）：

```json
{
  "mcpServers": {
    "nps": {
      "type": "http",
      "url": "http://127.0.0.1:8081/mcp",
      "headers": {
        "Authorization": "Bearer npsu_xxx"
      }
    }
  }
}
```

- `url` 必须是服务端可直接访问的地址；反向代理时应指向最终公网地址。
- 使用 `"type": "http"`。当前端点没有 SSE 兼容地址。
- 管理面板若配置了 `web_base_url` 前缀，`/mcp` 仍固定在根路径 `/mcp`，不受前缀影响。
- 服务端默认关闭本机保护，允许非 localhost 客户端连接；生产环境请务必置于 HTTPS 之后。
- 服务端按无状态模式运行，客户端仍会先执行 MCP `initialize`，但不需要保存或
  发送 `Mcp-Session-Id`；如果客户端发送了旧的或未知 session ID，服务端也会忽略它。
  每个请求都单独校验 Bearer Token，可配合多实例和无粘滞会话的反向代理部署。
- 无状态 MCP 和禁用事件流（认证后的 `GET /mcp` 返回 405）从 v1.1.49 开始；
  v1.1.48 使用有状态 Streamable HTTP，
  需要遵循 MCP 客户端的初始化/会话流程。REST HMAC nonce 防重放缓存则独立保存在
  每个 NPS 进程内存中，多副本不共享，不能因为 MCP 无状态就视为 REST HMAC 已跨实例防重放。
- 请求同时需要 `Authorization` 和 MCP 协议要求的
  `Accept: application/json, text/event-stream`。
- MCP 已启用时，缺少或无效的 Bearer Token 会先得到 `401 Unauthorized`；通过认证的
  `GET /mcp` 返回 `405 Method Not Allowed`，不会建立长连接事件流。关闭 MCP 时端点返回 `404`。
- 没有单独的 `/api/v1/info` 或 MCP 健康检查接口。做基础 HTTP/API 存活探测可请求公开的
  `GET /api/v1/`（返回 API 版本与能力发现 JSON）；配置了 `web_base_url=/nps` 时请求
  `/nps/api/v1/`，并确保反向代理保留该路径前缀。
  `/api/v1/time` 也是公开接口，但主要用于时间同步。
- 只支持 stdio 的老客户端应使用 `mcp-remote` 一类的桥接器，而不是把 `/mcp`
  改成 SSE URL。

HTTPS 使用私有 CA 时，Node.js 客户端启动前设置
`NODE_EXTRA_CA_CERTS=/path/to/ca.pem`。`curl -k` 只适合临时诊断，不要写进
MCP 配置或生产脚本。

## 工具清单

| 工具 | 说明 | 权限 |
| --- | --- | --- |
| `nps_dashboard` | 客户端、隧道、域名反代数量与在线客户端数 | 账号范围 |
| `nps_clients` | 列出客户端，支持搜索与单客户端过滤 | 账号范围 |
| `nps_client_get` | 单个客户端详情 | 账号范围 |
| `nps_tunnels` | 列出隧道，支持按客户端/模式过滤 | 账号范围 |
| `nps_hosts` | 列出域名反代规则 | 账号范围 |
| `nps_platform_domains` | 列出可用的平台泛域名（ID、泛域名、是否配置证书） | 账号范围 |
| `nps_client_create` / `update` / `delete` | 客户端维护 | 账号范围 |
| `nps_client_set_status` | 客户端启停 | 仅管理员 |
| `nps_tunnel_create` / `delete` / `set_status` | 隧道维护 | 账号范围 |
| `nps_host_create` / `delete` / `set_status` | 域名反代维护 | 账号范围 |
| `nps_users` | 列出用户 | 仅管理员 |
| `nps_audit` | 最近操作审计事件 | 用户本人范围；管理员全局 |
| `nps_global_config` | 全局配置脱敏概览 | 仅管理员 |

权限边界说明：

- MCP 没有 `start` / `stop` 工具；客户端启停使用 `nps_client_set_status`，
  隧道和域名反代使用对应的 `nps_*_set_status`。
- 用户 Token 只能维护本人拥有的资源，且创建/更新客户端时不能修改管理员分配的连接数、
  隧道数和带宽配额；管理员密钥可跨用户维护资源。管理员独有的全局读取能力不会扩展为
  用户管理或全局配置写入能力。
- `/api/v1/audit` 与 MCP `nps_audit` 都按当前账号隔离：普通用户只看本人
  范围，管理员看全局记录。
- 401 认证失败由 HTTP 层返回 `application/json` 对象，并带
  `WWW-Authenticate`；客户端应先处理 HTTP 状态，再读取 JSON 错误。
- MCP 工具结果只返回当前账号范围的资源；普通用户即使猜到他人物 ID，也会被
  服务端拒绝。

列表型工具 `nps_clients`、`nps_tunnels`、`nps_hosts` 和 `nps_users`
接受 `offset` / `limit`，返回匹配总数 `total`、当前页数据与分页参数；
`limit` 最大为 500。`nps_audit` 同样支持 `offset` / `limit`。

所有工具都声明了 MCP 安全提示：读取工具为只读、幂等；创建工具可能产生新
资源；更新和状态工具为幂等；删除工具标记为 destructive；工具均不访问
NPS 之外的开放世界资源。

## 域名反代：只创建平台域名

`nps_host_create` 只支持平台域名，不接受自定义主机名，参数如下：

| 参数 | 说明 |
| --- | --- |
| `client_id` | 必填，所属客户端 ID |
| `prefix` | 平台子域名前缀，单层标签，例如 `demo`；留空时服务端随机生成 8 位字母数字并在冲突时重试 |
| `platform_domain_id` | 平台泛域名 ID 或 `*.example.com`；留空时自动选择第一个可用泛域名 |
| `scheme` | `http`、`https` 或 `all`；未配置证书的平台域名只能创建 `http` |
| `location`、`target`、`remark` | 与面板字段一致 |

调用前先用 `nps_platform_domains` 确认存在可用泛域名。传入 `host` 或
`domain_mode=custom` 会被拒绝并提示“自定义域名请在 Web 面板创建”，MCP 不会
自动降级为自定义域名。成功响应包含 `id`、最终 `host` 和实际使用的
`platform_domain_id`，例如：

```json
{"id": 12, "host": "demo.example.com", "platform_domain_id": "pd_7f3c1a2b"}
```

前缀已被同路径规则占用时工具返回错误，换一个前缀重试即可；服务端不会静默改写
已有规则。

## 与 npsctl / Skill 的关系

- **CLI**：`npsctl` 使用同一套 `/api/v1` 接口，适合脚本与终端；通过
  `curl -fsSL https://raw.githubusercontent.com/ZiDuNet/nps/master/scripts/install-npsctl.sh | bash` 安装。
  它每天在后台检查一次 GitHub 上的新版本，成功时静默替换自身；也可运行
  `npsctl self-update` 立即检查。自动更新失败只打印警告，不影响命令结果，
  设置 `NPSCTL_NO_UPDATE_CHECK=1` 可完全关闭。
- **Skill**：面向 AI Agent 的本地技能，从面板 `/skill/nps.tar.gz` 或 GitHub
  Release 的 `nps-skill.tar.gz` 下载，解压到 `~/.codex/skills`（Claude 为
  `~/.claude/skills`），可引导部署 NPC 并输出 `/mcp` 片段。
- **MCP**：给支持 MCP 的 Agent 使用，工具即接口，无需理解 HTTP 细节。

三者共享同一凭据模型，管理员密钥与用户 Token 均可复用。
