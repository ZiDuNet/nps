# 客户端使用

## 直接运行

直接双击 `npc`/`npc.exe`，无任何参数，按提示输入即可：

| 菜单项 | 输入内容 |
| --- | --- |
| 注册系统服务 | 【快捷启动命令】或【TLS 快捷启动命令】|
| 卸载系统服务 |  隧道密钥 `vkey` |
| 启动 |  隧道密钥 `vkey` |
| 停止 |  隧道密钥 `vkey` |
| 更新客户端 | — |
| 查看 NPC 服务日志 | 选择服务名或 `vkey`，按 `Ctrl+C` 返回 |
| 重启所有 NPC 服务 | 输入 `y` 确认；会短暂中断所有已注册隧道 |

【快捷启动命令】在 web 后台 → 客户端列表 → 客户端前的小图标里复制，本质是 `服务端地址 + vkey` 的 base64 编码。TLS 模式有独立的【TLS 快捷启动命令】。

服务以 `nps-client-<vkey>` 命名注册，可在同一台机器并存多个客户端实例。

![image](/image/new/cmd.png)

## 直接命令行

```shell
./npc -server=ip:port -vkey=web界面中显示的密钥         # 标准
./npc -server=ip:8025 -vkey=xxx -tls_enable=true       # TLS 桥接
./npc -server=ip:8024 -vkey=ytkpyr0er676m0r7,iwnbjfbvygvzyzzt  # 同时拉起多个隧道 ID（逗号拼接）
```

## 配置文件模式

无配置文件模式适合由 Web 管理面板统一维护客户端、隧道和域名规则；配置文件模式则适合批量部署、将规则纳入基础设施代码，或使用健康检查、端口范围映射等进阶能力。

```shell
./npc -config=conf/npc.conf
```

一个配置文件使用 `[common]` 定义 `server_addr`、`vkey`、TLS 和日志等公共参数，再用多个规则节定义 TCP、UDP、Host、Secret、P2P 或文件访问。完整字段、模板环境变量和可运行示例见 [NPC 配置文件参考](config-file.md)。


## GUI 客户端

> 基于 Wails 开发的桌面 GUI，体积只有 5M，免安装，解压即用。

支持两种添加客户端方式：

1. 粘贴 web 后台的【快捷启动命令】
2. 手动填写 `-server`、`-vkey` 等参数

详细说明请参考 [GUI 客户端 README](https://github.com/ZiDuNet/nps/blob/master/cmd/npc/npc-gui/README.md)。

![img](/image/new/gui.png)


## 注册到系统服务
> 实现开机启动、守护进程

```shell
./npc          # linux/darwin（需 sudo）
npc.exe        # windows（管理员 cmd）
```

按菜单选「注册系统服务」并粘贴【快捷启动命令】或【TLS 快捷启动命令】即可，服务名为 `nps-client-<vkey>`。

> Windows 如果需要当客户端退出时自动重启客户端，请按照如图所示配置

![img](/image/windows_client_service_configuration.png)


## 系统服务日志
注册系统服务后日志按 **每个 vkey 一个文件** 保存，命名为 `npc-<vkey>.log`：

- windows：与 `npc.exe` 同级目录
- linux / darwin：`/var/log/`

日志文件默认权限为 `0644`，普通用户可以使用菜单的日志查看功能。升级 NPC 后请至少重启一次对应系统服务，使旧日志文件应用新的权限；如果仍提示 `permission denied`，请使用 root/管理员运行菜单，或检查 `/var/log` 的目录权限。


## 客户端更新
> 运行客户端，会显示当前客户端版本号。
```shell
./npc          # 选择菜单 [5] 更新客户端
./npc update   # 非交互更新命令
```

自动从 GitHub 最新正式 Release 下载当前平台的 `npc` 压缩包，校验 `checksums.txt` 后替换二进制。菜单更新完成后需要重新启动对应客户端服务；服务端下发的升级指令会在校验成功后自动重启客户端。

更新需要对当前 NPC 安装目录和可执行文件具有写权限。Linux/macOS 的系统服务通常由 root 运行，建议使用 `sudo` 执行手动更新，或让服务账户具备安装目录写权限；Windows 请使用管理员终端。GitHub API 返回 `403/429` 时表示触发了接口限流，稍后重试即可。

如果无法成功更新，可以直接下载 releases 压缩包覆盖原有的 `npc` 二进制。

### 服务端下发升级

管理员可以在 Web 面板的「客户端」列表中点击升级图标，向在线客户端发送升级指令。只有启用、在线且版本不低于 `v1.1.23` 的客户端支持该功能；旧版本需要先在客户端主机手动更新一次。客户端会在后台下载并校验 Release，不会阻塞隧道转发；下载完成后自动重启，服务管理器会继续守护服务。
