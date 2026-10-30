package controllers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"html/template"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/bridgeendpoint"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/quickcommand"
	"github.com/astaxie/beego"
)

func TestClientRemoteUpdateVersionGate(t *testing.T) {
	for _, tt := range []struct {
		version string
		want    bool
	}{
		{version: "1.1.22", want: false},
		{version: "v1.1.23", want: true},
		{version: "1.1.24", want: true},
		{version: "unknown", want: false},
	} {
		t.Run(tt.version, func(t *testing.T) {
			if got := supportsRemoteClientUpdate(tt.version); got != tt.want {
				t.Fatalf("supportsRemoteClientUpdate(%q) = %t, want %t", tt.version, got, tt.want)
			}
		})
	}
}

func TestClientListRowsExposeUserName(t *testing.T) {
	rows := newClientListRows([]*file.Client{
		{
			Id:          1,
			UserId:      10,
			UserName:    "alice",
			VerifyKey:   "client-vkey",
			WebPassword: "web-secret",
			Cnf: &file.Config{
				U: "basic-user",
				P: "basic-secret",
			},
		},
	})

	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal client list rows: %v", err)
	}

	got := string(b)
	if !strings.Contains(got, `"UserName":"alice"`) {
		t.Fatalf("expected client list row to expose UserName, got %s", got)
	}
	for _, expected := range []string{`"UserRemark":""`, `"WebUserName":""`, `"WebPasswordSet":true`, `"BasicUserName":"basic-user"`, `"BasicPasswordSet":true`} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected client list row to expose safe credential metadata %s, got %s", expected, got)
		}
	}
	for _, secret := range []string{"web-secret", "basic-user", "basic-secret"} {
		if secret != "basic-user" && strings.Contains(got, secret) {
			t.Fatalf("client list row must not expose credential %q: %s", secret, got)
		}
	}
}

func TestClientListRowsResolveOwnerRemark(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Users.Store(10, &file.User{
		Id:       10,
		UserName: "alice&amp;ops",
		Remark:   "研发&amp;运维",
		Status:   true,
	})
	rows := newClientListRows([]*file.Client{{
		Id:        1,
		UserId:    10,
		UserName:  "stale",
		VerifyKey: "client-vkey",
	}})
	if len(rows) != 1 {
		t.Fatalf("client row count = %d, want 1", len(rows))
	}
	if rows[0].UserName != "alice&ops" || rows[0].UserRemark != "研发&运维" {
		t.Fatalf("client owner = %q / %q, want alice&ops / 研发&运维", rows[0].UserName, rows[0].UserRemark)
	}
}

func TestClientFormTemplatesExposeUserSelect(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "add", path: "../views/client/add.html"},
		{name: "edit", path: "../views/client/edit.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := readClientTemplateForTest(t, tt.path)
			for _, expected := range []string{
				`id="user_id"`,
				`所属用户`,
				`name="user_id"`,
				`{{range .users}}`,
				`{{.UserName}}`,
			} {
				if !strings.Contains(content, expected) {
					t.Fatalf("%s template misses user select marker: %s", tt.name, expected)
				}
			}
			workflowName := map[string]string{"add": "client-create", "edit": "client-edit"}[tt.name]
			workflow := `data-form-workflow="` + workflowName + `"`
			if !strings.Contains(content, workflow) {
				t.Fatalf("%s template misses workflow marker: %s", tt.name, workflow)
			}
			for _, expected := range []string{
				`class="form-page form-page-client`,
				`form-page-back`,
				`form-workflow-card`,
			} {
				if !strings.Contains(content, expected) {
					t.Fatalf("%s template misses shared form layout marker: %s", tt.name, expected)
				}
			}
		})
	}
}

func TestLegacyClientLoginMergePreservesAndClearsExplicitly(t *testing.T) {
	username, password, err := mergeLegacyClientLogin("legacy-user", "legacy-pass", "", "", false)
	if err != nil || username != "legacy-user" || password != "legacy-pass" {
		t.Fatalf("blank edit should preserve legacy credentials: username=%q password=%q err=%v", username, password, err)
	}
	username, password, err = mergeLegacyClientLogin("legacy-user", "legacy-pass", "", "replacement", false)
	if err != nil || username != "legacy-user" || password != "replacement" {
		t.Fatalf("password-only edit should retain the username: username=%q password=%q err=%v", username, password, err)
	}
	username, password, err = mergeLegacyClientLogin("legacy-user", "legacy-pass", "", "", true)
	if err != nil || username != "" || password != "" {
		t.Fatalf("explicit clear should remove both legacy credentials: username=%q password=%q err=%v", username, password, err)
	}
	if _, _, err := mergeLegacyClientLogin("", "", "only-user", "", false); err == nil {
		t.Fatal("partial legacy credentials should be rejected")
	}
	if username, password, err := mergeLegacyClientLogin("legacy-user", "", "", "", false); err != nil || username != "legacy-user" || password != "" {
		t.Fatalf("unrelated edits should preserve an incomplete historical pair: username=%q password=%q err=%v", username, password, err)
	}
}

func TestRequestedClientOwnerIDRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"abc", "-1", "1.5"} {
		if _, err := parseClientOwnerID(value); err == nil {
			t.Fatalf("malformed owner %q should be rejected", value)
		}
	}
	for _, value := range []string{"", "0", "7"} {
		want := 0
		if value == "7" {
			want = 7
		}
		if got, err := parseClientOwnerID(value); err != nil || got != want {
			t.Fatalf("owner %q should be accepted as id %d, got %d err=%v", value, want, got, err)
		}
	}
}

func TestClientFormTemplatesKeepLegacyLoginSecondaryAndMasked(t *testing.T) {
	add := readClientTemplateForTest(t, "../views/client/add.html")
	edit := readClientTemplateForTest(t, "../views/client/edit.html")
	for name, content := range map[string]string{"add": add, "edit": edit} {
		if !strings.Contains(content, `class="form-workflow-advanced legacy-client-login-settings"`) {
			t.Fatalf("%s template must keep legacy login in a progressive disclosure section", name)
		}
		if !strings.Contains(content, "旧版客户端登录兼容") {
			t.Fatalf("%s template misses the legacy login explanation", name)
		}
	}
	if strings.Contains(edit, `value="{{.c.WebPassword}}"`) {
		t.Fatal("edit template must never render the stored legacy password")
	}
	for _, marker := range []string{"留空则保持现有密码", "clear_legacy_web_login"} {
		if !strings.Contains(edit, marker) {
			t.Fatalf("edit template misses safe legacy password handling marker: %s", marker)
		}
	}
	for _, path := range []string{"../views/client/add.html", "../views/client/edit.html"} {
		if _, err := template.ParseFiles(path); err != nil {
			t.Fatalf("client form template %s must remain valid Go template: %v", path, err)
		}
	}
}

func TestClientListTemplateUsesSafeAccessibleControls(t *testing.T) {
	content := readClientTemplateForTest(t, "../views/client/list.html")
	for _, marker := range []string{
		`$('body').setLang('#table');`,
		`npsBooleanMarkup(!!row.ConfigConnAllow)`,
		`npsApplyTableState(this, 'client')`,
		`type="button"`,
		`data-title-en="Disable client"`,
		`data-title-en="Delete client"`,
		`data-title-en="Copy command"`,
		`storageKey: 'client-v4'`,
		`client-version-pill`,
		`npsNotify('success', npsIsEnglish() ? 'Copied' : '复制成功')`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("client list misses safety/accessibility marker: %s", marker)
		}
	}
	for _, credentialField := range []string{"row.WebPassword,", "config.U", "config.P"} {
		if strings.Contains(content, credentialField) {
			t.Fatalf("client list must not render credential field: %s", credentialField)
		}
	}
	for _, marker := range []string{"row.WebUserName", "row.WebPasswordSet", "row.BasicUserName", "row.BasicPasswordSet"} {
		if !strings.Contains(content, marker) {
			t.Fatalf("client list must render safe credential metadata: %s", marker)
		}
	}
}

func TestClientListTemplateUsesServerGeneratedProtocolCommands(t *testing.T) {
	content := readClientTemplateForTest(t, "../views/client/list.html")
	for _, marker := range []string{
		"row.ConnectionCommands",
		"data-client-protocol-tab",
		"data-client-protocol-panel",
		"command.NormalCommand",
		"command.QuickCommand",
		"ConnectionProtocol",
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("client list misses data-driven protocol marker %q", marker)
		}
	}
	if strings.Contains(content, "-type={{.bridgeType}}") {
		t.Fatal("client list must not hard-code Bridge commands in the template")
	}
}

func TestAttachClientBridgeCommandsProducesCompatibleFormats(t *testing.T) {
	keys := []string{
		"bridge_type", "bridge_addr", "bridge_port", "bridge_path",
		"bridge_tcp_show", "bridge_tcp_show_ip", "bridge_tcp_show_port",
		"bridge_tls_show", "bridge_tls_show_ip", "bridge_tls_show_port",
		"bridge_ws_show", "bridge_ws_show_ip", "bridge_ws_show_port",
		"tls_enable", "tls_bridge_port", "bridge_ws_port", "bridge_kcp_port",
		"bridge_wss_port", "bridge_quic_port", "http3_proxy_port", "bridge_http3",
	}
	previous := make(map[string]string, len(keys))
	for _, key := range keys {
		previous[key] = beego.AppConfig.String(key)
	}
	t.Cleanup(func() {
		for key, value := range previous {
			beego.AppConfig.Set(key, value)
		}
	})
	for key, value := range map[string]string{
		"bridge_type": "tcp", "bridge_addr": "bridge.example.com", "bridge_port": "8024",
		"bridge_path": "/ws", "bridge_tcp_show": "true", "bridge_tcp_show_port": "8024",
		"bridge_tls_show": "true", "bridge_tls_show_port": "8025",
		"bridge_ws_show": "true", "bridge_ws_show_port": "443",
		"tls_enable": "true", "tls_bridge_port": "8025", "bridge_ws_port": "8027",
		"bridge_kcp_port": "0", "bridge_wss_port": "0", "bridge_quic_port": "0",
		"http3_proxy_port": "0", "bridge_http3": "false",
	} {
		beego.AppConfig.Set(key, value)
	}
	rows := []*clientListRow{{Remark: "兼容测试", VerifyKey: "test-vkey"}}
	endpoints := attachClientBridgeCommands(rows, "panel.example.com")
	if len(endpoints) < 2 || len(rows[0].ConnectionCommands) < 2 {
		t.Fatalf("expected TCP and TLS commands, endpoints=%#v rows=%#v", endpoints, rows[0].ConnectionCommands)
	}
	byKey := make(map[string]clientBridgeCommand)
	for _, command := range rows[0].ConnectionCommands {
		byKey[command.Key] = command
	}
	for _, key := range []string{"tcp", "tls"} {
		command, ok := byKey[key]
		if !ok {
			t.Fatalf("missing %s command: %#v", key, rows[0].ConnectionCommands)
		}
		if command.QuickFormat != "v1" {
			t.Fatalf("%s quick command must stay v1 for old clients: %#v", key, command)
		}
		decoded, err := quickcommand.DecodeBase64(command.QuickCommand)
		if err != nil {
			t.Fatalf("decode %s quick command: %v", key, err)
		}
		if decoded.Server != "bridge.example.com:"+map[string]string{"tcp": "8024", "tls": "8025"}[key] {
			t.Fatalf("unexpected %s server: %#v", key, decoded)
		}
	}
	if byKey["tls"].TLS != true || !strings.Contains(byKey["tls"].NormalCommand, "-tls_enable=true") {
		t.Fatalf("TLS command is incomplete: %#v", byKey["tls"])
	}
	if byKey["ws"].QuickFormat != "v2" {
		t.Fatalf("WS quick command must use v2: %#v", byKey["ws"])
	}
	if protocol := bridgeendpoint.ProtocolName("tls"); protocol != "TCP + TLS" {
		t.Fatalf("unexpected TLS protocol label %q", protocol)
	}
}

func TestAttachClientBridgeCommandsOnlyPinsSelfSignedTLS(t *testing.T) {
	keys := []string{
		"bridge_type", "bridge_addr", "bridge_port", "bridge_path",
		"bridge_tcp_show", "bridge_tcp_show_ip", "bridge_tcp_show_port",
		"bridge_tls_show", "bridge_tls_show_ip", "bridge_tls_show_port",
		"bridge_ws_show", "bridge_ws_show_ip", "bridge_ws_show_port",
		"tls_enable", "tls_bridge_port", "bridge_ws_port", "bridge_kcp_port",
		"bridge_wss_port", "bridge_quic_port", "http3_proxy_port", "bridge_http3",
	}
	previous := make(map[string]string, len(keys))
	for _, key := range keys {
		previous[key] = beego.AppConfig.String(key)
	}
	t.Cleanup(func() {
		for key, value := range previous {
			_ = beego.AppConfig.Set(key, value)
		}
	})
	for key, value := range map[string]string{
		"bridge_type": "tcp", "bridge_addr": "bridge.example.com", "bridge_port": "8024",
		"bridge_path": "/ws", "bridge_tcp_show": "true", "bridge_tcp_show_port": "8024",
		"bridge_tls_show": "true", "bridge_tls_show_port": "8025",
		"bridge_ws_show": "true", "bridge_ws_show_port": "8027",
		"tls_enable": "true", "tls_bridge_port": "8025", "bridge_ws_port": "8027",
		"bridge_kcp_port": "0", "bridge_wss_port": "0", "bridge_quic_port": "0",
		"http3_proxy_port": "0", "bridge_http3": "false",
	} {
		_ = beego.AppConfig.Set(key, value)
	}

	crypt.InitTls()
	t.Cleanup(crypt.InitTls)
	selfSignedRows := []*clientListRow{{Remark: "自签证书", VerifyKey: "self-signed-vkey"}}
	attachClientBridgeCommands(selfSignedRows, "panel.example.com")
	selfSignedTLS := findClientBridgeCommand(selfSignedRows[0].ConnectionCommands, "tls")
	selfSignedCommand, err := quickcommand.DecodeBase64(selfSignedTLS.QuickCommand)
	if err != nil {
		t.Fatalf("decode self-signed TLS command: %v", err)
	}
	if selfSignedCommand.TLSFingerprint == "" {
		t.Fatal("self-signed TLS command must include a certificate fingerprint")
	}

	certPath, keyPath := writeCASignedBridgeTestPair(t)
	if err := crypt.InitTlsFromFiles(certPath, keyPath); err != nil {
		t.Fatalf("load CA-signed Bridge certificate: %v", err)
	}
	caRows := []*clientListRow{{Remark: "CA 证书", VerifyKey: "ca-signed-vkey"}}
	attachClientBridgeCommands(caRows, "panel.example.com")
	caTLS := findClientBridgeCommand(caRows[0].ConnectionCommands, "tls")
	caCommand, err := quickcommand.DecodeBase64(caTLS.QuickCommand)
	if err != nil {
		t.Fatalf("decode CA-signed TLS command: %v", err)
	}
	if caCommand.TLSFingerprint != "" {
		t.Fatalf("CA-signed TLS command must rely on system trust instead of pinning: %#v", caCommand)
	}
	if caTLS.QuickFormat != "v1" {
		t.Fatalf("CA-signed TLS quick command should remain v1-compatible: %#v", caTLS)
	}
}

func findClientBridgeCommand(commands []clientBridgeCommand, key string) clientBridgeCommand {
	for _, command := range commands {
		if command.Key == key {
			return command
		}
	}
	return clientBridgeCommand{}
}

func writeCASignedBridgeTestPair(t *testing.T) (string, string) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NPS Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "bridge.example.com"},
		DNSNames:              []string{"bridge.example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath := filepath.Join(directory, "bridge.pem")
	keyPath := filepath.Join(directory, "bridge.key")
	certPEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...,
	)
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func readClientTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(b)
}
