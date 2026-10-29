package controllers

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

// bridgeCertificateStatus is the administrator-only view of the single Bridge
// certificate shared by TCP TLS, WSS, QUIC and the encrypted inner links. Only
// paths and public certificate metadata are exposed; private-key contents never
// leave the server.
type bridgeCertificateStatus struct {
	CertFilePath string `json:"cert_file_path"`
	KeyFilePath  string `json:"key_file_path"`
	Configured   bool   `json:"configured"`
	Complete     bool   `json:"complete"`
	Valid        bool   `json:"valid"`
	Status       string `json:"status"`
	Tone         string `json:"tone"`
	Detail       string `json:"detail,omitempty"`

	CheckHostname string   `json:"check_hostname,omitempty"`
	HostnameOK    bool     `json:"hostname_ok"`
	Subject       string   `json:"subject,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	NotBefore     string   `json:"not_before,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	DaysRemaining int      `json:"days_remaining"`
	SANs          []string `json:"sans,omitempty"`
	SANSummary    string   `json:"san_summary,omitempty"`
	Fingerprint   string   `json:"fingerprint,omitempty"`
	ShortHash     string   `json:"short_hash,omitempty"`
	PublicKeyAlg  string   `json:"public_key_algorithm,omitempty"`
	ChainLength   int      `json:"chain_length"`
	SelfSigned    bool     `json:"self_signed"`

	RuntimeConfigured    bool     `json:"runtime_configured"`
	RuntimeStatus        string   `json:"runtime_status"`
	RuntimeTone          string   `json:"runtime_tone"`
	RuntimeExpiresAt     string   `json:"runtime_expires_at,omitempty"`
	RuntimeDaysRemaining int      `json:"runtime_days_remaining"`
	RuntimeFingerprint   string   `json:"runtime_fingerprint,omitempty"`
	RuntimeShortHash     string   `json:"runtime_short_hash,omitempty"`
	PendingRestart       bool     `json:"pending_restart"`
	RestartHint          string   `json:"restart_hint,omitempty"`
	TLSEnabled           bool     `json:"tls_enabled"`
	TLSBindings          []string `json:"tls_bindings,omitempty"`
}

func bridgeTLSBindings() []string {
	bindings := make([]string, 0, 4)
	if beego.AppConfig.DefaultBool("tls_enable", false) {
		port := beego.AppConfig.DefaultInt("tls_bridge_port", 8025)
		bindings = append(bindings, "TCP TLS :"+strconv.Itoa(port))
	}
	if port := beego.AppConfig.DefaultInt("bridge_wss_port", 0); port > 0 {
		bindings = append(bindings, "WSS :"+strconv.Itoa(port))
	}
	if port := beego.AppConfig.DefaultInt("bridge_quic_port", 0); port > 0 {
		bindings = append(bindings, "QUIC :"+strconv.Itoa(port))
	}
	if beego.AppConfig.DefaultBool("bridge_http3", false) &&
		beego.AppConfig.DefaultInt("bridge_quic_port", 0) <= 0 {
		bindings = append(bindings, "HTTP/3 Bridge")
	}
	return bindings
}

func normalizeBridgeCertificateHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		return ip.String()
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	parsed, err := url.Parse(raw)
	if err == nil {
		if hostname := strings.TrimSuffix(strings.TrimSpace(parsed.Hostname()), "."); hostname != "" {
			return hostname
		}
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if host, _, splitErr := net.SplitHostPort(raw); splitErr == nil {
		return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	}
	if strings.Count(raw, ":") == 1 {
		if host, _, splitErr := strings.Cut(raw, ":"); splitErr {
			raw = host
		}
	}
	return strings.Trim(raw, "[]")
}

func defaultBridgeCertificateHostname(values map[string]string) string {
	if hostname := normalizeBridgeCertificateHostname(values["bridge_addr"]); hostname != "" {
		return hostname
	}
	if global := file.GetDb().GetGlobal(); global != nil {
		return normalizeBridgeCertificateHostname(global.ServerUrl)
	}
	return ""
}

func shortCertificateFingerprint(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if len(fingerprint) <= 16 {
		return fingerprint
	}
	return fingerprint[:16] + "…"
}

func statusToneForCertificate(inspection certificateInspection) string {
	switch {
	case inspection.Expired || inspection.NotYetValid:
		return "danger"
	case inspection.Valid && inspection.DaysRemaining < 15:
		return "warning"
	case inspection.Valid:
		return "success"
	default:
		return "danger"
	}
}

func inspectRuntimeBridgeCertificate() (certificateInspection, bool) {
	certificate := crypt.GetCert()
	if len(certificate.Certificate) == 0 {
		return certificateInspection{}, false
	}
	inspection, _, err := inspectCertificateChain(certificate.Certificate)
	if err != nil {
		inspection.Message = firstLine(err.Error())
		return inspection, true
	}
	inspection.Valid = !inspection.Expired && !inspection.NotYetValid
	inspection.Message = "运行中"
	return inspection, true
}

func inspectBridgeCertificateFiles(certPath, keyPath, hostname string) (certificateInspection, string, error) {
	certPath, keyPath = strings.TrimSpace(certPath), strings.TrimSpace(keyPath)
	if certPath == "" && keyPath == "" {
		return certificateInspection{Message: "未配置证书"}, "未配置证书，重启后使用临时自签名证书", nil
	}
	if certPath == "" || keyPath == "" {
		return certificateInspection{Message: "证书路径和私钥路径必须同时填写"}, "证书配置不完整", errors.New("证书路径和私钥路径必须同时填写")
	}
	certPEM, err := os.ReadFile(common.ResolveConfigPath(certPath))
	if err != nil {
		inspection := certificateInspection{Message: firstLine(err.Error())}
		return inspection, "证书文件不可读", err
	}
	keyPEM, err := os.ReadFile(common.ResolveConfigPath(keyPath))
	if err != nil {
		inspection := certificateInspection{Message: firstLine(err.Error())}
		return inspection, "私钥文件不可读", err
	}
	inspection, err := inspectCertificatePair(certPEM, keyPEM, hostname, false)
	if err != nil {
		switch {
		case inspection.Expired:
			return inspection, "证书已过期", err
		case inspection.NotYetValid:
			return inspection, "证书尚未生效", err
		case hostname != "":
			return inspection, "证书不覆盖 Bridge 域名", err
		default:
			return inspection, "证书不可用", err
		}
	}
	if inspection.DaysRemaining < 15 {
		return inspection, "证书即将到期", nil
	}
	return inspection, "证书有效", nil
}

func bridgeCertificateStatusSnapshot(hostname string) bridgeCertificateStatus {
	status := bridgeCertificateStatus{
		Status:      "无法读取 Bridge 证书配置",
		Tone:        "danger",
		RuntimeTone: "danger",
		TLSBindings: bridgeTLSBindings(),
	}
	status.TLSEnabled = len(status.TLSBindings) > 0

	if runtimeInspection, ok := inspectRuntimeBridgeCertificate(); ok {
		status.RuntimeConfigured = crypt.IsCertLoadedFromFiles()
		status.RuntimeStatus = "运行中"
		status.RuntimeTone = statusToneForCertificate(runtimeInspection)
		status.RuntimeExpiresAt = runtimeInspection.ExpiresAt
		status.RuntimeDaysRemaining = runtimeInspection.DaysRemaining
		status.RuntimeFingerprint = runtimeInspection.Fingerprint
		status.RuntimeShortHash = shortCertificateFingerprint(runtimeInspection.Fingerprint)
		if !status.RuntimeConfigured {
			status.RuntimeStatus = "临时自签名证书"
			status.RuntimeTone = "muted"
		} else if runtimeInspection.Expired {
			status.RuntimeStatus = "运行中证书已过期"
		} else if runtimeInspection.NotYetValid {
			status.RuntimeStatus = "运行中证书尚未生效"
		} else if runtimeInspection.DaysRemaining < 15 {
			status.RuntimeStatus = "运行中证书即将到期"
		}
	}

	content, err := os.ReadFile(serverConfigPath())
	if err != nil {
		status.Detail = firstLine(err.Error())
		return status
	}
	values := serverConfigKeyValues(string(content))
	if strings.TrimSpace(hostname) == "" {
		hostname = defaultBridgeCertificateHostname(values)
	}
	status.CheckHostname = normalizeBridgeCertificateHostname(hostname)
	status.CertFilePath = strings.TrimSpace(values["bridge_cert_file"])
	status.KeyFilePath = strings.TrimSpace(values["bridge_key_file"])
	status.Configured = status.CertFilePath != "" || status.KeyFilePath != ""
	status.Complete = status.CertFilePath != "" && status.KeyFilePath != ""

	inspection, configuredStatus, inspectErr := inspectBridgeCertificateFiles(status.CertFilePath, status.KeyFilePath, status.CheckHostname)
	status.Status = configuredStatus
	status.Valid = inspection.Valid
	status.Tone = statusToneForCertificate(inspection)
	if !status.Configured {
		if !crypt.IsCertLoadedFromFiles() {
			status.Status = "未配置证书，当前使用临时自签名证书"
		}
		status.Tone = "muted"
	}
	if status.Configured && !status.Complete {
		status.Tone = "danger"
	}
	if inspectErr != nil {
		status.Detail = firstLine(inspectErr.Error())
	}
	if status.Status == "证书即将到期" {
		status.Tone = "warning"
	}
	status.HostnameOK = inspection.HostnameOK
	status.Subject = inspection.Subject
	status.Issuer = inspection.Issuer
	status.NotBefore = inspection.NotBefore
	status.ExpiresAt = inspection.ExpiresAt
	status.DaysRemaining = inspection.DaysRemaining
	status.SANs = append([]string(nil), inspection.SANs...)
	status.SANSummary = strings.Join(inspection.SANs, ", ")
	status.Fingerprint = inspection.Fingerprint
	status.ShortHash = shortCertificateFingerprint(inspection.Fingerprint)
	status.PublicKeyAlg = inspection.PublicKeyAlgorithm
	status.ChainLength = inspection.ChainLength
	status.SelfSigned = inspection.SelfSigned

	switch {
	case inspection.Valid && (!crypt.IsCertLoadedFromFiles() || status.Fingerprint != status.RuntimeFingerprint):
		status.PendingRestart = true
		status.RestartHint = "证书文件已更新，重启 NPS 后应用"
	case !status.Configured && crypt.IsCertLoadedFromFiles():
		status.PendingRestart = true
		status.RestartHint = "证书配置已清空，重启 NPS 后切换到临时证书"
	}
	return status
}

// BridgeCertificate returns the current file configuration and active runtime
// certificate without exposing private material.
func (s *GlobalController) BridgeCertificate() {
	if !s.RequireAdmin() {
		return
	}
	if s.Ctx.Request.Method != http.MethodGet {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status := bridgeCertificateStatusSnapshot(s.GetString("hostname"))
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1,
		"msg":    "ok",
		"data":   status,
	})
	s.ServeJSON()
	s.StopRun()
}

// CheckBridgeCertificate validates submitted Bridge certificate material
// without changing nps.conf.
func (s *GlobalController) CheckBridgeCertificate() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	serveCertificateCheck(&s.BaseController)
}

// SaveBridgeCertificate updates the two existing nps.conf keys atomically.
// Upload and paste modes persist only the public certificate and private key
// files under conf/bridgessl; the configuration itself remains nps.conf-only.
func (s *GlobalController) SaveBridgeCertificate() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	request := s.Ctx.Request
	mode := strings.ToLower(strings.TrimSpace(s.GetString("mode")))
	if mode == "" {
		mode = "path"
	}
	hostname := normalizeBridgeCertificateHostname(s.GetString("hostname"))
	before := bridgeCertificateStatusSnapshot(hostname)

	var updates []serverConfigUpdate
	certPath, keyPath := "", ""
	materialChanged := false
	if mode == "clear" {
		updates = []serverConfigUpdate{
			{Key: "bridge_cert_file", Omit: true},
			{Key: "bridge_key_file", Omit: true},
		}
	} else {
		certPEM, keyPEM, err := certificateMaterialFromRequest(request, mode)
		if err != nil {
			s.AjaxErr("Bridge 证书无效: " + err.Error())
			return
		}
		if _, inspectErr := inspectCertificatePair(certPEM, keyPEM, hostname, false); inspectErr != nil {
			s.AjaxErr("Bridge 证书无效: " + inspectErr.Error())
			return
		}
		if mode == "path" {
			certPath = strings.TrimSpace(request.FormValue("cert"))
			keyPath = strings.TrimSpace(request.FormValue("key"))
		} else {
			certPath, keyPath, err = persistCertificatePair("bridge", "server", certPEM, keyPEM)
			if err != nil {
				s.AjaxErr("Bridge 证书保存失败: " + err.Error())
				return
			}
			materialChanged = true
		}
		if strings.ContainsAny(certPath+keyPath, "\r\n") {
			s.AjaxErr("Bridge 证书路径不能包含换行")
			return
		}
		updates = []serverConfigUpdate{
			{Key: "bridge_cert_file", Value: certPath},
			{Key: "bridge_key_file", Value: keyPath},
		}
	}

	changed, err := writeServerConfig(serverConfigPath(), updates)
	if err != nil {
		s.AjaxErr("保存 Bridge 证书配置失败: " + err.Error())
		return
	}
	after := bridgeCertificateStatusSnapshot(hostname)
	restartRequired := len(changed) > 0 || materialChanged || after.PendingRestart
	message := "Bridge 证书配置已保存，需要重启 NPS 后生效"
	if !restartRequired {
		message = "Bridge 证书配置未发生变化"
	}
	result := map[string]interface{}{
		"status":           1,
		"msg":              message,
		"changed":          changed,
		"restart_required": restartRequired,
		"data":             after,
	}
	details := map[string]string{
		"mode":             mode,
		"changed":          strconv.Itoa(len(changed)),
		"restart_required": strconv.FormatBool(restartRequired),
	}
	if after.Fingerprint != "" {
		details["fingerprint"] = after.Fingerprint
	}
	if before.Fingerprint != "" && before.Fingerprint != after.Fingerprint {
		details["previous_fingerprint"] = before.Fingerprint
	}
	s.auditMutation("global.bridge_certificate.update", "nps.conf", 0, 0, "", nil, details)
	s.Data["json"] = s.withRequestMeta(result)
	s.ServeJSON()
	s.StopRun()
}
