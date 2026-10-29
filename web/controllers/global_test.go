package controllers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"html/template"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
)

func TestGlobalIndexKeepsCertificateSettingsOnDedicatedTab(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/global/index.html"))
	data := map[string]interface{}{
		"web_base_url":         "",
		"serverUrl":            "nps.example.com",
		"globalBlackIpList":    "",
		"globalWhiteIpList":    "",
		"globalIpListPriority": 0,
		"globalMaxConn":        0,
		"globalRateLimit":      0,
		"geo": geoDataStatus{
			Files: []geoFileStatus{{Name: "geoip.dat", Path: "/tmp/geoip.dat"}},
		},
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute global index template: %v", err)
	}
	page := rendered.String()
	if !strings.Contains(page, "/global/certificate") {
		t.Fatal("global index must link to the certificate settings tab")
	}
	for _, marker := range []string{"bridge-certificate-card", `id="platform-domain-list"`, `id="platform-domains-input"`} {
		if strings.Contains(page, marker) {
			t.Fatalf("global index must not render certificate editor marker %q", marker)
		}
	}
}

func TestCertificateSettingsTemplateRendersCertificateEditorsAndScopedConfig(t *testing.T) {
	content := strings.Join([]string{
		"https_default_cert_file=conf/server.pem",
		"https_default_key_file=conf/server.key",
		"force_auto_ssl=false",
		"web_open_ssl=false",
		"tls_enable=true",
		"tls_bridge_port=8025",
	}, "\n")
	sections, summary, err := certificateConfigSections(content)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.ParseFiles("../views/global/certificate.html")
	if err != nil {
		t.Fatalf("certificate template does not parse: %v", err)
	}
	data := map[string]interface{}{
		"web_base_url":                        "",
		"config_path":                         "/tmp/nps/conf/nps.conf",
		"bridgeCertificate":                   bridgeCertificateStatus{Status: "证书有效", RuntimeStatus: "运行中", Tone: "success", RuntimeTone: "success"},
		"platformDomains":                     []platformDomainStatus{{ID: "platform-example", Wildcard: "*.example.com", Status: "证书有效"}},
		"certificate_config_sections":         sections,
		"certificate_config_summary":          summary,
		"certificate_config_error":            "",
		"certificate_config_validation_error": "",
	}
	var output bytes.Buffer
	if err := tpl.Execute(&output, data); err != nil {
		t.Fatalf("execute certificate template: %v", err)
	}
	page := output.String()
	for _, marker := range []string{
		"/global/index", "/global/registration", "/global/config", "/global/certificate",
		"bridge-certificate-card", "/global/bridgecertificate/check",
		"platform-domain-list", "/global/certificate/platform-domains",
		"https_default_cert_file", "config_force_auto_ssl",
		"config_web_open_ssl", "config_tls_bridge_port",
		`data-cert-depends-match="ssl_ca:ZeroSSL"`,
		"/global/certificate/config",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("certificate settings page is missing %q", marker)
		}
	}
}

func TestRequestedPlatformDomainsPreservesOmittedLegacyField(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("serverUrl=nps.example.com"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	domains, submitted, err := requestedPlatformDomains(request)
	if err != nil {
		t.Fatal(err)
	}
	if submitted || domains != nil {
		t.Fatalf("omitted platform_domains = %#v, submitted=%v; want no update", domains, submitted)
	}

	request = httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("platform_domains=%5B%5D"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	domains, submitted, err = requestedPlatformDomains(request)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted || len(domains) != 0 {
		t.Fatalf("explicit empty array = %#v, submitted=%v; want an intentional clear", domains, submitted)
	}

	request = httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("platform_domains="))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, submitted, err = requestedPlatformDomains(request); err == nil || !submitted {
		t.Fatalf("blank submitted field must be rejected, submitted=%v err=%v", submitted, err)
	}
}

func TestInspectPlatformDomainChecksWildcardCoverage(t *testing.T) {
	certPath, keyPath := writeControllerTestCertificate(t, []string{"*.example.com"})
	matched := inspectPlatformDomain(file.PlatformDomain{
		Wildcard:     "*.example.com",
		CertFilePath: certPath,
		KeyFilePath:  keyPath,
	})
	if !matched.Readable || matched.Status != "证书有效" {
		t.Fatalf("matching certificate status = %#v", matched)
	}

	mismatchedCert, mismatchedKey := writeControllerTestCertificate(t, []string{"*.other.example.com"})
	mismatched := inspectPlatformDomain(file.PlatformDomain{
		Wildcard:     "*.example.com",
		CertFilePath: mismatchedCert,
		KeyFilePath:  mismatchedKey,
	})
	if !mismatched.Readable || mismatched.Status != "证书不覆盖该平台泛域名" {
		t.Fatalf("mismatched certificate status = %#v", mismatched)
	}
}

func TestInspectPlatformDomainReportsHTTPOnlyWithoutCertificate(t *testing.T) {
	status := inspectPlatformDomain(file.PlatformDomain{ID: "http-only", Wildcard: "*.example.com"})
	if status.Readable || status.Status != "未配置证书（仅 HTTP）" {
		t.Fatalf("HTTP-only platform status = %#v", status)
	}
}

func writeControllerTestCertificate(t *testing.T, dnsNames []string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		DNSNames:              dnsNames,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(t.TempDir(), "platform.pem")
	keyPath := filepath.Join(t.TempDir(), "platform.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
