# NPS 1.1.51 升级说明

v1.1.51 加固管理员 `/api/v1` 鉴权，并扩充 `npsctl` 的分页、输出和 HTTPS
连接选项。Bridge/NPC 协议、SQLite schema 和现有业务数据格式均未改变；旧 NPC
无需为本次升级同步更新。

## API 管理员签名变化

v1.1.50 及更早版本的 `npsctl --key` 使用 `auth_key` 和时间戳生成 MD5 查询签名，
凭据会进入 URL 与代理访问日志，且签名在时间窗口内可重放。v1.1.51 默认改为
`NPS-HMAC` 请求头，签名覆盖 HTTP 方法、转义路径、规范化查询串、请求体摘要、时间戳、
一次性 nonce 和密钥类型。普通用户的 `npsu_` Bearer Token 用法不变。

推荐维护窗口内同步升级服务端和 `npsctl`，避免旧 CLI 因新服务端默认关闭旧签名而
暂时无法进行管理员 API 调用。若必须分阶段迁移：

1. 备份整个 `conf` 目录和当前 CLI/服务端二进制。
2. 升级服务端至 v1.1.51。若旧 CLI 暂时必须继续工作，可在新服务端配置
   `api_legacy_md5_enabled=true` 后重启服务；也可在服务端配置页面开启同名兼容项。
   该开关默认关闭，不要在升级前把它加到旧版本配置并误以为旧版会执行迁移。
3. 升级 `npsctl` 至 v1.1.51，验证 `npsctl version`，然后使用 `--key` 执行只读查询。
   CLI 会自动使用 HMAC-SHA256。`npsa_` 管理员 API 密钥可直接用于 `--key`；
   `nps.conf` 的 `auth_key` 需要 `--key-id auth_key`（如果自动识别存在歧义）。
4. 确认新的调用正常后，关闭 `api_legacy_md5_enabled` 并重启/应用配置。检查
   反向代理访问日志，避免继续保留旧 URL 签名记录。

旧 MD5 兼容仅允许明确列出的历史管理/API 路径与 HTTP 方法；不接受公开探针、未知路由
或不支持的方法。它仍会将签名写入 URL 日志且不能防重放，应只用于短期迁移。不要把
`auth_key` 直接当作 MCP Bearer Token：MCP 使用 `npsu_` 用户 Token 或 `npsa_` 管理员
API 密钥。MCP 配置可能把 Bearer Token 明文保存在本机文件中，按密码保护并避免提交或分享。

## 多副本部署注意

HMAC 时间戳默认有效期为 60 秒，可由 `api_hmac_time_window` 调整。nonce 防重放缓存只
存在于单个 NPS 进程内存中；多个服务副本之间不共享。无状态 MCP 会话不改变 REST HMAC
的 nonce 边界。需要跨副本防重放时，应由部署提供共享 nonce 存储；粘滞会话不是防重放
保证。

## CLI 新选项

- `--offset` 与 `--limit` 支持列表翻页。
- 默认列表以易读表格呈现；`--json` 输出 JSON，`--fields id,remark,status` 输出所选
  TSV 字段。
- `--timeout` 调整 HTTP 请求超时；`--ca-file` 指定 HTTPS 私有 CA。
- 管理员密钥类型可用 `--key-id auto|auth_key|managed_admin` 指定。

普通用户继续使用 `--token` 或 `NPS_TOKEN`。不要把 `--token`、`--key`、完整 MCP
配置或实际请求签名放入仓库、工单或截图。

## SQLite 与 NPC 兼容性

本版不执行 JSON 到 SQLite 迁移，也不修改 `conf/nps.db` schema。已经在 SQLite 上运行的
环境直接升级即可；仍处于 JSON 迁移流程的环境应遵循其原有的 [SQLite 升级说明](server/sqlite-storage.md)。
不要为 v1.1.51 重复运行 `-json-gateway`。

NPC 握手和 Bridge 业务协议没有变更；旧 NPC 可继续连接。CLI 的版本应与服务器 API
鉴权版本匹配，但这不构成 NPC 客户端强制升级要求。

## 升级后验收

1. 面板、`GET /api/v1/` 和 `npsctl version` 显示 `1.1.51`。
2. 使用普通用户 Token 执行只读列表，确认账号范围和 `--offset` 翻页正确。
3. 使用新 `npsctl --key` 执行管理员只读列表，确认 HMAC 成功。
4. 确认 MCP 配置使用 `type: "http"`，并验证所用 Token 的权限范围。
5. 确认迁移完成后 `api_legacy_md5_enabled=false`。
6. 检查旧 NPC 上线、隧道转发及服务端日志；本次不要求更新 NPC。

若 HMAC 请求因服务器时间偏差过大而失败，先校准 NTP，并确认反向代理未删除
`Authorization`、`X-NPS-Timestamp`、`X-NPS-Nonce` 或 `X-NPS-Key-Id` 请求头。不要
为了排查在长期生产配置中重新开启旧 MD5 签名。
