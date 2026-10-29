package controllers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	npsconf "ehang.io/nps/conf"
	"ehang.io/nps/lib/common"
)

// serverConfigMeta describes the small amount of presentation and validation
// information that cannot be inferred safely from an INI value alone. The
// actual list of fields comes from the shipped nps.conf template, so newly
// added template keys automatically appear in the panel as well.
type serverConfigMeta struct {
	Label       string
	Category    string
	Description string
	Type        string
	Options     []string
	Editable    bool
	Optional    bool
	Sensitive   bool
}

type serverConfigFieldView struct {
	Key          string
	Label        string
	Category     string
	Description  string
	Type         string
	Value        string
	DisplayValue string
	Options      []string
	Editable     bool
	Optional     bool
	Sensitive    bool
	Present      bool
	Enabled      bool
}

type serverConfigSectionView struct {
	Name        string
	Description string
	Fields      []serverConfigFieldView
}

type serverConfigUnknownView struct {
	Key          string
	Value        string
	DisplayValue string
	Sensitive    bool
}

type serverConfigSummary struct {
	Sections  int
	Fields    int
	Editable  int
	Enabled   int
	Sensitive int
	ReadOnly  int
	Missing   int
}

type serverConfigUpdate struct {
	Key    string
	Value  string
	Omit   bool
	Secret bool
}

var serverConfigMetaByKey = map[string]serverConfigMeta{
	"http_proxy_ip":           {Label: "HTTP/HTTPS 监听地址", Category: "HTTP(S) 入口", Description: "HTTP 主机代理和 HTTPS 主机代理监听的本机地址。"},
	"http_proxy_port":         {Label: "HTTP 代理端口", Category: "HTTP(S) 入口", Description: "域名反向代理的 HTTP 监听端口；不启用时关闭该项。", Optional: true},
	"https_proxy_port":        {Label: "HTTPS 代理端口", Category: "HTTP(S) 入口", Description: "域名反向代理的 HTTPS 监听端口；不启用时关闭该项。", Optional: true},
	"http_proxy_access_port":  {Label: "HTTP 对外端口", Category: "HTTP(S) 入口", Description: "仅用于面板生成访问地址，不改变实际监听端口。", Type: "int", Optional: true},
	"https_proxy_access_port": {Label: "HTTPS 对外端口", Category: "HTTP(S) 入口", Description: "仅用于面板生成访问地址，不改变实际监听端口。", Type: "int", Optional: true},
	"show_http_proxy_port":    {Label: "显示非标准 HTTP 端口", Category: "HTTP(S) 入口", Description: "面板访问地址中是否显示非 80 的 HTTP 端口。"},
	"http3_proxy_port":        {Label: "HTTP/3 反代端口", Category: "HTTP(S) 入口", Description: "使用 QUIC/HTTP/3 提供域名反代；需要 UDP 放行和可用证书。", Type: "int", Optional: true},
	"https_default_cert_file": {Label: "默认 HTTPS 证书", Category: "HTTP(S) 入口", Description: "Host 未单独配置证书时使用的证书文件。", Optional: true},
	"https_default_key_file":  {Label: "默认 HTTPS 私钥", Category: "HTTP(S) 入口", Description: "默认 HTTPS 证书对应的私钥文件路径；路径本身会正常回显，私钥内容不会读取。", Optional: true},
	"force_auto_ssl":          {Label: "自动申请 HTTPS 证书", Category: "HTTP(S) 入口", Description: "按 Host 自动申请/续期 ACME 证书；需要公网 80/443 和 DNS 解析。", Type: "bool", Optional: true},
	"ssl_path":                {Label: "证书缓存目录", Category: "HTTP(S) 入口", Description: "ACME 证书和缓存的存储目录。", Optional: true},
	"ssl_email":               {Label: "ACME 联系邮箱", Category: "HTTP(S) 入口", Description: "ACME 注册邮箱，用于续期失败通知。", Optional: true},
	"ssl_ca":                  {Label: "ACME 服务", Category: "HTTP(S) 入口", Description: "证书颁发服务；选择 ZeroSSL 时还需要填写 ZeroSSL API 密钥。", Type: "select", Options: []string{"LetsEncrypt", "ZeroSSL"}, Optional: true},
	"ssl_zerossl_api":         {Label: "ZeroSSL API 密钥", Category: "HTTP(S) 入口", Description: "ssl_ca=ZeroSSL 时使用的 API 密钥。", Optional: true, Sensitive: true},
	"ssl_cache_max":           {Label: "证书缓存上限", Category: "HTTP(S) 入口", Description: "缓存证书数量上限，0 表示不限制。", Type: "int", Optional: true},
	"ssl_cache_reload":        {Label: "证书重载周期", Category: "HTTP(S) 入口", Description: "检查证书更新的周期，单位秒。", Type: "int", Optional: true},
	"ssl_cache_idle":          {Label: "证书空闲时间", Category: "HTTP(S) 入口", Description: "证书缓存空闲回收时间，单位秒。", Type: "int", Optional: true},
	"x_nps_http_only":         {Label: "X-NPS-Http-Only 密钥", Category: "HTTP(S) 入口", Description: "可信前置代理携带该请求头时保持 HTTP，不自动跳转 HTTPS。", Optional: true, Sensitive: true},

	"bridge_type":               {Label: "主 Bridge 协议", Category: "Bridge 与传输", Description: "NPC 控制连接的主传输协议。", Type: "select", Options: []string{"tcp", "kcp"}},
	"bridge_port":               {Label: "Bridge 端口", Category: "Bridge 与传输", Description: "NPC 使用 -server 连接的主 Bridge 端口。"},
	"bridge_ip":                 {Label: "Bridge 监听地址", Category: "Bridge 与传输", Description: "Bridge 监听的本机地址。"},
	"bridge_kcp_port":           {Label: "并行 KCP 端口", Category: "Bridge 与传输", Description: "额外启用 KCP Bridge；不启用时保持关闭。", Optional: true},
	"bridge_ws_port":            {Label: "WebSocket Bridge 端口", Category: "Bridge 与传输", Description: "额外启用 WebSocket Bridge；不启用时保持关闭。", Optional: true},
	"bridge_wss_port":           {Label: "WebSocket TLS Bridge 端口", Category: "Bridge 与传输", Description: "额外启用 WebSocket TLS Bridge；不启用时保持关闭。", Optional: true},
	"bridge_quic_port":          {Label: "QUIC Bridge 端口", Category: "Bridge 与传输", Description: "额外启用 QUIC Bridge；NPC 使用 -type=quic。", Optional: true},
	"bridge_quic_ip":            {Label: "QUIC 监听地址", Category: "Bridge 与传输", Description: "留空时跟随 Bridge 监听地址。", Optional: true},
	"quic_alpn":                 {Label: "QUIC ALPN", Category: "Bridge 与传输", Description: "QUIC 服务端与 NPC 必须使用相同的 ALPN。", Optional: true},
	"quic_keep_alive_period":    {Label: "QUIC 保活周期", Category: "Bridge 与传输", Description: "QUIC 空闲连接保活周期，单位秒。", Optional: true},
	"quic_max_idle_timeout":     {Label: "QUIC 最大空闲超时", Category: "Bridge 与传输", Description: "QUIC 连接最大空闲时间，单位秒。", Optional: true},
	"quic_max_incoming_streams": {Label: "QUIC 最大并发流", Category: "Bridge 与传输", Description: "单个 QUIC 连接允许的最大并发流数。", Optional: true},
	"bridge_path":               {Label: "WebSocket 路径", Category: "Bridge 与传输", Description: "WebSocket Bridge 使用的 HTTP 路径；启用 WebSocket Bridge 时填写。", Optional: true},
	"bridge_show_path":          {Label: "命令展示 Path", Category: "Bridge 与传输", Description: "管理面板生成 WS/WSS 客户端命令时展示的路径；留空跟随 bridge_path。", Optional: true},
	"bridge_tcp_show":           {Label: "展示 TCP 命令", Category: "Bridge 与传输", Description: "是否在客户端详情中生成 TCP 连接命令；关闭只隐藏命令，不停止监听。", Type: "bool"},
	"bridge_tcp_show_ip":        {Label: "TCP 对外主机名", Category: "Bridge 与传输", Description: "TCP 命令中使用的公网域名或 IP；留空使用 bridge_addr、服务器地址或当前面板地址。", Optional: true},
	"bridge_tcp_show_port":      {Label: "TCP 对外端口", Category: "Bridge 与传输", Description: "TCP 命令中使用的对外端口；留空使用实际 bridge_port。", Type: "int", Optional: true},
	"bridge_tls_show":           {Label: "展示 TCP + TLS 命令", Category: "Bridge 与传输", Description: "是否生成 TCP + TLS 连接命令；关闭只隐藏命令，不停止 TLS 监听。", Type: "bool"},
	"bridge_tls_show_ip":        {Label: "TLS 对外主机名", Category: "Bridge 与传输", Description: "TCP + TLS 命令中使用的公网域名或 IP；留空使用自动地址。", Optional: true},
	"bridge_tls_show_port":      {Label: "TLS 对外端口", Category: "Bridge 与传输", Description: "TCP + TLS 命令中使用的对外端口；留空使用实际 tls_bridge_port。", Type: "int", Optional: true},
	"bridge_kcp_show":           {Label: "展示 KCP 命令", Category: "Bridge 与传输", Description: "是否生成 KCP 连接命令；关闭只隐藏命令，不停止 UDP 监听。", Type: "bool"},
	"bridge_kcp_show_ip":        {Label: "KCP 对外主机名", Category: "Bridge 与传输", Description: "KCP 命令中使用的公网域名或 IP；留空使用自动地址。", Optional: true},
	"bridge_kcp_show_port":      {Label: "KCP 对外端口", Category: "Bridge 与传输", Description: "KCP 命令中使用的对外 UDP 端口；留空使用实际监听端口。", Type: "int", Optional: true},
	"bridge_ws_show":            {Label: "展示 WS 命令", Category: "Bridge 与传输", Description: "是否生成 WebSocket 连接命令；关闭只隐藏命令，不停止监听。", Type: "bool"},
	"bridge_ws_show_ip":         {Label: "WS 对外主机名", Category: "Bridge 与传输", Description: "WS 命令中使用的公网域名或 IP；CDN 场景填写 CDN 主机名。", Optional: true},
	"bridge_ws_show_port":       {Label: "WS 对外端口", Category: "Bridge 与传输", Description: "WS 命令中使用的对外端口；留空使用实际 bridge_ws_port。", Type: "int", Optional: true},
	"bridge_wss_show":           {Label: "展示 WSS 命令", Category: "Bridge 与传输", Description: "是否生成 WebSocket TLS 连接命令；关闭只隐藏命令，不停止监听。", Type: "bool"},
	"bridge_wss_show_ip":        {Label: "WSS 对外主机名", Category: "Bridge 与传输", Description: "WSS 命令中使用的公网域名或 IP；CDN 场景填写 CDN 主机名。", Optional: true},
	"bridge_wss_show_port":      {Label: "WSS 对外端口", Category: "Bridge 与传输", Description: "WSS 命令中使用的对外端口；留空使用实际 bridge_wss_port。", Type: "int", Optional: true},
	"bridge_quic_show":          {Label: "展示 QUIC 命令", Category: "Bridge 与传输", Description: "是否生成 QUIC 连接命令；关闭只隐藏命令，不停止 UDP 监听。", Type: "bool"},
	"bridge_quic_show_ip":       {Label: "QUIC 对外主机名", Category: "Bridge 与传输", Description: "QUIC 命令中使用的公网域名或 IP；留空使用自动地址。", Optional: true},
	"bridge_quic_show_port":     {Label: "QUIC 对外端口", Category: "Bridge 与传输", Description: "QUIC 命令中使用的对外 UDP 端口；留空使用实际 bridge_quic_port。", Type: "int", Optional: true},
	"bridge_quic_show_alpn":     {Label: "QUIC 命令展示 ALPN", Category: "Bridge 与传输", Description: "QUIC 客户端命令中的 ALPN；留空跟随 quic_alpn。", Optional: true},
	"bridge_real_ip_header":     {Label: "Bridge 真实 IP 请求头", Category: "Bridge 与传输", Description: "可信反向代理转发客户端地址时使用的请求头，例如 X-Real-IP。", Optional: true},
	"bridge_trusted_ips":        {Label: "Bridge 可信代理地址", Category: "Bridge 与传输", Description: "允许使用真实 IP 请求头的前置代理 IP/CIDR，逗号或换行分隔。", Optional: true},
	"bridge_addr":               {Label: "Bridge 对外地址", Category: "Bridge 与传输", Description: "用于多入口或反向代理场景展示给 NPC 的连接地址。", Optional: true},
	"bridge_host":               {Label: "Bridge 复用 Host", Category: "Bridge 与传输", Description: "端口复用时用于分流 Bridge 流量的 Host。", Optional: true},
	"bridge_select_mode":        {Label: "同密钥选主策略", Category: "Bridge 与传输", Description: "同一 VKEY 存在多个连接时的选择策略。", Type: "select", Options: []string{"Primary", "RoundRobin", "Random"}, Optional: true},
	"bridge_http3":              {Label: "HTTP/3 Bridge", Category: "Bridge 与传输", Description: "允许通过 HTTP/3/QUIC 入口承载新版 Bridge。", Type: "bool", Optional: true},
	"bridge_cert_file":          {Label: "Bridge 证书", Category: "Bridge 与传输", Description: "Bridge 全协议共用 TLS 证书路径；请在“证书设置 → Bridge TLS 证书”中维护，留空时重启后使用临时证书。", Optional: true},
	"bridge_key_file":           {Label: "Bridge 私钥", Category: "Bridge 与传输", Description: "Bridge 全协议共用 TLS 私钥路径；路径会正常回显，私钥内容不会读取，修改后需要重启。", Optional: true},
	"client_history_limit":      {Label: "客户端连接历史条数", Category: "Bridge 与传输", Description: "每个客户端保留的最近连接历史数量，0 表示关闭。"},

	"public_vkey":           {Label: "公共验证密钥", Category: "客户端认证与 IP 登记", Description: "允许使用公共密钥连接的客户端；留空表示关闭。", Optional: true, Sensitive: true},
	"auth_time_window":      {Label: "握手时间窗口", Category: "客户端认证与 IP 登记", Description: "新版 NPC 握手允许的最大时间偏差，单位秒。"},
	"ip_limit":              {Label: "客户端来源 IP 限制", Category: "客户端认证与 IP 登记", Description: "按客户端来源地址限制 Bridge 连接；兼容旧部署。", Type: "bool", Optional: true},
	"public_register_key":   {Label: "公共 IP 登记密钥", Category: "客户端认证与 IP 登记", Description: "填写后启用 /ip/register 公共登记入口；留空关闭。", Optional: true, Sensitive: true},
	"ip_register_max_hours": {Label: "IP 登记最长时长", Category: "客户端认证与 IP 登记", Description: "公共 IP 登记的默认最长有效期，单位小时。"},
	"ip_register_ip":        {Label: "独立登记监听地址", Category: "客户端认证与 IP 登记", Description: "独立 IP 登记监听的本机地址；端口为 0 时可以关闭该项。", Optional: true},
	"ip_register_port":      {Label: "独立登记监听端口", Category: "客户端认证与 IP 登记", Description: "端口为 0 时仅使用管理面板的 /ip/register。", Optional: true},
	"ip_register_cert_file": {Label: "登记 HTTPS 证书", Category: "客户端认证与 IP 登记", Description: "独立登记监听的证书路径；需与私钥同时配置。", Optional: true},
	"ip_register_key_file":  {Label: "登记 HTTPS 私钥", Category: "客户端认证与 IP 登记", Description: "独立登记监听的私钥路径；需与证书同时配置。", Optional: true},

	"flow_store_interval": {Label: "流量持久化间隔", Category: "日志与审计", Description: "流量数据持久化间隔，单位分钟；不启用流量持久化时关闭该项。", Optional: true},
	"log_level":           {Label: "日志级别", Category: "日志与审计", Description: "支持 0 到 7，也支持 trace/debug/info/warn/error/fatal/panic/off。", Type: "text"},
	"log_path":            {Label: "日志文件路径", Category: "日志与审计", Description: "主日志文件路径；留空时使用运行目录默认路径。", Optional: true},
	"log_max_size":        {Label: "单文件大小上限", Category: "日志与审计", Description: "日志轮转单文件大小上限，单位 MB；不启用时使用内置默认值。", Optional: true},
	"log_max_files":       {Label: "日志归档数量", Category: "日志与审计", Description: "日志归档保留数量，0 表示不限制。", Optional: true},
	"log_max_days":        {Label: "日志归档天数", Category: "日志与审计", Description: "日志归档保留天数，0 表示不按天数清理。", Optional: true},
	"log_compress":        {Label: "压缩日志归档", Category: "日志与审计", Description: "是否压缩轮转后的日志归档。", Optional: true},
	"log_rotate_daily":    {Label: "按日期轮转日志", Category: "日志与审计", Description: "日期变化时是否轮转日志。", Optional: true},
	"audit_log_path":      {Label: "审计日志路径", Category: "日志与审计", Description: "管理操作审计日志路径；留空时关闭独立审计文件。", Optional: true},
	"connection_log_path": {Label: "连接日志路径", Category: "日志与审计", Description: "客户端与代理连接的 JSONL 日志路径；留空关闭连接日志。", Optional: true},

	"p2p_ip":                    {Label: "P2P 监听地址", Category: "P2P 与调试", Description: "P2P 中继监听地址；留空表示关闭。", Optional: true},
	"p2p_port":                  {Label: "P2P 监听端口", Category: "P2P 与调试", Description: "配置后启用 P2P 中继；留空或 0 表示关闭。", Type: "int", Optional: true},
	"p2p_stun_servers":          {Label: "P2P STUN 服务器", Category: "P2P 与调试", Description: "逗号分隔的 STUN 地址；留空使用内置列表，填写 none 关闭 STUN。", Optional: true},
	"p2p_enable_upnp_portmap":   {Label: "P2P UPnP 端口映射", Category: "P2P 与调试", Description: "允许访问端 NPC 在局域网网关申请 UPnP 映射；默认关闭。", Type: "bool", Optional: true},
	"p2p_enable_pcp_portmap":    {Label: "P2P PCP 端口映射", Category: "P2P 与调试", Description: "允许访问端 NPC 使用 PCP 申请端口映射；默认关闭。", Type: "bool", Optional: true},
	"p2p_enable_natpmp_portmap": {Label: "P2P NAT-PMP 端口映射", Category: "P2P 与调试", Description: "允许访问端 NPC 使用 NAT-PMP 申请端口映射；默认关闭。", Type: "bool", Optional: true},
	"p2p_portmap_lease_seconds": {Label: "P2P 映射租约时间", Category: "P2P 与调试", Description: "UPnP/PCP/NAT-PMP 映射租约时长，单位秒。", Type: "int", Optional: true},
	"p2p_probe_timeout_ms":      {Label: "P2P 探测超时", Category: "P2P 与调试", Description: "新版 P2P NAT 探测超时，单位毫秒；0 使用默认值。", Type: "int", Optional: true},
	"p2p_handshake_timeout_ms":  {Label: "P2P 协商超时", Category: "P2P 与调试", Description: "新版 P2P 控制协商超时，单位毫秒；0 使用默认值。", Type: "int", Optional: true},
	"p2p_transport_timeout_ms":  {Label: "P2P 传输超时", Category: "P2P 与调试", Description: "新版 P2P 直连传输建立超时，单位毫秒；0 使用默认值。", Type: "int", Optional: true},
	"pprof_ip":                  {Label: "pprof 监听地址", Category: "P2P 与调试", Description: "性能调试端点监听地址；仅建议内网临时开启。", Optional: true},
	"pprof_port":                {Label: "pprof 监听端口", Category: "P2P 与调试", Description: "性能调试端点监听端口；留空或 0 表示关闭。", Type: "int", Optional: true},

	"web_host":               {Label: "管理面板域名", Category: "Web 管理面板", Description: "端口复用场景下用于区分管理面板流量；不做端口复用时可关闭。", Optional: true},
	"web_username":           {Label: "管理员用户名", Category: "Web 管理面板", Description: "管理员登录账号。"},
	"web_password":           {Label: "管理员密码", Category: "Web 管理面板", Description: "留空保持现有密码；填写新密码后需重启生效。", Sensitive: true},
	"totp_secret":            {Label: "管理员 TOTP 密钥", Category: "Web 管理面板", Description: "留空关闭双因素认证。", Optional: true, Sensitive: true},
	"web_port":               {Label: "管理面板端口", Category: "Web 管理面板", Description: "管理面板监听端口。"},
	"web_ip":                 {Label: "管理面板监听地址", Category: "Web 管理面板", Description: "管理面板监听的本机地址。"},
	"web_base_url":           {Label: "管理面板 URL 前缀", Category: "Web 管理面板", Description: "反向代理到子路径时填写，例如 /nps。", Optional: true},
	"instance_key_file":      {Label: "会话实例密钥路径", Category: "Web 管理面板", Description: "留空时使用 conf/instance.key。", Optional: true},
	"web_open_ssl":           {Label: "管理面板 HTTPS", Category: "Web 管理面板", Description: "是否直接为管理面板启用 HTTPS。"},
	"web_cert_file":          {Label: "管理面板证书路径", Category: "Web 管理面板", Description: "web_open_ssl=true 时使用；关闭 HTTPS 时可不写入。", Optional: true},
	"web_key_file":           {Label: "管理面板私钥路径", Category: "Web 管理面板", Description: "web_open_ssl=true 时使用；关闭 HTTPS 时可不写入。", Optional: true},
	"web_close_on_not_found": {Label: "未知路径直接断开", Category: "Web 管理面板", Description: "访问不存在的管理路径时尽力关闭连接，不返回 404 响应。", Type: "bool", Optional: true},

	"auth_key":       {Label: "Web API 密钥", Category: "Web API 认证", Description: "签名 API 请求使用的密钥。", Sensitive: true},
	"auth_crypt_key": {Label: "API 密钥加密密钥", Category: "Web API 认证", Description: "必须恰好为 16 个字符。", Sensitive: true},

	"allow_user_login":      {Label: "允许普通用户登录", Category: "用户与资源权限", Description: "是否启用多用户面板登录。"},
	"allow_user_vkey_login": {Label: "允许 VKEY 登录面板", Category: "用户与资源权限", Description: "是否允许普通用户使用客户端 VKEY 登录管理面板。", Type: "bool"},

	"allow_user_register":          {Label: "允许普通用户注册", Category: "注册配置", Description: "打开后登录页显示注册入口；关闭时注册页面和提交接口都不可用。", Type: "bool"},
	"register_captcha":             {Label: "注册图片验证码", Category: "注册配置", Description: "注册提交时是否必须填写图片验证码。建议保持开启，防止自动化批量注册。", Type: "bool"},
	"register_max_clients":         {Label: "默认客户端配额", Category: "注册配置", Description: "新注册用户默认最多可拥有的客户端数；0 表示不限制。", Type: "int"},
	"register_max_tunnels":         {Label: "默认隧道配额", Category: "注册配置", Description: "新注册用户默认最多可创建的总隧道数，域名反代也计入；0 表示不限制。", Type: "int"},
	"register_max_conn":            {Label: "默认最大并发连接数", Category: "注册配置", Description: "新注册用户所有客户端共享的 NPS 转发并发上限；0 表示不限制。P2P 直连不计入。", Type: "int"},
	"register_upload_limit":        {Label: "默认上行带宽", Category: "注册配置", Description: "新注册用户所有客户端共享的访问者到后端方向带宽，单位 KB/s；0 表示不限制。", Type: "int"},
	"register_download_limit":      {Label: "默认下行带宽", Category: "注册配置", Description: "新注册用户所有客户端共享的后端到访问者方向带宽，单位 KB/s；0 表示不限制。", Type: "int"},
	"register_expire_days":         {Label: "默认注册有效期", Category: "注册配置", Description: "新注册用户从注册成功时起默认有效天数；0 表示永久有效。", Type: "int"},
	"register_rate_window":         {Label: "注册限频窗口", Category: "注册配置", Description: "注册提交次数和同 IP 成功注册数的统计窗口，单位秒。", Type: "int"},
	"register_rate_limit":          {Label: "注册提交次数上限", Category: "注册配置", Description: "同一 IP 或同一用户名在一个窗口内允许提交的注册次数；0 表示不限制。", Type: "int"},
	"register_ip_rate_limit":       {Label: "单 IP 注册账号上限", Category: "注册配置", Description: "同一来源 IP 在一个窗口内允许成功创建的账号数；0 表示不限制。", Type: "int"},
	"register_email_verify":        {Label: "启用邮箱验证", Category: "注册配置", Description: "开启后注册必须填写邮箱并输入邮件验证码；关闭时为用户名和密码注册。", Type: "bool"},
	"register_email_code_ttl":      {Label: "邮箱验证码有效期", Category: "注册配置", Description: "邮件验证码有效时间，单位秒。", Type: "int"},
	"register_email_code_cooldown": {Label: "邮箱验证码发送间隔", Category: "注册配置", Description: "同一邮箱或来源 IP 两次发送验证码的最小间隔，单位秒；0 表示不限制。", Type: "int"},
	"smtp_host":                    {Label: "SMTP 主机", Category: "注册配置", Description: "邮件服务器主机名，例如 smtp.example.com。启用邮箱验证时必填。", Optional: true},
	"smtp_port":                    {Label: "SMTP 端口", Category: "注册配置", Description: "STARTTLS 常用 587，隐式 TLS 常用 465。启用邮箱验证时必填。", Type: "int", Optional: true},
	"smtp_security":                {Label: "SMTP 加密方式", Category: "注册配置", Description: "starttls 使用 587 等端口升级加密；ssl 直接建立 TLS 连接；none 仅用于可信内网邮件中继。留空按 starttls 处理。", Type: "select", Options: []string{"starttls", "ssl", "none"}, Optional: true},
	"smtp_username":                {Label: "SMTP 用户名", Category: "注册配置", Description: "邮件服务器认证用户名；不需要认证的内网中继可以留空。", Optional: true},
	"smtp_password":                {Label: "SMTP 密码", Category: "注册配置", Description: "邮件服务器认证密码；页面不会回显，留空表示保持原值。", Optional: true, Sensitive: true},
	"smtp_from":                    {Label: "邮件发件人", Category: "注册配置", Description: "验证码邮件的 From 地址；启用邮箱验证时必填。", Optional: true},
	"allow_user_change_username":   {Label: "允许修改用户名", Category: "用户与资源权限", Description: "普通用户是否可以修改自己的用户名。"},
	"allow_user_local":             {Label: "允许用户使用本地代理", Category: "用户与资源权限", Description: "允许普通用户让 NPS 服务端使用本地代理访问目标；默认关闭。", Type: "bool", Optional: true},
	"allow_secret_link":            {Label: "允许 Secret 访问任意目标", Category: "用户与资源权限", Description: "允许 Secret 代理连接任意目标；默认关闭。", Type: "bool", Optional: true},
	"allow_secret_local":           {Label: "允许 Secret 访问服务端本地", Category: "用户与资源权限", Description: "允许 Secret 代理连接 NPS 所在主机的本地地址；默认关闭。", Type: "bool", Optional: true},
	"allow_remote_update":          {Label: "允许远程升级客户端", Category: "用户与资源权限", Description: "管理员是否可以从 Web 面板向在线 NPC 下发升级指令。", Type: "bool", Optional: true},
	"allow_time_limit":             {Label: "启用客户端时间限制", Category: "用户与资源权限", Description: "按客户端到期时间拒绝已过期连接。", Type: "bool", Optional: true},
	"allow_flow_limit":             {Label: "允许流量配额", Category: "用户与资源权限", Description: "是否在客户端页面维护流量上限。"},
	"flow_limit_type":              {Label: "流量配额计算口径", Category: "用户与资源权限", Description: "total 按上下行总量计算；actual 按实际消耗计算（总量乘 2）。", Type: "select", Options: []string{"total", "actual"}},
	"allow_rate_limit":             {Label: "允许带宽配额", Category: "用户与资源权限", Description: "是否在客户端和隧道页面维护带宽上限。"},
	"allow_tunnel_num_limit":       {Label: "允许隧道数量配额", Category: "用户与资源权限", Description: "是否限制客户端和用户的隧道数量。"},
	"allow_local_proxy":            {Label: "允许本地代理", Category: "用户与资源权限", Description: "是否允许隧道使用服务端本地代理访问目标。"},
	"allow_connection_num_limit":   {Label: "允许连接数配额", Category: "用户与资源权限", Description: "是否维护客户端最大连接数。"},
	"allow_multi_ip":               {Label: "允许绑定监听地址", Category: "用户与资源权限", Description: "是否允许每条隧道绑定不同的服务端监听 IP。"},
	"system_info_display":          {Label: "显示系统监控", Category: "用户与资源权限", Description: "仪表盘是否显示 CPU、内存和网络信息。"},

	"http_add_origin_header":      {Label: "添加来源请求头", Category: "HTTP 转发与缓存", Description: "向后端添加客户端真实 IP 相关请求头。"},
	"http_cache":                  {Label: "启用 HTTP 缓存", Category: "HTTP 转发与缓存", Description: "是否启用 HTTP 响应缓存。"},
	"http_cache_length":           {Label: "HTTP 缓存队列长度", Category: "HTTP 转发与缓存", Description: "HTTP 缓存队列长度；关闭 HTTP 缓存时可不写入。", Optional: true},
	"http_proxy_response_timeout": {Label: "后端响应头超时", Category: "HTTP 转发与缓存", Description: "等待后端响应头的最长时间，单位秒。", Type: "int"},

	"disconnect_timeout":   {Label: "Bridge 断开超时", Category: "运行与安全", Description: "未收到 mux 心跳回包的最大等待时间，单位秒。"},
	"open_captcha":         {Label: "登录验证码", Category: "运行与安全", Description: "是否启用管理面板登录验证码。"},
	"force_pow":            {Label: "登录工作量证明", Category: "运行与安全", Description: "是否启用登录 PoW 防护。"},
	"pow_bits":             {Label: "PoW 难度", Category: "运行与安全", Description: "SHA-256 前导零位数。"},
	"tls_enable":           {Label: "Bridge TLS", Category: "运行与安全", Description: "是否启用 Bridge TLS 加密监听。"},
	"tls_bridge_port":      {Label: "Bridge TLS 端口", Category: "运行与安全", Description: "Bridge TLS 加密监听端口；关闭 Bridge TLS 时可不写入。", Optional: true},
	"allow_x_real_ip":      {Label: "使用代理真实 IP 头", Category: "运行与安全", Description: "默认开启。启用后 Web/IP 登记/HTTP Host/WS 访问优先读取前置反向代理覆盖后的 X-Real-IP，其次读取 X-Forwarded-For 第一项。Docker + Nginx 场景无需额外填写可信代理 IP；请确保代理覆盖而不是追加这些请求头，面板若可直接被任意客户端访问则应显式关闭。", Type: "bool", Optional: true},
	"trusted_proxy_ips":    {Label: "旧版可信代理 IP（兼容）", Category: "运行与安全", Description: "为旧配置保留的兼容字段；当前 Web 登录和 HTTP 来源 IP 不再要求填写此列表。", Optional: true},
	"ntp_server":           {Label: "NTP 校时服务器", Category: "运行与安全", Description: "留空关闭校时；失败时继续使用上次成功偏移。", Optional: true},
	"ntp_interval":         {Label: "NTP 校时周期", Category: "运行与安全", Description: "校时周期，单位分钟；留空使用默认 5 分钟。", Type: "int", Optional: true},
	"timezone":             {Label: "服务时区", Category: "运行与安全", Description: "日志和时间显示使用的时区。", Optional: true},
	"geoip_path":           {Label: "GeoIP 数据路径", Category: "运行与安全", Description: "GeoIP 数据文件路径；规则中可使用 geoip: 前缀。留空时使用运行目录 conf/geoip.dat，文件存在才会加载。", Optional: true},
	"geosite_path":         {Label: "Geosite 数据路径", Category: "运行与安全", Description: "Geosite 数据文件路径；规则中可使用 geosite: 前缀。留空时使用运行目录 conf/geosite.dat，文件存在才会加载。", Optional: true},
	"acl_max_rules":        {Label: "ACL 最大规则数", Category: "运行与安全", Description: "每张用户资源黑名单、白名单或目标 ACL 的有效规则上限；空行和注释不计数。", Type: "int", Optional: true},
	"geo_reload_interval":  {Label: "Geo 数据刷新周期", Category: "运行与安全", Description: "检测 GeoIP/Geosite 文件变化的周期，单位秒。", Type: "int", Optional: true},
	"dns_server":           {Label: "DNS 服务器", Category: "运行与安全", Description: "新版 P2P/STUN 和目标解析使用的 DNS 服务器。", Optional: true},
	"login_ban_time":       {Label: "登录最小间隔", Category: "运行与安全", Description: "同一 IP 或用户名两次登录请求的最小间隔，单位秒；未写入时使用默认值。", Type: "int", Optional: true},
	"login_ip_ban_time":    {Label: "IP 失败重置周期", Category: "运行与安全", Description: "IP 维度失败次数的重置周期，单位秒；未写入时使用默认值。", Type: "int", Optional: true},
	"login_user_ban_time":  {Label: "用户名失败重置周期", Category: "运行与安全", Description: "用户名维度失败次数的重置周期，单位秒；未写入时使用默认值。", Type: "int", Optional: true},
	"login_max_fail_times": {Label: "最大登录失败次数", Category: "运行与安全", Description: "达到此次数后在对应重置周期内拒绝登录；未写入时使用默认值。", Type: "int", Optional: true},
	"login_max_body":       {Label: "登录请求体上限", Category: "运行与安全", Description: "登录请求体最大字节数；未写入时使用默认值。", Type: "int", Optional: true},
	"login_max_skew":       {Label: "登录时间偏移容忍度", Category: "运行与安全", Description: "RSA 登录时间戳允许的最大偏差，单位毫秒；设为 0 可关闭时间校验，未写入时使用默认值。", Type: "int", Optional: true},

	"allow_ports": {Label: "允许映射端口范围", Category: "运行与安全", Description: "限制客户端可映射的服务端端口，例如 9001-9009,10001；留空表示不限制。", Optional: true},
}

// Certificate settings have a dedicated administrator page. The source editor
// still exposes the complete nps.conf; only the visual editor is split so a
// single setting is not maintained in two places.
var certificateConfigGroups = []struct {
	Name        string
	Description string
	Keys        []string
}{
	{
		Name:        "HTTPS 默认证书与 ACME",
		Description: "域名反向代理的默认证书、自动签发参数和证书缓存。未单独配置证书的 Host 会使用这里的默认证书。",
		Keys: []string{
			"https_default_cert_file", "https_default_key_file", "force_auto_ssl",
			"ssl_path", "ssl_email", "ssl_ca", "ssl_zerossl_api",
			"ssl_cache_max", "ssl_cache_reload", "ssl_cache_idle",
		},
	},
	{
		Name:        "管理面板 HTTPS",
		Description: "直接为 NPS Web 管理面板启用 HTTPS。若管理面板由前置 Nginx、Caddy 或容器网关终止 TLS，这里保持关闭即可。",
		Keys:        []string{"web_open_ssl", "web_cert_file", "web_key_file"},
	},
	{
		Name:        "IP 登记 HTTPS",
		Description: "独立 IP 登记监听使用的证书与私钥。登记端口和监听地址仍属于“服务端配置 → 客户端认证与 IP 登记”。",
		Keys:        []string{"ip_register_cert_file", "ip_register_key_file"},
	},
	{
		Name:        "Bridge TLS 入口",
		Description: "控制 TCP TLS Bridge 是否监听及监听端口。证书本身由上方 Bridge TLS 证书卡片单独维护。",
		Keys:        []string{"tls_enable", "tls_bridge_port"},
	},
}

var certificateConfigKeySet = func() map[string]struct{} {
	keys := make(map[string]struct{})
	for _, group := range certificateConfigGroups {
		for _, key := range group.Keys {
			keys[key] = struct{}{}
		}
	}
	// The pair is configured through the richer Bridge certificate widget and
	// must still be excluded from the general visual editor.
	keys["bridge_cert_file"] = struct{}{}
	keys["bridge_key_file"] = struct{}{}
	return keys
}()

// Registration settings have a dedicated business-facing tab. The complete
// source editor still exposes nps.conf, but the general visual editor no longer
// duplicates these fields in a separate "注册配置" section.
var registrationConfigGroups = []struct {
	Name        string
	Description string
	Keys        []string
}{
	{
		Name:        "注册入口与默认权益",
		Description: "控制登录页注册入口，并设置新账号默认获得的客户端、隧道、并发连接、带宽和有效期。所有数值为 0 时表示不限。",
		Keys: []string{
			"allow_user_register", "register_captcha", "register_email_verify",
			"register_max_clients", "register_max_tunnels", "register_max_conn",
			"register_upload_limit", "register_download_limit", "register_expire_days",
		},
	},
	{
		Name:        "注册频率限制",
		Description: "限制重复提交和单 IP 批量注册。数值 0 可关闭对应维度；验证码和邮箱验证仍会同时生效。",
		Keys: []string{
			"register_rate_window", "register_rate_limit", "register_ip_rate_limit",
		},
	},
	{
		Name:        "邮箱验证与 SMTP",
		Description: "仅在启用邮箱验证时需要完整配置。密码不会在页面回显，留空表示保持现有值。",
		Keys: []string{
			"register_email_code_ttl", "register_email_code_cooldown",
			"smtp_host", "smtp_port", "smtp_security", "smtp_username",
			"smtp_password", "smtp_from",
		},
	},
}

var registrationConfigKeySet = func() map[string]struct{} {
	keys := make(map[string]struct{})
	for _, group := range registrationConfigGroups {
		for _, key := range group.Keys {
			keys[key] = struct{}{}
		}
	}
	return keys
}()

var serverConfigCategoryOrder = []string{
	"HTTP(S) 入口", "Bridge 与传输", "客户端认证与 IP 登记", "日志与审计", "P2P 与调试",
	"Web 管理面板", "Web API 认证", "注册配置", "用户与资源权限", "HTTP 转发与缓存", "运行与安全", "兼容/自定义",
}

var serverConfigCategoryDescriptions = map[string]string{
	"HTTP(S) 入口":   "域名反代监听端口、HTTP/3 与来源请求头；证书统一在“证书设置”维护。",
	"Bridge 与传输":   "NPC 接入端口、传输协议、多入口和连接复用；TLS 证书统一在“证书设置”维护。",
	"客户端认证与 IP 登记": "客户端接入密钥、来源限制和公网 IP 登记入口。",
	"日志与审计":        "运行日志、轮转策略和操作审计文件。",
	"P2P 与调试":      "P2P 中继、STUN、端口映射和性能调试端点。",
	"Web 管理面板":     "管理面板监听、登录账号和安全选项；HTTPS 证书统一在“证书设置”维护。",
	"Web API 认证":   "管理 API 的签名密钥和本地加密密钥。",
	"注册配置":         "自助注册开关、默认配额、限频、邮箱验证和 SMTP。",
	"用户与资源权限":      "普通用户登录、配额能力以及代理资源限制。",
	"HTTP 转发与缓存":   "反向代理请求头、超时和响应缓存。",
	"运行与安全":        "登录保护、校时、Geo 数据、DNS 和端口权限；TLS 入口与证书统一在“证书设置”维护。",
	"兼容/自定义":       "历史版本或自定义部署中的只读配置，会在保存时原样保留。",
}

var serverConfigKeyPriority = map[string]int{
	"http_proxy_ip": 10, "http_proxy_port": 20, "https_proxy_port": 30,
	"show_http_proxy_port": 40, "http_proxy_access_port": 50, "https_proxy_access_port": 60,
	"https_default_cert_file": 70, "https_default_key_file": 80, "force_auto_ssl": 90,
	"http3_proxy_port": 100, "ssl_path": 110, "ssl_email": 120, "ssl_ca": 130,
	"ssl_zerossl_api": 140,

	"bridge_ip": 10, "bridge_port": 20, "bridge_type": 30, "bridge_select_mode": 40,
	"bridge_kcp_port": 50, "bridge_ws_port": 60, "bridge_wss_port": 70,
	"bridge_quic_port": 80, "bridge_quic_ip": 90, "bridge_path": 100,
	"bridge_show_path": 110, "bridge_tcp_show": 120, "bridge_tcp_show_ip": 121,
	"bridge_tcp_show_port": 122, "bridge_tls_show": 130, "bridge_tls_show_ip": 131,
	"bridge_tls_show_port": 132, "bridge_kcp_show": 140, "bridge_kcp_show_ip": 141,
	"bridge_kcp_show_port": 142, "bridge_ws_show": 150, "bridge_ws_show_ip": 151,
	"bridge_ws_show_port": 152, "bridge_wss_show": 160, "bridge_wss_show_ip": 161,
	"bridge_wss_show_port": 162, "bridge_quic_show": 170,
	"bridge_quic_show_ip": 171, "bridge_quic_show_port": 172,
	"bridge_quic_show_alpn": 173, "bridge_addr": 190, "bridge_host": 200,
	"bridge_real_ip_header": 210, "bridge_trusted_ips": 220,
	"bridge_cert_file": 230, "bridge_key_file": 240,

	"public_register_key": 10, "ip_limit": 20, "public_vkey": 30,
	"ip_register_max_hours": 40, "ip_register_ip": 50, "ip_register_port": 60,
	"ip_register_cert_file": 70, "ip_register_key_file": 80, "auth_time_window": 90,

	"log_level": 10, "log_path": 20, "audit_log_path": 30, "connection_log_path": 35,
	"log_max_size": 40, "log_max_files": 50, "log_max_days": 60, "log_compress": 70,
	"log_rotate_daily": 80, "flow_store_interval": 90,

	"p2p_ip": 10, "p2p_port": 20, "p2p_stun_servers": 30,
	"p2p_enable_upnp_portmap": 40, "p2p_enable_pcp_portmap": 50,
	"p2p_enable_natpmp_portmap": 60, "p2p_portmap_lease_seconds": 70,
	"p2p_probe_timeout_ms": 80, "p2p_handshake_timeout_ms": 90,
	"p2p_transport_timeout_ms": 100, "pprof_ip": 110, "pprof_port": 120,

	"web_ip": 10, "web_port": 20, "web_username": 30, "web_password": 40,
	"web_base_url": 50, "web_open_ssl": 60, "web_cert_file": 70, "web_key_file": 80,
	"totp_secret": 90, "x_nps_http_only": 120, "web_host": 130,
	"web_close_on_not_found": 140,

	"auth_key": 10, "auth_crypt_key": 20,

	"allow_user_register": 10, "register_email_verify": 20, "register_captcha": 30,
	"register_max_clients": 40, "register_max_tunnels": 50, "register_max_conn": 60,
	"register_upload_limit": 70, "register_download_limit": 80, "register_expire_days": 90,
	"register_rate_window": 100, "register_rate_limit": 110, "register_ip_rate_limit": 120,
	"register_email_code_ttl": 130, "register_email_code_cooldown": 140,
	"smtp_host": 150, "smtp_port": 160, "smtp_security": 170,
	"smtp_username": 180, "smtp_password": 190, "smtp_from": 200,

	"allow_user_login": 10, "allow_user_vkey_login": 20, "allow_user_change_username": 30,
	"allow_user_local": 40, "allow_secret_link": 50, "allow_secret_local": 60,
	"allow_remote_update": 70, "allow_time_limit": 80, "allow_flow_limit": 90,
	"flow_limit_type": 100, "allow_rate_limit": 110, "allow_tunnel_num_limit": 120,
	"allow_local_proxy": 130, "allow_connection_num_limit": 140,
	"allow_multi_ip": 150, "system_info_display": 160,

	"http_add_origin_header": 10, "http_proxy_response_timeout": 20,
	"http_cache": 30, "http_cache_length": 40,

	"allow_x_real_ip": 10, "trusted_proxy_ips": 20, "tls_enable": 30,
	"tls_bridge_port": 40, "disconnect_timeout": 50, "open_captcha": 60,
	"force_pow": 70, "pow_bits": 80, "login_ban_time": 90,
	"login_max_fail_times": 100, "login_ip_ban_time": 110,
	"login_user_ban_time": 120, "login_max_body": 130, "login_max_skew": 140,
	"ntp_server": 150, "ntp_interval": 160, "timezone": 170,
	"geoip_path": 180, "geosite_path": 190, "geo_reload_interval": 200,
	"acl_max_rules": 210, "dns_server": 220, "allow_ports": 230,
}

var serverConfigExtraKeys = []string{
	"https_default_cert_file", "https_default_key_file", "https_just_proxy", "hostPort", "sessionConfig",
	"runmode", "appname", "bridge_tcp_port", "bridge_kcp_ip", "bridge_tcp_ip", "bridge_tls_port",
}

func serverConfigPath() string {
	return filepath.Join(common.GetRunPath(), "conf", "nps.conf")
}

func serverConfigKeyValues(content string) map[string]string {
	values := make(map[string]string)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" || strings.ContainsAny(key, " \t[]") {
			continue
		}
		values[key] = strings.TrimSpace(parts[1])
	}
	return values
}

func serverConfigKeyFromLine(raw string) (string, bool) {
	line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", false
	}
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", false
	}
	key := strings.TrimSpace(parts[0])
	if key == "" || strings.ContainsAny(key, " \t[]") {
		return "", false
	}
	return key, true
}

func serverConfigSensitive(key string) bool {
	key = strings.ToLower(key)
	if strings.HasSuffix(key, "_key_file") {
		// A TLS/session key file setting contains only the path. Hiding the path
		// makes the configuration impossible to audit and does not protect the
		// private-key contents, which NPS never reads into this page.
		return false
	}
	return strings.Contains(key, "password") || strings.Contains(key, "secret") ||
		strings.Contains(key, "vkey") || strings.Contains(key, "auth_key") ||
		strings.Contains(key, "api_key") || strings.Contains(key, "instance_key") ||
		strings.Contains(key, "private")
}

func serverConfigMetaFor(key, defaultValue string) serverConfigMeta {
	if meta, ok := serverConfigMetaByKey[key]; ok {
		if meta.Type == "" {
			meta.Type = inferServerConfigType(key, defaultValue)
		}
		if meta.Sensitive || serverConfigSensitive(key) {
			meta.Sensitive = true
			meta.Type = "secret"
		}
		return meta
	}
	meta := serverConfigMeta{
		Label:       key,
		Category:    "兼容/自定义",
		Description: "该配置由当前运行版本或历史版本识别；请确认用途后再修改。",
		Type:        inferServerConfigType(key, defaultValue),
		Editable:    false,
		Optional:    true,
		Sensitive:   serverConfigSensitive(key),
	}
	if meta.Sensitive {
		meta.Type = "secret"
	}
	return meta
}

func inferServerConfigType(key, value string) string {
	// strconv.ParseBool also accepts "0" and "1". Those values are common
	// for disabled listener ports and numeric limits, so treating them as a
	// boolean would render an integer field as a true/false selector and make
	// the configuration impossible to save. The shipped template uses the
	// explicit true/false spelling for booleans; metadata still overrides this
	// inference for legacy values that need a different representation.
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "true" || normalized == "false" {
		return "bool"
	}
	if _, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return "int"
	}
	if key == "bridge_type" {
		return "select"
	}
	if serverConfigSensitive(key) {
		return "secret"
	}
	return "text"
}

// serverConfigValueEnabled is only used for optional fields. A zero/false
// value is a valid value for required settings, but for an optional listener,
// secret, path, or feature knob it means the feature is not enabled and the
// panel should offer to omit the key entirely.
func serverConfigValueEnabled(meta serverConfigMeta, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch meta.Type {
	case "bool":
		return strings.EqualFold(value, "true")
	case "int":
		n, err := strconv.Atoi(value)
		return err == nil && n != 0
	default:
		return true
	}
}

func serverConfigCatalogKeys() []string {
	keys := make([]string, 0, len(serverConfigMetaByKey)+len(serverConfigExtraKeys))
	seen := make(map[string]struct{})
	for key := range serverConfigMetaByKey {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range serverConfigKeyValues(npsconf.DefaultNpsConf) {
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	for _, key := range serverConfigExtraKeys {
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func serverConfigSections(content string) ([]serverConfigSectionView, []serverConfigUnknownView, error) {
	values := serverConfigKeyValues(content)
	templateValues := serverConfigKeyValues(npsconf.DefaultNpsConf)
	keys := serverConfigCatalogKeys()
	seen := make(map[string]struct{}, len(keys))
	sections := make(map[string][]serverConfigFieldView)
	for _, key := range keys {
		seen[key] = struct{}{}
		meta := serverConfigMetaFor(key, templateValues[key])
		if _, ok := serverConfigMetaByKey[key]; ok {
			meta.Editable = true
		}
		value, present := values[key]
		if !present {
			value = templateValues[key]
		}
		if meta.Type == "bool" {
			value = strings.ToLower(strings.TrimSpace(value))
		}
		field := serverConfigFieldView{
			Key: key, Label: meta.Label, Category: meta.Category, Description: meta.Description,
			Type: meta.Type, Value: value, Options: meta.Options, Editable: meta.Editable,
			Optional: meta.Optional, Sensitive: meta.Sensitive, Present: present,
		}
		field.DisplayValue = value
		if strings.TrimSpace(field.DisplayValue) == "" {
			field.DisplayValue = "未配置"
		}
		field.Enabled = present && serverConfigValueEnabled(meta, value)
		if !meta.Optional {
			// A required key missing from an older configuration must remain
			// editable so the administrator can repair it from the panel.
			field.Enabled = true
		}
		if field.Sensitive {
			field.DisplayValue = "未配置"
			if present && strings.TrimSpace(value) != "" {
				field.DisplayValue = "已配置（留空保持）"
			}
			field.Value = ""
		}
		sections[meta.Category] = append(sections[meta.Category], field)
	}

	unknown := make([]serverConfigUnknownView, 0)
	for key, value := range values {
		if _, ok := seen[key]; ok {
			continue
		}
		sensitive := serverConfigSensitive(key)
		display := value
		rawValue := value
		if sensitive && strings.TrimSpace(value) != "" {
			display = "已配置（只读）"
			rawValue = ""
		}
		unknown = append(unknown, serverConfigUnknownView{Key: key, Value: rawValue, DisplayValue: display, Sensitive: sensitive})
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].Key < unknown[j].Key })

	result := make([]serverConfigSectionView, 0, len(sections))
	for _, category := range serverConfigCategoryOrder {
		fields := sections[category]
		if len(fields) == 0 {
			continue
		}
		sort.SliceStable(fields, func(i, j int) bool {
			left := serverConfigKeyPriority[fields[i].Key]
			right := serverConfigKeyPriority[fields[j].Key]
			if left == 0 {
				left = 10000
			}
			if right == 0 {
				right = 10000
			}
			if left != right {
				return left < right
			}
			return fields[i].Key < fields[j].Key
		})
		description := serverConfigCategoryDescriptions[category]
		if description == "" {
			description = "按业务分组维护此区域的服务端配置。"
		}
		result = append(result, serverConfigSectionView{Name: category, Description: description, Fields: fields})
	}
	return result, unknown, nil
}

func filterServerConfigSections(sections []serverConfigSectionView, excluded map[string]struct{}) []serverConfigSectionView {
	if len(excluded) == 0 {
		return sections
	}
	result := make([]serverConfigSectionView, 0, len(sections))
	for _, section := range sections {
		fields := make([]serverConfigFieldView, 0, len(section.Fields))
		for _, field := range section.Fields {
			if _, skip := excluded[field.Key]; skip {
				continue
			}
			fields = append(fields, field)
		}
		if len(fields) == 0 {
			continue
		}
		section.Fields = fields
		result = append(result, section)
	}
	return result
}

func certificateConfigSections(content string) ([]serverConfigSectionView, serverConfigSummary, error) {
	sections, _, err := serverConfigSections(content)
	if err != nil {
		return nil, serverConfigSummary{}, err
	}
	fields := make(map[string]serverConfigFieldView)
	for _, section := range sections {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}
	result := make([]serverConfigSectionView, 0, len(certificateConfigGroups))
	for _, group := range certificateConfigGroups {
		groupFields := make([]serverConfigFieldView, 0, len(group.Keys))
		for _, key := range group.Keys {
			if field, ok := fields[key]; ok {
				groupFields = append(groupFields, field)
			}
		}
		if len(groupFields) == 0 {
			continue
		}
		result = append(result, serverConfigSectionView{
			Name:        group.Name,
			Description: group.Description,
			Fields:      groupFields,
		})
	}
	return result, summarizeServerConfigSections(result, nil), nil
}

func registrationConfigSections(content string) ([]serverConfigSectionView, serverConfigSummary, error) {
	sections, _, err := serverConfigSections(content)
	if err != nil {
		return nil, serverConfigSummary{}, err
	}
	fields := make(map[string]serverConfigFieldView)
	for _, section := range sections {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}
	result := make([]serverConfigSectionView, 0, len(registrationConfigGroups))
	for _, group := range registrationConfigGroups {
		groupFields := make([]serverConfigFieldView, 0, len(group.Keys))
		for _, key := range group.Keys {
			if field, ok := fields[key]; ok {
				groupFields = append(groupFields, field)
			}
		}
		if len(groupFields) == 0 {
			continue
		}
		result = append(result, serverConfigSectionView{
			Name:        group.Name,
			Description: group.Description,
			Fields:      groupFields,
		})
	}
	return result, summarizeServerConfigSections(result, nil), nil
}

func generalServerConfigExcludedKeys() map[string]struct{} {
	excluded := make(map[string]struct{}, len(certificateConfigKeySet)+len(registrationConfigKeySet))
	for key := range certificateConfigKeySet {
		excluded[key] = struct{}{}
	}
	for key := range registrationConfigKeySet {
		excluded[key] = struct{}{}
	}
	return excluded
}

func summarizeServerConfigSections(sections []serverConfigSectionView, unknown []serverConfigUnknownView) serverConfigSummary {
	summary := serverConfigSummary{Sections: len(sections), ReadOnly: len(unknown)}
	for _, section := range sections {
		summary.Fields += len(section.Fields)
		for _, field := range section.Fields {
			if field.Editable {
				summary.Editable++
			} else {
				summary.ReadOnly++
			}
			if field.Enabled {
				summary.Enabled++
			}
			if field.Sensitive {
				summary.Sensitive++
			}
			if !field.Present && !field.Optional {
				summary.Missing++
			}
		}
	}
	summary.Fields += len(unknown)
	return summary
}

func (s *GlobalController) Config() {
	if !s.RequireAdmin() {
		return
	}
	content, err := os.ReadFile(serverConfigPath())
	if err != nil {
		s.Data["config_error"] = err.Error()
	} else {
		sections, unknown, _ := serverConfigSections(string(content))
		sections = filterServerConfigSections(sections, generalServerConfigExcludedKeys())
		s.Data["config_sections"] = sections
		s.Data["config_unknown"] = unknown
		s.Data["config_summary"] = summarizeServerConfigSections(sections, unknown)
		if err := validateServerConfigSource(string(content)); err != nil {
			s.Data["config_validation_error"] = err.Error()
		}
	}
	s.Data["menu"] = "global"
	s.SetInfo("server config")
	s.Data["config_path"] = serverConfigPath()
	s.display("global/config")
}

// Registration presents the registration and new-user default limits as a
// dedicated business settings page while continuing to store every value in
// nps.conf.
func (s *GlobalController) Registration() {
	if !s.RequireAdmin() {
		return
	}
	s.Data["menu"] = "global"
	s.SetInfo("registration settings")
	s.Data["config_path"] = serverConfigPath()
	content, err := os.ReadFile(serverConfigPath())
	if err != nil {
		s.Data["registration_config_error"] = err.Error()
		s.Data["registration_config_sections"] = []serverConfigSectionView{}
		s.Data["registration_config_summary"] = serverConfigSummary{}
	} else {
		sections, summary, sectionErr := registrationConfigSections(string(content))
		if sectionErr != nil {
			s.Data["registration_config_error"] = sectionErr.Error()
		}
		s.Data["registration_config_sections"] = sections
		s.Data["registration_config_summary"] = summary
	}
	s.display("global/registration")
}

func registrationConfigKeys() []string {
	keys := make([]string, 0, len(registrationConfigKeySet))
	for _, group := range registrationConfigGroups {
		keys = append(keys, group.Keys...)
	}
	return keys
}

// SaveRegistration updates only fields owned by the registration tab.
func (s *GlobalController) SaveRegistration() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	path := serverConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		s.AjaxErr("读取 nps.conf 失败: " + err.Error())
		return
	}
	updates, err := parseServerConfigUpdatesForKeys(s.Ctx.Request, string(content), registrationConfigKeys())
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	changed, err := writeServerConfig(path, updates)
	if err != nil {
		s.AjaxErr("保存用户注册配置失败: " + err.Error())
		return
	}
	s.auditMutation("global.registration_config.update", "nps.conf", 0, 0, "", nil, map[string]string{"changed": strconv.Itoa(len(changed))})
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":           1,
		"msg":              "用户注册配置已保存，需要重启 NPS 才会生效",
		"changed":          changed,
		"restart_required": len(changed) > 0,
	})
	s.ServeJSON()
}

func parseServerConfigUpdates(request *http.Request, content string) ([]serverConfigUpdate, error) {
	keys := make([]string, 0, len(serverConfigMetaByKey))
	for key := range serverConfigMetaByKey {
		keys = append(keys, key)
	}
	return parseServerConfigUpdatesForKeys(request, content, keys)
}

func parseServerConfigUpdatesForKeys(request *http.Request, content string, keys []string) ([]serverConfigUpdate, error) {
	if request == nil {
		return nil, errors.New("请求无效")
	}
	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	current := serverConfigKeyValues(content)
	defaults := serverConfigKeyValues(npsconf.DefaultNpsConf)
	updates := make([]serverConfigUpdate, 0)
	for _, key := range keys {
		if _, supported := serverConfigMetaByKey[key]; !supported {
			continue
		}
		meta := serverConfigMetaFor(key, defaults[key])
		// Entries in serverConfigMetaByKey are the explicitly supported
		// editable catalog. The zero value of Meta.Editable keeps the metadata
		// concise, so mark it here before parsing the submitted form.
		meta.Editable = true
		raw := strings.TrimSpace(request.FormValue("config_" + key))
		if strings.ContainsAny(raw, "\r\n") {
			switch key {
			case "trusted_proxy_ips", "allow_ports", "p2p_stun_servers":
				lines := strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' || r == ',' })
				raw = strings.Join(lines, ",")
			default:
				return nil, fmt.Errorf("配置 %s 不能包含换行", key)
			}
		}
		enabled := true
		if meta.Optional {
			enabled = request.FormValue("config_enabled_"+key) != ""
		}
		clearSecret := request.FormValue("config_clear_"+key) != ""
		if meta.Optional && !enabled {
			if _, present := current[key]; present {
				updates = append(updates, serverConfigUpdate{Key: key, Omit: true, Secret: meta.Sensitive})
			}
			continue
		}
		if meta.Sensitive && raw == "" && !clearSecret {
			// A masked secret is intentionally submitted empty to preserve the
			// current value rather than accidentally clearing credentials.
			continue
		}
		if clearSecret {
			if !meta.Optional {
				return nil, fmt.Errorf("配置 %s 不能清空", key)
			}
			updates = append(updates, serverConfigUpdate{Key: key, Omit: true, Secret: true})
			continue
		}
		if err := validateServerConfigValue(key, meta, raw); err != nil {
			return nil, err
		}
		if raw == "" && meta.Optional {
			updates = append(updates, serverConfigUpdate{Key: key, Omit: true, Secret: meta.Sensitive})
			continue
		}
		updates = append(updates, serverConfigUpdate{Key: key, Value: raw, Secret: meta.Sensitive})
	}
	if err := validateServerConfigUpdateSet(current, updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func effectiveServerConfigValues(current map[string]string, updates []serverConfigUpdate) map[string]string {
	values := make(map[string]string, len(current)+len(updates))
	for key, value := range current {
		values[key] = value
	}
	for _, update := range updates {
		if update.Omit {
			delete(values, update.Key)
			continue
		}
		values[update.Key] = update.Value
	}
	return values
}

func validateServerConfigUpdateSet(current map[string]string, updates []serverConfigUpdate) error {
	values := effectiveServerConfigValues(current, updates)
	if strings.EqualFold(strings.TrimSpace(values["web_open_ssl"]), "true") {
		if strings.TrimSpace(values["web_cert_file"]) == "" || strings.TrimSpace(values["web_key_file"]) == "" {
			return errors.New("启用管理面板 HTTPS 时，证书路径和私钥路径必须同时填写")
		}
	}
	bridgeCertSet := strings.TrimSpace(values["bridge_cert_file"]) != ""
	bridgeKeySet := strings.TrimSpace(values["bridge_key_file"]) != ""
	if bridgeCertSet != bridgeKeySet {
		return errors.New("Bridge TLS 证书和私钥必须成对配置")
	}
	registerPort := strings.TrimSpace(values["ip_register_port"])
	if registerPort != "" {
		port, err := strconv.Atoi(registerPort)
		if err != nil || port < 0 || port > 65535 {
			return errors.New("IP 登记监听端口范围必须是 0-65535")
		}
	}
	certSet := strings.TrimSpace(values["ip_register_cert_file"]) != ""
	keySet := strings.TrimSpace(values["ip_register_key_file"]) != ""
	if certSet != keySet {
		return errors.New("IP 登记 HTTPS 证书和私钥必须成对配置")
	}
	if strings.EqualFold(strings.TrimSpace(values["register_email_verify"]), "true") {
		if strings.TrimSpace(values["smtp_host"]) == "" {
			return errors.New("启用注册邮箱验证时，SMTP 主机不能为空")
		}
		port, err := strconv.Atoi(strings.TrimSpace(values["smtp_port"]))
		if err != nil || port < 1 || port > 65535 {
			return errors.New("启用注册邮箱验证时，SMTP 端口必须是 1-65535")
		}
		if strings.TrimSpace(values["smtp_from"]) == "" {
			return errors.New("启用注册邮箱验证时，邮件发件人不能为空")
		}
		username := strings.TrimSpace(values["smtp_username"])
		password := strings.TrimSpace(values["smtp_password"])
		if username != "" && password == "" {
			return errors.New("填写 SMTP 用户名时必须同时配置 SMTP 密码")
		}
	}
	return nil
}

func validateServerConfigValue(key string, meta serverConfigMeta, value string) error {
	if value == "" {
		if meta.Optional {
			return nil
		}
		return fmt.Errorf("配置 %s 不能为空", key)
	}
	switch meta.Type {
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("配置 %s 必须是 true 或 false", key)
		}
	case "int":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("配置 %s 必须是非负整数", key)
		}
		if strings.Contains(key, "port") && n > 65535 {
			return fmt.Errorf("配置 %s 端口范围必须是 0-65535", key)
		}
	case "select":
		valid := false
		for _, option := range meta.Options {
			if value == option {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("配置 %s 的值无效", key)
		}
	}
	if key == "log_level" {
		level := strings.ToLower(strings.TrimSpace(value))
		for _, name := range []string{"trace", "debug", "info", "informational", "notice", "warn", "warning", "error", "fatal", "critical", "panic", "alert", "emergency", "off", "none"} {
			if level == name {
				return nil
			}
		}
		n, err := strconv.Atoi(level)
		if err != nil || n < 0 || n > 7 {
			return errors.New("log_level 必须是 0-7 或受支持的日志级别名称")
		}
	}
	if key == "auth_crypt_key" && len(value) != 16 {
		return errors.New("auth_crypt_key 必须恰好为 16 个字符")
	}
	if (key == "bridge_path" || key == "bridge_show_path") && !strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s 必须以 / 开头", key)
	}
	if key == "web_base_url" && value != "" && !strings.HasPrefix(value, "/") {
		return errors.New("web_base_url 必须为空或以 / 开头")
	}
	if strings.HasPrefix(key, "register_") && (strings.HasSuffix(key, "_days") || strings.HasSuffix(key, "_limit") ||
		strings.HasSuffix(key, "_window") || strings.HasSuffix(key, "_ttl") || strings.HasSuffix(key, "_cooldown")) {
		n, _ := strconv.Atoi(value)
		if n > 36500 {
			return fmt.Errorf("配置 %s 的数值过大", key)
		}
	}
	if key == "register_email_code_ttl" {
		n, _ := strconv.Atoi(value)
		if n < 60 {
			return errors.New("register_email_code_ttl 不能小于 60 秒")
		}
	}
	if key == "smtp_from" && value != "" && !strings.Contains(value, "@") {
		return errors.New("smtp_from 必须是有效的邮箱地址")
	}
	return nil
}

func writeServerConfig(path string, updates []serverConfigUpdate) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	original := string(data)
	updateByKey := make(map[string]serverConfigUpdate, len(updates))
	for _, update := range updates {
		updateByKey[update.Key] = update
	}
	seen := make(map[string]struct{})
	changed := make([]string, 0)
	lines := strings.SplitAfter(original, "\n")
	result := make([]string, 0, len(lines)+len(updates)+2)
	for _, line := range lines {
		key, ok := serverConfigKeyFromLine(line)
		update, hasUpdate := updateByKey[key]
		if !ok || !hasUpdate {
			result = append(result, line)
			continue
		}
		seen[key] = struct{}{}
		if update.Omit {
			changed = append(changed, key)
			continue
		}
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		replacement := key + "=" + update.Value + newline
		if strings.TrimSpace(strings.TrimSuffix(line, "\n")) != strings.TrimSpace(strings.TrimSuffix(replacement, "\n")) {
			changed = append(changed, key)
		}
		result = append(result, replacement)
	}
	missing := make([]string, 0)
	for _, update := range updates {
		if _, exists := seen[update.Key]; exists || update.Omit {
			continue
		}
		missing = append(missing, update.Key+"="+update.Value)
		changed = append(changed, update.Key)
	}
	if len(missing) > 0 {
		text := strings.Join(result, "")
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n# Maintained by the NPS Web configuration panel.\n" + strings.Join(missing, "\n") + "\n"
		result = []string{text}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	updated := strings.Join(result, "")
	if err := writeServerConfigAtomically(path, []byte(updated)); err != nil {
		return nil, err
	}
	return uniqueStrings(changed), nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func writeServerConfigAtomically(path string, data []byte) error {
	target := path
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("解析配置文件软链接失败: %w", err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("配置文件不是普通文件: %s", target)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".nps.conf.web-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

func (s *GlobalController) SaveConfig() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	path := serverConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		s.AjaxErr("读取 nps.conf 失败: " + err.Error())
		return
	}
	updates, err := parseServerConfigUpdates(s.Ctx.Request, string(content))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	changed, err := writeServerConfig(path, updates)
	if err != nil {
		s.AjaxErr("保存 nps.conf 失败: " + err.Error())
		return
	}
	s.auditMutation("server_config.update", "nps.conf", 0, 0, "", nil, map[string]string{"changed": strconv.Itoa(len(changed))})
	s.Data["json"] = map[string]interface{}{
		"status":           1,
		"msg":              "配置已保存，需要重启 NPS 才会生效",
		"changed":          changed,
		"restart_required": len(changed) > 0,
	}
	s.ServeJSON()
}

// SaveCertificateConfig updates only fields owned by the certificate settings
// page. Restricting the key set prevents an isolated form submission from
// removing unrelated optional keys that were never rendered on that page.
func (s *GlobalController) SaveCertificateConfig() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	path := serverConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		s.AjaxErr("读取 nps.conf 失败: " + err.Error())
		return
	}
	keys := make([]string, 0)
	for _, group := range certificateConfigGroups {
		keys = append(keys, group.Keys...)
	}
	updates, err := parseServerConfigUpdatesForKeys(s.Ctx.Request, string(content), keys)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	changed, err := writeServerConfig(path, updates)
	if err != nil {
		s.AjaxErr("保存证书配置失败: " + err.Error())
		return
	}
	s.auditMutation("global.certificate_config.update", "nps.conf", 0, 0, "", nil, map[string]string{"changed": strconv.Itoa(len(changed))})
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":           1,
		"msg":              "证书设置已保存，需要重启 NPS 才会生效",
		"changed":          changed,
		"restart_required": len(changed) > 0,
	})
	s.ServeJSON()
}

func (s *GlobalController) Restart() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	// A foreground/local development process has no service manager to ask to
	// restart. Returning a clear, non-error response lets the UI explain that
	// the operator must restart the process manually instead of pretending the
	// new listener configuration is already active.
	if !runningUnderServiceManager() {
		s.Data["json"] = map[string]interface{}{
			"status":      0,
			"restartable": false,
			"msg":         "当前 NPS 不是由系统服务托管，请手动重启进程后配置才会生效",
		}
		s.ServeJSON()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		s.AjaxErr("无法定位 NPS 可执行文件: " + err.Error())
		return
	}
	confPath := common.GetRunPath()
	command := exec.Command(executable, "restart", "-conf_path="+confPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		s.AjaxErr("发起 NPS 重启失败: " + err.Error())
		return
	}
	go func() {
		_ = command.Wait()
	}()
	s.auditMutation("server.restart", "nps", 0, 0, "", nil, nil)
	s.Data["json"] = map[string]interface{}{
		"status":      1,
		"restartable": true,
		"msg":         "已发起 NPS 重启请求，连接可能会短暂中断。",
	}
	s.ServeJSON()
}

// runningUnderServiceManager reports whether the current process was launched
// by the NPS service wrapper. Service arguments may include -conf_path or
// other flags before the trailing "service" marker, so checking os.Args[1]
// alone would incorrectly refuse a restart for a normally installed service.
func runningUnderServiceManager() bool {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "service" {
			return true
		}
	}
	return false
}
