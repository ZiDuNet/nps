# 更新日志

所有重要变更记录在此。

## [1.1.2] - 2026-06-23

### 修复

- 修复现代化样式覆盖隧道新增/编辑页字段显隐的问题。
- 对齐上游不同隧道类型字段：TCP、UDP、HTTP 代理、SOCKS5、私密代理、P2P、文件访问分别展示对应配置项。
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

- 基于上游 nps 0.26.10 建立二次开发基线。
- 修复流量统计。
- 重设计 Web UI。
- 新增 Docker Compose。

## [0.26.10] - 2024

- 上游 [ehang-io/nps](https://github.com/ehang-io/nps) 原始基线。
