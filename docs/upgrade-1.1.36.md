# NPS 1.1.36 升级说明

本文说明从 1.1.35 或更早版本升级到 1.1.36 时的行为变化、配置迁移和兼容边界。1.1.36 主要完善自助注册、邮箱验证、IP 登记权限、使用说明页面和服务端配置维护，不改变 Bridge 协议，也不要求 NPC 同步升级。

## 本版本变化

- 自助注册现在创建独立用户并写入 `users.json`，不再把新账号写进客户端记录的旧版 Web 登录字段。
- 管理员可配置新注册用户的默认客户端配额、隧道总配额和有效天数；`0` 分别表示不限或永久。
- 注册支持图片验证码、提交限频、单 IP 成功注册上限和可选 SMTP 邮箱验证码。
- 新增管理员 API `POST /api/v1/users/register`，可以使用注册默认值，也可以显式指定配额和有效期。
- 普通用户可管理自己客户端下的 IP 登记；公共登记桶和其他用户的记录仍仅由管理员管理。
- 「系统 → 全局参数」的「全局参数」Tab 可加载远程 URL、`conf` 下的 HTML 文件，或把粘贴的 HTML 保存为 `conf/qa.html`。
- 服务端配置页增加摘要、搜索、分组折叠、源码视图、并发冲突检测和注册/SMTP 配置校验。

## 配置迁移

启动 1.1.36 时会读取并合并现有 `conf/nps.conf`，保留已有值、注释、权限和自定义项。旧配置缺少以下注册基础字段时会自动补齐：

- `register_captcha`
- `register_max_clients`
- `register_max_tunnels`
- `register_expire_days`
- `register_rate_window`
- `register_rate_limit`
- `register_ip_rate_limit`
- `register_email_verify`
- `register_email_code_ttl`
- `register_email_code_cooldown`

SMTP 配置属于可选字段，已有配置文件升级时可以不写入；需要邮箱验证时，可在「系统 → 全局参数」的「用户注册」Tab 中填写 `smtp_host`、`smtp_port`、`smtp_security`、`smtp_from` 和可选认证信息。启用邮箱验证前必须完成 SMTP 主机、端口和发件人配置。

`allow_user_register` 仍是公开注册的总开关，默认保持 `false`。已有显式值不会被升级覆盖。关闭邮箱验证时注册只要求用户名和密码；图片验证码是否启用由 `register_captcha` 控制。

## 数据兼容

- `users.json` 仅新增可选的 `email` 字段；旧用户没有该字段时保持原样，不需要手工迁移。
- 新注册用户和手工创建用户都按现有独立用户体系管理，不改变客户端归属、隧道配额和到期时间的数据结构。
- `global.json` 可使用可选的 `UsageGuideMode` / `UsageGuideSource` 保存使用说明配置；未配置时帮助页正常打开。
- `conf/qa.html` 是可选文件，不参与启动配置合并，也不会阻止 NPS 启动。
- Geo 数据路径留空时，1.1.36 会尝试加载运行目录 `conf/geoip.dat` 和 `conf/geosite.dat`；文件不存在时相关规则保持不可用。

## 二进制部署

1. 备份 `conf` 目录。
2. 停止 NPS，替换 `nps` 二进制并启动。
3. 查看启动日志确认配置迁移完成，然后强制刷新管理面板。
4. 如需开放注册，在「系统 → 全局参数」的「用户注册」Tab 中设置参数并保存；公开入口仍由 `allow_user_register` 控制，注册参数保存后即时生效。

## Docker 部署

```bash
docker pull wushuo98/nps:1.1.36
docker compose up -d
docker logs nps --tail=100
```

也可继续使用 `wushuo98/nps:latest`，生产环境建议固定版本号并在升级时明确执行拉取和重建。

## 验证清单

- 管理员登录后，全局参数和服务端配置页可以正常打开。
- 关闭 `allow_user_register` 时，登录页不显示可用注册入口，注册提交被拒绝。
- 开启注册后，新账号出现在用户管理中，并拥有配置的配额和有效期。
- 启用邮箱验证时，验证码发送、错误验证码拒绝和验证码过期均符合预期。
- 普通用户只能看到自己客户端下的 IP 登记，无法操作公共登记桶。
- 使用说明配置 URL、`conf` HTML 或粘贴 HTML 后，普通用户和管理员均能打开帮助页。
- 已有客户端、隧道、域名反代、API Token 和旧版客户端连接保持正常。

## 回滚

恢复 1.1.35 二进制和升级前的 `conf` 备份即可。1.1.36 没有删除或重命名既有持久化字段；回滚前建议停止写入并再次备份当前 `conf`，因为旧版本不会读取 1.1.36 新增的可选邮箱、使用说明和注册配置。

## 兼容边界

Bridge 协议、客户端版本分流规则、既有 Web 管理接口和 `/api/v1` 现有调用均保持不变。NPS 与 NPC 不需要同步升级；只有需要使用新增服务端功能时才升级 NPS。
