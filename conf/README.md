# 配置文件说明

## INI 配置

- `nps.conf`：服务端主配置。首次启动时，如果文件不存在，程序会自动从程序内置模板释放；模板中的随机管理密码、`auth_key` 和 `auth_crypt_key` 会自动生成。发布包中的 `CHANGE_ME` 占位值以及历史固定弱默认值会在启动时轮换，显式留空的可选鉴权项保持不变，公共客户端密钥默认关闭。仓库中的 `conf/nps.conf` 就是该内置模板的唯一来源。
- `npc.conf`：客户端完整配置示例，不会由服务端或 NPC 自动生成。使用配置文件模式时，请复制并按需修改它，再通过 `./npc -config=conf/npc.conf` 启动；也可以直接使用 `-server` 和 `-vkey` 参数启动。
- `multi_account.conf`：SOCKS5 等多账号代理认证文件，每行使用 `用户名=密码`。它不会自动复制，`npc.conf` 中的 `multi_account` 路径必须指向实际文件。

只有 `nps.conf` 会在服务端首次启动时自动创建。`npc.conf`、`multi_account.conf`、JSON 数据文件和证书属于部署文件或运行数据，不会被程序覆盖或自动释放；Docker 部署请将它们按需挂载到 `/conf`。

## JSON 数据文件

JSON 文件由管理面板和服务端自动维护，不要手工加入注释、尾逗号或修改字段名。

- `clients.json`：客户端列表。常用字段：`Id` 客户端编号、`VerifyKey` 验证密钥、`Remark` 备注、`Status` 是否允许连接、`RateLimit` 速率上限（KB/S）、`MaxConn` 最大连接数、`MaxTunnelNum` 最大隧道数、`IpWhite` 是否启用 IP 白名单、`ExpireTime` 到期时间。
- `tasks.json`：端口隧道列表。常用字段：`Id` 编号、`Port` 公网端口、`ServerIp` 监听 IP、`Mode` 代理模式、`Client.Id` 所属客户端、`Target.TargetStr` 内网目标、`Status` 是否启用、`HealthCheckType` 健康检查类型。
- `hosts.json`：HTTP/HTTPS 主机列表。常用字段：`Host` 域名、`Scheme` 协议、`Location` URL 路径、`Target.TargetStr` 内网目标、`CertFilePath`/`KeyFilePath` 证书和私钥、`AutoHttps` 是否自动 HTTPS、`HeaderChange` 请求头改写。
- `global.json`：全局运行数据。`BlackIpList` 是全局 IP 黑名单，`ServerUrl` 是客户端连接服务端时使用的地址，`PlatformDomains` 是管理员维护的平台泛域名池；每项包含稳定 ID、`*.example.com` 泛域名和对应证书/私钥路径。已被 Host 引用的平台域名不能删除或改名，证书路径更新会同步关联 Host。
- `users.json`：面板用户数据，首次启用多用户功能后自动生成。密码字段为敏感数据，请限制文件权限。

服务端保存 JSON 时会使用 `*#*` 作为记录分隔符，这是内部格式，不要删除或替换。
