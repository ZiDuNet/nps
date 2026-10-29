# 配置文件说明

## INI 配置

- `nps.conf`：服务端主配置。首次启动时，如果文件不存在，程序会自动从程序内置模板释放；模板中的随机管理密码、`auth_key` 和 `auth_crypt_key` 会自动生成。发布包中的 `CHANGE_ME` 占位值以及历史固定弱默认值会在启动时轮换，显式留空的可选鉴权项保持不变，公共客户端密钥默认关闭。升级时会把新版模板中缺失的必需配置项追加到现有文件，保留已有值、注释、自定义项和文件权限；由 Web 面板关闭并移除的可选监听、证书、日志和调试配置不会在下次启动时被重新添加。仓库中的 `conf/nps.conf` 就是该内置模板的唯一来源。
- `npc.conf`：客户端完整配置示例，不会由服务端或 NPC 自动生成。使用配置文件模式时，请复制并按需修改它，再通过 `./npc -config=conf/npc.conf` 启动；也可以直接使用 `-server` 和 `-vkey` 参数启动。
- `multi_account.conf`：SOCKS5 等多账号代理认证文件，每行使用 `用户名=密码`。它不会自动复制，`npc.conf` 中的 `multi_account` 路径必须指向实际文件。

只有 `nps.conf` 会在服务端首次启动时自动创建。`npc.conf`、`multi_account.conf`、JSON 数据文件和证书属于部署文件或运行数据，不会被程序覆盖或自动释放；Docker 部署请将它们按需挂载到 `/conf`。

管理员可以在 Web 面板「系统 → 全局参数」的「服务端配置」Tab 中维护当前运行目录下的 `nps.conf`。可视化页面按功能分类展示非证书、非注册配置；证书、私钥和 ACME 字段统一放在「证书设置」Tab，注册入口、新用户默认权益、限频和 SMTP 放在「用户注册」Tab，完整 `nps.conf` 仍可在源码视图中查看和维护。可选监听、证书、密钥、日志归档和调试项关闭后会从有效配置中移除。密码、API 密钥和私钥不会回显，保存后必须重启 NPS 才会应用。历史版本或自定义键会只读展示并在保存时保留。

Bridge 全协议共用一套 TLS 证书。管理员可在「系统 → 全局参数」的「证书设置」Tab 中通过路径、上传或粘贴方式维护 `bridge_cert_file`、`bridge_key_file`；上传/粘贴文件写入 `conf/bridgessl`，最终配置仍只保存在 `nps.conf`，保存后需重启 NPS 才会生效。平台泛域名和 HTTPS 默认证书、管理面板 HTTPS、IP 登记 HTTPS 也在同一 Tab 中维护。平台泛域名使用上传/粘贴时写入 `conf/certs/platform_<ID>.pem` 和 `conf/certs/platform_<ID>.key`；使用路径模式时不会复制文件。

客户端详情中的各协议连接命令由 `bridge_*_show` 控制是否展示，由 `bridge_*_show_ip`、`bridge_*_show_port` 和 `bridge_show_path` 覆盖公网地址、端口与 WS 路径。这些字段只影响命令文本，不改变服务端监听；`bridge_*_show=false` 会作为显式配置保留，升级时不会被自动恢复为 `true`。

## JSON 数据文件

JSON 文件由管理面板和服务端自动维护，不要手工加入注释、尾逗号或修改字段名。
带宽字段统一使用 KB/s，流量配额统一使用 MB，均按 1024 进制换算。

- `clients.json`：客户端列表。常用字段：`Id` 客户端编号、`VerifyKey` 验证密钥、`Remark` 备注、`Status` 是否允许连接、`RateLimit` 速率上限（KB/s）、`MaxConn` 最大连接数、`MaxTunnelNum` 最大隧道数、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单优先级及黑白名单、`RegisterKey` IP 登记密钥、`RegisterMaxHours` 登记最长有效期、`RegisteredOnly` 是否仅允许已登记 IP、`ExpireTime` 到期时间。登记记录本身只存在内存中，不写入 JSON。
- `tasks.json`：端口隧道列表。常用字段：`Id` 编号、`Port` 公网端口、`ServerIp` 监听 IP、`Mode` 代理模式、`Client.Id` 所属客户端、`Target.TargetStr` 内网目标、`Status` 是否启用、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单、`RegisteredOnly` 是否只允许已登记来源、`HealthCheckType` 健康检查类型、`UploadLimit`/`DownloadLimit` 规则上下行限速（KB/s）。旧数据缺少限速字段时按 `0`（不限速）处理。
- `hosts.json`：HTTP/HTTPS 主机列表。常用字段：`Host` 域名、`Scheme` 协议、`Location` URL 路径、`Target.TargetStr` 内网目标、`CertFilePath`/`KeyFilePath` 证书和私钥、`AutoHttps` 是否自动 HTTPS、`HeaderChange` 请求头改写、`IpListPriority`/`BlackIpList`/`WhiteIpList` 来源名单、`RegisteredOnly` 是否只允许已登记来源、`UploadLimit`/`DownloadLimit` 规则上下行限速（KB/s）。
- `global.json`：全局运行数据。`BlackIpList`/`WhiteIpList` 是全局 IP 黑白名单，`ServerUrl` 是客户端连接服务端时使用的地址，`PlatformDomains` 是管理员维护的平台泛域名池；每项包含稳定 ID、`*.example.com` 泛域名和对应证书/私钥路径。已被 Host 引用的平台域名不能删除或改名，证书路径更新会同步关联 Host。
- `users.json`：面板用户数据，首次启用多用户功能后自动生成。`MaxClientNum`、`MaxTunnelNum` 分别是客户端与总隧道配额，`MaxConn`、`UploadLimit`、`DownloadLimit` 分别是全部所属客户端共享的并发连接数及上下行带宽（KB/s）；这些配额使用 `0` 表示不限制。密码字段为敏感数据，请限制文件权限。

服务端保存 JSON 时会使用 `*#*` 作为记录分隔符，这是内部格式，不要删除或替换。
