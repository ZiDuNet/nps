# 平台泛域名、证书热更新与规则诊断

本页说明 Web 管理面板中的平台泛域名池。它用于把管理员已经准备好的泛解析域名和证书交给多个用户创建子域名规则；NPS 仍然是 HTTP(S) 反向代理，不是 DNS 服务器。基础路由、非标准端口和本地转发说明见[域名代理与路由](domain-proxy.md)。

## 使用前提

使用 HTTPS 的平台域名要求管理员完成以下准备；如果证书和私钥都留空，则只需完成前两项并按 HTTP 使用：

1. 为 `*.example.com` 配置 A 或 AAAA 泛解析，指向 NPS 的公网地址。
2. 放行 NPS 实际监听的 HTTP/HTTPS 端口；端口不是 `80`/`443` 时，访问链接必须显式带端口。
3. （HTTPS）让运行 NPS 的进程可读取对应的证书和私钥文件。

`*.example.com` 只匹配它的直接子域名，例如 `api.example.com`；它不覆盖根域名 `example.com`，也不能用作多层前缀 `a.b.example.com`。根域名需要单独的 DNS 记录和 Host 规则。

## 管理员：维护平台域名池

进入 Web 后台的「全局参数」，在“平台泛域名”中维护一行或多行记录：

| 字段 | 要求 | 用途 |
| --- | --- | --- |
| 泛域名 | `*.example.com` 格式 | 供用户选择的子域名后缀。 |
| 证书路径 | NPS 主机中的证书文件，可留空 | 平台子域名的 TLS 证书。通常使用 `.pem` 文件。 |
| 私钥路径 | NPS 主机中的私钥文件，可留空 | 与证书匹配的私钥。通常使用 `.key` 或 `.pem` 文件。 |

配置保存到现有的 `conf/global.json` 的 `PlatformDomains` 字段，和全局黑名单、服务地址使用同一个持久化位置。请通过管理面板维护它，不要手工编辑运行中的 JSON 文件。

全局参数页会显示每项证书是否可读取、到期时间、剩余天数及当前引用它的 Host 数量。文件名不强制以 `.pem` 或 `.key` 结尾，关键条件是证书和私钥内容有效且能够配对；推荐使用这两个后缀，便于续期脚本和运维识别。证书和私钥可以同时留空，这表示该泛域名只提供 HTTP；只填写其中一个仍会被拒绝。

保存时 NPS 会校验证书文件可读、证书与私钥匹配、证书当前有效，并确认它覆盖该泛域名的直接子域名；对于同时留空的 HTTP-only 项跳过文件校验。任一项不满足时不会保存为可分配的平台域名；如果历史文件被手工改坏，普通用户也不会在下拉框中看到它，直到管理员修复配置。创建平台 Host 时，HTTP-only 项会锁定为 HTTP，不能选择 HTTPS 或 HTTP + HTTPS。

已被 Host 引用的平台泛域名不能删除或改名，以免已发布的地址被悄悄迁移到其他域名。管理员可以更新其证书和私钥路径，NPS 会把新路径同步到关联 Host；普通用户不会看到服务器文件系统路径。

示例配置的结构如下，`ID` 由系统维护：

```json
{
  "PlatformDomains": [
    {
      "ID": "系统生成的稳定标识",
      "Wildcard": "*.example.com",
      "CertFilePath": "/etc/nps/certs/example.com/fullchain.pem",
      "KeyFilePath": "/etc/nps/certs/example.com/privkey.key"
    }
  ]
}
```

## 用户：创建自定义或平台域名

在「域名」的新增或编辑页选择域名来源：

| 模式 | 用户填写内容 | 证书行为 |
| --- | --- | --- |
| 自定义域名 | 完整主机名，例如 `app.example.net`；用户自己完成 DNS 解析 | 可以填写自己的证书和私钥，或使用 TLS 透传。 |
| 平台域名 | 选择管理员提供的 `*.example.com`，再填写前缀 | 服务端自动写入并锁定该平台的证书和私钥路径；HTTP-only 项只能创建 HTTP 规则。 |

选择平台域名时，表单会生成一个可修改的 8 位字母数字前缀，例如 `a7k2m9qz`。前缀最终必须是一个合法 DNS 标签（1 到 63 位，字母、数字或连字符），并且在所有 Host 中全局唯一。浏览器的即时检查只用于提示；保存时服务端会再次检查，因此接口调用或 NPC 配置不能绕过平台域名、证书绑定和重复保护。

平台域名中的 Host 仍是一条普通域名规则，会计入已有的客户端和用户隧道数量限制，不会产生额外的“域名配额”。已有 Host 默认继续按自定义域名处理，不会因升级被改写。

域名列表会按规则的协议显示可直接打开的 HTTP、HTTPS 或两个访问链接。使用非标准入口时，链接会包含已配置的端口；仍应确认云安全组、宿主机防火墙、Docker 端口映射和前置反向代理都允许访问。

## DNS 泛解析示例

假设 NPS 公网地址为 `203.0.113.10`，平台泛域名为 `*.demo.example.com`：

```text
*.demo.example.com  A  203.0.113.10
```

用户选择该平台域名并将前缀设为 `portal` 后，访问地址是 `portal.demo.example.com`。如果 HTTP 入口配置为 `30111`，则完整地址是：

```text
http://portal.demo.example.com:30111/
```

DNS 解析成功只说明名称能找到 NPS 地址，不代表 NPS 已监听该端口，也不代表 Host 规则或内网目标可用。

## 证书续期与热更新

平台域名和使用文件路径的自定义域名支持外部续期脚本更新证书。NPS 会在新的 TLS 连接建立时检查证书与私钥文件：

- 新的证书和私钥有效且匹配时，新连接立即使用新证书，无需重启 NPS。
- 文件短暂缺失、只更新了一半、内容损坏、证书已过期/尚未生效或证书与私钥不匹配时，NPS 保留最近一次有效证书，新连接不会被错误的新文件替换。
- 已建立的 HTTPS 或 WebSocket 连接不会因证书文件更新而被主动断开；它们会继续使用当前连接的 TLS 会话。

续期脚本应先写入同目录临时文件、校验后再用 `mv` 替换，避免直接覆盖正在读取的文件。两个文件无法作为一个文件系统操作同时原子替换，因此两次 `mv` 之间可能短暂出现新旧不匹配；NPS 会拒绝这组无效组合并继续使用旧证书，等两个文件就绪后自动采用新组合。

```sh
#!/usr/bin/env sh
set -eu

cert_dir=/etc/nps/certs/example.com
source_cert=/var/lib/acme/example.com/fullchain.pem
source_key=/var/lib/acme/example.com/privkey.pem
tmp_cert=$(mktemp "${cert_dir}/.fullchain.pem.XXXXXX")
tmp_key=$(mktemp "${cert_dir}/.privkey.key.XXXXXX")
trap 'rm -f "$tmp_cert" "$tmp_key"' 0

install -m 0644 "$source_cert" "$tmp_cert"
install -m 0600 "$source_key" "$tmp_key"
openssl x509 -in "$tmp_cert" -noout
openssl pkey -in "$tmp_key" -noout

# 可选：在脚本中进一步比较证书与私钥的公钥是否一致。
mv -f "$tmp_cert" "${cert_dir}/fullchain.pem"
mv -f "$tmp_key" "${cert_dir}/privkey.key"
trap - 0
```

脚本运行用户需要能读取续期产物并写入 NPS 使用的目录；NPS 运行用户至少需要读取最终证书和私钥。若证书状态页显示不可读或即将过期，先检查挂载、文件权限、SELinux/AppArmor 规则和路径是否与全局参数一致。

## 域名规则诊断

在「域名」列表点击“规则诊断”，输入收到的 `Host`、请求路径和协议（HTTP 或 HTTPS）。诊断会显示：

- 命中的 Host / 路径规则和所属客户端；
- 规则当前是否启用，以及会选择的内网目标；
- 使用的平台域名或自定义域名状态；
- 未命中时的原因，例如 Host、协议、路径不匹配、规则已停用或没有内网目标。

诊断用于解释 NPS 的路由选择，不会代替 DNS 查询、云防火墙检查或对内网目标发起健康探测。诊断命中后仍访问失败时，按[域名代理排查顺序](domain-proxy.md#排查顺序)继续检查端口、客户端在线状态和目标服务可达性。
