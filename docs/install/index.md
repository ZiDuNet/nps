# 安装与部署

NPS 分为服务端 `nps`、命令行客户端 `npc` 和桌面 GUI 客户端。服务端部署在具有公网可达地址的主机上，客户端部署在需要暴露服务的内网设备上。

本页保留上游的快速上手结构；关于端口规划、持久化、系统服务和回滚的完整细节，请继续阅读[完整部署参考](../install.md)。

## 程序安装

从 [GitHub Releases](https://github.com/ZiDuNet/nps/releases) 下载与操作系统、CPU 架构匹配的包。服务端和客户端是独立发布包。

### 服务端

首次启动 `nps` 时，程序会在配置目录自动生成 `conf/nps.conf`，并生成 `web_password`、`auth_key` 和 `auth_crypt_key`。请从启动日志或配置文件中保存 Web 管理密码。

```shell
./nps
```

如需使用交互式服务管理菜单：

```shell
./nps -server
```

### 客户端

客户端可以直接双击运行，或使用 Web 面板中复制的连接参数启动：

```shell
./npc -server=<NPS 地址>:8024 -vkey=<VerifyKey>
```

需要 TLS Bridge 时改连 TLS 端口：

```shell
./npc -server=<NPS 地址>:8025 -vkey=<VerifyKey> -tls_enable=true
```

### GUI 客户端

桌面用户可下载 NPC GUI，粘贴 Web 后台生成的【快捷启动命令】，或手动填写服务端地址和 `VerifyKey`。GUI 使用、日志位置和自动更新见 [NPC GUI 客户端](../gui.md)。

## Docker 安装

服务端容器必须持久化 `/conf`，它保存 `nps.conf`、客户端、隧道、域名和用户数据。以下示例使用当前项目镜像：

```shell
docker pull wushuo98/nps:latest

docker run -d \
  --restart=unless-stopped \
  --name nps \
  -p 8024:8024 \
  -p 8025:8025 \
  -p 80:80 \
  -p 443:443 \
  -p 127.0.0.1:8081:8081 \
  -v /opt/nps/conf:/conf \
  wushuo98/nps:latest
```

仅发布实际使用的端口。管理面板示例仅绑定到宿主机回环地址；远程管理应通过 HTTPS 反向代理、VPN 或受控管理网段访问。

客户端容器示例：

```shell
docker run -d \
  --restart=unless-stopped \
  --name npc \
  wushuo98/npc:latest \
  -server=<NPS 地址>:8024 \
  -vkey=<VerifyKey>
```

容器内的 `127.0.0.1` 指向容器自身。目标服务运行在 Docker 宿主机时，请根据网络模式选择 `--network host` 或宿主机可达地址。更多 Compose、升级和多架构镜像说明见 [Docker 部署](../docker.md)。

## 宝塔面板部署

宝塔面板可通过 Docker 应用管理 NPS。安装后请确认：

1. `conf/` 使用持久化目录或卷挂载。
2. Bridge、HTTP(S) 和需要的隧道端口均已在云安全组和主机防火墙放行。
3. Web 面板不要直接暴露到公网；优先通过 HTTPS 反向代理访问。
4. 80/443 被其他服务占用时，调整 `http_proxy_port`、`https_proxy_port`，并在访问地址中包含非标准端口。

### 安装宝塔面板

从 [宝塔官网](https://www.bt.cn/new/download.html) 获取与服务器系统匹配的安装方式，完成初始化后在面板中安装 Docker / Docker Compose。面板本身应使用强密码、双因素认证和受限来源访问。

### 在宝塔中部署 NPS

在 Docker 应用中创建 NPS 服务，选择 `wushuo98/nps:latest` 或指定的版本标签，并将容器 `/conf` 挂载到持久化目录。根据实际需求发布 Bridge、HTTP(S)、隧道和管理端口；不要默认把全部端口暴露到公网。

### 在宝塔中部署 NPC

在内网机器或其 Docker 环境中创建 NPC 服务，填写 NPS Bridge 地址和客户端 `VerifyKey`。NPC 能否访问目标服务取决于它自身的网络命名空间，容器中的 `127.0.0.1` 不是宿主机回环地址。

详细操作与截图见 [宝塔面板部署](../bt.md)。

## 源码安装

项目的 `go.mod` 定义了所需的 Go 版本。克隆仓库后可直接构建服务端和客户端：

```shell
git clone https://github.com/ZiDuNet/nps.git
cd nps
go build -o nps ./cmd/nps/nps.go
go build -o npc ./cmd/npc/npc.go
```

也可使用：

```shell
make build
```

## 下一步

1. 阅读[快速开始](../start.md)，创建客户端并完成第一条 TCP 隧道。
2. 阅读[隧道模式](../tunnel.md)，选择 TCP、UDP、HTTP(S)、SOCKS5、Secret、P2P 或文件访问。
3. 阅读[服务端配置文件](../server/server_config.md)，按实际网络环境调整监听地址和安全限制。
4. 需要配置文件模式时，阅读[NPC 配置文件参考](../client/config-file.md)。
