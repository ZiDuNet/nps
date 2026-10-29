# 服务端使用

## 首次启动

新安装默认访问 `http://127.0.0.1:8081`。管理员用户名为 `admin`，
`web_password`、`auth_key` 和 `auth_crypt_key` 会在首次启动时随机生成并
打印到终端。发布模板中的 `CHANGE_ME` 和历史弱占位值也会自动轮换。

远程管理应通过 HTTPS 反向代理，或把 `web_ip` 限制为受控管理网段并开启
`web_open_ssl`。完整安全边界见[部署安全与参数速查](../server_config.md)。

## Web 管理入口

当前导航按资源类型分组：

- 客户端、用户管理、域名反代和各类隧道。
- 「系统 → 全局参数」：业务参数、用户注册、`nps.conf` 和证书设置。
- 「系统 → 登录封禁」：查看和管理登录失败封禁状态。
- 「系统 → 审计与连接」：操作审计与客户端连接日志。
- 「系统 → IP 登记」：管理员查看全部登记；普通用户只查看自己客户端下的登记。
- 「系统 → API 接入」：管理员密钥和普通用户 Token。
- 「系统 → 使用说明」：显示管理员配置的帮助内容。

「全局参数」页面包含四个 Tab：

1. 全局参数
2. 用户注册
3. 服务端配置
4. 证书设置

完整字段见[服务端配置文件参考](server_config.md)，增强功能见
[服务端增强功能](nps_extend.md)。

## 配置重载与重启

Linux/macOS 上可重新加载认证和管理面板配置：

```bash
sudo nps reload
```

`reload` 适用于 `web_username`、`web_password`、`auth_key`、
`auth_crypt_key` 等可在线读取的认证配置。Windows，以及监听端口、Bridge、
代理、证书、日志和隧道相关配置变更，应执行：

```bash
sudo nps restart
```

Windows：

```cmd
nps.exe restart
```

变更前建议先备份 `conf` 目录。

## 系统服务

```bash
./nps install
nps start
nps stop
nps restart
nps uninstall
```

Windows 使用管理员 CMD 或 PowerShell 执行 `nps.exe install/start/stop/restart`。
交互菜单：

```bash
./nps -server
```

菜单覆盖安装、卸载、启停、重启和更新。系统服务与交互菜单的具体命令见
[运行命令速查](../run.md#nps-服务端)。

## 工作目录与数据

服务端直接运行时使用程序目录下的 `conf`；Linux 系统服务默认使用
`/etc/nps/conf`。Docker 通常挂载到 `/conf`。

```text
conf/
├── nps.conf
├── clients.json
├── tasks.json
├── hosts.json
├── users.json
├── global.json
├── audit.log
└── connection.log
```

证书、日志和 Geo 数据还会在 `conf` 下创建或引用相应文件。不要只备份
`nps.conf` 而遗漏 JSON 数据和证书。

## 更新与迁移

```bash
./nps -server
```

选择“更新”或按[构建发布](../build.md)中的正式 Release 流程操作。更新程序
默认只替换二进制，不删除配置和业务数据。

迁移到新服务器时停止 NPS，完整复制 `conf` 目录，再使用相同运行权限启动。
现有 `nps.conf` 会自动补齐新版必需字段并保留已有值、注释和自定义项；升级前
仍应先备份并检查启动日志。

## 调试

启动失败时先停止系统服务，再直接运行 `./nps` 查看控制台日志：

```bash
sudo nps stop
./nps
```

注意确认当前工作目录，因为直接运行与系统服务使用的配置目录可能不同。
