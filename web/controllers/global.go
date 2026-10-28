package controllers

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/ipreg"
)

type GlobalController struct {
	BaseController
}

// ipRegistrationRow is the management view of an in-memory IP registration.
// Keys are never rendered; only the owning bucket, address and expiry are
// exposed so an administrator can audit the registry or a user can manage
// their own clients.
type ipRegistrationRow struct {
	ClientID  int    `json:"client_id"`
	Client    string `json:"client"`
	IP        string `json:"ip"`
	Until     string `json:"until"`
	Remaining string `json:"remaining"`
}

func (s *GlobalController) ipRegistrationScope() (bool, map[int]struct{}, bool) {
	if s.IsAdmin() {
		return true, nil, true
	}
	if s.userAPIAuthorized || s.hasActiveNonAdminPrincipal() {
		return false, s.GetAllowedClientIds(), true
	}
	s.rejectRequest(http.StatusForbidden, "permission denied")
	return false, nil, false
}

func ipRegistrationCount(admin bool, clientIDs map[int]struct{}) int {
	if admin {
		return ipreg.Count()
	}
	return ipreg.CountForClients(clientIDs)
}

// IPRegisterPage renders the account-scoped IP registration view.
// Registration state is deliberately not persisted and is therefore presented
// as live runtime state rather than configuration.
func (s *GlobalController) IPRegisterPage() {
	admin, _, allowed := s.ipRegistrationScope()
	if !allowed {
		return
	}
	if s.Ctx.Request.Method != http.MethodGet {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.Data["menu"] = "ipregister"
	s.SetInfo("ipregister")
	s.Data["ip_registration_configured"] = ipRegistrationConfigured()
	s.Data["ip_registration_admin"] = admin
	s.Data["ip_registration_count"] = ipRegistrationCount(admin, s.GetAllowedClientIds())
	s.display("global/ipregister")
}

// IPRegisterList returns a paginated snapshot for the management table.
func (s *GlobalController) IPRegisterList() {
	admin, allowedIDs, authorized := s.ipRegistrationScope()
	if !authorized || !s.RequirePost() {
		return
	}
	rows := ipreg.List()
	if !admin {
		rows = ipreg.ListForClients(allowedIDs)
	}
	search := strings.ToLower(strings.TrimSpace(s.GetString("search")))
	result := make([]*ipRegistrationRow, 0, len(rows))
	for _, item := range rows {
		clientName := "公共登记密钥"
		if item.ClientID != ipreg.AnyClient {
			clientName = "客户端 #" + strconv.Itoa(item.ClientID)
			if client, err := file.GetDb().GetClient(item.ClientID); err == nil && client != nil {
				client.RLock()
				if strings.TrimSpace(client.Remark) != "" {
					clientName = client.Remark
				}
				client.RUnlock()
			}
		}
		ipText := item.IP.String()
		if search != "" && !strings.Contains(strings.ToLower(ipText), search) && !strings.Contains(strings.ToLower(clientName), search) {
			continue
		}
		remaining := time.Until(item.Until)
		if remaining < 0 {
			remaining = 0
		}
		result = append(result, &ipRegistrationRow{
			ClientID:  item.ClientID,
			Client:    clientName,
			IP:        ipText,
			Until:     item.Until.Local().Format("2006-01-02 15:04:05"),
			Remaining: formatRegistrationRemaining(remaining),
		})
	}
	start, length := s.GetAjaxParams()
	if start < 0 {
		start = 0
	}
	if length <= 0 {
		length = 20
	}
	total := len(result)
	if start > total {
		start = total
	}
	end := start + length
	if end > total {
		end = total
	}
	s.AjaxTable(result[start:end], end-start, total, nil)
}

func formatRegistrationRemaining(remaining time.Duration) string {
	minutes := int(remaining / time.Minute)
	if minutes < 1 {
		return "少于 1 分钟"
	}
	if minutes < 60 {
		return strconv.Itoa(minutes) + " 分钟"
	}
	hours := minutes / 60
	if hours < 24 {
		return strconv.Itoa(hours) + " 小时"
	}
	days := hours / 24
	return strconv.Itoa(days) + " 天"
}

// IPRegisterRevoke revokes one client/global bucket registration.
func (s *GlobalController) IPRegisterRevoke() {
	admin, allowedIDs, authorized := s.ipRegistrationScope()
	if !authorized || !s.RequirePost() {
		return
	}
	clientID, err := strconv.Atoi(strings.TrimSpace(s.GetString("client_id")))
	if err != nil || clientID < 0 {
		s.AjaxErr("客户端标识无效")
		return
	}
	if !admin {
		if clientID == ipreg.AnyClient {
			s.rejectRequest(http.StatusForbidden, "公共 IP 登记仅管理员可管理")
			return
		}
		if _, visible := allowedIDs[clientID]; !visible {
			s.rejectRequest(http.StatusForbidden, "不能管理其他用户的 IP 登记")
			return
		}
	}
	ip := strings.TrimSpace(s.GetString("ip"))
	if !ipreg.RevokeString(clientID, ip) {
		s.AjaxErr("登记记录不存在或已过期")
		return
	}
	s.auditMutation("global.ip_registration.revoke", "ip_registration", clientID, 0, "", nil, map[string]string{"ip": ip})
	s.AjaxOk("已撤销 IP 登记")
}

// IPRegisterClean removes expired runtime registrations.
func (s *GlobalController) IPRegisterClean() {
	admin, allowedIDs, authorized := s.ipRegistrationScope()
	if !authorized || !s.RequirePost() {
		return
	}
	removed := ipreg.ClearExpired()
	if !admin {
		removed = ipreg.ClearExpiredForClients(allowedIDs)
	}
	s.auditMutation("global.ip_registration.clean", "ip_registration", 0, 0, "", nil, map[string]string{"removed": strconv.Itoa(removed)})
	s.AjaxOk("已清理 " + strconv.Itoa(removed) + " 条过期登记")
}

// IPRegisterClear explicitly resets all runtime registrations.
func (s *GlobalController) IPRegisterClear() {
	admin, allowedIDs, authorized := s.ipRegistrationScope()
	if !authorized || !s.RequirePost() {
		return
	}
	count := ipreg.Count()
	if admin {
		ipreg.Reset()
	} else {
		count = ipreg.ResetClients(allowedIDs)
	}
	s.auditMutation("global.ip_registration.clear", "ip_registration", 0, 0, "", nil, map[string]string{"removed": strconv.Itoa(count)})
	s.AjaxOk("已清空 IP 登记")
}

// Banlist exposes the in-memory login protection records to administrators.
// Records are never persisted and contain neither passwords nor session data.
func (s *GlobalController) Banlist() {
	if !s.RequireAdmin() {
		return
	}
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "banlist"
		s.SetInfo("banlist")
		s.display("global/banlist")
		return
	}
	if !s.RequirePost() {
		return
	}
	rows := currentLoginBanList()
	s.AjaxTable(rows, len(rows), len(rows), nil)
}

func (s *GlobalController) Unban() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if !removeLoginBan(s.GetString("key")) {
		s.AjaxErr("封禁记录不存在或已过期")
		return
	}
	s.auditMutation("global.login_ban.remove", "login_ban", 0, 0, "", nil, map[string]string{"key": s.GetString("key")})
	s.AjaxOk("解封成功")
}

func (s *GlobalController) Unbanall() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	removeAllLoginBans()
	s.auditMutation("global.login_ban.clear", "login_ban", 0, 0, "", nil, nil)
	s.AjaxOk("已清空登录封禁记录")
}

func (s *GlobalController) Banclean() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	cleanLoginBans()
	s.auditMutation("global.login_ban.clean", "login_ban", 0, 0, "", nil, nil)
	s.AjaxOk("已清理过期登录记录")
}

// geoFileStatus is the administrator-facing state of one geodata file.
type geoFileStatus struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// geoDataStatus is the aggregate geodata state shown on the global settings
// page and returned by the update endpoint.
type geoDataStatus struct {
	Version    string          `json:"version"`
	Configured bool            `json:"configured"`
	Loaded     bool            `json:"loaded"`
	Files      []geoFileStatus `json:"files"`
}

// geoUpdating keeps only one online geodata update running at a time.
var geoUpdating atomic.Bool

func geoStatusSnapshot() geoDataStatus {
	ipPath, sitePath := acl.GeoFiles()
	status := geoDataStatus{
		Configured: ipPath != "" || sitePath != "",
		Loaded:     acl.GeoDataConfigured(),
	}
	if ipPath != "" {
		status.Version = install.GeoDataVersion(ipPath)
	}
	for _, geoFile := range install.GeoFiles(ipPath, sitePath) {
		row := geoFileStatus{Name: geoFile.Name, Path: geoFile.Path}
		if info, err := os.Stat(geoFile.Path); err == nil {
			row.Exists = true
			row.Size = info.Size()
			row.ModTime = info.ModTime().Local().Format("2006-01-02 15:04:05")
		}
		status.Files = append(status.Files, row)
	}
	return status
}

// GeoStatus returns the current geodata state without performing any network
// request. The page uses it to refresh the card after an update.
func (s *GlobalController) GeoStatus() {
	if !s.RequireAdmin() {
		return
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1,
		"msg":    "ok",
		"data":   geoStatusSnapshot(),
	})
	s.ServeJSON()
	s.StopRun()
}

// UpdateGeoData downloads the latest geoip.dat and geosite.dat and applies
// them to the configured paths. A single update may run at a time; the files
// are verified against the upstream SHA-256 digests and replaced atomically.
func (s *GlobalController) UpdateGeoData() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if !geoUpdating.CompareAndSwap(false, true) {
		s.AjaxErr("Geo 数据更新正在进行中，请稍候")
		return
	}
	defer geoUpdating.Store(false)

	ipPath, sitePath := acl.GeoFiles()
	if ipPath == "" && sitePath == "" {
		s.AjaxErr("未配置 Geo 数据路径，无法在线更新")
		return
	}
	result, err := install.UpdateGeoData(ipPath, sitePath)
	if err != nil {
		s.AjaxErr("Geo 数据更新失败: " + err.Error())
		return
	}
	if len(result.Updated) > 0 {
		acl.ReloadGeoData()
		file.GetDb().RefreshAccessLists()
	}
	s.auditMutation("global.geodata.update", "global", 0, 0, "", nil, map[string]string{"version": result.Version, "updated": fmt.Sprintf("%d", len(result.Updated))})
	message := "Geo 数据已是最新"
	if len(result.Updated) > 0 {
		message = "Geo 数据更新成功，已更新 " + strconv.Itoa(len(result.Updated)) + " 个文件"
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1,
		"msg":    message,
		"data":   geoStatusSnapshot(),
	})
	s.ServeJSON()
	s.StopRun()
}

// platformDomainStatus is rendered only on the administrator-only global
// settings page. The paths stay server-side and are never included in the
// public platform-domain options served to ordinary users.
type platformDomainStatus struct {
	ID             string
	Wildcard       string
	CertFilePath   string
	KeyFilePath    string
	Status         string
	Detail         string
	ExpiresAt      string
	DaysRemaining  int
	ReferenceCount int
	Readable       bool
}

func inspectPlatformDomain(domain file.PlatformDomain) platformDomainStatus {
	status := platformDomainStatus{
		ID:           domain.ID,
		Wildcard:     domain.Wildcard,
		CertFilePath: domain.CertFilePath,
		KeyFilePath:  domain.KeyFilePath,
		Status:       "证书不可用",
	}
	if domain.CertFilePath == "" && domain.KeyFilePath == "" {
		status.Status = "未配置证书（仅 HTTP）"
		return status
	}
	pair, err := tls.LoadX509KeyPair(common.ResolveConfigPath(domain.CertFilePath), common.ResolveConfigPath(domain.KeyFilePath))
	if err != nil {
		status.Status = "读取或校验证书失败"
		status.Detail = firstLine(err.Error())
		return status
	}
	if len(pair.Certificate) == 0 {
		status.Status = "证书内容为空"
		return status
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		status.Status = "无法解析证书"
		return status
	}
	status.Readable = true
	status.ExpiresAt = certificate.NotAfter.Local().Format("2006-01-02 15:04:05")
	status.DaysRemaining = int(time.Until(certificate.NotAfter).Hours() / 24)
	probeHost := "probe." + strings.TrimPrefix(domain.Wildcard, "*.")
	if err := certificate.VerifyHostname(probeHost); err != nil {
		status.Status = "证书不覆盖该平台泛域名"
		return status
	}
	switch {
	case time.Now().After(certificate.NotAfter):
		status.Status = "证书已过期"
	case status.DaysRemaining < 15:
		status.Status = "即将到期"
	default:
		status.Status = "证书有效"
	}
	return status
}

// firstLine keeps UI error details compact while still showing the most
// actionable part of a load failure (usually the resolved path or PEM parse
// error).
func firstLine(message string) string {
	message = strings.TrimSpace(message)
	if index := strings.IndexAny(message, "\r\n"); index >= 0 {
		return message[:index]
	}
	if len(message) > 240 {
		return message[:240] + "…"
	}
	return message
}

// requestedPlatformDomain is the wire format for the platform-domain pool.
// CertMode/CertData/KeyData let the administrator paste or upload a
// certificate pair; path mode keeps the legacy path fields unchanged.
type requestedPlatformDomain struct {
	file.PlatformDomain
	CertMode string `json:"cert_mode"`
	CertData string `json:"cert_data"`
	KeyData  string `json:"key_data"`
}

// requestedPlatformDomains distinguishes an omitted field from an explicit
// empty JSON array. Older scripts only knew about the original global fields;
// preserving the domain pool for those requests prevents a stale page from
// deleting otherwise valid platform settings.
func requestedPlatformDomains(request *http.Request) ([]requestedPlatformDomain, bool, error) {
	if request == nil {
		return nil, false, errors.New("请求无效")
	}
	if err := request.ParseForm(); err != nil {
		return nil, false, fmt.Errorf("读取平台域名配置失败: %w", err)
	}
	values, submitted := request.Form["platform_domains"]
	if !submitted {
		return nil, false, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return nil, true, errors.New("平台域名配置不能为空；清空请提交 []")
	}
	var platformDomains []requestedPlatformDomain
	if err := json.Unmarshal([]byte(values[0]), &platformDomains); err != nil {
		return nil, true, fmt.Errorf("平台域名配置格式无效: %w", err)
	}
	return platformDomains, true, nil
}

func platformDomainStatuses() []platformDomainStatus {
	domains := file.GetDb().GetPlatformDomains()
	statuses := make([]platformDomainStatus, 0, len(domains))
	for _, domain := range domains {
		status := inspectPlatformDomain(domain)
		status.ReferenceCount = file.GetDb().PlatformDomainReferenceCount(domain.ID)
		statuses = append(statuses, status)
	}
	return statuses
}

func (s *GlobalController) Index() {
	if !s.RequireAdmin() {
		return
	}
	s.Data["menu"] = "global"
	s.SetInfo("global")

	// A fresh installation may not have a global.json yet. Keep the template
	// input typed and non-nil in that case; comparing a missing value with the
	// integer options in the priority select would otherwise make Beego abort
	// rendering and return an empty 200 response.
	s.Data["globalBlackIpList"] = ""
	s.Data["globalWhiteIpList"] = ""
	s.Data["globalIpListPriority"] = 0
	s.Data["globalMaxConn"] = 0
	s.Data["globalRateLimit"] = 0
	s.Data["serverUrl"] = ""

	global := file.GetDb().GetGlobal()
	if global != nil {
		s.Data["globalBlackIpList"] = strings.Join(global.BlackIpList, "\r\n")
		s.Data["globalWhiteIpList"] = strings.Join(global.WhiteIpList, "\r\n")
		s.Data["globalIpListPriority"] = global.IpListPriority
		s.Data["globalMaxConn"] = global.MaxConn
		s.Data["globalRateLimit"] = global.RateLimit
		s.Data["serverUrl"] = global.ServerUrl
		guideValue, guideError := usageGuideEditorValue(global)
		s.Data["helpSourceInput"] = guideValue
		s.Data["helpConfigError"] = guideError
	} else {
		s.Data["helpSourceInput"] = ""
		s.Data["helpConfigError"] = ""
	}
	s.Data["platformDomains"] = platformDomainStatuses()
	s.Data["geo"] = geoStatusSnapshot()
	s.display("global/index")
}

// 添加全局参数
func (s *GlobalController) Save() {
	if !s.RequirePost() {
		return
	}
	if !s.RequireAdmin() {
		return
	}

	requestedDomains, submitted, err := requestedPlatformDomains(s.Ctx.Request)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	var platformDomains []file.PlatformDomain
	if !submitted {
		platformDomains = file.GetDb().GetPlatformDomains()
	} else {
		platformDomains = make([]file.PlatformDomain, 0, len(requestedDomains))
		for _, domain := range requestedDomains {
			certData := strings.TrimSpace(domain.CertData)
			keyData := strings.TrimSpace(domain.KeyData)
			if certData != "" || keyData != "" {
				certPEM, certErr := decodeCertificateData(certData)
				if certErr != nil {
					s.AjaxErr("平台域名证书无效: " + certErr.Error())
					return
				}
				keyPEM, keyErr := decodeCertificateData(keyData)
				if keyErr != nil {
					s.AjaxErr("平台域名私钥无效: " + keyErr.Error())
					return
				}
				if _, inspectErr := inspectCertificatePair(certPEM, keyPEM, domain.Wildcard, true); inspectErr != nil {
					s.AjaxErr("平台域名证书无效: " + inspectErr.Error())
					return
				}
				certPath, keyPath, persistErr := persistCertificatePair("platform", domain.ID, certPEM, keyPEM)
				if persistErr != nil {
					s.AjaxErr("平台域名证书保存失败: " + persistErr.Error())
					return
				}
				domain.CertFilePath = certPath
				domain.KeyFilePath = keyPath
			}
			platformDomains = append(platformDomains, domain.PlatformDomain)
		}
	}

	maxConn := s.GetIntNoErr("globalMaxConn")
	rateLimit := s.GetIntNoErr("globalRateLimit")
	if maxConn < 0 {
		s.AjaxErr("全局最大连接数不能小于 0")
		return
	}
	if rateLimit < 0 {
		s.AjaxErr("全局限速不能小于 0")
		return
	}
	priority := s.GetIntNoErr("globalIpListPriority")
	if priority != 1 {
		priority = 0
	}
	blackIPList := parseIPRules(s.getEscapeString("globalBlackIpList"))
	whiteIPList := parseIPRules(s.getEscapeString("globalWhiteIpList"))
	if err := validateSourceAccessLists(acl.ScopeGlobal, priority, blackIPList, whiteIPList); err != nil {
		s.AjaxErr("全局 IP 名单无效: " + err.Error())
		return
	}
	t := file.GetDb().GetGlobal()
	if t == nil {
		t = &file.Glob{}
	}
	t.BlackIpList = blackIPList
	t.WhiteIpList = whiteIPList
	t.IpListPriority = priority
	t.MaxConn = maxConn
	t.RateLimit = rateLimit
	t.ServerUrl = s.getEscapeString("serverUrl")
	t.PlatformDomains = platformDomains

	var guideRollback func()
	if s.Ctx.Request.Form != nil {
		if _, submitted := s.Ctx.Request.Form["guide_source"]; submitted {
			update, err := prepareUsageGuideUpdate(s.GetString("guide_source"))
			if err != nil {
				s.AjaxErr("使用说明配置无效: " + err.Error())
				return
			}
			guideRollback = update.rollback
			t.UsageGuideMode = update.Mode
			t.UsageGuideSource = update.Source
		}
	}

	if err := file.GetDb().SaveGlobal(t); err != nil {
		if guideRollback != nil {
			guideRollback()
		}
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("global.update", "global", 0, 0, "", nil, map[string]string{"server_url": t.ServerUrl, "platform_domains": fmt.Sprintf("%d", len(t.PlatformDomains))})
	s.AjaxOk("save success")
}

func parseIPRules(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' || r == ',' })
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		rule := strings.TrimSpace(part)
		if rule == "" {
			continue
		}
		if _, exists := seen[rule]; exists {
			continue
		}
		seen[rule] = struct{}{}
		result = append(result, rule)
	}
	return result
}
