# NPS 1.1.48 升级说明

本版本修复反向代理环境下登录请求过期的问题，并新增 MCP、`npsctl` 和内置
Skill 三类接入能力。不改变 Bridge 握手、NPC 业务协议或业务 JSON 结构。

## 主要变化

- 登录改为无状态挑战：前端先请求 `/auth/challenge` 取得一次性挑战，服务端
  不再把 nonce 依赖在会话里。Nginx 缓存、多实例或会话粘滞配置不当时，不再
  反复出现 `login_expired`。
- 新增 MCP 服务端：`/mcp` 使用有状态 Streamable HTTP，`mcp_enabled` 默认开启；客户端按 MCP Streamable HTTP 会话流程处理初始化及会话 ID。v1.1.48 不支持后续版本的无状态会话与禁用流式响应行为。
  用户 API Token（`npsu_...`）和 Web 生成的管理员 API 密钥（`npsa_...`）
  可作为 Bearer Token；`nps_users`、`nps_global_config` 仅管理员可用，
  `nps_audit` 对普通用户只返回本人范围、管理员可查看全局。`nps.conf` 的
  `auth_key` 仍可用于 `npsctl --key` 的时间戳签名。
- 新增 `npsctl` CLI：覆盖客户端、隧道、域名反代、用户、审计和仪表盘查询，
  并可通过 `npsctl mcp` 生成 MCP 配置片段。
- 新增内置 NPS Skill：面板 `/skill/nps.tar.gz` 可直接下载；GitHub Release
  同时提供 `nps-skill.tar.gz`。
- 「账户管理」更名为「接入与安全」，页面按 Tab 组织，新增 Skill / CLI / MCP
  的 Agent 接入说明与一键复制。

## 升级步骤

1. 备份整个 `conf` 目录（含 `nps.conf`、客户端、隧道、域名反代和用户数据）。
2. 停止服务，替换 `nps` 二进制，再启动服务。
3. 打开面板确认版本为 `1.1.48`，并重新登录一次。
4. 需要 CLI 时执行 `scripts/install-npsctl.sh`，或从 Release 下载对应平台的
   `npsctl_<os>_<arch>`。
5. 需要 Agent 接入时，在「接入与安全」复制 Skill 提示词或 MCP 配置。

## 验证清单

- `GET /api/v1/` 返回 `version: "1.1.48"`；配置 `web_base_url` 时使用对应前缀。
- 管理员和普通用户都能正常登录；退出登录后 3 天免登凭证按预期失效。
- `/mcp` 无凭据返回 401；带 Bearer Token 先 `initialize` 再调用工具。
- v1.1.48 的 `/mcp` 是有状态 Streamable HTTP；不要套用 v1.1.49+ 的“无需保存/发送 `Mcp-Session-Id`”说明。
- `npsctl version` 输出 `1.1.48`，`npsctl clients --limit 5` 能看到
  `rows` 和 `total`。
- `/skill/nps.tar.gz` 可下载，解压后根目录为 `nps/`。

## 兼容性

- 旧 NPC 继续使用既有 Bridge 握手和业务协议，无需升级。
- 既有 `conf` JSON、历史快捷命令和 `/api/v1` 字段保持兼容。
- MCP 是新增入口，关闭 `mcp_enabled` 即恢复为 404，不影响其他功能。

## 回滚

替换回 1.1.47 二进制并重启即可。1.1.48 未新增必须迁移的数据文件；如已生成
API Token 或 TOTP 绑定，回滚后仍由原有字段保存。

## 注意事项

- MCP 客户端请使用 `type: "http"`（VS Code / Claude Code）或
  `streamableHttp`（Cline），不要填 `sse`；Claude Desktop 需要
  `mcp-remote` 桥接。
- v1.1.48 的 MCP 使用有状态 Streamable HTTP。客户端应按其 MCP 实现处理
  `initialize` 响应和会话 ID；不要用 `GET /mcp` 做健康检查。公开探针使用
  `GET /api/v1/`（设置 `web_base_url` 时添加该前缀）。无状态会话和禁用流式
  GET 是 v1.1.49 起的行为。
- Node.js MCP 客户端遇到证书链错误时设置 `NODE_EXTRA_CA_CERTS`；curl
  调试可临时使用 `-k`，不要在生产脚本中长期关闭校验。
