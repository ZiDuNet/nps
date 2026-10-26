# NPS 1.1.31 升级说明

本文说明从 1.1.30 或更早版本升级到 1.1.31 时的行为变化和兼容边界，重点覆盖 Secret/P2P 目标类型、管理面板和 API 凭据。

## 本版本变化

- Secret/P2P 隧道的 `target_type` 从配置兼容字段升级为真实转发能力。`tcp`、`udp`、`all` 会分别只启动对应协议的连接；UDP 使用 4 字节长度前缀的数据报帧协议，在 Secret 中继和 P2P 直连上保留报文边界。
- NPC 的 P2P 访客新增 `-fallback_secret`（默认开启）。直连打洞失败时自动回退同任务的 Secret 中继，访问端本地端口保持可用；显式关闭后直连失败直接报错。
- 普通隧道新增隧道级 `max_conn`（最大并发连接）和 `flow_limit`（总流量配额，MB）。0 表示不限，旧记录缺省时同样按不限处理。
- 域名反代证书支持路径、上传、粘贴三种录入方式并提供有效性校验；平台泛域名与自定义域名使用同一套入口。
- 管理面板新增「系统 → API 接入」页面。普通用户的 Token 可以再次查看和撤销；管理员拥有 `nps.conf` 的 `auth_key` 与网页管理密钥（`npsa_` 前缀）两套等价凭据，查看原文需要验证登录密码。
- 全局参数新增 Geo 数据状态和在线更新；未配置 `geoip_path`/`geosite_path` 时默认使用运行目录 `conf/geoip.dat` 与 `conf/geosite.dat`。
- 管理面板全面移除 ZUI 运行时，统一使用本地内置 Bootstrap 5.3.3；新增/编辑表单按业务分组，高级设置默认收起，列表详情采用紧凑多列布局。

## 对旧部署的影响

- 旧 Secret/P2P 记录没有 `target_type`，继续按 `tcp` 处理；旧 NPC（1.1.28 及以下）不发送现代数据报帧，仍按原始流兼容，不需要立即升级。
- `-fallback_secret` 只影响 1.1.31 及以上的 P2P 访客。旧访客不带该参数，行为与升级前一致。
- 公共 vkey 注册流程新分配的验证密钥长度从 10 位变为 16 位。已有客户端和已分配的验证密钥不受影响；只有通过公共 vkey 新注册的客户端使用新长度。
- `quic-go` 升级到 `v0.64.1`。P2P 直连的 QUIC 传输协议本身不变，服务端和 NPC 需要一起升级到 1.1.31 才能避免旧版 provider 在 Go 1.26 环境下的握手崩溃。
- API 签名协议不变：`md5(auth_key + timestamp)` 与普通用户 Bearer Token 的既有调用方式继续有效，只是新增了网页管理密钥作为 `auth_key` 的等价替代。
- ZUI 静态资源会在服务端启动时自动清理。依赖旧前端资源路径的外部页面不受影响，管理面板本身随新版本一次性切换。

## 配置示例

配置文件模式下的 UDP P2P 任务：

```ini
[dns_udp]
mode=p2p
password=p2p-dns
target_addr=192.168.1.53:53
target_type=udp
fallback_secret=true
```

P2P 访客命令行（直连失败自动回退 Secret）：

```bash
./npc -server=nps.example.com:8024 -vkey=<VerifyKey> \
  -password=p2p-dns -target=192.168.1.53:53 \
  -target_type=udp -local_type=p2p -local_port=5300 \
  -fallback_secret=true
```

Secret 访客命令行（UDP 数据报）：

```bash
./npc -server=nps.example.com:8024 -vkey=<VerifyKey> \
  -password=p2p-dns -target_type=udp \
  -local_type=secret -local_port=5300
```

## 升级与回滚

1. 先备份 NPS 的 `conf/` 目录和 NPC 配置文件。
2. 同时替换 NPS 与 NPC 到 1.1.31；仅替换一侧时，UDP 数据报帧和 P2P 直连修复只在两端都升级后生效，其余功能仍兼容旧版本。
3. 如使用系统服务，按原服务名重启，并检查客户端版本号是否在列表中以可点击入口显示。
4. 如需回滚到 1.1.30，删除新任务中的 `target_type=udp/all`、关闭 `-fallback_secret` 即可回到旧的 TCP 行为；旧记录数据无需恢复。

正式发布版本号以 `lib/version.VERSION` 和 GitHub Release 标签为准；在线升级仍只安装经过完整校验的正式 Release。
