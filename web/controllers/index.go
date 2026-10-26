package controllers

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/server/traffic"

	"github.com/astaxie/beego"
)

type IndexController struct {
	BaseController
}

func normalizedTunnelMode(mode string) string {
	switch mode {
	case "tcp", "udp", "socks5", "mixProxy", "secret", "p2p", "file":
		return mode
	case "httpProxy":
		return mode
	default:
		return "tcp"
	}
}

// normalizeTargetType maps nps2 target_type values to the stored form. Any
// unknown or empty value falls back to tcp, which matches every record created
// before this field existed.
func normalizeTargetType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "udp", "all":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "tcp"
	}
}

// supportedTunnelMode validates the mode received from a form before any
// task state is generated or persisted. Unknown modes cannot be handed to
// server.NewMode because that would leave an unusable task in the database.
func supportedTunnelMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "tcp", "udp", "httpProxy", "socks5", "mixProxy", "secret", "p2p", "file":
		return true
	default:
		return false
	}
}

// tunnelSidebarMenu maps the internal proxy mode to the sidebar destination.
// Legacy HTTP/SOCKS records and the new mixed listener share one navigation
// entry so operators do not have to maintain duplicate proxy lists.
func tunnelSidebarMenu(mode string) string {
	switch normalizedTunnelMode(mode) {
	case "httpProxy", "socks5", "mixProxy":
		return "mixProxy"
	}
	return normalizedTunnelMode(mode)
}

func tunnelModeChangeAllowed(currentMode, desiredMode string) bool {
	if currentMode == desiredMode {
		return true
	}
	// The unified proxy form intentionally migrates legacy records without
	// asking the operator to recreate the listener.
	return desiredMode == "mixProxy" && (currentMode == "httpProxy" || currentMode == "socks5")
}

// tunnelTemplatePath keeps the mode-specific view entry points in one place.
// Each entry includes the shared form/list implementation, so old URLs remain
// compatible while the rendered page can be scoped to a single proxy mode.
func tunnelTemplatePath(mode, action string) string {
	return "index/tunnel/" + normalizedTunnelMode(mode) + "/" + action
}

// hostListRow is intentionally narrower than file.Host. The management list
// must never serialize filesystem certificate paths, private keys, or client
// credentials to the browser.
type hostListRow struct {
	Id                   int
	Host                 string
	PlatformDomainID     string
	Remark               string
	Location             string
	Scheme               string
	IsClose              bool
	AutoHttps            bool
	TargetIsHTTPS        bool
	HTTPSJustProxy       bool
	AutoSSL              bool
	TLSOffload           bool
	RegisteredOnly       bool
	AutoCORS             bool
	CompatMode           bool
	PlatformManaged      bool
	BasicAuthSet         bool
	HeaderChange         string
	ResponseHeaderChange string
	HostChange           string
	PathRewrite          string
	RedirectURL          string
	IpListPriority       int
	BlackIpList          []string
	WhiteIpList          []string
	HttpPort             string
	HttpsPort            string
	HealthCheckType      string
	HealthCheckTarget    string
	HealthCheckTimeout   int
	HealthMaxFail        int
	HealthCheckInterval  int
	HttpHealthUrl        string
	UploadLimit          int
	DownloadLimit        int
	UploadRate           int64
	DownloadRate         int64
	Client               hostListClient
	Target               hostListTarget
}

type hostListClient struct {
	Id        int
	Remark    string
	IsConnect bool
}

type hostListTarget struct {
	TargetStr     string
	LocalProxy    bool
	ProxyProtocol int
}

// tunnelListRow adds public access metadata to the historical Tunnel JSON
// shape without mutating the persisted model.  Existing clients still receive
// every Tunnel field they already used, while newer clients can consume an
// address that is assembled from the configured public host and tunnel port.
type tunnelListRow struct {
	*file.Tunnel
	AccessHost    string `json:"AccessHost,omitempty"`
	AccessAddr    string `json:"AccessAddr,omitempty"`
	AccessAddress string `json:"AccessAddress,omitempty"`
	AccessURL     string `json:"AccessURL,omitempty"`
	// Keep the resource-level rate fields explicit on the API row instead of
	// relying on the embedded Tunnel's omitempty fields.  A zero value means
	// "unlimited" (for limits) or "no bytes in the last sample" (for rates),
	// and callers should be able to distinguish that from a missing field.
	UploadLimit   int   `json:"UploadLimit"`
	DownloadLimit int   `json:"DownloadLimit"`
	UploadRate    int64 `json:"UploadRate"`
	DownloadRate  int64 `json:"DownloadRate"`
}

type platformDomainOption struct {
	ID                    string
	Wildcard              string
	CertificateConfigured bool
}

type hostDiagnosticResult struct {
	Host    string              `json:"host"`
	Path    string              `json:"path"`
	Scheme  string              `json:"scheme"`
	Matched bool                `json:"matched"`
	Reason  string              `json:"reason"`
	Rule    *hostDiagnosticRule `json:"rule,omitempty"`
}

type hostDiagnosticRule struct {
	ID              int    `json:"id"`
	Host            string `json:"host"`
	Location        string `json:"location"`
	Scheme          string `json:"scheme"`
	Remark          string `json:"remark"`
	Client          string `json:"client"`
	Target          string `json:"target"`
	PlatformManaged bool   `json:"platformManaged"`
}

func newHostListRows(hosts []*file.Host) []*hostListRow {
	rows := make([]*hostListRow, 0, len(hosts))
	for _, host := range hosts {
		if host == nil {
			continue
		}
		host.RLock()
		client, target := host.Client, host.Target
		row := &hostListRow{
			Id:                   host.Id,
			Host:                 host.Host,
			PlatformDomainID:     host.PlatformDomainID,
			Remark:               host.Remark,
			Location:             host.Location,
			Scheme:               host.Scheme,
			IsClose:              host.IsClose,
			AutoHttps:            host.AutoHttps,
			TargetIsHTTPS:        host.TargetIsHTTPS,
			HTTPSJustProxy:       host.HTTPSJustProxy,
			AutoSSL:              host.AutoSSL,
			TLSOffload:           host.TLSOffload,
			RegisteredOnly:       host.RegisteredOnly,
			AutoCORS:             host.AutoCORS,
			CompatMode:           host.CompatMode,
			PlatformManaged:      host.PlatformDomainID != "",
			BasicAuthSet:         file.BasicAuthConfigured(host.Auth),
			HeaderChange:         host.HeaderChange,
			ResponseHeaderChange: host.ResponseHeaderChange,
			HostChange:           host.HostChange,
			PathRewrite:          host.PathRewrite,
			RedirectURL:          host.RedirectURL,
			IpListPriority:       host.IpListPriority,
			BlackIpList:          append([]string(nil), host.BlackIpList...),
			WhiteIpList:          append([]string(nil), host.WhiteIpList...),
			HttpPort:             publicProxyPort(publicAccessPort("http_proxy_access_port", "http_proxy_port"), "80"),
			HttpsPort:            publicProxyPort(publicAccessPort("https_proxy_access_port", "https_proxy_port"), "443"),
		}
		host.RUnlock()
		row.UploadLimit, row.DownloadLimit = host.RateLimitSnapshot()
		health := file.HealthConfigSnapshot(&host.Health)
		row.HealthCheckType = health.HealthCheckType
		row.HealthCheckTarget = health.HealthCheckTarget
		row.HealthCheckTimeout = health.HealthCheckTimeout
		row.HealthMaxFail = health.HealthMaxFail
		row.HealthCheckInterval = health.HealthCheckInterval
		row.HttpHealthUrl = health.HttpHealthUrl
		row.UploadRate, row.DownloadRate = host.CurrentRates()

		if client != nil {
			client.RLock()
			row.Client = hostListClient{Id: client.Id, Remark: client.Remark, IsConnect: client.IsConnect}
			client.RUnlock()
		}
		if target != nil {
			target.RLock()
			row.Target = hostListTarget{TargetStr: target.TargetStr, LocalProxy: target.LocalProxy, ProxyProtocol: target.ProxyProtocol}
			target.RUnlock()
		}
		rows = append(rows, row)
	}
	return rows
}

func publicTunnelHost(requestHost string) string {
	if global := file.GetDb().GetGlobal(); global != nil {
		if host := publicServerHost(global.ServerUrl); host != "" {
			return host
		}
	}
	return publicServerHost(requestHost)
}

func tunnelAccessAddress(host string, port int) string {
	if port <= 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func newTunnelListRows(tunnels []*file.Tunnel, requestHost string) []*tunnelListRow {
	host := publicTunnelHost(requestHost)
	rows := make([]*tunnelListRow, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel == nil {
			continue
		}
		tunnel.RLock()
		port := tunnel.Port
		tunnel.RUnlock()
		uploadRate, downloadRate := tunnel.CurrentRates()
		uploadLimit, downloadLimit := tunnel.RateLimitSnapshot()
		accessAddr := tunnelAccessAddress(host, port)
		row := &tunnelListRow{
			Tunnel:        tunnel,
			AccessHost:    host,
			AccessAddr:    accessAddr,
			AccessAddress: accessAddr,
			AccessURL:     "",
			UploadLimit:   uploadLimit,
			DownloadLimit: downloadLimit,
			UploadRate:    uploadRate,
			DownloadRate:  downloadRate,
		}
		if accessAddr != "" {
			row.AccessURL = "tcp://" + accessAddr
		}
		rows = append(rows, row)
	}
	return rows
}

// tunnelCreatedResponse assembles the create/copy success payload with the
// public access address metadata already present on list rows, so API callers
// do not have to refetch the list to learn the assigned address.
func tunnelCreatedResponse(task *file.Tunnel, requestHost string) map[string]interface{} {
	payload := map[string]interface{}{"status": 1, "msg": "add success", "id": task.Id}
	rows := newTunnelListRows([]*file.Tunnel{task}, requestHost)
	if len(rows) == 1 {
		row := rows[0]
		payload["AccessHost"] = row.AccessHost
		payload["AccessAddr"] = row.AccessAddr
		payload["AccessAddress"] = row.AccessAddress
		payload["AccessURL"] = row.AccessURL
	}
	return payload
}

func publicProxyPort(configuredPort, defaultPort string) string {
	configuredPort = strings.TrimSpace(configuredPort)
	if configuredPort == "" || configuredPort == defaultPort {
		return ""
	}
	return ":" + configuredPort
}

func platformDomainOptions() []platformDomainOption {
	domains := file.GetDb().GetUsablePlatformDomains()
	options := make([]platformDomainOption, 0, len(domains))
	for _, domain := range domains {
		options = append(options, platformDomainOption{
			ID:                    domain.ID,
			Wildcard:              domain.Wildcard,
			CertificateConfigured: domain.CertFilePath != "" && domain.KeyFilePath != "",
		})
	}
	return options
}

func generatedPlatformPrefix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "npshost1"
	}
	for i, value := range bytes {
		bytes[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(bytes)
}

func (s *IndexController) setHostFormData() {
	s.Data["platformDomains"] = platformDomainOptions()
	s.Data["platformDefaultPrefix"] = generatedPlatformPrefix()
}

func (s *IndexController) canAccessHost(host *file.Host) bool {
	if host == nil {
		return false
	}
	if s.IsAdmin() {
		return true
	}
	host.RLock()
	client := host.Client
	host.RUnlock()
	if client == nil {
		return false
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	return isAllowedClient(clientID, s.GetAllowedClientIds())
}

func (s *IndexController) authorizedHost(id int) (*file.Host, error) {
	host, err := file.GetDb().GetHostById(id)
	if err != nil {
		return nil, errors.New("host ID not found")
	}
	if !s.canAccessHost(host) {
		return nil, errors.New("permission denied")
	}
	return host, nil
}

func (s *IndexController) canAccessTask(task *file.Tunnel) bool {
	if task == nil {
		return false
	}
	if s.IsAdmin() {
		return true
	}
	task.RLock()
	client := task.Client
	task.RUnlock()
	if client == nil {
		return false
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	return isAllowedClient(clientID, s.GetAllowedClientIds())
}

func (s *IndexController) authorizedTask(id int) (*file.Tunnel, error) {
	task, err := file.GetDb().GetTask(id)
	if err != nil {
		return nil, errors.New("tunnel ID not found")
	}
	if !s.canAccessTask(task) {
		return nil, errors.New("permission denied")
	}
	return task, nil
}

func (s *IndexController) hostDomainFromRequest(excludeHostID int) (string, string, error) {
	platformID := strings.TrimSpace(s.GetString("platform_domain_id"))
	prefix := strings.TrimSpace(s.GetString("platform_prefix"))
	hostName := strings.TrimSpace(s.GetString("host"))
	mode, err := validateHostDomainRequest(s.GetString("domain_mode"), hostName, platformID, prefix)
	if err != nil {
		return "", "", err
	}
	if mode == "custom" {
		return s.getEscapeString("host"), "", nil
	}
	host, err := file.GetDb().ResolvePlatformHost(platformID, prefix)
	if err != nil {
		return "", "", err
	}
	available, err := file.GetDb().IsPlatformHostAvailable(platformID, prefix,
		s.getEscapeString("location"), s.getEscapeString("scheme"), excludeHostID)
	if err != nil {
		return "", "", err
	}
	if !available {
		return "", "", errors.New("平台域名与路径规则已被使用")
	}
	return host, platformID, nil
}

func validateHostDomainRequest(requestedMode, hostName, platformID, prefix string) (string, error) {
	mode := strings.TrimSpace(requestedMode)
	hostName, platformID, prefix = strings.TrimSpace(hostName), strings.TrimSpace(platformID), strings.TrimSpace(prefix)
	if mode == "" {
		// Compatibility for older clients: platform fields identify the
		// managed-domain form; otherwise a plain host is a legacy custom rule.
		if platformID != "" || prefix != "" {
			mode = "platform"
		} else {
			mode = "custom"
		}
	}
	switch mode {
	case "custom":
		if platformID != "" || prefix != "" {
			return "", errors.New("自定义域名模式不能同时提交平台域名字段")
		}
		if hostName == "" {
			return "", errors.New("自定义域名不能为空")
		}
		return mode, nil
	case "platform":
		if hostName != "" {
			return "", errors.New("平台域名模式不能同时提交自定义域名")
		}
		if platformID == "" || prefix == "" {
			return "", errors.New("平台域名和前缀不能为空")
		}
	default:
		return "", errors.New("域名模式必须是 platform 或 custom")
	}
	return mode, nil
}

// validateHostTLSOptions keeps mutually exclusive HTTPS modes explicit. The
// public listener can terminate TLS, connect to an HTTPS backend, or proxy the
// opaque TLS stream, but a single rule must not request two incompatible
// termination strategies at once.
func validateHostTLSOptions(scheme string, httpsJustProxy, autoSSL, tlsOffload bool) error {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if httpsJustProxy || autoSSL || tlsOffload {
		if scheme == "http" {
			return errors.New("HTTPS 选项仅适用于 HTTPS 或 HTTP+HTTPS 规则")
		}
	}
	if httpsJustProxy && tlsOffload {
		return errors.New("HTTPS 原样透传与 TLS 卸载不能同时开启")
	}
	if httpsJustProxy && autoSSL {
		return errors.New("HTTPS 原样透传不能同时申请平台证书")
	}
	return nil
}

func (s *IndexController) requestedLocalProxy(requested bool) bool {
	if !requested || !beego.AppConfig.DefaultBool("allow_local_proxy", false) {
		return false
	}
	// Administrators keep the historical global switch semantics. Ordinary
	// users need the explicit second gate so enabling server-side local proxy
	// does not silently grant every tenant access to the NPS host network.
	return s.IsAdmin() || beego.AppConfig.DefaultBool("allow_user_local", false)
}

func validateBridgeTargetPermission(isAdmin bool, target string) error {
	if isAdmin {
		return nil
	}
	target = strings.ReplaceAll(target, "\r\n", "\n")
	for _, item := range strings.Split(target, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item)), "bridge://") {
			return errors.New("bridge:// 系统入口仅允许管理员配置")
		}
	}
	return nil
}

func healthFieldsSubmitted(request *http.Request) bool {
	if request == nil {
		return false
	}
	_ = request.ParseForm()
	for _, key := range []string{
		"health_check_type", "health_check_target", "health_check_timeout",
		"health_max_failed", "health_check_interval", "health_http_url",
	} {
		if _, ok := request.Form[key]; ok {
			return true
		}
	}
	return false
}

func parseHealthFromRequest(s *IndexController) (file.Health, error) {
	health := file.Health{
		HealthCheckType:     strings.TrimSpace(s.getEscapeString("health_check_type")),
		HealthCheckTarget:   s.getEscapeString("health_check_target"),
		HealthCheckTimeout:  s.GetIntNoErr("health_check_timeout"),
		HealthMaxFail:       s.GetIntNoErr("health_max_failed"),
		HealthCheckInterval: s.GetIntNoErr("health_check_interval"),
		HttpHealthUrl:       strings.TrimSpace(s.getEscapeString("health_http_url")),
	}
	if err := file.NormalizeHealth(&health); err != nil {
		return file.Health{}, err
	}
	return health, nil
}

func parseResourceRateLimits(s *IndexController, upload, download int) (int, int, error) {
	if s == nil {
		return 0, 0, errors.New("资源限速请求无效")
	}
	if s.Ctx != nil && s.Ctx.Request != nil {
		_ = s.Ctx.Request.ParseForm()
	}
	parse := func(key string, current int) (int, error) {
		if s.Ctx == nil || s.Ctx.Request == nil {
			return current, nil
		}
		values, submitted := s.Ctx.Request.Form[key]
		if !submitted || len(values) == 0 {
			return current, nil
		}
		value := strings.TrimSpace(values[len(values)-1])
		if value == "" {
			return 0, nil
		}
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 0 {
			return 0, fmt.Errorf("%s 必须是大于或等于 0 的整数", key)
		}
		return parsed, nil
	}
	var err error
	if upload, err = parse("upload_limit", upload); err != nil {
		return 0, 0, err
	}
	if download, err = parse("download_limit", download); err != nil {
		return 0, 0, err
	}
	return upload, download, nil
}

// parseTunnelCapacity reads optional tunnel-level capacity controls.
// max_conn is the concurrent connection limit (0 = unlimited) and flow_limit
// is the total traffic quota in MB (0 = unlimited). Both are additive fields:
// when the request does not submit them, the current values are preserved.
func parseTunnelCapacity(s *IndexController, currentMaxConn int, currentFlowLimitMB int64) (int, int64, error) {
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return currentMaxConn, currentFlowLimitMB, nil
	}
	if err := s.Ctx.Request.ParseForm(); err != nil {
		return currentMaxConn, currentFlowLimitMB, errors.New("读取隧道容量设置失败")
	}
	parseInt := func(key string, current int64) (int64, error) {
		values, submitted := s.Ctx.Request.Form[key]
		if !submitted || len(values) == 0 {
			return current, nil
		}
		value := strings.TrimSpace(values[len(values)-1])
		if value == "" {
			return 0, nil
		}
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || parsed < 0 {
			return 0, fmt.Errorf("%s 必须是大于或等于 0 的整数", key)
		}
		return parsed, nil
	}
	maxConn, err := parseInt("max_conn", int64(currentMaxConn))
	if err != nil {
		return currentMaxConn, currentFlowLimitMB, err
	}
	flowLimit, err := parseInt("flow_limit", currentFlowLimitMB)
	if err != nil {
		return currentMaxConn, currentFlowLimitMB, err
	}
	return int(maxConn), flowLimit, nil
}

func parseHostProxyProtocol(s *IndexController, current int) (int, error) {
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return current, nil
	}
	if err := s.Ctx.Request.ParseForm(); err != nil {
		return current, errors.New("读取 PROXY Protocol 设置失败")
	}
	values, submitted := s.Ctx.Request.Form["proxy_protocol"]
	value := ""
	if len(values) > 0 {
		value = strings.TrimSpace(values[len(values)-1])
	}
	return parseHostProxyProtocolValue(value, current, submitted)
}

func parseHostProxyProtocolValue(value string, current int, submitted bool) (int, error) {
	if !submitted {
		return current, nil
	}
	version, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || version < 0 || version > 2 {
		return current, errors.New("PROXY Protocol 仅支持关闭、v1 或 v2")
	}
	return version, nil
}

// parseSourceListsFromRequest keeps omitted fields unchanged on edits, so older
// API callers can continue updating resources without clearing their ACL.
func parseSourceListsFromRequest(s *IndexController, current *file.SourceLists) (priority int, black, white []string, submitted bool, err error) {
	if current != nil {
		priority, black, white = current.AccessListSnapshot()
	}
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return priority, black, white, false, nil
	}
	if err = s.Ctx.Request.ParseForm(); err != nil {
		return priority, black, white, false, errors.New("读取 IP 名单设置失败")
	}
	request := s.Ctx.Request
	if _, ok := request.Form["blackiplist"]; ok {
		black = file.SplitSourceListText(s.getEscapeString("blackiplist"))
		submitted = true
	}
	if _, ok := request.Form["whiteiplist"]; ok {
		white = file.SplitSourceListText(s.getEscapeString("whiteiplist"))
		submitted = true
	}
	if _, ok := request.Form["iplistpriority"]; ok {
		priority, err = parseSourceListPriority(s.GetString("iplistpriority"), priority, true)
		if err != nil {
			return priority, black, white, submitted, err
		}
		submitted = true
	}
	return priority, black, white, submitted, nil
}

func parseSourceListPriority(value string, current int, submitted bool) (int, error) {
	if !submitted {
		return current, nil
	}
	priority, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || (priority != 0 && priority != 1) {
		return current, errors.New("IP 名单优先级必须是 0（黑名单优先）或 1（白名单优先）")
	}
	return priority, nil
}

// parseMixProxySettings accepts the mixed-proxy form fields and keeps omitted
// values unchanged on edits. The form defaults both protocols on for a new mixed
// listener, but at least one must remain enabled.
func parseMixProxySettings(s *IndexController, current *file.Tunnel) (httpEnabled, socksEnabled bool, aclMode int, aclRules string, submitted bool, err error) {
	if current != nil {
		current.RLock()
		switch current.Mode {
		case "httpProxy":
			httpEnabled, socksEnabled = true, false
		case "socks5":
			httpEnabled, socksEnabled = false, true
		default:
			httpEnabled, socksEnabled = current.HttpProxy, current.Socks5Proxy
		}
		aclMode, aclRules = current.DestAclMode, current.DestAclRules
		current.RUnlock()
	} else {
		httpEnabled, socksEnabled = true, true
	}
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return httpEnabled, socksEnabled, aclMode, aclRules, false, nil
	}
	request := s.Ctx.Request
	if parseErr := request.ParseForm(); parseErr != nil {
		return false, false, 0, "", false, errors.New("读取混合代理设置失败")
	}
	lastValue := func(key string) string {
		values := request.Form[key]
		if len(values) == 0 {
			return ""
		}
		return strings.TrimSpace(values[len(values)-1])
	}
	if _, ok := request.Form["enable_http"]; ok {
		httpEnabled, err = strconv.ParseBool(lastValue("enable_http"))
		if err != nil {
			return false, false, 0, "", false, errors.New("HTTP 代理开关值无效")
		}
		submitted = true
	}
	if _, ok := request.Form["enable_socks5"]; ok {
		socksEnabled, err = strconv.ParseBool(lastValue("enable_socks5"))
		if err != nil {
			return false, false, 0, "", false, errors.New("SOCKS5 代理开关值无效")
		}
		submitted = true
	}
	if _, ok := request.Form["dest_acl_mode"]; ok {
		aclMode, err = strconv.Atoi(lastValue("dest_acl_mode"))
		if err != nil || (aclMode != file.AclOff && aclMode != file.AclWhitelist && aclMode != file.AclBlacklist) {
			return false, false, 0, "", false, errors.New("目标 ACL 模式无效")
		}
		submitted = true
	}
	if _, ok := request.Form["dest_acl_rules"]; ok {
		aclRules = lastValue("dest_acl_rules")
		submitted = true
	}
	if !httpEnabled && !socksEnabled {
		return false, false, 0, "", false, errors.New("HTTP 代理和 SOCKS5 代理不能同时关闭")
	}
	if aclMode == file.AclOff && aclRules != "" {
		return false, false, 0, "", false, errors.New("目标 ACL 已关闭，不能同时填写规则")
	}
	if aclMode != file.AclOff {
		if _, err = acl.CompileDest(aclMode, aclRules); err != nil {
			return false, false, 0, "", false, fmt.Errorf("目标 ACL 规则无效: %w", err)
		}
	}
	return httpEnabled, socksEnabled, aclMode, aclRules, submitted, nil
}

func copyHealthConfig(dst *file.Health, src *file.Health) {
	if dst == nil {
		return
	}
	snapshot := file.Health{}
	if src != nil {
		snapshot = file.HealthConfigSnapshot(src)
	}
	dst.HealthLock()
	dst.HealthCheckTimeout = snapshot.HealthCheckTimeout
	dst.HealthMaxFail = snapshot.HealthMaxFail
	dst.HealthCheckInterval = snapshot.HealthCheckInterval
	dst.HttpHealthUrl = snapshot.HttpHealthUrl
	dst.HealthCheckType = snapshot.HealthCheckType
	dst.HealthCheckTarget = snapshot.HealthCheckTarget
	dst.HealthNextTime = time.Time{}
	dst.HealthMap = nil
	dst.HealthRemoveArr = nil
	dst.HealthUnlock()
}

// refreshHealthConfig is best-effort: persistence and listener lifecycle are
// the primary mutation, while an offline/legacy NPC will receive the same
// effective profile on its next compatible connection (or keep its local
// [health_*] settings when it is too old to understand the new message).
func refreshHealthConfig(clientIDs ...int) {
	if server.Bridge == nil {
		return
	}
	seen := make(map[int]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		if clientID <= 0 {
			continue
		}
		if _, exists := seen[clientID]; exists {
			continue
		}
		seen[clientID] = struct{}{}
		_ = server.Bridge.RequestClientHealthConfig(clientID)
	}
}

func clientTunnelLimitReached(client *file.Client) (bool, int) {
	if client == nil {
		return false, 0
	}
	client.RLock()
	maxTunnelNum, userID := client.MaxTunnelNum, client.UserId
	client.RUnlock()
	if maxTunnelNum > 0 && client.GetTunnelNum() >= maxTunnelNum {
		return true, userID
	}
	return false, userID
}

func (s *IndexController) Index() {
	s.Data["menu"] = "index"
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Data["data"] = s.dashboardSnapshot()
	s.SetInfo("dashboard")
	s.display("index/index")
}

// DashboardData is the lightweight JSON snapshot used by the dashboard's
// periodic refresh. Scope is derived from the authenticated principal on each
// request, so a user cannot widen the response by changing query parameters.
func (s *IndexController) DashboardData() {
	s.Data["json"] = map[string]interface{}{
		"status": 1,
		"data":   s.dashboardSnapshot(),
	}
	s.ServeJSON()
	s.StopRun()
}

// Overview renders the standalone, continuously refreshed resource topology.
// It deliberately uses BaseController.Prepare for the same session validation
// as the management console, so opening this URL directly cannot bypass login.
func (s *IndexController) Overview() {
	accountName := "管理员"
	if !s.IsAdmin() {
		if name, ok := s.GetSession("username").(string); ok && strings.TrimSpace(name) != "" {
			accountName = strings.TrimSpace(name)
		} else {
			accountName = "当前用户"
		}
	}
	s.Data["overview_account_name"] = accountName
	s.Data["version"] = version.VERSION
	s.Layout = ""
	if s.IsAdmin() {
		s.TplName = "index/overview-admin.html"
		return
	}
	s.TplName = "index/overview.html"
}

// TopologyData returns a live, in-memory view for the topology screen. It is
// intentionally recomputed for every request; no metric snapshot is persisted.
func (s *IndexController) TopologyData() {
	var data server.TopologySnapshot
	if s.IsAdmin() {
		data = server.GetTopologyData(nil, true)
	} else {
		data = server.GetTopologyData(s.GetAllowedClientIds(), false)
	}
	s.Data["json"] = map[string]interface{}{
		"status": 1,
		"data":   data,
	}
	s.ServeJSON()
	s.StopRun()
}

func (s *IndexController) dashboardSnapshot() map[string]interface{} {
	if s.IsAdmin() {
		return server.GetDashboardData()
	}
	return server.GetDashboardDataForClients(s.GetAllowedClientIds())
}

func (s *IndexController) Help() {
	s.Data["menu"] = "help"
	s.SetInfo("about")
	s.display("index/help")
}

func (s *IndexController) Tcp() {
	s.Data["menu"] = "tcp"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("tcp")
	s.SetType("tcp")
	s.display(tunnelTemplatePath("tcp", "list"))
}

func (s *IndexController) Udp() {
	s.Data["menu"] = "udp"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("udp")
	s.SetType("udp")
	s.display(tunnelTemplatePath("udp", "list"))
}

func (s *IndexController) Socks5() {
	// Keep the historical URL working, but show legacy SOCKS5 rules in the
	// unified mixed-proxy list.
	s.MixProxy()
}

func (s *IndexController) MixProxy() {
	s.Data["menu"] = "mixProxy"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("mix proxy")
	s.SetType("mixProxy")
	s.display(tunnelTemplatePath("mixProxy", "list"))
}

func (s *IndexController) Http() {
	// Keep the historical URL working, but show legacy HTTP rules in the
	// unified mixed-proxy list.
	s.MixProxy()
}
func (s *IndexController) File() {
	s.Data["menu"] = "file"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("file server")
	s.SetType("file")
	s.display(tunnelTemplatePath("file", "list"))
}

func (s *IndexController) Secret() {
	s.Data["menu"] = "secret"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("secret")
	s.SetType("secret")
	s.display(tunnelTemplatePath("secret", "list"))
}
func (s *IndexController) P2p() {
	s.Data["menu"] = "p2p"
	s.setOwnerFilterData()
	s.setClientFilterData()
	s.SetInfo("p2p")
	s.SetType("p2p")
	s.display(tunnelTemplatePath("p2p", "list"))
}

func (s *IndexController) Host() {
	s.Data["menu"] = "host"
	s.setOwnerFilterData()
	s.SetInfo("host")
	s.SetType("hostServer")
	s.display("index/list")
}

func (s *IndexController) All() {
	s.Data["menu"] = "client"
	s.setOwnerFilterData()
	s.setClientFilterData()
	clientId := s.getEscapeString("client_id")
	s.Data["client_id"] = clientId
	s.SetInfo("client id:" + clientId)
	s.display("index/list")
}

func (s *IndexController) GetTunnel() {
	start, length := s.GetAjaxParams()
	taskType := s.getEscapeString("type")
	clientId := s.GetIntNoErr("client_id")
	// A client-scoped list (opened from the client page) keeps its explicit
	// client_id. The administrator-only toolbar uses a separate filter key so
	// its default "all clients" value cannot accidentally widen that scope.
	if clientId == 0 {
		filterClientID := strings.TrimSpace(s.GetString("filter_client_id"))
		if filterClientID != "" {
			parsed, err := strconv.Atoi(filterClientID)
			if err != nil || parsed < 0 {
				s.AjaxErr("客户端筛选无效")
				return
			}
			clientId = parsed
		}
	}
	var allowed map[int]struct{}
	if !s.IsAdmin() {
		allowed = s.GetAllowedClientIds()
	}
	var owner *file.OwnerFilter
	if s.IsAdmin() {
		var err error
		owner, err = s.getOwnerListFilter()
		if err != nil {
			s.AjaxErr(err.Error())
			return
		}
	}
	list, cnt := server.GetTunnelByOwnerFilter(start, length, taskType, clientId, s.getEscapeString("search"), s.getEscapeString("sort"), s.getEscapeString("order"), owner, allowed)
	s.AjaxTable(newTunnelListRows(list, s.Ctx.Request.Host), cnt, cnt, nil)
}

// TrafficDebug streams a transient, task-scoped traffic inspection session.
// The stream is intentionally GET/SSE so the browser can reconnect without a
// custom websocket client. No event is replayed after reconnecting.
func (s *IndexController) TrafficDebug() {
	s.serveTrafficDebug(traffic.Resource{Kind: "tunnel", ID: s.GetIntNoErr("id")})
}

// HostTrafficDebug is the equivalent stream for domain proxy rules. Hosts are
// stored separately from ordinary tunnels and therefore use their own endpoint
// and authorization branch.
func (s *IndexController) HostTrafficDebug() {
	s.serveTrafficDebug(traffic.Resource{Kind: "host", ID: s.GetIntNoErr("id")})
}

func (s *IndexController) serveTrafficDebug(resource traffic.Resource) {
	if resource.ID <= 0 {
		http.Error(s.Ctx.ResponseWriter, "invalid resource id", http.StatusBadRequest)
		s.StopRun()
		return
	}
	if resource.Kind == "host" {
		if _, err := s.authorizedHost(resource.ID); err != nil {
			http.Error(s.Ctx.ResponseWriter, "permission denied", http.StatusForbidden)
			s.StopRun()
			return
		}
	} else {
		if _, err := s.authorizedTask(resource.ID); err != nil {
			http.Error(s.Ctx.ResponseWriter, "permission denied", http.StatusForbidden)
			s.StopRun()
			return
		}
	}

	w := s.Ctx.ResponseWriter
	_, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		s.StopRun()
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, closeStream := traffic.Subscribe(resource)
	defer closeStream()
	writeEvent := func(event traffic.Event) error {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		w.Flush()
		return nil
	}
	if err := writeEvent(traffic.Event{
		Type:     "ready",
		Time:     time.Now().UTC(),
		Resource: resource,
	}); err != nil {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || writeEvent(event) != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			w.Flush()
		case <-s.Ctx.Request.Context().Done():
			return
		}
	}
}

func (s *IndexController) Add() {
	if s.Ctx.Request.Method == "GET" {
		rawTunnelType := strings.TrimSpace(s.getEscapeString("type"))
		tunnelType := normalizedTunnelMode(rawTunnelType)
		if tunnelType == "httpProxy" || tunnelType == "socks5" {
			// New Web-created proxy rules use the unified listener. The old
			// query values remain accepted for bookmarked URLs and API clients.
			tunnelType = "mixProxy"
		}
		s.Data["type"] = tunnelType
		s.Data["menu"] = tunnelSidebarMenu(tunnelType)
		// A typed URL is one of the dedicated mode pages. Keep the historical
		// /index/add URL without a type as the mode-picker fallback.
		s.Data["dedicated"] = rawTunnelType != ""
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.Data["allow_ports"] = strings.TrimSpace(beego.AppConfig.String("allow_ports"))
		s.SetInfo("add tunnel")
		if rawTunnelType == "" {
			s.display("index/add")
		} else {
			s.display(tunnelTemplatePath(tunnelType, "add"))
		}
	} else {
		if !s.RequirePost() {
			return
		}
		tunnelMode := strings.TrimSpace(s.getEscapeString("type"))
		if !supportedTunnelMode(tunnelMode) {
			s.AjaxErr("unsupported tunnel mode")
			return
		}
		health, healthErr := parseHealthFromRequest(s)
		if healthErr != nil {
			s.AjaxErr(healthErr.Error())
			return
		}
		uploadLimit, downloadLimit, limitErr := parseResourceRateLimits(s, 0, 0)
		if limitErr != nil {
			s.AjaxErr(limitErr.Error())
			return
		}
		maxConn, flowLimitMB, capacityErr := parseTunnelCapacity(s, 0, 0)
		if capacityErr != nil {
			s.AjaxErr(capacityErr.Error())
			return
		}
		aclPriority, aclBlack, aclWhite, _, aclErr := parseSourceListsFromRequest(s, nil)
		if aclErr != nil {
			s.AjaxErr("隧道 IP 名单无效: " + aclErr.Error())
			return
		}
		if err := validateSourceAccessLists(acl.ScopeUser, aclPriority, aclBlack, aclWhite); err != nil {
			s.AjaxErr("隧道 IP 名单无效: " + err.Error())
			return
		}
		mixHTTP, mixSocks5, destACLMode, destACLRules := false, false, file.AclOff, ""
		if tunnelMode == "mixProxy" {
			var mixErr error
			mixHTTP, mixSocks5, destACLMode, destACLRules, _, mixErr = parseMixProxySettings(s, nil)
			if mixErr != nil {
				s.AjaxErr(mixErr.Error())
				return
			}
		}
		if file.HealthConfigEnabled(&health) && tunnelMode != "tcp" {
			s.AjaxErr("健康检查目前仅支持 TCP 隧道")
			return
		}
		target := s.getEscapeString("target")
		if err := validateBridgeTargetPermission(s.IsAdmin(), target); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		id := int(file.GetDb().JsonDb.GetTaskId())
		t := &file.Tunnel{
			Port:           s.GetIntNoErr("port"),
			ServerIp:       s.getEscapeString("server_ip"),
			Mode:           tunnelMode,
			Target:         &file.Target{TargetStr: target, LocalProxy: s.requestedLocalProxy(s.GetBoolNoErr("local_proxy"))},
			Id:             id,
			Status:         true,
			Remark:         s.getEscapeString("remark"),
			Password:       s.getEscapeString("password"),
			TargetType:     normalizeTargetType(s.GetString("target_type")),
			LocalPath:      s.getEscapeString("local_path"),
			StripPre:       s.getEscapeString("strip_pre"),
			ProtoVersion:   s.getEscapeString("proto_version"),
			Flow:           &file.Flow{FlowLimit: flowLimitMB},
			MaxConn:        maxConn,
			RateLimits:     file.RateLimits{UploadLimit: uploadLimit, DownloadLimit: downloadLimit},
			Health:         health,
			RegisteredOnly: s.GetBoolNoErr("registered_only"),
			HttpProxy:      mixHTTP,
			Socks5Proxy:    mixSocks5,
		}
		t.SetSourceLists(aclPriority, aclBlack, aclWhite, acl.ScopeUser, "tunnel")
		if err := t.SetDestinationACL(destACLMode, destACLRules); err != nil {
			s.AjaxErr("目标 ACL 无效: " + err.Error())
			return
		}

		if t.Port <= 0 {
			t.Port = tool.GenerateServerPort(t.Mode)
		}

		if !tool.TestServerPort(t.Port, t.Mode) {
			s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
			return
		}
		var err error
		if t.Client, err = file.GetDb().GetClient(s.GetIntNoErr("client_id")); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if !s.IsAdmin() && !isAllowedClient(t.Client.Id, s.GetAllowedClientIds()) {
			s.AjaxErr("permission denied")
			return
		}
		if reached, userID := clientTunnelLimitReached(t.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}
		if err := file.GetDb().NewTask(t); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := server.AddTask(t); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			refreshHealthConfig(t.Client.Id)
			t.Client.RLock()
			ownerID := t.Client.UserId
			t.Client.RUnlock()
			s.auditMutation("tunnel.create", "tunnel", id, ownerID, "", nil, map[string]string{"mode": t.Mode, "remark": t.Remark})
			s.Data["json"] = s.withRequestMeta(tunnelCreatedResponse(t, s.Ctx.Request.Host))
			s.ServeJSON()
			s.StopRun()
		}
	}
}

func (s *IndexController) Copy() {
	if !s.RequirePost() {
		return
	}
	oldId := s.GetIntNoErr("id")
	oldTask, err := s.authorizedTask(oldId)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	oldTask.RLock()
	oldClient := oldTask.Client
	oldMode := oldTask.Mode
	oldServerIP := oldTask.ServerIp
	oldRemark := oldTask.Remark
	oldPassword := oldTask.Password
	oldLocalPath := oldTask.LocalPath
	oldStripPre := oldTask.StripPre
	oldProtoVersion := oldTask.ProtoVersion
	oldTarget := oldTask.Target
	oldUploadLimit, oldDownloadLimit := oldTask.RateLimitSnapshot()
	oldHealth := file.HealthConfigSnapshot(&oldTask.Health)
	oldRegisteredOnly := oldTask.RegisteredOnly
	oldHTTPProxy, oldSocks5Proxy := oldTask.HttpProxy, oldTask.Socks5Proxy
	oldDestACLMode, oldDestACLRules := oldTask.DestAclMode, oldTask.DestAclRules
	oldACLPriority, oldBlack, oldWhite := oldTask.AccessListSnapshot()
	oldTask.RUnlock()
	if oldClient == nil {
		s.AjaxErr("modified error,the client is not exist")
		return
	}
	oldClient.RLock()
	oldClientID := oldClient.Id
	oldClient.RUnlock()
	if client, err := file.GetDb().GetClient(oldClientID); err != nil {
		s.AjaxErr("modified error,the client is not exist")
		return
	} else {
		var targetStr string
		var localProxy bool
		if oldTarget != nil {
			oldTarget.RLock()
			targetStr, localProxy = oldTarget.TargetStr, oldTarget.LocalProxy
			oldTarget.RUnlock()
		}

		id := int(file.GetDb().JsonDb.GetTaskId())
		newTask := &file.Tunnel{
			Client:         client,
			Port:           tool.GenerateServerPort(oldMode),
			ServerIp:       oldServerIP,
			Mode:           oldMode,
			Target:         &file.Target{TargetStr: targetStr, LocalProxy: localProxy},
			Id:             id,
			Status:         true,
			Remark:         oldRemark,
			Password:       oldPassword,
			LocalPath:      oldLocalPath,
			StripPre:       oldStripPre,
			ProtoVersion:   oldProtoVersion,
			Flow:           &file.Flow{},
			RateLimits:     file.RateLimits{UploadLimit: oldUploadLimit, DownloadLimit: oldDownloadLimit},
			Health:         oldHealth,
			RegisteredOnly: oldRegisteredOnly,
			HttpProxy:      oldHTTPProxy,
			Socks5Proxy:    oldSocks5Proxy,
		}
		newTask.SetSourceLists(oldACLPriority, oldBlack, oldWhite, acl.ScopeUser, "tunnel")
		if err := newTask.SetDestinationACL(oldDestACLMode, oldDestACLRules); err != nil {
			s.AjaxErr("目标 ACL 无效: " + err.Error())
			return
		}
		if !tool.TestServerPort(newTask.Port, newTask.Mode) {
			s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
			return
		}

		if reached, userID := clientTunnelLimitReached(newTask.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}
		if err := file.GetDb().NewTask(newTask); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := server.AddTask(newTask); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			refreshHealthConfig(oldClientID)
			client.RLock()
			ownerID := client.UserId
			client.RUnlock()
			s.auditMutation("tunnel.copy", "tunnel", id, ownerID, "", nil, map[string]string{"mode": newTask.Mode, "remark": newTask.Remark})
			s.Data["json"] = s.withRequestMeta(tunnelCreatedResponse(newTask, s.Ctx.Request.Host))
			s.ServeJSON()
			s.StopRun()
		}
	}
}

func (s *IndexController) GetOneTunnel() {
	id := s.GetIntNoErr("id")
	t, err := s.authorizedTask(id)
	if err != nil {
		s.Data["json"] = s.ajaxDetail(nil, err)
		s.ServeJSON()
		s.StopRun()
		return
	}
	rows := newTunnelListRows([]*file.Tunnel{t}, s.Ctx.Request.Host)
	if len(rows) != 1 {
		s.Data["json"] = s.ajaxDetail(nil, errors.New("tunnel ID not found"))
		s.ServeJSON()
		s.StopRun()
		return
	}
	s.Data["json"] = s.ajaxDetail(rows[0], nil)
	s.ServeJSON()
	s.StopRun()
}
func (s *IndexController) Edit() {
	id := s.GetIntNoErr("id")
	if id <= 0 {
		if s.Ctx.Request.Method == "GET" {
			s.error()
		} else {
			s.AjaxErr("tunnel ID not found")
		}
		return
	}
	if s.Ctx.Request.Method == "GET" {
		tunnelMode := "tcp"
		if t, err := s.authorizedTask(id); err != nil {
			s.error()
			return
		} else {
			s.Data["t"] = t
			t.RLock()
			s.Data["BlackIpList"] = strings.Join(t.BlackIpList, "\r\n")
			s.Data["WhiteIpList"] = strings.Join(t.WhiteIpList, "\r\n")
			tunnelMode = normalizedTunnelMode(t.Mode)
			if tunnelMode == "httpProxy" || tunnelMode == "socks5" {
				// Legacy proxy records are edited through the unified form. Saving
				// the form migrates the record to mixProxy while preserving its
				// port, client and access controls.
				tunnelMode = "mixProxy"
			}
			s.Data["type"] = tunnelMode
			s.Data["menu"] = tunnelSidebarMenu(tunnelMode)
			t.RUnlock()
			s.Data["dedicated"] = true
			s.Data["allow_ports"] = strings.TrimSpace(beego.AppConfig.String("allow_ports"))
		}
		s.SetInfo("edit tunnel")
		s.display(tunnelTemplatePath(tunnelMode, "edit"))
	} else {
		if !s.RequirePost() {
			return
		}
		if t, err := s.authorizedTask(id); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			t.RLock()
			currentClient := t.Client
			t.RUnlock()
			if currentClient == nil {
				s.AjaxErr("modified error,the client is not exist")
				return
			}
			desiredClient := currentClient
			requestedClientIDText := strings.TrimSpace(s.GetString("client_id"))
			if requestedClientIDText != "" {
				requestedClientID, parseErr := strconv.Atoi(requestedClientIDText)
				if parseErr != nil || requestedClientID <= 0 {
					s.AjaxErr("modified error,客户端 ID 无效")
					return
				}
				desiredClient, err = file.GetDb().GetClient(requestedClientID)
				if err != nil {
					s.AjaxErr("modified error,the client can not be found")
					return
				}
			}
			if !s.IsAdmin() && !isAllowedClient(desiredClient.Id, s.GetAllowedClientIds()) {
				s.AjaxErr("permission denied")
				return
			}
			currentClient.RLock()
			currentClientID, currentUserID := currentClient.Id, currentClient.UserId
			currentClient.RUnlock()
			desiredClient.RLock()
			desiredClientID, desiredUserID := desiredClient.Id, desiredClient.UserId
			desiredClient.RUnlock()
			if desiredClientID != currentClientID {
				if reached, _ := clientTunnelLimitReached(desiredClient); reached {
					s.AjaxErr("The number of tunnels exceeds the limit")
					return
				}
				if desiredUserID != 0 && desiredUserID != currentUserID && file.GetDb().IsUserTunnelLimitReached(desiredUserID) {
					s.AjaxErr("The number of user tunnels exceeds the limit")
					return
				}
			}
			desiredPort := s.GetIntNoErr("port")
			desiredMode := strings.TrimSpace(s.getEscapeString("type"))
			if !supportedTunnelMode(desiredMode) {
				s.AjaxErr("unsupported tunnel mode")
				return
			}
			t.RLock()
			currentPort, currentMode := t.Port, t.Mode
			t.RUnlock()
			// HTTP and SOCKS5 records created by older releases are edited
			// through the unified mixProxy form. Treat that one-way migration as
			// compatible with the historical mode; all other mode changes remain
			// forbidden to avoid changing the semantics of a live tunnel.
			if !tunnelModeChangeAllowed(currentMode, desiredMode) {
				s.AjaxErr("tunnel mode cannot be changed")
				return
			}
			if desiredPort <= 0 {
				desiredPort = tool.GenerateServerPort(desiredMode)
			}
			if desiredPort != currentPort || desiredMode != currentMode {
				if !tool.TestServerPort(desiredPort, desiredMode) {
					s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
					return
				}
			}
			desiredHealth := file.HealthConfigSnapshot(&t.Health)
			desiredRegisteredOnly := t.RegisteredOnly
			if _, submitted := s.Ctx.Request.Form["registered_only"]; submitted {
				desiredRegisteredOnly = s.GetBoolNoErr("registered_only")
			}
			if healthFieldsSubmitted(s.Ctx.Request) {
				parsedHealth, healthErr := parseHealthFromRequest(s)
				if healthErr != nil {
					s.AjaxErr(healthErr.Error())
					return
				}
				desiredHealth = parsedHealth
			}
			if file.HealthConfigEnabled(&desiredHealth) && desiredMode != "tcp" {
				s.AjaxErr("健康检查目前仅支持 TCP 隧道")
				return
			}
			currentUploadLimit, currentDownloadLimit := t.RateLimitSnapshot()
			desiredUploadLimit, desiredDownloadLimit, limitErr := parseResourceRateLimits(s, currentUploadLimit, currentDownloadLimit)
			if limitErr != nil {
				s.AjaxErr(limitErr.Error())
				return
			}
			t.RLock()
			currentMaxConn := t.MaxConn
			var currentFlowLimitMB int64
			if t.Flow != nil {
				currentFlowLimitMB = t.Flow.FlowLimit
			}
			t.RUnlock()
			desiredMaxConn, desiredFlowLimitMB, capacityErr := parseTunnelCapacity(s, currentMaxConn, currentFlowLimitMB)
			if capacityErr != nil {
				s.AjaxErr(capacityErr.Error())
				return
			}
			aclPriority, aclBlack, aclWhite, aclSubmitted, aclErr := parseSourceListsFromRequest(s, &t.SourceLists)
			if aclErr != nil {
				s.AjaxErr("隧道 IP 名单无效: " + aclErr.Error())
				return
			}
			if aclSubmitted {
				if err := validateSourceAccessLists(acl.ScopeUser, aclPriority, aclBlack, aclWhite); err != nil {
					s.AjaxErr("隧道 IP 名单无效: " + err.Error())
					return
				}
			}
			mixHTTP, mixSocks5, destACLMode, destACLRules, mixSubmitted := false, false, file.AclOff, "", false
			if desiredMode == "mixProxy" {
				var mixErr error
				mixHTTP, mixSocks5, destACLMode, destACLRules, mixSubmitted, mixErr = parseMixProxySettings(s, t)
				if mixErr != nil {
					s.AjaxErr(mixErr.Error())
					return
				}
			}
			desiredTargetValue := s.getEscapeString("target")
			if err := validateBridgeTargetPermission(s.IsAdmin(), desiredTargetValue); err != nil {
				s.AjaxErr(err.Error())
				return
			}
			t.RLock()
			currentTargetType := t.TargetType
			t.RUnlock()
			desiredTargetType := currentTargetType
			if _, submitted := s.Ctx.Request.Form["target_type"]; submitted {
				desiredTargetType = normalizeTargetType(s.GetString("target_type"))
			}
			desiredTarget := &file.Target{TargetStr: desiredTargetValue}
			desiredTarget.LocalProxy = s.requestedLocalProxy(s.GetBoolNoErr("local_proxy"))
			// Build the replacement atomically. Proxy workers take the same
			// task lock when selecting their client, target and mode.
			t.Lock()
			t.Client = desiredClient
			t.Port = desiredPort
			t.ServerIp = s.getEscapeString("server_ip")
			t.Mode = desiredMode
			t.Target = desiredTarget
			t.Password = s.getEscapeString("password")
			t.TargetType = desiredTargetType
			t.Id = id
			t.LocalPath = s.getEscapeString("local_path")
			t.ProtoVersion = s.getEscapeString("proto_version")
			t.StripPre = s.getEscapeString("strip_pre")
			t.Remark = s.getEscapeString("remark")
			if desiredMode == "mixProxy" && mixSubmitted {
				t.HttpProxy = mixHTTP
				t.Socks5Proxy = mixSocks5
			}
			t.SetRateLimits(desiredUploadLimit, desiredDownloadLimit)
			t.MaxConn = desiredMaxConn
			if t.Flow != nil {
				t.Flow.SetLimit(desiredFlowLimitMB)
			} else {
				t.Flow = &file.Flow{FlowLimit: desiredFlowLimitMB}
			}
			copyHealthConfig(&t.Health, &desiredHealth)
			t.RegisteredOnly = desiredRegisteredOnly
			t.Unlock()
			if desiredMode == "mixProxy" && mixSubmitted {
				if err := t.SetDestinationACL(destACLMode, destACLRules); err != nil {
					s.AjaxErr("目标 ACL 无效: " + err.Error())
					return
				}
			}
			if aclSubmitted {
				t.SetSourceLists(aclPriority, aclBlack, aclWhite, acl.ScopeUser, "tunnel")
			}
			if err := file.GetDb().UpdateTask(t); err != nil {
				s.AjaxErr("modified error")
				return
			}
			if err := server.StopServer(t.Id); err != nil {
				// A stopped task has no runtime entry and can be started directly;
				// surface real close failures instead of claiming a successful edit.
				if _, running := server.RunList.Load(t.Id); running {
					s.AjaxErr("stop error")
					return
				}
			}
			if err := server.StartTask(t.Id); err != nil {
				s.AjaxErr("start error")
				return
			}
			refreshHealthConfig(currentClientID, desiredClientID)
			t.RLock()
			ownerID, mode, remark := 0, t.Mode, t.Remark
			if t.Client != nil {
				t.Client.RLock()
				ownerID = t.Client.UserId
				t.Client.RUnlock()
			}
			t.RUnlock()
			s.auditMutation("tunnel.update", "tunnel", id, ownerID, "", nil, map[string]string{"mode": mode, "remark": remark})
		}
		s.AjaxOk("modified success")
	}
}

func (s *IndexController) Stop() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	task, err := s.authorizedTask(id)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.StopServer(id); err != nil {
		s.AjaxErr("stop error")
		return
	}
	task.RLock()
	clientID := 0
	if task.Client != nil {
		clientID = task.Client.Id
	}
	task.RUnlock()
	refreshHealthConfig(clientID)
	s.auditTunnelAction("tunnel.stop", task)
	s.AjaxOk("stop success")
}

func (s *IndexController) Del() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	task, err := s.authorizedTask(id)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.DelTask(id); err != nil {
		s.AjaxErr("delete error")
		return
	}
	task.RLock()
	clientID := 0
	if task.Client != nil {
		clientID = task.Client.Id
	}
	task.RUnlock()
	refreshHealthConfig(clientID)
	s.auditTunnelAction("tunnel.delete", task)
	s.AjaxOk("delete success")
}

func (s *IndexController) Start() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	task, err := s.authorizedTask(id)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.StartTask(id); err != nil {
		s.AjaxErr("start error")
		return
	}
	task.RLock()
	clientID := 0
	if task.Client != nil {
		clientID = task.Client.Id
	}
	task.RUnlock()
	refreshHealthConfig(clientID)
	s.auditTunnelAction("tunnel.start", task)
	s.AjaxOk("start success")
}

func (s *IndexController) auditTunnelAction(action string, task *file.Tunnel) {
	if task == nil {
		return
	}
	task.RLock()
	id, mode, remark := task.Id, task.Mode, task.Remark
	client := task.Client
	task.RUnlock()
	ownerID := 0
	if client != nil {
		client.RLock()
		ownerID = client.UserId
		client.RUnlock()
	}
	s.auditMutation(action, "tunnel", id, ownerID, "", nil, map[string]string{"mode": mode, "remark": remark})
}

func (s *IndexController) HostList() {
	if s.Ctx.Request.Method == "GET" {
		s.setOwnerFilterData()
		s.setClientFilterData()
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.Data["menu"] = "host"
		s.SetInfo("host list")
		s.display("index/hlist")
	} else {
		start, length := s.GetAjaxParams()
		clientId := s.GetIntNoErr("client_id")
		if clientId == 0 {
			filterClientID := strings.TrimSpace(s.GetString("filter_client_id"))
			if filterClientID != "" {
				parsed, err := strconv.Atoi(filterClientID)
				if err != nil || parsed < 0 {
					s.AjaxErr("客户端筛选无效")
					return
				}
				clientId = parsed
			}
		}
		var allowed map[int]struct{}
		if !s.IsAdmin() {
			allowed = s.GetAllowedClientIds()
		}
		var owner *file.OwnerFilter
		if s.IsAdmin() {
			var err error
			owner, err = s.getOwnerListFilter()
			if err != nil {
				s.AjaxErr(err.Error())
				return
			}
		}
		list, cnt := file.GetDb().GetHostByAllowedClientsFiltered(start, length, clientId, s.getEscapeString("search"), owner, allowed)
		s.AjaxTable(newHostListRows(list), cnt, cnt, nil)
	}
}

func (s *IndexController) GetHost() {
	if !s.RequirePost() {
		return
	}
	host, err := s.authorizedHost(s.GetIntNoErr("id"))
	if err != nil {
		s.Data["json"] = s.ajaxDetail(nil, err)
		s.ServeJSON()
		s.StopRun()
		return
	}
	rows := newHostListRows([]*file.Host{host})
	if len(rows) != 1 {
		s.Data["json"] = s.ajaxDetail(nil, errors.New("host ID not found"))
		s.ServeJSON()
		s.StopRun()
		return
	}
	s.Data["json"] = s.ajaxDetail(rows[0], nil)
	s.ServeJSON()
	s.StopRun()
}

func (s *IndexController) DelHost() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	host, err := s.authorizedHost(id)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := file.GetDb().DelHost(id); err != nil {
		s.AjaxErr("delete error")
		return
	}
	host.RLock()
	clientID := 0
	if host.Client != nil {
		clientID = host.Client.Id
	}
	host.RUnlock()
	refreshHealthConfig(clientID)
	s.auditHostAction("host.delete", host)
	s.AjaxOk("delete success")
}

func (s *IndexController) HostStop() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if h, err := s.authorizedHost(id); err != nil {
		s.AjaxErr(err.Error())
		return
	} else {
		h.RLock()
		clientID := 0
		if h.Client != nil {
			clientID = h.Client.Id
		}
		h.RUnlock()
		h.Lock()
		h.IsClose = true
		h.Unlock()
		file.GetDb().JsonDb.StoreHostToJsonFile()
		refreshHealthConfig(clientID)
		s.auditHostAction("host.stop", h)
	}
	s.AjaxOk("stop success")
}

func (s *IndexController) HostStart() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if h, err := s.authorizedHost(id); err != nil {
		s.AjaxErr(err.Error())
		return
	} else {
		h.RLock()
		clientID := 0
		if h.Client != nil {
			clientID = h.Client.Id
		}
		h.RUnlock()
		h.Lock()
		h.IsClose = false
		h.Unlock()
		file.GetDb().JsonDb.StoreHostToJsonFile()
		refreshHealthConfig(clientID)
		s.auditHostAction("host.start", h)
	}
	s.AjaxOk("start success")
}

func (s *IndexController) AddHost() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.Data["menu"] = "host"
		s.setHostFormData()
		s.SetInfo("add host")
		s.display("index/hadd")
	} else {
		if !s.RequirePost() {
			return
		}
		hostName, platformDomainID, err := s.hostDomainFromRequest(0)
		if err != nil {
			s.AjaxErr("add fail, " + err.Error())
			return
		}
		health, healthErr := parseHealthFromRequest(s)
		if healthErr != nil {
			s.AjaxErr("add fail, " + healthErr.Error())
			return
		}
		uploadLimit, downloadLimit, limitErr := parseResourceRateLimits(s, 0, 0)
		if limitErr != nil {
			s.AjaxErr("add fail, " + limitErr.Error())
			return
		}
		aclPriority, aclBlack, aclWhite, _, aclErr := parseSourceListsFromRequest(s, nil)
		if aclErr != nil {
			s.AjaxErr("域名 IP 名单无效: " + aclErr.Error())
			return
		}
		if err := validateSourceAccessLists(acl.ScopeUser, aclPriority, aclBlack, aclWhite); err != nil {
			s.AjaxErr("域名 IP 名单无效: " + err.Error())
			return
		}
		proxyProtocol, proxyProtocolErr := parseHostProxyProtocol(s, 0)
		if proxyProtocolErr != nil {
			s.AjaxErr("add fail, " + proxyProtocolErr.Error())
			return
		}
		targetIsHTTPS := s.GetBoolNoErr("target_is_https")
		httpsJustProxy := s.GetBoolNoErr("https_just_proxy")
		autoSSL := s.GetBoolNoErr("auto_ssl")
		tlsOffload := s.GetBoolNoErr("tls_offload")
		if err := validateHostTLSOptions(s.getEscapeString("scheme"), httpsJustProxy, autoSSL, tlsOffload); err != nil {
			s.AjaxErr("add fail, " + err.Error())
			return
		}
		target := s.getEscapeString("target")
		if err := validateBridgeTargetPermission(s.IsAdmin(), target); err != nil {
			s.AjaxErr("add fail, " + err.Error())
			return
		}
		id := int(file.GetDb().JsonDb.GetHostId())
		certFilePath, keyFilePath, certErr := s.resolveHostCertificatePaths(id, hostName, s.Ctx.Request)
		if certErr != nil {
			s.AjaxErr("add fail, " + certErr.Error())
			return
		}
		h := &file.Host{
			Id:                   id,
			Host:                 hostName,
			PlatformDomainID:     platformDomainID,
			Target:               &file.Target{TargetStr: target, LocalProxy: s.requestedLocalProxy(s.GetBoolNoErr("local_proxy")), ProxyProtocol: proxyProtocol},
			HeaderChange:         s.getEscapeString("header"),
			ResponseHeaderChange: s.getEscapeString("response_header"),
			HostChange:           s.getEscapeString("hostchange"),
			Remark:               s.getEscapeString("remark"),
			Location:             s.getEscapeString("location"),
			PathRewrite:          s.getEscapeString("path_rewrite"),
			RedirectURL:          s.getEscapeString("redirect_url"),
			Auth:                 strings.TrimSpace(s.GetString("auth")),
			Flow:                 &file.Flow{},
			RateLimits:           file.RateLimits{UploadLimit: uploadLimit, DownloadLimit: downloadLimit},
			Scheme:               s.getEscapeString("scheme"),
			KeyFilePath:          keyFilePath,
			CertFilePath:         certFilePath,
			AutoHttps:            s.GetBoolNoErr("AutoHttps"),
			TargetIsHTTPS:        targetIsHTTPS,
			HTTPSJustProxy:       httpsJustProxy,
			AutoSSL:              autoSSL,
			TLSOffload:           tlsOffload,
			AutoCORS:             s.GetBoolNoErr("auto_cors"),
			CompatMode:           s.GetBoolNoErr("compat_mode"),
			RegisteredOnly:       s.GetBoolNoErr("registered_only"),
			Health:               health,
		}
		h.SetSourceLists(aclPriority, aclBlack, aclWhite, acl.ScopeUser, "host")

		if h.Scheme == "http" {
			h.AutoHttps = false
		}

		if h.Client, err = file.GetDb().GetClient(s.GetIntNoErr("client_id")); err != nil {
			s.AjaxErr("add error the client can not be found")
			return
		}
		if !s.IsAdmin() && !isAllowedClient(h.Client.Id, s.GetAllowedClientIds()) {
			s.AjaxErr("permission denied")
			return
		}
		if reached, userID := clientTunnelLimitReached(h.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}

		if err := file.GetDb().NewHost(h); err != nil {
			s.AjaxErr("add fail, " + err.Error())
			return
		}
		refreshHealthConfig(h.Client.Id)
		s.auditHostAction("host.create", h)
		s.AjaxOkWithId("add success", id)
	}
}

func (s *IndexController) EditHost() {
	id := s.GetIntNoErr("id")
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "host"
		if h, err := s.authorizedHost(id); err != nil {
			s.error()
			return
		} else {
			s.Data["h"] = h
			h.RLock()
			s.Data["BlackIpList"] = strings.Join(h.BlackIpList, "\r\n")
			s.Data["WhiteIpList"] = strings.Join(h.WhiteIpList, "\r\n")
			platformDomainID, hostName := h.PlatformDomainID, h.Host
			h.RUnlock()
			s.Data["hostIsPlatform"] = platformDomainID != ""
			if domain, domainErr := file.GetDb().GetPlatformDomain(platformDomainID); domainErr == nil {
				s.Data["platformPrefix"] = strings.TrimSuffix(hostName, "."+strings.TrimPrefix(domain.Wildcard, "*."))
			}
		}
		s.setHostFormData()
		s.SetInfo("edit")
		s.display("index/hedit")
	} else {
		if !s.RequirePost() {
			return
		}
		storedHost, err := s.authorizedHost(id)
		if err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			desiredHost, platformDomainID, domainErr := s.hostDomainFromRequest(id)
			if domainErr != nil {
				s.AjaxErr("modified error," + domainErr.Error())
				return
			}
			desiredLocation := s.getEscapeString("location")
			desiredScheme := s.getEscapeString("scheme")
			storedHost.RLock()
			currentClient := storedHost.Client
			currentAuth := storedHost.Auth
			currentRegisteredOnly := storedHost.RegisteredOnly
			storedHost.RUnlock()
			aclPriority, aclBlack, aclWhite, aclSubmitted, aclErr := parseSourceListsFromRequest(s, &storedHost.SourceLists)
			if aclErr != nil {
				s.AjaxErr("域名 IP 名单无效: " + aclErr.Error())
				return
			}
			if aclSubmitted {
				if err := validateSourceAccessLists(acl.ScopeUser, aclPriority, aclBlack, aclWhite); err != nil {
					s.AjaxErr("域名 IP 名单无效: " + err.Error())
					return
				}
			}
			if currentClient == nil {
				s.AjaxErr("modified error,the client is not exist")
				return
			}
			currentUploadLimit, currentDownloadLimit := storedHost.RateLimitSnapshot()
			desiredUploadLimit, desiredDownloadLimit, limitErr := parseResourceRateLimits(s, currentUploadLimit, currentDownloadLimit)
			if limitErr != nil {
				s.AjaxErr("modified error," + limitErr.Error())
				return
			}
			desiredClient := currentClient
			requestedClientIDText := strings.TrimSpace(s.GetString("client_id"))
			if requestedClientIDText != "" {
				requestedClientID, parseErr := strconv.Atoi(requestedClientIDText)
				if parseErr != nil || requestedClientID <= 0 {
					s.AjaxErr("modified error,客户端 ID 无效")
					return
				}
				desiredClient, err = file.GetDb().GetClient(requestedClientID)
				if err != nil {
					s.AjaxErr("modified error,the client can not be found")
					return
				}
			}
			if !s.IsAdmin() && !isAllowedClient(desiredClient.Id, s.GetAllowedClientIds()) {
				s.AjaxErr("permission denied")
				return
			}
			currentClient.RLock()
			currentClientID, currentUserID := currentClient.Id, currentClient.UserId
			currentClient.RUnlock()
			desiredClient.RLock()
			desiredClientID, desiredUserID := desiredClient.Id, desiredClient.UserId
			desiredClient.RUnlock()
			if desiredClientID != currentClientID {
				if reached, _ := clientTunnelLimitReached(desiredClient); reached {
					s.AjaxErr("The number of tunnels exceeds the limit")
					return
				}
				if desiredUserID != 0 && desiredUserID != currentUserID && file.GetDb().IsUserTunnelLimitReached(desiredUserID) {
					s.AjaxErr("The number of user tunnels exceeds the limit")
					return
				}
			}
			currentProxyProtocol := 0
			if storedHost.Target != nil {
				storedHost.Target.RLock()
				currentProxyProtocol = storedHost.Target.ProxyProtocol
				storedHost.Target.RUnlock()
			}
			proxyProtocol, proxyProtocolErr := parseHostProxyProtocol(s, currentProxyProtocol)
			if proxyProtocolErr != nil {
				s.AjaxErr("modified error," + proxyProtocolErr.Error())
				return
			}
			desiredTargetValue := s.getEscapeString("target")
			if err := validateBridgeTargetPermission(s.IsAdmin(), desiredTargetValue); err != nil {
				s.AjaxErr("modified error," + err.Error())
				return
			}
			desiredTarget := &file.Target{TargetStr: desiredTargetValue, LocalProxy: s.requestedLocalProxy(s.GetBoolNoErr("local_proxy")), ProxyProtocol: proxyProtocol}
			desiredAuth := strings.TrimSpace(s.GetString("auth"))
			clearHostAuth := strings.EqualFold(strings.TrimSpace(s.GetString("clear_host_auth")), "1") || strings.EqualFold(strings.TrimSpace(s.GetString("clear_host_auth")), "true")
			if desiredAuth == "" && !clearHostAuth {
				desiredAuth = currentAuth
			}
			desiredHealth := file.HealthConfigSnapshot(&storedHost.Health)
			if healthFieldsSubmitted(s.Ctx.Request) {
				parsedHealth, healthErr := parseHealthFromRequest(s)
				if healthErr != nil {
					s.AjaxErr("modified error," + healthErr.Error())
					return
				}
				desiredHealth = parsedHealth
			}
			autoHTTPS := s.GetBoolNoErr("AutoHttps")
			if desiredScheme == "http" {
				autoHTTPS = false
			}
			desiredRegisteredOnly := currentRegisteredOnly
			_ = s.Ctx.Request.ParseForm()
			if _, submitted := s.Ctx.Request.Form["registered_only"]; submitted {
				desiredRegisteredOnly = s.GetBoolNoErr("registered_only")
			}
			storedHost.RLock()
			desiredTargetIsHTTPS := storedHost.TargetIsHTTPS
			desiredHTTPSJustProxy := storedHost.HTTPSJustProxy
			desiredAutoSSL := storedHost.AutoSSL
			desiredTLSOffload := storedHost.TLSOffload
			storedHost.RUnlock()
			if _, submitted := s.Ctx.Request.Form["target_is_https"]; submitted {
				desiredTargetIsHTTPS = s.GetBoolNoErr("target_is_https")
			}
			if _, submitted := s.Ctx.Request.Form["https_just_proxy"]; submitted {
				desiredHTTPSJustProxy = s.GetBoolNoErr("https_just_proxy")
			}
			if _, submitted := s.Ctx.Request.Form["auto_ssl"]; submitted {
				desiredAutoSSL = s.GetBoolNoErr("auto_ssl")
			}
			if _, submitted := s.Ctx.Request.Form["tls_offload"]; submitted {
				desiredTLSOffload = s.GetBoolNoErr("tls_offload")
			}
			if err := validateHostTLSOptions(desiredScheme, desiredHTTPSJustProxy, desiredAutoSSL, desiredTLSOffload); err != nil {
				s.AjaxErr("modified error," + err.Error())
				return
			}
			desiredCertFilePath, desiredKeyFilePath, certErr := s.resolveHostCertificatePaths(id, desiredHost, s.Ctx.Request)
			if certErr != nil {
				s.AjaxErr("modified error," + certErr.Error())
				return
			}
			replacement := &file.Host{
				Id:                   id,
				Host:                 desiredHost,
				PlatformDomainID:     platformDomainID,
				Client:               desiredClient,
				Target:               desiredTarget,
				HeaderChange:         s.getEscapeString("header"),
				ResponseHeaderChange: s.getEscapeString("response_header"),
				HostChange:           s.getEscapeString("hostchange"),
				Remark:               s.getEscapeString("remark"),
				Location:             desiredLocation,
				PathRewrite:          s.getEscapeString("path_rewrite"),
				RedirectURL:          s.getEscapeString("redirect_url"),
				Auth:                 desiredAuth,
				RateLimits:           file.RateLimits{UploadLimit: desiredUploadLimit, DownloadLimit: desiredDownloadLimit},
				Scheme:               desiredScheme,
				KeyFilePath:          desiredKeyFilePath,
				CertFilePath:         desiredCertFilePath,
				AutoHttps:            autoHTTPS,
				TargetIsHTTPS:        desiredTargetIsHTTPS,
				HTTPSJustProxy:       desiredHTTPSJustProxy,
				AutoSSL:              desiredAutoSSL,
				TLSOffload:           desiredTLSOffload,
				AutoCORS:             s.GetBoolNoErr("auto_cors"),
				CompatMode:           s.GetBoolNoErr("compat_mode"),
				RegisteredOnly:       desiredRegisteredOnly,
				Health:               desiredHealth,
			}
			replacement.SetSourceLists(aclPriority, aclBlack, aclWhite, acl.ScopeUser, "host")
			if err := file.GetDb().UpdateHost(replacement); err != nil {
				s.AjaxErr("modified error," + err.Error())
				return
			}
			refreshHealthConfig(currentClientID, desiredClientID)
			s.auditHostAction("host.update", replacement)
		}
		s.AjaxOk("modified success")
	}
}

func (s *IndexController) auditHostAction(action string, host *file.Host) {
	if host == nil {
		return
	}
	host.RLock()
	id, hostName, remark, client := host.Id, host.Host, host.Remark, host.Client
	host.RUnlock()
	ownerID := 0
	if client != nil {
		client.RLock()
		ownerID = client.UserId
		client.RUnlock()
	}
	s.auditMutation(action, "host", id, ownerID, "", nil, map[string]string{"host": hostName, "remark": remark})
}

// PlatformHostAvailable provides immediate feedback while a user edits a
// managed wildcard prefix. NewHost and UpdateHost repeat the validation while
// holding the data-layer mutation lock, so this endpoint is only a UX aid.
func (s *IndexController) PlatformHostAvailable() {
	if !s.RequirePost() {
		return
	}
	platformID := strings.TrimSpace(s.GetString("platform_domain_id"))
	prefix := strings.TrimSpace(s.GetString("platform_prefix"))
	host, err := file.GetDb().ResolvePlatformHost(platformID, prefix)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	available, err := file.GetDb().IsPlatformHostAvailable(platformID, prefix,
		s.getEscapeString("location"), s.getEscapeString("scheme"), s.GetIntNoErr("id"))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.Data["json"] = map[string]interface{}{
		"status":    1,
		"available": available,
		"host":      host,
	}
	s.ServeJSON()
	s.StopRun()
}

func diagnosticHostRuleMatches(host, rule string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	rule = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule)), ".")
	if host == "" || rule == "" {
		return false
	}
	if !strings.Contains(rule, "*") {
		return host == rule
	}
	if !strings.HasPrefix(rule, "*.") || strings.Count(rule, "*") != 1 {
		return false
	}
	suffix := strings.TrimPrefix(rule, "*.")
	return len(host) > len(suffix)+1 && strings.HasSuffix(host, "."+suffix)
}

func diagnosticPathMatches(path, location string) bool {
	if location == "" || location == "/" {
		return strings.HasPrefix(path, "/")
	}
	if !strings.HasPrefix(path, location) {
		return false
	}
	if len(path) == len(location) {
		return true
	}
	next := path[len(location)]
	return next == '/' || next == '?'
}

func (s *IndexController) explainHostDiagnosticFailure(host, path, scheme string) string {
	var allowed map[int]struct{}
	if !s.IsAdmin() {
		allowed = s.GetAllowedClientIds()
	}
	hosts, _ := file.GetDb().GetHostByAllowedClients(0, 100000, 0, "", allowed)
	matchedHost, matchedEnabled, matchedScheme, matchedPath := false, false, false, false
	for _, candidate := range hosts {
		if candidate == nil || !s.canAccessHost(candidate) {
			continue
		}
		candidate.RLock()
		rule, location, ruleScheme, isClose, target := candidate.Host, candidate.Location, candidate.Scheme, candidate.IsClose, candidate.Target
		candidate.RUnlock()
		if !diagnosticHostRuleMatches(host, rule) {
			continue
		}
		matchedHost = true
		if isClose {
			continue
		}
		matchedEnabled = true
		if ruleScheme != "" && ruleScheme != "all" && ruleScheme != scheme {
			continue
		}
		matchedScheme = true
		if !diagnosticPathMatches(path, location) {
			continue
		}
		matchedPath = true
		if target == nil {
			return "匹配规则尚未配置内网目标。"
		}
		target.RLock()
		targetStr := strings.TrimSpace(target.TargetStr)
		target.RUnlock()
		if targetStr == "" {
			return "匹配规则尚未配置内网目标。"
		}
	}
	switch {
	case !matchedHost:
		return "没有匹配的域名规则。"
	case !matchedEnabled:
		return "匹配到的域名规则均已停用。"
	case !matchedScheme:
		return "请求协议与匹配规则的协议不一致。"
	case !matchedPath:
		return "请求路径没有匹配到已启用规则的路由。"
	default:
		return "规则当前没有可用的内网目标。"
	}
}

func (s *IndexController) HostDiagnose() {
	if s.Ctx.Request.Method == http.MethodGet {
		s.Data["menu"] = "host"
		s.SetInfo("host diagnose")
		s.display("index/hdiagnose")
		return
	}
	if !s.RequirePost() {
		return
	}

	host := common.GetIpByAddr(strings.TrimSpace(s.GetString("host")))
	path := strings.TrimSpace(s.GetString("path"))
	scheme := strings.ToLower(strings.TrimSpace(s.GetString("scheme")))
	if host == "" {
		s.AjaxErr("Host 不能为空")
		return
	}
	if scheme != "http" && scheme != "https" {
		s.AjaxErr("协议必须是 HTTP 或 HTTPS")
		return
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		s.AjaxErr("路径必须以 / 开头")
		return
	}
	parsedPath, err := url.ParseRequestURI(path)
	if err != nil {
		s.AjaxErr("路径格式无效")
		return
	}
	request := &http.Request{Host: host, URL: &url.URL{Scheme: scheme, Path: parsedPath.Path, RawQuery: parsedPath.RawQuery}, RequestURI: path}
	result := hostDiagnosticResult{Host: host, Path: path, Scheme: scheme}
	matched, matchErr := file.GetDb().GetInfoByHost(host, request)
	if matchErr != nil || !s.canAccessHost(matched) {
		result.Reason = s.explainHostDiagnosticFailure(host, path, scheme)
		s.Data["json"] = map[string]interface{}{"status": 1, "data": result}
		s.ServeJSON()
		s.StopRun()
		return
	}

	matched.RLock()
	client, target := matched.Client, matched.Target
	rule := &hostDiagnosticRule{
		ID:              matched.Id,
		Host:            matched.Host,
		Location:        matched.Location,
		Scheme:          matched.Scheme,
		Remark:          matched.Remark,
		PlatformManaged: matched.PlatformDomainID != "",
	}
	matched.RUnlock()
	if client != nil {
		client.RLock()
		clientID, clientRemark := client.Id, strings.TrimSpace(client.Remark)
		client.RUnlock()
		rule.Client = "客户端 " + strconv.Itoa(clientID)
		if clientRemark != "" {
			rule.Client += " · " + clientRemark
		}
	}
	if target != nil {
		if selected, targetErr := target.PreviewTarget(); targetErr == nil {
			rule.Target = selected
		} else {
			result.Reason = "规则已命中，但没有可用的内网目标。"
		}
	}
	if rule.Target == "" && result.Reason == "" {
		result.Reason = "规则已命中，但没有可用的内网目标。"
	}
	result.Matched = true
	result.Rule = rule
	s.Data["json"] = map[string]interface{}{"status": 1, "data": result}
	s.ServeJSON()
	s.StopRun()
}
