# 配置文件说明

## INI 配置

- `nps.conf`：服务端主配置。首次启动时，如果文件不存在，程序会自动释放默认模板；模板中的随机管理密码、`auth_key` 和 `auth_crypt_key` 会自动生成。
- `npc.conf`：客户端配置示例。`[common]` 是公共连接参数，其他节是代理任务。
- `multi_account.conf`：SOCKS5 等多账号代理认证文件，每行使用 `用户名=密码`。

## JSON 数据文件

JSON 文件由管理面板和服务端自动维护，不要手工加入注释、尾逗号或修改字段名。

- `clients.json`：客户端列表。常用字段：`Id` 客户端编号、`VerifyKey` 验证密钥、`Remark` 备注、`Status` 是否允许连接、`RateLimit` 速率上限（KB/S）、`MaxConn` 最大连接数、`MaxTunnelNum` 最大隧道数、`IpWhite` 是否启用 IP 白名单、`ExpireTime` 到期时间。
- `tasks.json`：端口隧道列表。常用字段：`Id` 编号、`Port` 公网端口、`ServerIp` 监听 IP、`Mode` 代理模式、`Client.Id` 所属客户端、`Target.TargetStr` 内网目标、`Status` 是否启用、`HealthCheckType` 健康检查类型。
- `hosts.json`：HTTP/HTTPS 主机列表。常用字段：`Host` 域名、`Scheme` 协议、`Location` URL 路径、`Target.TargetStr` 内网目标、`CertFilePath`/`KeyFilePath` 证书和私钥、`AutoHttps` 是否自动 HTTPS、`HeaderChange` 请求头改写。
- `global.json`：全局运行数据。`BlackIpList` 是全局 IP 黑名单，`ServerUrl` 是客户端连接服务端时使用的地址。
- `users.json`：面板用户数据，首次启用多用户功能后自动生成。密码字段为敏感数据，请限制文件权限。

服务端保存 JSON 时会使用 `*#*` 作为记录分隔符，这是内部格式，不要删除或替换。
