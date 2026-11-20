# NPS Pro MCP 接入

NPS Pro 内置一个 [Model Context Protocol](https://modelcontextprotocol.io) 服务端，
位于 **`/mcp`**，供 AI Agent（Claude、Codex、Cherry Studio 等）通过 Bearer Token
调用平台的只读与维护能力。

## 启用与关闭

`mcp_enabled` 默认开启，可在 `conf/nps.conf` 或「全局参数 → 可视化配置 → Web API 认证」
中修改。关闭后 `/mcp` 返回 404。

## 认证

`/mcp` 复用平台的 API 凭据，使用标准 `Authorization: Bearer <token>` 头：

| 凭据 | 前缀 | 权限 |
| --- | --- | --- |
| 用户 API Token | `npsu_` | 自动限定为当前账号名下的客户端、隧道、域名反代 |
| 管理员 API 密钥 | `npsa_` | 全局权限，可读取用户与全局配置 |

Token 在「接入与安全」页面生成；生成后仍可再次查看，重新生成会立即使旧值失效。

## 客户端配置

将以下片段写入你的 MCP 客户端配置（例如 `claude_desktop_config.json` 的
`mcpServers`，或 Codex 的 MCP 配置）：

```json
{
  "mcpServers": {
    "nps": {
      "url": "http://127.0.0.1:8081/mcp",
      "headers": {
        "Authorization": "Bearer npsu_xxx"
      }
    }
  }
}
```

- `url` 必须是服务端可直接访问的地址；反向代理时应指向最终公网地址。
- 管理面板若配置了 `web_base_url` 前缀，`/mcp` 仍固定在根路径 `/mcp`，不受前缀影响。
- 服务端默认关闭本机保护，允许非 localhost 客户端连接；生产环境请务必置于 HTTPS 之后。

## 工具清单

| 工具 | 说明 | 权限 |
| --- | --- | --- |
| `nps_dashboard` | 客户端、隧道、域名反代数量与在线客户端数 | 账号范围 |
| `nps_clients` | 列出客户端，支持搜索与单客户端过滤 | 账号范围 |
| `nps_client_get` | 单个客户端详情 | 账号范围 |
| `nps_tunnels` | 列出隧道，支持按客户端/模式过滤 | 账号范围 |
| `nps_hosts` | 列出域名反代规则 | 账号范围 |
| `nps_client_create` / `update` / `delete` / `set_status` | 客户端维护 | 账号范围（改状态仅管理员） |
| `nps_tunnel_create` / `delete` / `set_status` | 隧道维护 | 账号范围 |
| `nps_host_create` / `delete` / `set_status` | 域名反代维护 | 账号范围 |
| `nps_users` | 列出用户 | 仅管理员 |
| `nps_audit` | 最近操作审计事件 | 仅管理员 |
| `nps_global_config` | 全局配置脱敏概览 | 仅管理员 |

## 与 npsctl / Skill 的关系

- **CLI**：`npsctl` 使用同一套 `/api/v1` 接口，适合脚本与终端；通过
  `curl -fsSL https://raw.githubusercontent.com/ZiDuNet/nps/master/scripts/install-npsctl.sh | bash` 安装。
- **Skill**：面向 AI Agent 的本地技能，从面板 `/skill/nps.tar.gz` 或 GitHub
  Release 的 `nps-skill.tar.gz` 下载，解压到 `~/.codex/skills`（Claude 为
  `~/.claude/skills`），可引导部署 NPC 并输出 `/mcp` 片段。
- **MCP**：给支持 MCP 的 Agent 使用，工具即接口，无需理解 HTTP 细节。

三者共享同一凭据模型，管理员密钥与用户 Token 均可复用。
