package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
)

// certificateInspection is the read-only result returned by the certificate
// validation widget. It never echoes private material or server paths.
type certificateInspection struct {
	Valid              bool     `json:"valid"`
	Message            string   `json:"message"`
	Subject            string   `json:"subject"`
	Issuer             string   `json:"issuer"`
	NotBefore          string   `json:"not_before"`
	ExpiresAt          string   `json:"expires_at"`
	DaysRemaining      int      `json:"days_remaining"`
	HostnameOK         bool     `json:"hostname_ok"`
	SANs               []string `json:"sans"`
	Fingerprint        string   `json:"fingerprint"`
	PublicKeyAlgorithm string   `json:"public_key_algorithm"`
	ChainLength        int      `json:"chain_length"`
	SelfSigned         bool     `json:"self_signed"`
	Expired            bool     `json:"expired"`
	NotYetValid        bool     `json:"not_yet_valid"`
}

// managedCertificateDirectory keeps Bridge TLS material isolated from
// administrator-uploaded host and platform certificates. Paths stored in
// nps.conf and JSON data are relative to the NPS run directory, so both the web
// process and the runtime loaders resolve them to the same files.
func managedCertificateDirectory(scope string) (string, string) {
	directory := "certs"
	if strings.EqualFold(strings.TrimSpace(scope), "bridge") {
		directory = "bridgessl"
	}
	return filepath.Join(common.GetRunPath(), "conf", directory), directory
}

func sanitizeCertificateToken(token string) string {
	token = strings.TrimSpace(token)
	var builder strings.Builder
	for _, r := range token {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
		if builder.Len() >= 64 {
			break
		}
	}
	return builder.String()
}

func randomCertificateToken() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func writeCertificateFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("创建证书目录失败: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".cert-*.tmp")
	if err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	return nil
}

// decodeCertificateData accepts either a base64 payload from the upload/paste
// widgets or, defensively, a PEM block submitted by an older client.
func decodeCertificateData(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.Contains(raw, "-----BEGIN") {
		return []byte(raw), nil
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, errors.New("证书内容不是有效的 Base64 编码")
	}
	return data, nil
}

// inspectCertificatePair validates a certificate/private-key pair. hostname is
// optional; when set, the leaf certificate must cover it. A wildcard hostname
// (for example "*.example.com") is probed with a synthetic "probe." prefix,
// matching the behaviour of the platform-domain certificate checker.
func inspectCertificatePair(certPEM, keyPEM []byte, hostname string, wildcard bool) (certificateInspection, error) {
	result := certificateInspection{}
	if len(strings.TrimSpace(string(certPEM))) == 0 || len(strings.TrimSpace(string(keyPEM))) == 0 {
		result.Message = "证书与私钥都必须填写"
		return result, errors.New(result.Message)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		result.Message = firstLine(err.Error())
		return result, fmt.Errorf("证书与私钥不匹配或格式错误: %s", result.Message)
	}
	result, certificate, err := inspectCertificateChain(pair.Certificate)
	if err != nil {
		return result, err
	}
	now := time.Now()
	result.NotYetValid = now.Before(certificate.NotBefore)
	result.Expired = now.After(certificate.NotAfter)
	if result.NotYetValid || result.Expired {
		result.Message = "证书当前未生效或已过期"
		return result, errors.New(result.Message)
	}
	probeHost := strings.TrimSpace(hostname)
	if wildcard {
		probeHost = "probe." + strings.TrimPrefix(probeHost, "*.")
	}
	if probeHost != "" {
		if err := certificate.VerifyHostname(probeHost); err != nil {
			result.Message = "证书不覆盖域名 " + probeHost
			return result, errors.New(result.Message)
		}
		result.HostnameOK = true
	}
	result.Valid = true
	result.Message = "证书有效"
	return result, nil
}

func inspectCertificateChain(chain [][]byte) (certificateInspection, *x509.Certificate, error) {
	result := certificateInspection{SANs: []string{}}
	if len(chain) == 0 {
		result.Message = "证书内容为空"
		return result, nil, errors.New(result.Message)
	}
	certificate, err := x509.ParseCertificate(chain[0])
	if err != nil {
		result.Message = firstLine(err.Error())
		return result, nil, fmt.Errorf("无法解析证书: %s", result.Message)
	}
	result.Subject = certificate.Subject.String()
	result.Issuer = certificate.Issuer.String()
	result.NotBefore = certificate.NotBefore.Local().Format("2006-01-02 15:04:05")
	result.ExpiresAt = certificate.NotAfter.Local().Format("2006-01-02 15:04:05")
	result.DaysRemaining = int(time.Until(certificate.NotAfter).Hours() / 24)
	result.SANs = append([]string(nil), certificate.DNSNames...)
	for _, address := range certificate.IPAddresses {
		result.SANs = append(result.SANs, address.String())
	}
	digest := sha256.Sum256(certificate.Raw)
	result.Fingerprint = hex.EncodeToString(digest[:])
	result.PublicKeyAlgorithm = certificate.PublicKeyAlgorithm.String()
	result.ChainLength = len(chain)
	result.SelfSigned = len(certificate.RawIssuer) > 0 &&
		len(certificate.RawSubject) > 0 &&
		string(certificate.RawIssuer) == string(certificate.RawSubject) &&
		certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature) == nil
	return result, certificate, nil
}

// certificateMaterialFromRequest resolves certificate bytes for the requested
// input mode. Path mode reads the files pointed to by the submitted paths;
// upload and paste modes decode base64 payloads.
func certificateMaterialFromRequest(request *http.Request, mode string) ([]byte, []byte, error) {
	if request == nil {
		return nil, nil, errors.New("请求无效")
	}
	if err := request.ParseForm(); err != nil {
		return nil, nil, fmt.Errorf("读取证书请求失败: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "path":
		certPath := strings.TrimSpace(request.FormValue("cert"))
		keyPath := strings.TrimSpace(request.FormValue("key"))
		if certPath == "" || keyPath == "" {
			return nil, nil, errors.New("证书路径与私钥路径都必须填写")
		}
		certPEM, certErr := common.ReadAllFromFile(common.ResolveConfigPath(certPath))
		if certErr != nil {
			return nil, nil, fmt.Errorf("读取证书文件失败: %s", firstLine(certErr.Error()))
		}
		keyPEM, keyErr := common.ReadAllFromFile(common.ResolveConfigPath(keyPath))
		if keyErr != nil {
			return nil, nil, fmt.Errorf("读取私钥文件失败: %s", firstLine(keyErr.Error()))
		}
		return certPEM, keyPEM, nil
	case "upload", "paste":
		certPEM, certErr := decodeCertificateData(request.FormValue("cert"))
		if certErr != nil {
			return nil, nil, certErr
		}
		keyPEM, keyErr := decodeCertificateData(request.FormValue("key"))
		if keyErr != nil {
			return nil, nil, keyErr
		}
		return certPEM, keyPEM, nil
	default:
		return nil, nil, errors.New("证书模式必须是 path、upload 或 paste")
	}
}

// persistCertificatePair validates a pair and atomically writes it under the
// scope-specific managed certificate directory. Bridge material uses
// conf/bridgessl; host and platform material continue to use conf/certs.
func persistCertificatePair(scope, id string, certPEM, keyPEM []byte) (string, string, error) {
	if _, err := inspectCertificatePair(certPEM, keyPEM, "", false); err != nil {
		return "", "", err
	}
	token := sanitizeCertificateToken(id)
	if token == "" {
		token = randomCertificateToken()
	}
	baseName := scope + "_" + token
	directory, relativeDirectory := managedCertificateDirectory(scope)
	certPath := filepath.Join(directory, baseName+".pem")
	keyPath := filepath.Join(directory, baseName+".key")
	if err := writeCertificateFileAtomic(certPath, certPEM, 0644); err != nil {
		return "", "", err
	}
	if err := writeCertificateFileAtomic(keyPath, keyPEM, 0600); err != nil {
		_ = os.Remove(certPath)
		return "", "", err
	}
	return filepath.ToSlash(filepath.Join("conf", relativeDirectory, baseName+".pem")),
		filepath.ToSlash(filepath.Join("conf", relativeDirectory, baseName+".key")), nil
}

func serveCertificateCheck(s *BaseController) {
	mode := strings.ToLower(strings.TrimSpace(s.GetString("mode")))
	if mode == "" {
		mode = "path"
	}
	certPEM, keyPEM, err := certificateMaterialFromRequest(s.Ctx.Request, mode)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	hostname := strings.TrimSpace(s.GetString("hostname"))
	wildcard := s.GetBoolNoErr("wildcard")
	inspection, inspectErr := inspectCertificatePair(certPEM, keyPEM, hostname, wildcard)
	if inspectErr != nil {
		inspection.Message = inspectErr.Error()
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1,
		"msg":    inspection.Message,
		"data":   inspection,
	})
	s.ServeJSON()
	s.StopRun()
}

// CheckCertificate validates certificate material without writing anything.
// The widget uses this for its "validate" button; host and platform saves
// repeat the same validation before persisting.
func (s *IndexController) CheckCertificate() {
	if !s.RequirePost() {
		return
	}
	serveCertificateCheck(&s.BaseController)
}

// CheckCertificate on the administrator settings page is intentionally a
// separate endpoint so platform wildcard validation remains admin-only while
// host validation stays available to ordinary users.
func (s *GlobalController) CheckCertificate() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	serveCertificateCheck(&s.BaseController)
}

// resolveHostCertificatePaths accepts only uploaded or pasted material for
// custom domains. Existing path-based certificates remain usable, but a
// request may only preserve the exact paths already stored on the host. This
// keeps legacy data compatible without exposing a server-file picker to Host
// owners. Platform-managed domains never persist host-level certificate paths.
func (s *IndexController) resolveHostCertificatePaths(
	hostID int,
	hostname string,
	request *http.Request,
	storedCertPath string,
	storedKeyPath string,
	platformManaged bool,
) (string, string, error) {
	if platformManaged {
		return "", "", nil
	}
	if request == nil {
		return "", "", errors.New("证书请求无效")
	}
	if err := request.ParseForm(); err != nil {
		return "", "", fmt.Errorf("读取证书请求失败: %w", err)
	}
	storedCertPath = strings.TrimSpace(storedCertPath)
	storedKeyPath = strings.TrimSpace(storedKeyPath)
	mode := strings.ToLower(strings.TrimSpace(request.FormValue("cert_mode")))
	if mode == "" {
		// Legacy forms may not submit a mode. With no new path values this
		// branch is a no-op and preserves the stored pair.
		mode = "path"
	}
	switch mode {
	case "path":
		requestedCert := strings.TrimSpace(request.FormValue("cert_file_path"))
		requestedKey := strings.TrimSpace(request.FormValue("key_file_path"))
		if requestedCert == "" && requestedKey == "" {
			return storedCertPath, storedKeyPath, nil
		}
		if requestedCert == storedCertPath && requestedKey == storedKeyPath {
			return storedCertPath, storedKeyPath, nil
		}
		return "", "", errors.New("自定义域名不支持服务器路径模式，请上传或粘贴证书")
	case "upload", "paste":
		certPEM, keyPEM, err := certificateMaterialFromRequest(request, mode)
		if err != nil {
			return "", "", err
		}
		if len(strings.TrimSpace(string(certPEM))) == 0 && len(strings.TrimSpace(string(keyPEM))) == 0 {
			return storedCertPath, storedKeyPath, nil
		}
		if _, err := inspectCertificatePair(certPEM, keyPEM, hostname, false); err != nil {
			return "", "", err
		}
		return persistCertificatePair("host", fmt.Sprintf("%d", hostID), certPEM, keyPEM)
	default:
		return "", "", errors.New("证书模式必须是 upload 或 paste；历史路径证书只能保持原值")
	}
}
