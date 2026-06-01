# FAQ

## 服务端无法启动

服务端默认使用 8024、8080、80、443 端口，如果端口冲突请修改 `conf/nps.conf` 中的配置。

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
- 首次启动查看终端输出的随机密码
- 尝试 `http://127.0.0.1:8080` 本地访问

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
