# 快速开始

## 1. 安装服务端

### Docker 部署（推荐）

```bash
# 创建目录
mkdir -p /opt/nps/conf

# 创建 docker-compose.yml
cat > /opt/nps/docker-compose.yml << 'EOF'
version: '3.8'
services:
  nps:
    image: wushuo98/nps:latest
    container_name: nps
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "8024:8024"
      - "8025:8025"
      - "8080:8080"
    volumes:
      - ./conf:/conf
EOF

# 启动
cd /opt/nps && docker compose up -d

# 查看初始密码
docker logs nps | head -20
```

### 二进制安装

从 [Releases](https://github.com/ZiDuNet/nps/releases) 下载对应平台版本。

```bash
# 直接运行
./nps

# 或安装为系统服务
./nps -server    # 交互式引导安装
```

首次启动自动生成随机密码并打印到终端。

## 2. 访问管理面板

浏览器打开 `http://服务器IP:8080`，输入终端显示的用户名和密码。

## 3. 创建客户端

在管理面板点击「客户端」→「新增」，填写备注即可，系统自动生成连接密钥。

## 4. 启动客户端

### 方式一：无配置文件模式（推荐）

所有配置在服务端管理，客户端只需一条命令：

```bash
./npc -server=你的IP:8024 -vkey=你的密钥
```

TLS 加密：
```bash
./npc -server=你的IP:8025 -vkey=你的密钥 -tls_enable=true
```

### 方式二：交互式启动

```bash
./npc
# 或 Windows 下直接双击 npc.exe
```

按提示输入快捷启动命令即可。

### 方式三：Docker

```bash
docker run -d --name npc \
  wushuo98/npc \
  -server=你的IP:8024 -vkey=你的密钥
```

## 5. 添加隧道

客户端连接后，在 Web 面板添加穿透隧道。例如添加 TCP 隧道：

1. 点击对应客户端的「隧道」
2. 选择「TCP 隧道」
3. 填写服务端端口和内网目标地址
4. 保存即可生效

## 默认端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 代理 |
| 443 | HTTPS 代理 |
| 8024 | Bridge TCP（客户端连接） |
| 8025 | Bridge TLS（加密连接） |
| 8080 | Web 管理面板 |

## 注册为系统服务

**Linux/macOS:**
```bash
sudo ./nps install    # 服务端
sudo ./npc install -server=ip:8024 -vkey=key  # 客户端
sudo nps start        # 启动服务端
sudo npc start        # 启动客户端
```

**Windows（管理员 CMD）:**
```cmd
nps.exe install
npc.exe install -server=ip:8024 -vkey=key
nps.exe start
npc.exe start
```
