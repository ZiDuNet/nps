# NPS 1.1.32 升级说明

本文说明从 1.1.31 或更早版本升级到 1.1.32 时的兼容边界。1.1.32 只新增对外 API，不改变 NPS/NPC 通信协议、持久化格式或现有 Web 管理接口。

## 本版本变化

- 新增统一对外 API `/api/v1`，覆盖概览、客户端、隧道、域名反代、用户、全局配置、Geo、登录保护、IP 登记、审计、状态与资源拓扑。
- `/api/v1` 使用 `status`、`code`、`msg`、`request_id` 统一信封；失败响应额外包含 `error_code`。
- 管理员继续使用 `auth_key + timestamp` 签名，也可以使用网页管理密钥；普通用户使用 `Authorization: Bearer npsu_...`。
- 写接口支持 `application/json`，并继续兼容表单与查询参数。列表默认每页 20 条，最大 500 条，支持 `offset`/`limit` 和 `page`/`page_size`。
- `/api/v1` 不接受浏览器 Session。普通用户 Token 仍按授权客户端过滤资源，用户管理、全局配置、审计等管理资源只允许管理员凭据。

完整端点和示例见[对外 API v1](api.md)。

## 兼容性

- 无新增 `nps.conf` 字段，无配置迁移。
- 无数据库或 JSON 数据格式变更；现有 `clients.json`、`users.json`、`tasks.json`、`hosts.json` 和 `global.json` 可直接使用。
- NPS 与 NPC 的 Bridge 握手、控制消息和数据协议不变。NPS 与 NPC 不需要为了 `/api/v1` 同步升级，现有在线客户端不会断开或降级。
- 旧 `/auth/*`、`/client/*`、`/index/*`、`/user/*`、`/global/*` 的路径、参数和返回格式保持不变，现有 Web 页面、NPC 与其他集成无需修改。
- 开发期间短暂出现的 `/v1/auth/*` 没有进入任何正式 Release，已在 1.1.32 开发阶段移除。正式 1.1.31 不包含该路径，因此已发布用户没有迁移或兼容负担。

## 二进制部署

1. 按既有流程备份 `conf` 目录。
2. 停止 NPS，替换 `nps` 二进制后重新启动。
3. 使用旧接口和新接口各执行一次只读请求，确认鉴权与资源范围符合预期。

无需修改现有配置文件。如果 NPC 当前连接稳定，不必仅为本次升级重启 NPC。

## Docker 部署

```bash
docker pull wushuo98/nps:1.1.32
docker compose up -d
docker logs nps --tail=100
```

也可以继续使用 `wushuo98/nps:latest`，但生产环境建议固定版本号，升级时明确执行 `docker pull` 并重建容器。

## 验证与回滚

推荐验证：

```bash
curl -i http://127.0.0.1:8081/api/v1
curl -i http://127.0.0.1:8081/api/v1/time
curl -i -X POST http://127.0.0.1:8081/auth/gettime
```

预期 `/api/v1` 与 `/api/v1/time` 返回统一信封，旧 `/auth/gettime` 继续返回历史格式。验证普通用户 Token 时，只能访问该用户获授权的客户端、隧道、Host 和统计资源。

如需回滚，恢复 1.1.31 二进制和备份配置即可。1.1.32 没有修改持久化数据，旧版本不会因数据格式无法读取而受影响。
