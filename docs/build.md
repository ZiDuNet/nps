# 构建发布

本项目包含服务端 `nps`、命令行客户端 `npc`、GUI 客户端和 Docker 镜像。

## 环境要求

- Go 1.24+
- Node.js 20+
- Yarn 1.22.22（GUI 前端）
- Wails v2.11.0（GUI 客户端）
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

Wails 构建：

```bash
cd cmd/npc/npc-gui
wails build -m -s -trimpath -skipbindings -platform windows/amd64
```

`wails.json` 已配置为使用 Yarn。不要混用 npm 生成 `package-lock.json`。

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

GUI 前端阶段固定使用：

```bash
yarn install --frozen-lockfile
yarn build
```

注意：`package.json` 和 `wails.json` 必须是无 BOM 的 UTF-8，否则 Yarn 或 Wails 在 CI 中可能报 `Invalid package.json` 或 JSON 解析失败。
