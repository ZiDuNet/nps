package controllers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"html/template"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	beecontext "github.com/astaxie/beego/context"
)

type bridgeCertificateTestPair struct {
	certPEM  []byte
	keyPEM   []byte
	certPath string
	keyPath  string
}

func writeBridgeCertificateTestPair(t *testing.T, dnsNames []string, notBefore, notAfter time.Time) bridgeCertificateTestPair {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subject := "bridge.test"
	if len(dnsNames) > 0 && strings.TrimSpace(dnsNames[0]) != "" {
		subject = dnsNames[0]
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: subject},
		DNSNames:              append([]string(nil), dnsNames...),
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pair := bridgeCertificateTestPair{
		certPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		keyPEM:   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		certPath: filepath.Join(directory, "bridge.pem"),
		keyPath:  filepath.Join(directory, "bridge.key"),
	}
	if err := os.WriteFile(pair.certPath, pair.certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pair.keyPath, pair.keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return pair
}

func useBridgeCertificateTestRunPath(t *testing.T, content string) string {
	t.Helper()
	previous := common.ConfPath
	common.ConfPath = t.TempDir()
	t.Cleanup(func() {
		common.ConfPath = previous
	})
	confDirectory := filepath.Join(common.ConfPath, "conf")
	if err := os.MkdirAll(confDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(confDirectory, "nps.conf")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newBridgeCertificateTestController(t *testing.T, form url.Values) (*GlobalController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://console.test/global/bridgecertificate", strings.NewReader(form.Encode()))
	request.Host = "console.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	session := &clientPermissionSession{values: map[interface{}]interface{}{
		"auth":    true,
		"isAdmin": true,
	}}
	ctx.Input.CruSession = session
	controller := &GlobalController{}
	controller.Init(ctx, "GlobalController", "SaveBridgeCertificate", controller)
	controller.CruSession = session
	return controller, recorder
}

func runBridgeCertificateSave(t *testing.T, form url.Values) map[string]interface{} {
	t.Helper()
	controller, recorder := newBridgeCertificateTestController(t, form)
	runClientPermissionAction(t, controller.SaveBridgeCertificate)
	return readClientPermissionResponse(t, recorder)
}

func TestBridgeCertificateStatusAndPageShowPendingRestart(t *testing.T) {
	pair := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	useBridgeCertificateTestRunPath(t, strings.Join([]string{
		"web_port=8081",
		"bridge_cert_file=" + filepath.ToSlash(pair.certPath),
		"bridge_key_file=" + filepath.ToSlash(pair.keyPath),
	}, "\n"))

	status := bridgeCertificateStatusSnapshot("bridge.example.com")
	if !status.Valid || !status.Complete || status.Status != "证书有效" {
		t.Fatalf("configured certificate status = %#v", status)
	}
	if !status.PendingRestart {
		t.Fatalf("configured certificate should be pending restart before the process loads it: %#v", status)
	}
	if err := crypt.InitTlsFromFiles(pair.certPath, pair.keyPath); err != nil {
		t.Fatalf("load configured test certificate: %v", err)
	}
	t.Cleanup(crypt.InitTls)
	status = bridgeCertificateStatusSnapshot("bridge.example.com")
	if !status.RuntimeConfigured || status.PendingRestart {
		t.Fatalf("runtime certificate should match nps.conf without pending restart: %#v", status)
	}

	content, err := os.ReadFile("../views/global/certificate.html")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.New("global-certificate").Parse(string(content))
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]interface{}{
		"web_base_url":                        "",
		"serverUrl":                           "",
		"globalBlackIpList":                   "",
		"globalWhiteIpList":                   "",
		"globalIpListPriority":                0,
		"globalMaxConn":                       0,
		"globalRateLimit":                     0,
		"platformDomains":                     []platformDomainStatus{},
		"bridgeCertificate":                   status,
		"certificate_config_sections":         []serverConfigSectionView{},
		"certificate_config_summary":          serverConfigSummary{},
		"certificate_config_error":            "",
		"certificate_config_validation_error": "",
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("render certificate settings: %v", err)
	}
	page := rendered.String()
	for _, marker := range []string{
		"bridge-certificate-card",
		"Bridge TLS 证书",
		"当前运行证书",
		"nps.conf 配置证书",
		"待重启",
		"/global/bridgecertificate/check",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("Bridge certificate card is missing %q", marker)
		}
	}
}

func TestBridgeCertificateEmptyConfigurationUsesNeutralTemporaryState(t *testing.T) {
	crypt.InitTls()
	t.Cleanup(crypt.InitTls)
	useBridgeCertificateTestRunPath(t, "web_port=8081\nbridge_cert_file=\nbridge_key_file=\n")
	status := bridgeCertificateStatusSnapshot("")
	if status.Configured || status.Complete || status.PendingRestart {
		t.Fatalf("empty Bridge certificate configuration = %#v", status)
	}
	if status.Tone != "muted" || !strings.Contains(status.Status, "当前使用临时自签名证书") {
		t.Fatalf("empty Bridge certificate should use a neutral temporary state: %#v", status)
	}
}

func TestSaveBridgeCertificatePathPreservesConfigAndClearRemovesPair(t *testing.T) {
	pair := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	path := useBridgeCertificateTestRunPath(t, strings.Join([]string{
		"# deployment-specific comment",
		"web_port=8081",
		"custom_deployment_flag=keep-me",
		"",
	}, "\n"))

	response := runBridgeCertificateSave(t, url.Values{
		"mode":     {"path"},
		"cert":     {pair.certPath},
		"key":      {pair.keyPath},
		"hostname": {"bridge.example.com"},
	})
	if response["status"] != float64(1) {
		t.Fatalf("save response = %#v", response)
	}
	if response["restart_required"] != true {
		t.Fatalf("save response must require restart: %#v", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{
		"# deployment-specific comment",
		"web_port=8081",
		"bridge_cert_file=" + filepath.ToSlash(pair.certPath),
		"bridge_key_file=" + filepath.ToSlash(pair.keyPath),
		"custom_deployment_flag=keep-me",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("saved nps.conf is missing %q:\n%s", expected, text)
		}
	}

	response = runBridgeCertificateSave(t, url.Values{
		"mode": {"clear"},
	})
	if response["status"] != float64(1) {
		t.Fatalf("clear response = %#v", response)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = string(content)
	if strings.Contains(text, "bridge_cert_file=") || strings.Contains(text, "bridge_key_file=") {
		t.Fatalf("clear retained Bridge certificate keys:\n%s", text)
	}
	if !strings.Contains(text, "custom_deployment_flag=keep-me") {
		t.Fatalf("clear removed unrelated configuration:\n%s", text)
	}
}

func TestSaveBridgeCertificateRejectsIncompleteInvalidExpiredAndMismatchedPairs(t *testing.T) {
	valid := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	other := writeBridgeCertificateTestPair(t, []string{"other.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	expired := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	notYetValid := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour))

	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{
			name: "missing private key",
			form: url.Values{"mode": {"path"}, "cert": {valid.certPath}},
			want: "证书路径与私钥路径都必须填写",
		},
		{
			name: "mismatched private key",
			form: url.Values{"mode": {"path"}, "cert": {valid.certPath}, "key": {other.keyPath}},
			want: "证书与私钥不匹配",
		},
		{
			name: "expired certificate",
			form: url.Values{"mode": {"path"}, "cert": {expired.certPath}, "key": {expired.keyPath}},
			want: "未生效或已过期",
		},
		{
			name: "not yet valid certificate",
			form: url.Values{"mode": {"path"}, "cert": {notYetValid.certPath}, "key": {notYetValid.keyPath}},
			want: "未生效或已过期",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := useBridgeCertificateTestRunPath(t, "web_port=8081\nbridge_cert_file=old.pem\nbridge_key_file=old.key\n")
			response := runBridgeCertificateSave(t, test.form)
			if response["status"] != float64(0) || !strings.Contains(response["msg"].(string), test.want) {
				t.Fatalf("invalid certificate response = %#v, want error containing %q", response, test.want)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(content), "bridge_cert_file=old.pem") ||
				!strings.Contains(string(content), "bridge_key_file=old.key") {
				t.Fatalf("invalid certificate changed nps.conf:\n%s", content)
			}
		})
	}
}

func TestSaveBridgeCertificateUploadAndPastePersistManagedFiles(t *testing.T) {
	pair := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	for _, mode := range []string{"upload", "paste"} {
		t.Run(mode, func(t *testing.T) {
			path := useBridgeCertificateTestRunPath(t, "web_port=8081\nbridge_cert_file=\nbridge_key_file=\n")
			response := runBridgeCertificateSave(t, url.Values{
				"mode": {mode},
				"cert": {base64.StdEncoding.EncodeToString(pair.certPEM)},
				"key":  {base64.StdEncoding.EncodeToString(pair.keyPEM)},
			})
			if response["status"] != float64(1) {
				t.Fatalf("%s response = %#v", mode, response)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			values := serverConfigKeyValues(string(content))
			if values["bridge_cert_file"] != "conf/bridgessl/bridge_server.pem" ||
				values["bridge_key_file"] != "conf/bridgessl/bridge_server.key" {
				t.Fatalf("%s stored paths = %#v", mode, values)
			}
			storedCert, err := os.ReadFile(common.ResolveConfigPath(values["bridge_cert_file"]))
			if err != nil {
				t.Fatal(err)
			}
			storedKey, err := os.ReadFile(common.ResolveConfigPath(values["bridge_key_file"]))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(storedCert, pair.certPEM) || !bytes.Equal(storedKey, pair.keyPEM) {
				t.Fatalf("%s did not persist the submitted certificate material", mode)
			}
			info, err := os.Stat(common.ResolveConfigPath(values["bridge_key_file"]))
			if err != nil {
				t.Fatal(err)
			}
			if permissions := info.Mode().Perm(); permissions != 0600 {
				t.Fatalf("%s private key permissions = %o, want 600", mode, permissions)
			}
		})
	}
}

func TestManagedCertificateDirectoryUsesDedicatedBridgeFolder(t *testing.T) {
	bridgeDirectory, bridgeRelative := managedCertificateDirectory("bridge")
	if filepath.Base(bridgeDirectory) != "bridgessl" || bridgeRelative != "bridgessl" {
		t.Fatalf("bridge certificate directory = %q, %q", bridgeDirectory, bridgeRelative)
	}
	for _, scope := range []string{"host", "platform"} {
		directory, relative := managedCertificateDirectory(scope)
		if filepath.Base(directory) != "certs" || relative != "certs" {
			t.Fatalf("%s certificate directory = %q, %q", scope, directory, relative)
		}
	}
}

func TestNormalizeBridgeCertificateHostnameHandlesURLsAndIPv6(t *testing.T) {
	tests := map[string]string{
		"https://bridge.example.com:9443/path": "bridge.example.com",
		"bridge.example.com:8024":              "bridge.example.com",
		"[2001:db8::1]:8024":                   "2001:db8::1",
		"2001:db8::1":                          "2001:db8::1",
	}
	for input, want := range tests {
		if got := normalizeBridgeCertificateHostname(input); got != want {
			t.Fatalf("normalizeBridgeCertificateHostname(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInspectCertificatePairMarksSelfSignedLeaf(t *testing.T) {
	pair := writeBridgeCertificateTestPair(t, []string{"bridge.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	inspection, err := inspectCertificatePair(pair.certPEM, pair.keyPEM, "bridge.example.com", false)
	if err != nil {
		t.Fatalf("inspectCertificatePair() error = %v", err)
	}
	if !inspection.SelfSigned || !inspection.HostnameOK || inspection.ChainLength != 1 {
		t.Fatalf("certificate inspection = %#v", inspection)
	}
}
