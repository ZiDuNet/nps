# 服务端配置文件

配置文件位置：
- 直接运行模式：当前目录下 `conf/nps.conf`
- 安装服务模式：Linux `/etc/nps/conf/nps.conf`，Windows `C:\Program Files\nps\conf\nps.conf`
- 首次启动自动生成默认配置，密码随机生成

## 配置项

| 名称 | 含义 | 默认值 |
|------|------|--------|
| web_port | Web 管理端口 | 8080 |
| web_password | Web 管理密码 | 随机生成 |
| web_username | Web 管理账号 | 随机生成 |
| web_base_url | Web 管理子路径 | / |
| bridge_port | 客户端连接端口（TCP） | 8024 |
| tls_bridge_port | TLS 加密连接端口 | 8025 |
| tls_enable | 是否启用 TLS | false |
| https_proxy_port | HTTPS 代理端口 | 443 |
| http_proxy_port | HTTP 代理端口 | 80 |
| auth_key | Web API 密钥 | 随机生成 |
| auth_crypt_key | API 密钥加密密钥（16位） | 随机生成 |
| bridge_type | 连接方式（tcp/kcp） | tcp |
| public_vkey | 公钥模式密钥，留空关闭 | - |
| ip_limit | 是否限制 IP 访问 | false |
| flow_store_interval | 流量持久化间隔（分钟） | - |
| log_level | 日志级别（0-7） | 7 |
| p2p_ip | P2P 模式服务端 IP | - |
| p2p_port | P2P UDP 端口 | - |
| p2p_port_range | P2P 端口范围 | - |
| disconnect_timeout | 连接超时（单位 5s） | 60 |
| system_info_display | 显示系统信息图表 | false |
| open_captcha | 登录验证码 | false |
| allow_user_login | 允许多用户登录 | false |
| allow_user_register | 允许用户注册 | false |
| allow_flow_limit | 允许流量限制 | false |
| allow_rate_limit | 允许带宽限制 | false |
| allow_connection_num_limit | 允许连接数限制 | false |
| allow_tunnel_num_limit | 允许隧道数限制 | false |
| allow_multi_ip | 允许多 IP 监听 | false |
| allow_local_proxy | 允许代理到服务端本地 | false |
| allow_ports | 端口白名单 | - |
| server_ip | 服务地址（用于客户端命令显示） | - |
