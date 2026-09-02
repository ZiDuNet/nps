# 构建发布

本项目包含服务端 `nps`、命令行客户端 `npc`、GUI 客户端和 Docker 镜像。

## 环境要求

- Go 1.24+
- Node.js 20+
- Yarn 1.22.22（GUI 前端）
- Wails v3.0.0-beta.12（GUI 客户端，需与 `cmd/npc/npc-gui/go.mod` 一致）
- Docker Buildx（Docker 多架构镜像）

## 本地构建

服务端：

```bash
go build ./cmd/nps/nps.go
```

客户端：

```bash
go build ./cmd/npc/npc.go
```

Makefile：

```bash
make build
make test
make lint
make ci
```

## 交叉编译

Linux amd64 服务端：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" ./cmd/nps/nps.go
```

Windows amd64 客户端：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-s -w" ./cmd/npc/npc.go
```

## GUI 客户端

前端依赖：

```bash
cd cmd/npc/npc-gui/frontend
corepack enable
corepack prepare yarn@1.22.22 --activate
yarn install --frozen-lockfile
yarn build
```

Wails 3 构建（Windows/macOS）：

```bash
cd cmd/npc/npc-gui
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.12
wails3 build --tags npcgui
```

`wails.json` 已配置为使用 Yarn。不要混用 npm 生成 `package-lock.json`。

Linux GUI 还需要 GTK 4、WebKitGTK 6、libsoup 3 等桌面依赖，并使用 `webkit2_41` 标签：

```bash
sudo apt-get install build-essential libgtk-4-dev libwebkitgtk-6.0-dev \
  libsoup-3.0-dev libjavascriptcoregtk-6.0-dev \
  libayatana-appindicator3-dev librsvg2-dev patchelf pkg-config
GOOS=linux GOARCH=amd64 WAILS_TAGS=webkit2_41 wails3 build --tags npcgui
```

## Docker 镜像

服务端：

```bash
docker build -f Dockerfile.nps -t nps .
```

客户端：

```bash
docker build -f Dockerfile.npc -t npc .
```

## GitHub Actions 发布

`.github/workflows/release.yml` 包含：

- 跨平台 `nps` / `npc` 二进制打包
- `config-examples.tar.gz` 配置示例包（不含运行数据、证书和私钥）
- Android APK 打包
- Docker 多架构镜像构建与推送
- Windows/macOS/Linux GUI 客户端打包
- Release 附件上传

`.github/workflows/ci.yml` 在 `master` 分支提交和 Pull Request 上执行服务端/客户端构建、核心 Go 包测试，以及 GUI 前端的冻结依赖安装和构建。文档站使用 `docs/package-lock.json` 与 `npm ci` 构建，确保 Pages 部署可复现。

每次推送到 `master` 都会执行 CI 和独立的 Docker 工作流。Docker 工作流只构建并推送多架构 `latest` 与不可变的 `sha-<短提交号>` 标签，并会取消同一分支上较旧的镜像构建；master 提交不会生成正式 GitHub Release，也不会产生供软件更新器读取的版本资产。

推送与源码版本严格一致的标签（例如源码为 `1.1.7` 时推送 `v1.1.7`）才会运行 Release 工作流并创建 GitHub Release。正式标签必须指向当前 `master` 提交，工作流会在构建前校验版本元数据和核心 Go 测试；CLI/GUI 自动更新仍只读取经过完整校验的 Release，版本标签、平台资产名称和 `checksums.txt` 格式保持不变。所有平台产物完成后一次性上传 CLI、Android、GUI 和 `checksums.txt`，避免并行任务争抢同一个 Release；标签工作流只推送对应的 Docker 版本标签（如 `1.1.7`），`latest` 由 master 的 Docker 工作流唯一维护。也可以在 Actions 页面手动执行 `workflow_dispatch`，但 `release_tag` 必须与 `lib/version.VERSION` 对应（例如 `v1.1.7`），并且该标签已推送且指向当前 `master`。Docker 镜像发布需要配置 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN` secrets。

GUI 构建由 Wails 3 调用 `wails.json` 中配置的 Yarn 前端流程：

```bash
wails3 build --tags npcgui
```

注意：`package.json` 和 `wails.json` 必须是无 BOM 的 UTF-8，否则 Yarn 或 Wails 在 CI 中可能报 `Invalid package.json` 或 JSON 解析失败。
