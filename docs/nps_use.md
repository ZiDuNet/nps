# 服务端使用

## Web 管理

新安装默认访问 `127.0.0.1:8081`。如需远程管理，请使用 HTTPS 反向代理或将 `web_ip` 限制为受控管理网段并开启 `web_open_ssl`。首次启动时管理员账号为 `admin`，终端会打印随机生成的管理员密码。

## 配置重载

```bash
# Linux/macOS
sudo nps reload
```

`reload` 仅在 Linux/macOS 上可用，会重新加载认证和管理面板配置，例如 `auth_key`、`auth_crypt_key`、`web_username`、`web_password`。Windows 以及监听端口、Bridge、代理和隧道配置变更请执行 `nps restart`；生产环境变更前建议先备份 `conf` 目录。

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
