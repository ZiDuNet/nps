#!/usr/bin/env bash
# NPC Pro quick reference. Run this file directly or read it in an Agent.
set -euo pipefail

cat <<'HELP'
NPC Pro 部署速查

先读取真实的 Bridge 入口：
  - 管理面板：客户端详情页点击“复制命令”或“快捷启动命令”
  - API/CLI：npsctl clients 查看响应顶层的 bridgeEndpoints（它是服务端全局入口列表，
    不属于某个客户端行）；客户端自身的命令请使用面板为该客户端生成的命令
  - 不要因为文档曾写 8024/8025 就假设服务器仍使用这两个端口。

1. 普通 TCP（使用服务端返回的 host/port）：
   npc -server=<bridge_host>:<bridge_port> -vkey=<vkey>

2. TCP + TLS（仅在 bridgeEndpoints 返回 TLS 入口时）：
   npc -type=tls -tls_enable=true -server=<tls_host>:<tls_port> -vkey=<vkey>

3. 其他传输：
   使用面板生成的完整命令；WS/WSS 还要带服务端返回的 -bridge_path，
   QUIC 还要带服务端返回的 -quic_alpn，不要只替换端口。

4. 注册系统服务：
   运行 npc 后选择 [1]；删除/卸载选 [2]，启动 [3]，停止 [4]，更新 [5]。
   菜单还提供已注册服务列表、状态、日志和统一重启入口。

5. 同一 VKey 多机运行：
   每台机器单独运行 NPC；服务端会按在线实例展示主机、系统、协议和
   局域网 IP。不是让多个进程共用同一份系统服务配置。

6. 连接失败时先检查：
   - bridgeEndpoints 中的协议、host、port、path、ALPN 和 TLS 要求
   - 云安全组、主机防火墙和 UDP 是否放行（KCP/QUIC/HTTP3 需要 UDP）
   - VKey 是否属于当前客户端，客户端是否已停用或删除
   - 使用私有 CA 时配置 -tls_ca_file 或服务端给出的证书指纹；
     不要在生产环境使用 -tls_insecure_skip_verify=true。
HELP
