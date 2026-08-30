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
- Android APK 打包
- Docker 多架构镜像构建与推送
- Windows/macOS/Linux GUI 客户端打包
- Release 附件上传

`.github/workflows/ci.yml` 在 `master` 分支提交和 Pull Request 上执行服务端/客户端构建、核心 Go 包测试，以及 GUI 前端的冻结依赖安装和构建。

推送 `v*` 标签会自动触发发布，也可以在 Actions 页面手动执行 `workflow_dispatch`；从普通分支手动发布时必须填写 `release_tag`（例如 `v1.1.2`）。Docker 镜像发布需要配置 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN` secrets。

GUI 构建由 Wails 3 调用 `wails.json` 中配置的 Yarn 前端流程：

```bash
wails3 build --tags npcgui
```

注意：`package.json` 和 `wails.json` 必须是无 BOM 的 UTF-8，否则 Yarn 或 Wails 在 CI 中可能报 `Invalid package.json` 或 JSON 解析失败。
