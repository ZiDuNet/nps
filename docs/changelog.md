# 更新日志

## v1.1.0 (2026-06-10)

### 合并上游更新
- Bridge Client 并发安全（sync.Mutex 保护 signal/tunnel/file 字段）
- Mux IsClose 改为 atomic.Bool，避免竞态条件
- pmux 优雅关闭，防止 send on closed channel

### Bug 修复
- 隧道/域名解析/UDP 流量始终为 0：CopyBuffer 传入 host 参数
- JSON 持久化 panic → logs.Error，不再崩溃
- netpackager UnPack 错误时归还 buf pool，修复内存泄漏
- P2P UDP 连接添加 30 秒超时，避免无限等待
- SOCKS5 doConnect/handleUDP 用 io.ReadFull 替代 c.Read
- HTTPS ClientHello recordLen 添加 16384 上限检查

### 新功能
- 客户端到期时间：支持设置 ExpireTime，定时检查自动暂停
- NPC 菜单新增"更新客户端"选项
- NPS/NPC 菜单显示当前版本号
- 更新前自动检查版本，已是最新则跳过

### 其他
- rand.Seed → rand.New(rand.NewSource)
- ioutil.WriteFile → os.WriteFile
- GetTunnel 性能优化（单次 Range 遍历）
- Dashboard IO 速率改为后台缓存采集，不再 Sleep 500ms
- GenerateServerPort 添加重试上限
- CI/CD：修复 Go 版本、更新 actions 版本、Docker Hub 简介说明优化内容

## v1.0.0 (2026-06-02)

### Web UI 现代化改造
- 全新 modern.css 主题，支持明暗主题一键切换
- 登录页面重新设计（双栏布局）
- 侧边栏菜单分组优化
- 按钮、表格、表单、卡片、统计面板全面美化
- ECharts 图表自适应明暗主题
- 负载显示优化（保留 2 位小数）
- 仪表盘紧凑化布局

### Bug 修复（33 项）
- HTTP/HTTPS/SOCKS5/P2P 代理服务连接泄漏
- mux 连接池 goroutine 泄漏、内存泄漏
- 登录验证码绕过漏洞
- 注册密码强度校验
- 客户端列表网速/连接数不显示
- 限速零值令牌桶永久阻塞

### 安全加固
- 登录验证码绕过修复
- 注册密码最少 6 位
- 首次启动随机密码

### 其他
- 版本号升级至 1.0.0
- README 全面重写
- 新增 CLAUDE.md 项目指引
- 新增 docker-compose.yml

## v0.26.33 (2026-05-23)

- 配置文件自动生成，首次启动随机密码
- Web 静态文件嵌入可执行文件
- 修复：限速隧道中断、关闭端口仍可用、TCP 负载均衡异常、网速不显示

## v0.26.32 (2026-03-27)

- 修复客户端注册参数、HTTPS 反向代理

## v0.26.31 (2026-03-23)

- 新增域名解析开关、TCP 隧道 Basic 认证

## v0.26.30 (2026-03-12)

- 修复 HTTP WebSocket、域名端口跳转、自动 HTTPS

## v0.26.29 (2026-01-24)

- 新增 GUI 桌面客户端（Wails）

## v0.26.28 (2025-12-06)

- 全局参数新增服务地址配置
- IP 授权优化
- 限速器重构

## v0.26.27 (2025-11-05)

- 客户端 IP 白名单
- 移除压缩/加密功能

## v0.26.25 (2025-05-28)

- `nps -server` 服务管理命令
- TLS 快捷启动命令

## v0.26.24 (2025-04-16)

- 隧道复制功能

## v0.26.23 (2025-04-11)

- TCP Proxy Protocol 支持
- 协程增长优化

## v0.26.21 (2025-01-07)

- 快捷启动命令（Base64）
- 交互式客户端启动
- vkey 缩短至 10 位

## v0.26.19 (2024-06-01)

- Go 1.22
- 自动 HTTPS
- 多隧道 ID 启动

## v0.26.18 (2024-02-27)

- TLS Bridge 端口支持

## v0.26.16 (2023-06-01)

- HTTPS 流量统计修复
- 全局黑名单 IP
- 客户端在线时间
