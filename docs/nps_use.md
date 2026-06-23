# 服务端使用

## Web 管理

访问 `公网IP:Web端口`（默认 8081）。首次启动时管理员账号为 `admin`，终端会打印随机生成的管理员密码。

## 配置重载

```bash
# Linux/macOS
sudo nps reload

# Windows
nps.exe reload
```

支持部分配置热重载：`auth_key`、`auth_crypt_key`、`web_username`、`web_password` 等。

## 停止与重启

```bash
# Linux/macOS
sudo nps stop
sudo nps restart

# Windows
nps.exe stop
nps.exe restart
```

## 调试模式

如果启动失败，先停止服务再直接运行查看日志：

```bash
nps.exe stop
./nps
```

- Windows 日志：当前运行目录下
- Linux/macOS 日志：`/var/log/nps.log`
