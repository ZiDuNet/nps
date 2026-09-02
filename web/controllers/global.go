package controllers

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
)

type GlobalController struct {
	BaseController
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

	global := file.GetDb().GetGlobal()
	if global != nil {
		s.Data["globalBlackIpList"] = strings.Join(global.BlackIpList, "\r\n")
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

	t := &file.Glob{
		BlackIpList:     RemoveRepeatedElement(strings.Split(s.getEscapeString("globalBlackIpList"), "\r\n")),
		ServerUrl:       s.getEscapeString("serverUrl"),
		PlatformDomains: platformDomains,
	}

	if err := file.GetDb().SaveGlobal(t); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.AjaxOk("save success")
}
