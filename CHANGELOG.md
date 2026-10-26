# 更新日志

所有重要变更记录在此。

## [1.1.31] - 2026-10-26

### Added

- Secret 与 P2P 隧道的 `target_type` 由“仅配置兼容”升级为真实转发能力：`tcp` 只转发 TCP，`udp` 只转发 UDP，`all` 在同一任务上同时支持 TCP 与 UDP；UDP 使用 4 字节长度前缀的数据报帧协议，在 Bridge/QUIC/mux 上保留数据报边界。旧记录和旧 NPC 无此字段，继续按 TCP 处理。
- NPC 的 P2P 访客命令行新增 `-fallback_secret`（默认开启），直连打洞失败后自动回退到同任务的 Secret 中继；Secret 访客继续按任务密码直接建立中继连接。
- 普通隧道新增隧道级最大连接数（`max_conn`）与总流量配额（`flow_limit`，MB），0 表示不限，Web 表单和 API 均可配置。
- 域名反代的自定义域名与平台泛域名证书统一支持三种录入方式：服务器路径、文件上传、直接粘贴，并提供有效性校验；证书私钥按既有方式安全保存。
- 管理面板新增「系统 → API 接入」页面：普通用户可生成、再次查看和撤销自己的 API Token；管理员可查看并管理两套等价的管理员密钥（`nps.conf` 的 `auth_key` 与网页管理密钥），密钥脱敏显示，需验证登录密码后才能查看原文。
- 全局参数新增 Geo 数据状态与「在线更新」操作，从可信上游拉取 geoip.dat/geosite.dat 并校验 SHA-256；未配置路径时默认使用 `conf/geoip.dat` 与 `conf/geosite.dat`。
- 仪表盘补齐配置信息：Bridge TCP/KCP/TLS/WS/WSS/QUIC 端口与 WS 路径、P2P 入口地址、IPv4/IPv6 出口地址、已登记 IP 数量、最小安全握手版本等；配置、系统和状态指标按卡片分组展示。
- 普通用户可以在客户端新增/编辑页设置自己名下客户端的独立到期时间，边界受所属用户有效期约束。
- 客户端列表的版本号改为可点击的升级入口，点击后弹窗检测 GitHub 最新版本并下发远程升级，不再依赖单独的空白操作按钮。

### Changed

- 管理面板全面移除 ZUI 运行时与资源：统一使用本地内置的 Bootstrap 5.3.3 构建弹窗、下拉菜单、表单和响应式布局。
- 导航图标由 ZUI ZenIcon 字体迁移到内置 Font Awesome 实心图标；旧版 `data-toggle` / `data-dismiss` 调用全部改为 Bootstrap 5 的 `data-bs-*`。
- 客户端和用户的有效期选择器由 ZUI DatetimePicker 改为内置 Flatpickr，支持中文界面和深色主题，同时保留无 JavaScript 增强时的纯文本输入回退。
- 启动时自动清理已部署目录中的 ZUI 静态文件，升级到新版本后不会再残留旧 UI 资源。
- 新增与编辑表单按业务分组，高级设置默认收起并支持字段联动；列表折叠详情改为多列紧凑排版，重要信息用气泡、状态色和短标签突出显示。
- 所有列表统一工具栏位置和操作交互，尽量用模态框替代 `alert`，能局部刷新时不整页刷新；列表展开详情补齐字段，目标地址输入支持粘贴 `http://host:port` 等写法并自动解析校验。
- 公共 vkey 注册流程分配的新验证密钥由 10 字符改为 16 字符，与 NPC 配置文件模式固定的 16 字节读取对齐。
- `quic-go` 升级到 `v0.64.1`，适配 Go 1.26 的 crypto/tls 行为，修复 P2P 直连 QUIC 握手完成后服务端 panic 导致 provider 掉线的问题。

### Fixed

- 修复 NPC 配置文件模式使用公共 vkey 时注册流程卡死、客户端永远无法建立配置连接的问题。
- 修复 P2P 直连建立成功时 provider 因 QUIC session ticket 断言 panic、服务端随后清理任务的连锁故障。

## [1.1.30] - 2026-10-25

### Added

- NPS/NPC 配置文件和命令行支持自定义 DNS；多个 DNS 服务器会轮询并在失败时切换，未配置时保持系统解析器。
- NPC 出站 Bridge 代理支持 HTTP/HTTPS CONNECT、SOCKS4/4a、SOCKS5/SOCKS5H，以及 `ALL_PROXY`/`NO_PROXY` 环境变量；代理认证统一使用标准 `Proxy-Authorization`。
- `local_ip` 与 `local_ip_forward` 现在实际作用于 TCP、UDP、SOCKS5 UDP、KCP、QUIC 和 P2P 相关出站连接；私网目标仍保持默认路由。
- NPC 命令行支持按位置同时连接多个 NPS 服务端，配置文件模式也支持逗号分隔的多份配置；新增管理员登录封禁查看、解封和过期清理页面。
- NPC 直接启动模式支持 `-disable_remote_update=true`；新增 `npc update-force`，可重新安装同版本的最新正式 Release。

### Changed

- 补齐 NPC Docker/配置文件文档，明确自定义 DNS、代理协议和本地出口绑定的边界。
- QUIC/KCP 使用显式本地地址时复用同一套连接生命周期和超时处理，不改变空配置的默认行为。
- SOCKS5 UDP ASSOCIATE 改为共享 UDP 监听与会话注册，支持多来源绑定、队列保护、空闲回收、断线回收和目标访问控制；大于单个 MTU 的合法 SOCKS5 UDP 数据报不再被固定缓冲区截断。
- 隧道列表将配置、监听、客户端三种状态合并为紧凑状态组，表头支持悬浮查看完整字段名，窄屏继续使用卡片布局避免横向滚动。

## [1.1.29] - 2026-10-24

### Added

- HTTP 正向代理与 SOCKS5 在 Web 面板统一为混合代理，可在同一公网端口按需启用两种协议；旧规则继续兼容，编辑时可平滑迁移。
- 管理员新增「全局参数 → 服务端配置」页面，按功能分组维护 `nps.conf`，支持敏感字段脱敏、可选配置开关、原子保存和重启提示。
- Bridge 配置页与文档补齐 WebSocket/WSS、QUIC、IP 登记和日志轮转等配置项；升级时不会重新添加已关闭的可选配置。

- Web 隧道与域名规则支持 HTTP 200/TCP 后端健康检查，并将有效配置同步到兼容的新 NPC。
- TCP 隧道 API 返回 `AccessHost`、`AccessAddr`、`AccessAddress` 和 `AccessURL`，Host API 返回健康检查字段。
- 普通隧道和域名规则支持独立的上行/下行限速与当前速率采样，旧 JSON 数据默认按不限速兼容。

### Changed

- 资源新增、编辑、启停和删除后会刷新客户端健康检查配置；旧版 NPC 不接收新控制帧，继续使用原有配置。
- API 列表和单条查询始终返回 `UploadLimit`、`DownloadLimit`、`UploadRate`、`DownloadRate`，并补齐 NPC 配置文件和升级迁移说明。

### Fixed

- 修复首次启动 Web 管理面板时 Cookie 会话加密密钥长度错误导致服务端 panic 的问题。

## [1.1.28] - 2026-10-22

### Added

- 客户端列表展开详情安全显示 Basic 认证用户名和旧版客户端用户名；密码只显示是否已设置，不返回明文。
- 域名解析新增“跨域与兼容”开关说明，明确自动 CORS、兼容模式、OPTIONS 预检和 WebSocket 的实际行为。

### Changed

- 域名解析表单将 CORS 和兼容模式说明按开关分组展示，新增和编辑页面保持一致。

## [1.1.27] - 2026-10-22

### Fixed

- 修复 NPC 系统服务日志默认权限过严导致普通用户无法查看日志的问题；日志文件和轮转文件统一使用 `0644`，并为旧日志权限错误提供操作提示。
- 修复 GUI-NPC 未注册服务端远程升级处理器的问题；收到升级指令后可下载、校验、替换并自动重启。
- 修复 NPC 手动更新和服务端下发更新并发替换可执行文件的风险，并避免重复请求 GitHub Release 元数据。

### Changed

- Web 客户端列表中的升级按钮移入常驻操作列，避免窄屏或隐藏版本列时无法找到。
- 补齐 NPC 服务日志、批量重启菜单和客户端升级流程文档。

## [1.1.26] - 2026-10-22

### Changed

- 恢复 v1.1.24 的产品功能基线，保留客户端远程升级、连接日志分类和普通用户删除自有客户端等能力。
- 恢复 v1.1.23 发布流水线：master 推送触发 CI、文档和 Docker 测试构建，Docker 同时更新 `latest` 与不可变 SHA 标签；正式版本标签构建跨平台 Release 和版本号镜像。

## [1.1.25] - 2026-10-22

### Changed

- 日常推送不再自动触发流水线；正式版本标签统一触发 CI、文档部署和 Release。
- 正式 Release 的 Docker 多架构构建同时推送版本号标签和 `latest`，不再需要额外的镜像晋升任务。
- Release 页面正文自动提取本文件的当前版本记录，并保留 GitHub 自动生成的变更摘要。

## [1.1.24] - 2026-10-21

### Added

- 普通用户可从客户端列表删除自己名下的客户端。
- 删除操作会同步清理该客户端的隧道、Host 域名规则和在线连接，并在归属变更竞态下再次校验所有权。
- master Docker 构建只发布 `sha-<短提交号>` 测试标签；正式 Release 同时发布版本号标签和 `latest` 稳定标签。

### Security

- 旧版按客户端登录的会话不能获得自助删除权限；管理员仍可删除任意客户端。

## [1.1.23] - 2026-10-21

### Added

- 管理面板管理员可对在线且支持远程升级的 NPC 下发升级指令；客户端在后台下载并校验 GitHub Release 后替换自身并自动重启。
- 升级操作遵循 NPC 服务账户权限，不尝试提权；安装目录不可写或下载校验失败时会记录错误并保持当前进程运行。

### Changed

- 首次启用远程升级要求客户端至少手动升级到 v1.1.23，旧客户端无法识别服务端升级信号。

## [1.1.22] - 2026-10-15

### Release

- 修复 Bridge 连接、代理认证和更新包校验等兼容性问题。

## [1.1.21] - 2026-10-12

### Fixed

- Fixed CAPTCHA routes when the Web panel is mounted under `web_base_url` behind a reverse proxy.
- Resolved relative Web panel TLS certificate paths from the NPS run directory for system-service launches.
- Made SOCKS5 UDP `BND.ADDR` prefer the public listener address used by the client, with validated HTTPS fallback IP discovery.
- Made the server updater restart an installed NPS service after a successful binary replacement.

## [1.1.20] - 2026-09-22

### Changed

- Persisted resource-topology shortcut keys as authenticated encrypted ciphertext so authorized accounts can view and reopen them after refreshing the console.
- Moved administrator API token generation, rotation, and revocation controls into the API Token status column; the Actions column now contains only account state, edit, and delete controls.
- Narrowed user-list client and tunnel count columns for compact three-digit values.

## [1.1.19] - 2026-09-22

### Added

- Added self-service ordinary-user API token generation, rotation, status, and revocation.
- Added administrator token status/revocation controls without exposing token plaintext.
- Added scoped, read-only resource topology shortcut keys for administrators and ordinary users.
- Documented API integration and unattended dashboard access in the Web API guides.

## [1.1.18] - 2026-09-21

### Added

- Added a configurable resource-topology refresh interval for administrator and user dashboards. The default is 60 seconds, with a browser-local setting from 5 to 3600 seconds.

## [1.1.17] - 2026-09-21

### Fixed

- Added retries for Linux GUI release dependency downloads and Wails builds so transient network failures do not skip the formal Release.
- Synchronized GUI installer metadata to `1.1.17`.

## [1.1.16] - 2026-09-21

### Added

- Added administrator operation audit logs for login, user, client, tunnel, Host, and global configuration changes. The local JSONL journal excludes passwords, API tokens, certificate keys, and proxied request bodies.
- Added scoped per-user API tokens with Bearer authentication. Tokens are shown only once and become invalid when the user is disabled or expired.
- Added Host path rewriting, response-header changes, redirects, automatic CORS, and compatibility mode.

### Fixed

- Protected HTTP framing headers while rewriting raw Host responses so response-body chunking cannot be corrupted by optional header rules.

## [1.1.15] - 2026-09-20

### Fixed

- Fixed MUX remote-close handling that discarded queued trailing frames. HTTP/1.0 Host responses without `Content-Length`, chunked responses, and TCP streams now drain buffered data before EOF.
- Added a bounded 30-second cleanup for peer-closed streams that are never read; normal streams are released immediately after their queued data is consumed.

## [1.1.14] - 2026-09-20

### Changed

- Rebuilt the resource topology page from the supplied user-dashboard prototype while retaining live, account-scoped NPS data.
- The dashboard now opens the topology page in a separate tab; the page keeps global authentication and removes user switching.

## [1.1.13] - 2026-09-20

### Added

- Added a standalone, account-scoped resource topology view for clients, tunnels, and domain hosts.
- The dashboard now links to the live view, which refreshes operational data without persisting metric snapshots.

### Changed

- Direct topology access reuses management-console authentication and never returns credentials, certificate paths, or per-resource authentication settings.

## [1.1.12] - 2026-09-19

### Changed

- Corrected directional traffic and quota accounting across proxy modes.
- Clarified tunnel configuration/listener/client states and added active TCP connection counts.
- Live traffic inspection now shows complete request and response headers for authorized management users.

## [1.1.11] - 2026-09-19

### 实时请求调试

- 隧道和域名规则新增临时实时调试面板，显示来源地址、监听地址、实际后端、请求与响应元数据。
- HTTP(S) 域名代理在 NPS 可见明文时支持实时文本请求体和响应体预览，JSON 自动格式化；文件和二进制内容不显示正文。
- 调试采样采用有界内存队列与非阻塞发布策略，面板消费慢或断开不会等待、阻塞或影响真实代理转发。

## [1.1.10] - 2026-09-10

- 修复 Linux NPC 注册 systemd/SysV 系统服务时引用临时解压路径的问题。
- 防止从 `/usr/bin/npc` 重复安装时自覆盖二进制。
- 统一命令行和交互式服务注册的 Linux 服务模板与错误处理。

## [1.1.9] - 2026-09-09

### Host 反向代理稳定性

- 修复客户端请求使用 `Connection: close` 时，Host 反向代理未等待后端响应完成就关闭连接，导致分块或压缩响应被截断。
- 增加分块响应终止块和客户端半关闭连接的回归测试，保留有限响应与 SSE 的原有清理行为。

## [1.1.8] - 2026-09-04

### 用户资源与有效期

- 管理员可为用户设置最大客户端数量；普通用户可在配额内自助创建客户端，名下隧道继续共享用户隧道配额。
- 客户端到期时间不得晚于所属用户到期时间；留空时跟随用户，用户未设置到期时间时客户端可独立设置或永不过期。
- 客户端新增/编辑支持 Basic 认证、压缩、加密和访问控制，编辑保存统一经过配额与有效期校验。

### 控制台与品牌

- 统一客户端、用户和隧道表单的到期控件、配额提示及浏览器自动填充策略，透明 NPS Logo 接入登录页、控制台和文档站。
- 后台顶栏显示当前服务端版本，并在检测到更高的正式 Release 时提示更新。

## [1.1.7] - 2026-09-03

### 稳定性修复

- 修复控制台快速连续切换菜单时的导航竞态，始终以最新请求更新内容，避免地址栏与页面错位。
- 新增和编辑隧道在写入配置或停止运行实例前严格校验代理模式，拒绝未知模式污染持久化数据。
- 修复通用新增隧道页面切换模式后返回链接仍指向旧列表的问题。

## [1.1.6] - 2026-09-02

### 发布整理

- 同步控制台 UI、平台泛域名与证书热更新改进，统一 NPS、NPC 和 GUI 发布元数据，准备 GitHub Release 与 Docker 版本标签发布。
- 控制台运行时移除 Bootstrap、Bootstrap Table、Popper 和 DataTables 静态资源，表格改用 ZUI 主题下的原生 NPS 表格适配器，保留原有服务端分页、排序与筛选接口。

## [1.1.5] - 2026-09-02

### 仪表盘与流式代理

- 仪表盘按管理员/普通用户权限隔离客户端、隧道、Host、流量和配额，新增客户端/隧道运行状态、NPS 代理速率、配额与健康告警、最后更新时间和 15 秒局部刷新。
- 修正当前代理连接、内存百分比和资源状态图表，修复菜单切换及全屏加载闪烁。
- Host SSE、长连接与 WebSocket 双向转发增加持续输出、客户端中断和资源释放回归保护。

### 控制台表单与平台域名

- 新增/编辑页统一为清新蓝色 ZUI 响应式表单，桌面端双列、移动端单列，长字段全宽并消除横向滚动。
- 平台泛域名的证书和私钥可同时留空，留空时明确作为仅 HTTP 域名；编辑隧道和 Host 时客户端名称与 ID 只读，服务端忽略篡改的客户端 ID。

## [1.1.4] - 2026-09-02

### 域名与证书

- 全局参数新增平台泛域名池：管理员可维护多个 `*.example.com` 及各自的证书、私钥路径，配置继续保存到 `conf/global.json`。
- 域名规则支持“自定义域名 / 平台域名”两种来源。平台子域名默认生成可修改的 8 位前缀，服务端强制校验前缀、唯一性、证书绑定和权限，旧 Host 保持兼容。
- 平台泛域名被 Host 使用后禁止删除或改名；更新证书路径会同步关联 Host，普通用户不会看到服务器证书路径。
- TLS 证书文件支持续期脚本原子替换：新连接自动采用有效的新证书；读取失败、文件不完整或证书私钥不匹配时保留最近一次有效证书，不重启服务也不主动断开已有连接。

### 管理面板与诊断

- 域名列表按协议展示 HTTP / HTTPS 访问链接，并新增规则诊断入口，可查看 Host、路径、协议命中的规则和选择的目标或不匹配原因。
- 客户端、隧道、用户、域名和全局参数的新增/编辑表单统一为响应式双列布局，长字段全宽，移动端收为单列。

### 文档与发布

- 新增平台泛域名、DNS 泛解析、证书托管/热更新和规则诊断文档，更新域名路由、全局参数、配置存储及功能索引。
- NPS/NPC/GUI 版本统一升级到 `1.1.4`，同步 macOS、Windows 和 Linux GUI 打包元数据。

## [1.1.3] - 2026-09-02

### Bridge 协议与兼容性

- 增加 Bridge 握手超时、HTTP 正向代理 CONNECT 鉴权、SOCKS5 认证协商、客户端本地地址上报和更新包校验逻辑。
- 保留多用户、客户端到期、流量统计和现代化管理面板，并兼容历史客户端传输配置。

### Web 管理面板

- 统一为面向运维的控制台视觉体系，支持浅色/深色主题、窄屏响应式布局、简体中文/English 和键盘可访问交互。
- 默认语言改为简体中文；登录页和管理页均可切换中英文。
- 修复 Bootstrap 下拉菜单缺少 Popper.js 导致语言菜单无法打开的问题。
- 客户端新增/编辑页恢复 `compress`（压缩）和 `crypt`（加密）传输选项。
- 补齐列表加载、空数据、错误提示、表单校验和操作反馈，并统一转义动态详情内容。

### 运行时可靠性与安全

- 客户端连接数配额改为原子预留，流量计量提供一致快照；HTTP、UDP、WebSocket 代理补强并发关闭和连接生命周期处理。
- 修复日志路径变量遮蔽，收紧配置、日志和安装目录权限，发布模板移除固定弱凭据；首次启动会轮换已知 `CHANGE_ME`/历史弱默认值。
- 随机凭据改用系统 CSPRNG，IP 白名单授权接口限制为同源访问并移除敏感凭据日志。

### 兼容性

- NPC 配置文件兼容历史别名：`server` 等价于 `server_addr`，`tp` 等价于 `conn_type`。

### 发布与更新

- 正式 Release 改为在全部构建完成后统一上传，并附带 SHA-256 校验文件；普通 `master` 构建保留为 14 天 Actions artifacts。
- CLI 和 GUI 更新器统一从 `ZiDuNet/nps` 获取严格的 `vX.Y.Z` Release，拒绝历史 `master` 等非版本标签，按发布资产下载并校验 SHA-256。
- 补齐 Linux ARM 通用包、Windows ARM64 CLI 包和 macOS `.app` 整包更新，Docker 镜像增加不可变提交标签。

## [1.1.2] - 2026-06-23

### 修复

- 修复现代化样式覆盖隧道新增/编辑页字段显隐的问题。
- 隧道表单按 TCP/UDP、HTTP 代理/SOCKS5、私密代理/P2P 和文件访问类型展示相应字段。
- 启动时同步嵌入模板，避免二进制升级后仍使用旧 `web/views` 文件。

### 构建

- 新增 `.dockerignore`，减少 Docker build context，排除 `.git`、`node_modules`、本地构建产物等无关文件。
- 优化 Dockerfile 分层，先缓存 `go.mod` / `go.sum` 依赖，再复制源码构建。
- Docker 多平台镜像改用 Go 交叉编译和 GitHub Actions 缓存，减少 QEMU 模拟构建开销。

## [1.1.1] - 2026-06-15

### 新增

- 新增独立用户体系，普通用户数据保存到 `conf/users.json`。
- 支持一个用户管理多个客户端。
- 支持用户级最大隧道数限制，普通隧道和 Host 域名规则统一计数。
- 支持用户到期自动停用，并断开/清理名下客户端运行资源。
- 新增用户管理文档、升级迁移文档和构建发布文档。

### 兼容

- 启动时自动将旧客户端 `WebUserName` / `WebPassword` 迁移为普通用户。
- 同名同密码合并，同名不同密码生成 `用户名_客户端ID`。
- 保留旧客户端登录方式兼容。

### 修复

- 修复 GUI 前端 `package.json` 带 BOM 导致 GitHub Actions 中 `yarn install --frozen-lockfile` 失败的问题。
- 清理 `wails.json` BOM 隐患。
- GUI 前端构建命令统一改为 Yarn，并在 CI 中固定 Yarn 1.22.22。

### 文档

- 重写快速开始、服务端配置、客户端配置、隧道模式、Docker、GUI、Web API 等核心文档。
- 更新 README 和 Docker Hub 说明。

## [1.1.0] - 2026-06-10

### 新增

- Web UI 现代化改造，支持明暗主题切换。
- 支持客户端到期时间。
- NPC 菜单新增更新客户端选项。
- NPS/NPC 菜单显示当前版本号。

### 修复

- 修复 HTTP/HTTPS/SOCKS5/P2P 代理连接资源释放问题。
- 修复 mux 连接池 goroutine 和内存泄漏问题。
- 修复登录验证码绕过问题。
- 修复客户端列表网速和连接数显示问题。
- 修复限速零值导致永久阻塞问题。

## [1.0.0] - 2026-05

- 设置 NPS `0.26.10` 核心版本基线。
- 修复流量统计。
- 重设计 Web UI。
- 新增 Docker Compose。

## [0.26.10] - 2024

- 基于原版 NPS 原始基线，当前项目仓库：[ZiDuNet/nps](https://github.com/ZiDuNet/nps)。
