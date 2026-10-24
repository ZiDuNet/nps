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
	"client_history_limit":      {Label: "客户端连接历史条数", Category: "Bridge 与传输", Description: "每个客户端保留的最近连接历史数量，0 表示关闭。"},

	"public_vkey":           {Label: "公共验证密钥", Category: "客户端认证与 IP 登记", Description: "允许使用公共密钥连接的客户端；留空表示关闭。", Optional: true, Sensitive: true},
	"auth_time_window":      {Label: "握手时间窗口", Category: "客户端认证与 IP 登记", Description: "新版 NPC 握手允许的最大时间偏差，单位秒。"},
	"ip_limit":              {Label: "客户端来源 IP 限制", Category: "客户端认证与 IP 登记", Description: "按客户端来源地址限制 Bridge 连接；兼容旧部署。", Type: "bool", Optional: true},
	"public_register_key":   {Label: "公共 IP 登记密钥", Category: "客户端认证与 IP 登记", Description: "填写后启用 /ip/register 公共登记入口；留空关闭。", Optional: true, Sensitive: true},
	"ip_register_max_hours": {Label: "IP 登记最长时长", Category: "客户端认证与 IP 登记", Description: "公共 IP 登记的默认最长有效期，单位小时。"},
	"ip_register_ip":        {Label: "独立登记监听地址", Category: "客户端认证与 IP 登记", Description: "独立 IP 登记监听的本机地址；端口为 0 时可以关闭该项。", Optional: true},
	"ip_register_port":      {Label: "独立登记监听端口", Category: "客户端认证与 IP 登记", Description: "端口为 0 时仅使用管理面板的 /ip/register。", Optional: true},
	"ip_register_cert_file": {Label: "登记 HTTPS 证书", Category: "客户端认证与 IP 登记", Description: "独立登记监听的证书路径；需与私钥同时配置。", Optional: true},
	"ip_register_key_file":  {Label: "登记 HTTPS 私钥", Category: "客户端认证与 IP 登记", Description: "独立登记监听的私钥路径；需与证书同时配置。", Optional: true, Sensitive: true},

	"flow_store_interval": {Label: "流量持久化间隔", Category: "日志与审计", Description: "流量数据持久化间隔，单位分钟；不启用流量持久化时关闭该项。", Optional: true},
	"log_level":           {Label: "日志级别", Category: "日志与审计", Description: "支持 0 到 7，也支持 trace/debug/info/warn/error/fatal/panic/off。", Type: "text"},
	"log_path":            {Label: "日志文件路径", Category: "日志与审计", Description: "主日志文件路径；留空时使用运行目录默认路径。", Optional: true},
	"log_max_size":        {Label: "单文件大小上限", Category: "日志与审计", Description: "日志轮转单文件大小上限，单位 MB；不启用时使用内置默认值。", Optional: true},
	"log_max_files":       {Label: "日志归档数量", Category: "日志与审计", Description: "日志归档保留数量，0 表示不限制。", Optional: true},
	"log_max_days":        {Label: "日志归档天数", Category: "日志与审计", Description: "日志归档保留天数，0 表示不按天数清理。", Optional: true},
	"log_compress":        {Label: "压缩日志归档", Category: "日志与审计", Description: "是否压缩轮转后的日志归档。", Optional: true},
	"log_rotate_daily":    {Label: "按日期轮转日志", Category: "日志与审计", Description: "日期变化时是否轮转日志。", Optional: true},
	"audit_log_path":      {Label: "审计日志路径", Category: "日志与审计", Description: "管理操作审计日志路径；留空时关闭独立审计文件。", Optional: true},

	"p2p_ip":     {Label: "P2P 监听地址", Category: "P2P 与调试", Description: "P2P 中继监听地址；留空表示关闭。", Optional: true},
	"p2p_port":   {Label: "P2P 监听端口", Category: "P2P 与调试", Description: "配置后启用 P2P 中继；留空或 0 表示关闭。", Type: "int", Optional: true},
	"pprof_ip":   {Label: "pprof 监听地址", Category: "P2P 与调试", Description: "性能调试端点监听地址；仅建议内网临时开启。", Optional: true},
	"pprof_port": {Label: "pprof 监听端口", Category: "P2P 与调试", Description: "性能调试端点监听端口；留空或 0 表示关闭。", Type: "int", Optional: true},

	"web_host":          {Label: "管理面板域名", Category: "Web 管理面板", Description: "端口复用场景下用于区分管理面板流量；不做端口复用时可关闭。", Optional: true},
	"web_username":      {Label: "管理员用户名", Category: "Web 管理面板", Description: "管理员登录账号。"},
	"web_password":      {Label: "管理员密码", Category: "Web 管理面板", Description: "留空保持现有密码；填写新密码后需重启生效。", Sensitive: true},
	"totp_secret":       {Label: "管理员 TOTP 密钥", Category: "Web 管理面板", Description: "留空关闭双因素认证。", Optional: true, Sensitive: true},
	"web_port":          {Label: "管理面板端口", Category: "Web 管理面板", Description: "管理面板监听端口。"},
	"web_ip":            {Label: "管理面板监听地址", Category: "Web 管理面板", Description: "管理面板监听的本机地址。"},
	"web_base_url":      {Label: "管理面板 URL 前缀", Category: "Web 管理面板", Description: "反向代理到子路径时填写，例如 /nps。", Optional: true},
	"instance_key_file": {Label: "会话实例密钥路径", Category: "Web 管理面板", Description: "留空时使用 conf/instance.key。", Optional: true, Sensitive: true},
	"web_open_ssl":      {Label: "管理面板 HTTPS", Category: "Web 管理面板", Description: "是否直接为管理面板启用 HTTPS。"},
	"web_cert_file":     {Label: "管理面板证书路径", Category: "Web 管理面板", Description: "web_open_ssl=true 时使用；关闭 HTTPS 时可不写入。", Optional: true},
	"web_key_file":      {Label: "管理面板私钥路径", Category: "Web 管理面板", Description: "web_open_ssl=true 时使用；关闭 HTTPS 时可不写入。", Optional: true, Sensitive: true},

	"auth_key":       {Label: "Web API 密钥", Category: "Web API 认证", Description: "签名 API 请求使用的密钥。", Sensitive: true},
	"auth_crypt_key": {Label: "API 密钥加密密钥", Category: "Web API 认证", Description: "必须恰好为 16 个字符。", Sensitive: true},

	"allow_user_login":           {Label: "允许普通用户登录", Category: "用户与资源权限", Description: "是否启用多用户面板登录。"},
	"allow_user_register":        {Label: "允许普通用户注册", Category: "用户与资源权限", Description: "是否允许公开注册面板账号。"},
	"allow_user_change_username": {Label: "允许修改用户名", Category: "用户与资源权限", Description: "普通用户是否可以修改自己的用户名。"},
	"allow_flow_limit":           {Label: "允许流量配额", Category: "用户与资源权限", Description: "是否在客户端页面维护流量上限。"},
	"allow_rate_limit":           {Label: "允许带宽配额", Category: "用户与资源权限", Description: "是否在客户端和隧道页面维护带宽上限。"},
	"allow_tunnel_num_limit":     {Label: "允许隧道数量配额", Category: "用户与资源权限", Description: "是否限制客户端和用户的隧道数量。"},
	"allow_local_proxy":          {Label: "允许本地代理", Category: "用户与资源权限", Description: "是否允许隧道使用服务端本地代理访问目标。"},
	"allow_connection_num_limit": {Label: "允许连接数配额", Category: "用户与资源权限", Description: "是否维护客户端最大连接数。"},
	"allow_multi_ip":             {Label: "允许绑定监听地址", Category: "用户与资源权限", Description: "是否允许每条隧道绑定不同的服务端监听 IP。"},
	"system_info_display":        {Label: "显示系统监控", Category: "用户与资源权限", Description: "仪表盘是否显示 CPU、内存和网络信息。"},

	"http_add_origin_header": {Label: "添加来源请求头", Category: "HTTP 转发与缓存", Description: "向后端添加客户端真实 IP 相关请求头。"},
	"http_cache":             {Label: "启用 HTTP 缓存", Category: "HTTP 转发与缓存", Description: "是否启用 HTTP 响应缓存。"},
	"http_cache_length":      {Label: "HTTP 缓存队列长度", Category: "HTTP 转发与缓存", Description: "HTTP 缓存队列长度；关闭 HTTP 缓存时可不写入。", Optional: true},

	"disconnect_timeout": {Label: "Bridge 断开超时", Category: "运行与安全", Description: "未收到 mux 心跳回包的最大等待时间，单位秒。"},
	"open_captcha":       {Label: "登录验证码", Category: "运行与安全", Description: "是否启用管理面板登录验证码。"},
	"force_pow":          {Label: "登录工作量证明", Category: "运行与安全", Description: "是否启用登录 PoW 防护。"},
	"pow_bits":           {Label: "PoW 难度", Category: "运行与安全", Description: "SHA-256 前导零位数。"},
	"tls_enable":         {Label: "Bridge TLS", Category: "运行与安全", Description: "是否启用 Bridge TLS 加密监听。"},
	"tls_bridge_port":    {Label: "Bridge TLS 端口", Category: "运行与安全", Description: "Bridge TLS 加密监听端口；关闭 Bridge TLS 时可不写入。", Optional: true},

	"allow_ports": {Label: "允许映射端口范围", Category: "运行与安全", Description: "限制客户端可映射的服务端端口，例如 9001-9009,10001；留空表示不限制。", Optional: true},
}

var serverConfigCategoryOrder = []string{
	"HTTP(S) 入口", "Bridge 与传输", "客户端认证与 IP 登记", "日志与审计", "P2P 与调试",
	"Web 管理面板", "Web API 认证", "用户与资源权限", "HTTP 转发与缓存", "运行与安全", "兼容/自定义",
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
	return strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "vkey") || strings.Contains(key, "auth_key") || strings.Contains(key, "instance_key") || strings.Contains(key, "private") || strings.Contains(key, "key_file")
}

func serverConfigMetaFor(key, defaultValue string) serverConfigMeta {
	if meta, ok := serverConfigMetaByKey[key]; ok {
		if meta.Type == "" {
			meta.Type = inferServerConfigType(key, defaultValue)
		}
		return meta
	}
	meta := serverConfigMeta{
		Label:       key,
		Category:    "兼容/自定义",
		Description: "该配置由当前运行版本或历史版本识别；请确认用途后再修改。",
		Type:        inferServerConfigType(key, defaultValue),
		Editable:    false,
		Sensitive:   serverConfigSensitive(key),
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
			field.Enabled = present
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
		sort.SliceStable(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
		result = append(result, serverConfigSectionView{Name: category, Description: "保存后需要重启 NPS 才会应用。", Fields: fields})
	}
	return result, unknown, nil
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
		s.Data["config_sections"] = sections
		s.Data["config_unknown"] = unknown
	}
	s.Data["menu"] = "global"
	s.SetInfo("server config")
	s.Data["config_path"] = serverConfigPath()
	s.display("global/config")
}

func parseServerConfigUpdates(request *http.Request, content string) ([]serverConfigUpdate, error) {
	if request == nil {
		return nil, errors.New("请求无效")
	}
	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	current := serverConfigKeyValues(content)
	defaults := serverConfigKeyValues(npsconf.DefaultNpsConf)
	updates := make([]serverConfigUpdate, 0)
	for key, meta := range serverConfigMetaByKey {
		meta = serverConfigMetaFor(key, defaults[key])
		// Entries in serverConfigMetaByKey are the explicitly supported
		// editable catalog. The zero value of Meta.Editable keeps the metadata
		// concise, so mark it here before parsing the submitted form.
		meta.Editable = true
		raw := strings.TrimSpace(request.FormValue("config_" + key))
		if strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("配置 %s 不能包含换行", key)
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
	if key == "bridge_path" && !strings.HasPrefix(value, "/") {
		return errors.New("bridge_path 必须以 / 开头")
	}
	if key == "web_base_url" && value != "" && !strings.HasPrefix(value, "/") {
		return errors.New("web_base_url 必须为空或以 / 开头")
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
