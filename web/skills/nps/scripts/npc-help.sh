#!/usr/bin/env bash
# NPC Pro quick reference. Source this file or run it directly.
set -euo pipefail

cat <<'HELP'
NPC Pro 部署速查

1. 直连 TCP Bridge:
   npc -server=<host>:8024 -vkey=<vkey>

2. TLS Bridge:
   npc -type=tls -tls_enable=true -server=<host>:8025 -vkey=<vkey>

3. 其他传输:
   -type=kcp | ws | wss | quic （配合对应端口、-bridge_path、-quic_alpn）

4. 注册系统服务:
   运行 npc 后选择菜单 [1]；卸载选 [2]，启动 [3]，停止 [4]，更新 [5]。

5. 同一 VKey 多机运行:
   每台机器单独启动 npc；服务端“在线实例”会分别展示并支持单独断开。
HELP
