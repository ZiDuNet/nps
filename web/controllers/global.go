package controllers

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
)

type GlobalController struct {
	BaseController
}

// ipRegistrationRow is the administrator-facing view of an in-memory IP
// registration. Keys are never rendered; only the owning bucket, address and
// expiry are exposed so an administrator can safely revoke access.
type ipRegistrationRow struct {
	ClientID  int    `json:"client_id"`
	Client    string `json:"client"`
	IP        string `json:"ip"`
	Until     string `json:"until"`
	Remaining string `json:"remaining"`
}

// IPRegisterPage renders the administrator view for short-lived IP
// registrations. Registration state is deliberately not persisted and is
// therefore presented as live runtime state rather than configuration.
func (s *GlobalController) IPRegisterPage() {
	if !s.RequireAdmin() {
		return
	}
	if s.Ctx.Request.Method != http.MethodGet {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.Data["menu"] = "ipregister"
	s.SetInfo("ipregister")
	s.Data["ip_registration_configured"] = ipRegistrationConfigured()
	s.Data["ip_registration_count"] = ipreg.Count()
	s.display("global/ipregister")
}

// IPRegisterList returns a paginated snapshot for the management table.
func (s *GlobalController) IPRegisterList() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	rows := ipreg.List()
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
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	clientID, err := strconv.Atoi(strings.TrimSpace(s.GetString("client_id")))
	if err != nil || clientID < 0 {
		s.AjaxErr("客户端标识无效")
		return
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
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	removed := ipreg.ClearExpired()
	s.auditMutation("global.ip_registration.clean", "ip_registration", 0, 0, "", nil, map[string]string{"removed": strconv.Itoa(removed)})
	s.AjaxOk("已清理 " + strconv.Itoa(removed) + " 条过期登记")
}

// IPRegisterClear explicitly resets all runtime registrations.
func (s *GlobalController) IPRegisterClear() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	count := ipreg.Count()
	ipreg.Reset()
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

// platformDomainStatus is rendered only on the administrator-only global
// settings page. The paths stay server-side and are never included in the
// public platform-domain options served to ordinary users.
type platformDomainStatus struct {
	ID             string
	Wildcard       string
	CertFilePath   string
	KeyFilePath    string
	Status         string
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
	pair, err := tls.LoadX509KeyPair(domain.CertFilePath, domain.KeyFilePath)
	if err != nil {
		status.Status = "读取或校验证书失败"
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

// requestedPlatformDomains distinguishes an omitted field from an explicit
// empty JSON array. Older scripts only knew about the original global fields;
// preserving the domain pool for those requests prevents a stale page from
// deleting otherwise valid platform settings.
func requestedPlatformDomains(request *http.Request) ([]file.PlatformDomain, bool, error) {
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
	var platformDomains []file.PlatformDomain
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
	}
	s.Data["platformDomains"] = platformDomainStatuses()
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

	platformDomains, submitted, err := requestedPlatformDomains(s.Ctx.Request)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if !submitted {
		platformDomains = file.GetDb().GetPlatformDomains()
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
	t := &file.Glob{
		BlackIpList:     blackIPList,
		WhiteIpList:     whiteIPList,
		IpListPriority:  priority,
		MaxConn:         maxConn,
		RateLimit:       rateLimit,
		ServerUrl:       s.getEscapeString("serverUrl"),
		PlatformDomains: platformDomains,
	}
	if current := file.GetDb().GetGlobal(); current != nil {
		current.RLock()
		t.DashboardKeyHash = current.DashboardKeyHash
		t.DashboardKeyCiphertext = current.DashboardKeyCiphertext
		current.RUnlock()
	}

	if err := file.GetDb().SaveGlobal(t); err != nil {
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
