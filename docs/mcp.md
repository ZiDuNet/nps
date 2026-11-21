# NPS Pro MCP 接入

NPS Pro 内置一个 [Model Context Protocol](https://modelcontextprotocol.io) 服务端，
位于 **`/mcp`**，供 AI Agent（Claude、Codex、Cherry Studio 等）通过 Bearer Token
调用平台的只读与维护能力。

该端点实现 **Streamable HTTP**，不是旧式 SSE 端点。客户端配置应使用
`"type": "http"`，不要改成 `"type": "sse"`。

## 启用与关闭

`mcp_enabled` 默认开启，可在 `conf/nps.conf` 或「全局参数 → 可视化配置 → Web API 认证」
中修改。关闭后 `/mcp` 返回 404。

## 认证

`/mcp` 复用平台的 API 凭据，使用标准 `Authorization: Bearer <token>` 头：

| 凭据 | 前缀 | `npsctl` 用法 | MCP 直连 | 权限 |
| --- | --- | --- | --- | --- |
| 用户 API Token | `npsu_` | `--token` / `NPS_TOKEN` | 是 | 当前账号名下的客户端、隧道、域名反代；REST 审计仅显示本人范围 |
| Web 生成的管理员 API 密钥 | `npsa_` | `--key` / `NPS_KEY`，自动时间戳签名 | 是 | 全局 |
| `nps.conf` 中的 `auth_key` | 无固定前缀 | `--key` / `NPS_KEY`，自动时间戳签名 | 否 | 全局 |

Token 在「接入与安全」页面生成；生成后仍可再次查看，重新生成会立即使旧值失效。
管理员 API 密钥与 `auth_key` 一样可作为 `npsctl --key` 的签名密钥，但不能直接
作为 `/mcp` 的 Bearer Token。

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
- 客户端会先执行 MCP `initialize`，后续请求携带 `Mcp-Session-Id`；请求同时需要
  `Authorization` 和 MCP 协议要求的 `Accept: application/json, text/event-stream`。
- `GET /mcp` 可能保持长连接用于服务端事件流，不要把它当作普通健康检查接口；
  健康检查请使用面板或 `/api/v1/info`。
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
| `nps_client_create` / `update` / `delete` | 客户端维护 | 账号范围 |
| `nps_client_set_status` | 客户端启停 | 仅管理员 |
| `nps_tunnel_create` / `delete` / `set_status` | 隧道维护 | 账号范围 |
| `nps_host_create` / `delete` / `set_status` | 域名反代维护 | 账号范围 |
| `nps_users` | 列出用户 | 仅管理员 |
| `nps_audit` | 最近操作审计事件 | 仅管理员 |
| `nps_global_config` | 全局配置脱敏概览 | 仅管理员 |

权限边界说明：

- MCP 没有 `start` / `stop` 工具；客户端启停使用 `nps_client_set_status`，
  隧道和域名反代使用对应的 `nps_*_set_status`。
- `/api/v1/audit` 对普通用户返回其自身可见范围，但当前 MCP `nps_audit` 仅
  管理员可用。两种入口的权限语义并不完全一致，Agent 不应假设普通用户能通过
  MCP 读取审计。
- 401 认证失败由 HTTP 层直接返回，可能不是 JSON-RPC 错误对象；客户端应先
  处理 HTTP 状态和 `WWW-Authenticate`。
- MCP 工具结果只返回当前账号范围的资源；普通用户即使猜到他人物 ID，也会被
  服务端拒绝。

列表型工具返回 `total` 和对应资源数组；当前 MCP 工具集不是完整分页 API，
需要大规模数据时使用 `/api/v1` 的 `offset` / `limit` / `page_size`。

## 与 npsctl / Skill 的关系

- **CLI**：`npsctl` 使用同一套 `/api/v1` 接口，适合脚本与终端；通过
  `curl -fsSL https://raw.githubusercontent.com/ZiDuNet/nps/master/scripts/install-npsctl.sh | bash` 安装。
- **Skill**：面向 AI Agent 的本地技能，从面板 `/skill/nps.tar.gz` 或 GitHub
  Release 的 `nps-skill.tar.gz` 下载，解压到 `~/.codex/skills`（Claude 为
  `~/.claude/skills`），可引导部署 NPC 并输出 `/mcp` 片段。
- **MCP**：给支持 MCP 的 Agent 使用，工具即接口，无需理解 HTTP 细节。

三者共享同一凭据模型，管理员密钥与用户 Token 均可复用。
