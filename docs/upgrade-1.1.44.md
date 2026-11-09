# NPS 1.1.44 升级说明

本版完善登录操作审计与“审计与连接”页面，不改变 NPC 通信协议、账号和业务数据。
旧 NPC 无需随服务端同步升级；历史 JSONL 可直接读取，API 的 `Action` 字段继续使用稳定代码。

## 日志与页面

- `auth.login` 覆盖登录成功，以及请求过大/格式、PoW、图片验证码、请求过期、限频、密码和 TOTP 失败。
- `auth.remember.issue` 记录 3 天免登凭证签发；`auth.remember.restore` 记录真实会话恢复成功或失败，已登录会话不重复记录恢复。
- `auth.logout` 在清理会话前记录已验证账号。成功登录、退出及用户自己的设备撤销记录可由该用户查看；匿名失败只向管理员开放，不凭输入账号匹配归属。
- 注册账号和发送注册邮件的前置校验也记入操作审计。日志只保留安全元数据，不保存密码、Token、Cookie、验证码、PoW 答案、查询串或业务正文。
- 动作与资源显示中文/英文名称，鼠标悬浮动作可看原始代码；展开详情可复制完整 JSON。新增请求 ID、请求路径/方法、浏览器、认证方式、失败阶段、直连地址与 IP 识别来源。
- 页面铺满可用内容区，桌面记录区独立滚动、表头固定，手机自动切换卡片。更多诊断字段可在“列”中打开；日志关闭或读取失败会明确提示。

## 部署与升级

1. 备份完整配置、证书、`instance.key`、`web_sessions.json`、业务 JSON 和日志文件。
2. 替换 NPS 二进制或更新 Docker 镜像，保留原挂载目录并重启；浏览器强制刷新。
3. 在“全局参数 → 服务端配置 → 日志与审计”确认路径非空、目录可写。查看登录成功/失败、退出和实际免登录恢复记录。

Docker 路径是**容器内的文件路径**。若挂载 `./logs:/logs`，推荐配置：

```ini
log_path=/logs/nps.log
audit_log_path=/logs/audit.log
connection_log_path=/logs/connection.log
```

若只挂载 `./conf:/conf`，可使用 `/conf/logs/audit.log` 和 `/conf/logs/connection.log`。
目录会在写入时按需创建，但容器进程必须有写权限；修改挂载需要重建容器，修改配置需要重启。
审计日志和连接日志是独立 JSONL 文件，不要与 `nps.log` 指向同一个文件。
`log_max_size/log_max_files/log_max_days` 只控制主运行日志，不会自动轮转这两个独立日志；生产环境需安排外部归档和磁盘监控。

来源 IP 遵循现有 `allow_x_real_ip`、`http_forwarded_trust_mode`、`trusted_proxy_ips` 配置；前置反代必须覆盖真实 IP 头。直连地址单独保留，不将转发头当成不可伪造的证据。

旧日志没有记录的信息无法补回。若需回滚，停止进程后恢复旧程序即可；新增 JSON 字段不会要求迁移已有账号或业务数据。
