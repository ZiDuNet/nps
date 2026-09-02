# FAQ

## 服务端无法启动

服务端默认使用 8024、8025、8081、80、443 端口，如果端口冲突请修改 `conf/nps.conf` 中的配置。

## 客户端无法连接服务端

- 检查所有端口是否在安全组/防火墙中放行
- 检查 vkey 是否正确
- 检查客户端与服务端版本是否兼容

## 服务端配置文件修改无效

使用 `install` 安装后：
- Linux 配置文件在 `/etc/nps/conf/nps.conf`
- Windows 配置文件在 `C:\Program Files\nps\conf\nps.conf`

## Web 面板访问不了

- 检查 `web_port` 端口是否放行
- 如果是首次生成配置，查看终端输出的随机密码；已有 `conf/nps.conf` 时以文件中的 `web_username` 和 `web_password` 为准
- 尝试 `http://127.0.0.1:8081` 本地访问

如果页面能打开但语言菜单或下拉框没有反应，确认页面已加载 ZUI 脚本和 `static/js/language.js`，然后重启 NPS 让嵌入式静态资源同步，再清理浏览器缓存。

## `nps install` 还能用吗？

能用。`nps install`、`nps start`、`nps stop`、`nps restart`、`nps uninstall` 是非交互命令，适合脚本化部署。

`nps -server` 是交互菜单入口，不是安装命令本身。菜单里也可以选择安装、删除/卸载、更新、查看状态、启动、停止和重启。NPS 服务端只有一个系统服务，所以用「查看状态」即可，不需要服务列表。

NPC 也一样。`npc install -server=<服务器IP>:8024 -vkey=<VerifyKey>` 仍然是可用的非交互安装命令；直接运行 `npc` 进入的是客户端交互菜单。客户端交互菜单支持注册服务、删除/卸载服务、启动服务、停止服务、更新客户端、查看已注册服务列表和查看指定服务状态。

## SOCKS5 账号密码在哪里设置？

SOCKS5 账号密码在「客户端」的认证配置里设置，不在「新增 SOCKS5 隧道」页面设置。

路径：

```text
客户端 -> 新增/编辑客户端 -> 认证配置 -> Basic 认证用户名 / Basic 认证密码
```

如果这两个字段为空，SOCKS5 会允许无认证连接。公网开放 SOCKS5 时，建议务必设置账号密码。

## P2P 穿透失败

双方 NAT 类型都是 Symmetric NAT 时无法成功。建议先检测 NAT 类型：

```bash
./npc nat -stun_addr=stun.stunprotocol.org:3478
```

## Windows 客户端闪退

使用 CMD 运行而非直接双击，以便查看错误信息：

```cmd
npc.exe -server=ip:8024 -vkey=your-key
```
