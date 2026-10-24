package controllers

import (
	"html/template"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	npsconf "ehang.io/nps/conf"
)

func TestServerConfigTemplateParses(t *testing.T) {
	if _, err := template.ParseFiles("../views/global/config.html"); err != nil {
		t.Fatalf("server config template does not parse: %v", err)
	}
}

func TestServerConfigSectionsExposeAllTemplateAndCompatibilityKeys(t *testing.T) {
	sections, unknown, err := serverConfigSections(npsconf.DefaultNpsConf)
	if err != nil {
		t.Fatalf("serverConfigSections() error = %v", err)
	}
	fields := make(map[string]serverConfigFieldView)
	for _, section := range sections {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}
	for _, key := range []string{"http_proxy_port", "bridge_port", "allow_ports", "web_password", "auth_crypt_key"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("configuration field %q is missing from grouped view", key)
		}
	}
	if fields["web_password"].DisplayValue != "已配置（留空保持）" {
		t.Fatalf("web_password should be masked, got %q", fields["web_password"].DisplayValue)
	}
	if fields["web_password"].Value != "" {
		t.Fatal("web_password raw value must not be sent to the template")
	}
	if fields["http_proxy_port"].Optional != true {
		t.Fatal("http_proxy_port should be represented as an optional listener")
	}
	for _, key := range []string{"bridge_kcp_port", "bridge_ws_port", "bridge_quic_port", "ip_register_port"} {
		if fields[key].Type != "int" {
			t.Fatalf("%s should be represented as an integer port, got %q", key, fields[key].Type)
		}
	}
	compatibilityVisible := false
	for _, section := range sections {
		for _, field := range section.Fields {
			if field.Key == "https_just_proxy" && !field.Editable {
				compatibilityVisible = true
			}
		}
	}
	if !compatibilityVisible || unknown == nil {
		t.Fatal("expected compatibility/custom fields to remain visible")
	}
	if !serverConfigSensitive("https_default_key_file") {
		t.Fatal("private key path must be treated as sensitive")
	}
}

func TestParseServerConfigUpdatesCanOmitOptionalSetting(t *testing.T) {
	defaults := serverConfigKeyValues(npsconf.DefaultNpsConf)
	form := url.Values{}
	for key, meta := range serverConfigMetaByKey {
		if meta.Optional {
			continue
		}
		value := defaults[key]
		if value == "" && !meta.Sensitive {
			value = "1"
		}
		form.Set("config_"+key, value)
	}
	request := httptest.NewRequest("POST", "/global/config/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updates, err := parseServerConfigUpdates(request, npsconf.DefaultNpsConf)
	if err != nil {
		t.Fatalf("parseServerConfigUpdates() error = %v", err)
	}
	foundOmit := false
	for _, update := range updates {
		if update.Key == "http_proxy_port" && update.Omit {
			foundOmit = true
		}
	}
	if !foundOmit {
		t.Fatal("an optional setting that was not enabled should be omitted")
	}

	form.Set("config_enabled_http_proxy_port", "1")
	form.Set("config_http_proxy_port", "80")
	request = httptest.NewRequest("POST", "/global/config/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updates, err = parseServerConfigUpdates(request, npsconf.DefaultNpsConf)
	if err != nil {
		t.Fatalf("parseServerConfigUpdates(enabled) error = %v", err)
	}
	for _, update := range updates {
		if update.Key == "http_proxy_port" && update.Omit {
			t.Fatal("enabled optional setting was unexpectedly omitted")
		}
	}
}

func TestWriteServerConfigPreservesCustomEntriesAndRemovesOmittedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nps.conf")
	original := "web_port=8081\ncustom_release_flag=kept\nhttp_proxy_port=80\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := writeServerConfig(path, []serverConfigUpdate{
		{Key: "http_proxy_port", Omit: true},
		{Key: "web_port", Value: "18081"},
	})
	if err != nil {
		t.Fatalf("writeServerConfig() error = %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed keys = %v, want two keys", changed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "http_proxy_port=") {
		t.Fatal("omitted optional key was retained")
	}
	if !strings.Contains(content, "web_port=18081") || !strings.Contains(content, "custom_release_flag=kept") {
		t.Fatalf("custom or updated entries were lost: %q", content)
	}
}

func TestValidateServerConfigUpdateSetRequiresCertificatePairs(t *testing.T) {
	if err := validateServerConfigUpdateSet(map[string]string{
		"web_open_ssl": "true", "web_cert_file": "server.pem", "web_key_file": "",
	}, nil); err == nil {
		t.Fatal("expected HTTPS certificate pair validation error")
	}
	if err := validateServerConfigUpdateSet(map[string]string{
		"ip_register_port": "9443", "ip_register_cert_file": "cert.pem", "ip_register_key_file": "key.pem",
	}, nil); err != nil {
		t.Fatalf("valid IP registration TLS pair rejected: %v", err)
	}
	if err := validateServerConfigUpdateSet(map[string]string{
		"ip_register_port": "9080",
	}, nil); err != nil {
		t.Fatalf("plain HTTP IP registration listener should remain supported: %v", err)
	}
}

func TestValidateServerConfigValueAcceptsLogLevelAliases(t *testing.T) {
	meta := serverConfigMetaByKey["log_level"]
	for _, value := range []string{"6", "debug", "warning", "off"} {
		if err := validateServerConfigValue("log_level", meta, value); err != nil {
			t.Fatalf("log level %q rejected: %v", value, err)
		}
	}
	if err := validateServerConfigValue("log_level", meta, "9"); err == nil {
		t.Fatal("invalid numeric log level was accepted")
	}
}

func TestInferServerConfigTypeDoesNotTreatNumericZeroAsBoolean(t *testing.T) {
	for _, value := range []string{"0", "1", "10"} {
		if got := inferServerConfigType("listener_port", value); got != "int" {
			t.Fatalf("inferServerConfigType(listener_port, %q) = %q, want int", value, got)
		}
	}
	for _, value := range []string{"true", "false"} {
		if got := inferServerConfigType("feature", value); got != "bool" {
			t.Fatalf("inferServerConfigType(feature, %q) = %q, want bool", value, got)
		}
	}
}

func TestRunningUnderServiceManagerFindsTrailingServiceMarker(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	os.Args = []string{"nps", "-conf_path=/etc/nps", "service"}
	if !runningUnderServiceManager() {
		t.Fatal("service marker after configuration flags was not detected")
	}
	os.Args = []string{"nps", "-conf_path=/tmp/nps"}
	if runningUnderServiceManager() {
		t.Fatal("foreground process was incorrectly classified as a service")
	}
}
