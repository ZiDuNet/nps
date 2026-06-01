# 快速启动

## 服务端

### 直接运行

```bash
./nps
```

启动后在终端输出随机生成的管理员用户名和密码，访问 `http://ip:8080` 进入管理面板。

### 交互式服务管理

```bash
./nps -server
```

支持安装/卸载系统服务（Linux 需要 sudo，Windows 需要管理员权限）。

### 安装为系统服务

**Linux/macOS:**
```bash
sudo ./nps install
sudo nps start
```

**Windows（管理员 CMD）:**
```cmd
nps.exe install
nps.exe start
```

安装后配置文件位置：
- Windows: `C:\Program Files\nps`
- Linux/macOS: `/etc/nps`

### 默认端口

| 端口 | 用途 |
|------|------|
| 80 | HTTP 代理 |
| 443 | HTTPS 代理 |
| 8024 | Bridge TCP（客户端连接） |
| 8025 | Bridge TLS（加密连接） |
| 8080 | Web 管理面板 |

## 客户端

### 无配置文件模式（推荐）

所有配置在服务端 Web 面板管理，客户端只需一条命令：

```bash
./npc -server=your-ip:8024 -vkey=your-key
```

TLS 加密模式：
```bash
./npc -server=your-ip:8025 -vkey=your-key -tls_enable=true
```

### 交互式启动

```bash
./npc
```

直接双击运行（Windows），按提示输入快捷启动命令即可。

### 快捷启动命令

在 Web 面板的客户端列表中，展开客户端详情可看到快捷启动命令（Base64 编码），直接复制运行即可一键启动。

## 版本检查

```bash
./nps -version
./npc -version
```
