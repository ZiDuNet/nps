package controllers

import (
	"bytes"
	"errors"
	"html"
	"html/template"
	"net/http"
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

func TestServerConfigTemplateUsesResponsiveDenseCardGrid(t *testing.T) {
	content, err := os.ReadFile("../views/global/config.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(content)
	for _, marker := range []string{
		"grid-template-columns: repeat(3, minmax(0, 1fr))",
		"@media (min-width: 1600px)",
		"grid-template-columns: repeat(4, minmax(0, 1fr))",
		"@media (max-width: 1180px)",
		"grid-template-columns: minmax(0, 1fr); padding: 7px",
		"body.nps-bootstrap .server-config-field__control .input-group > .server-config-secret-reveal",
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("server config template is missing responsive grid rule %q", marker)
		}
	}
}

func TestServerConfigTemplateRendersConfiguredValues(t *testing.T) {
	content := strings.Join([]string{
		"http_proxy_ip=127.0.0.1",
		"http_proxy_port=30111",
		"https_default_cert_file=conf/server.pem",
		"https_default_key_file=conf/server.key",
		"web_host=127.0.0.1",
		"web_username=wushuo",
		"web_password=secret-password",
		"register_email_verify=false",
		"register_email_code_ttl=600",
		"register_email_code_cooldown=60",
	}, "\n")
	sections, unknown, err := serverConfigSections(content)
	if err != nil {
		t.Fatalf("serverConfigSections() error = %v", err)
	}
	tmpl, err := template.ParseFiles("../views/global/config.html")
	if err != nil {
		t.Fatalf("parse server config template: %v", err)
	}
	var output bytes.Buffer
	err = tmpl.Execute(&output, map[string]interface{}{
		"config_path":             "/tmp/nps/conf/nps.conf",
		"config_sections":         sections,
		"config_unknown":          unknown,
		"config_summary":          summarizeServerConfigSections(sections, unknown),
		"config_error":            "",
		"config_validation_error": "",
		"web_base_url":            "",
	})
	if err != nil {
		t.Fatalf("render server config template: %v", err)
	}
	html := output.String()
	for _, fragment := range []string{
		`name="config_http_proxy_ip" value="127.0.0.1"`,
		`name="config_http_proxy_port" value="30111"`,
		`name="config_https_default_key_file" value="conf/server.key"`,
		`name="config_web_username" value="wushuo"`,
		`name="config_register_email_code_ttl" value="600"`,
		`name="config_register_email_code_cooldown" value="60"`,
		`data-config-value="wushuo"`,
		`data-config-value="600"`,
		`data-config-value="60"`,
		`hydrateInitialValues()`,
		`window.addEventListener('pageshow', hydrateInitialValues)`,
		`sourceLinesNode.scrollTop`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("rendered configuration is missing %q", fragment)
		}
	}
	if strings.Contains(html, "secret-password") {
		t.Fatal("administrator password must not be rendered into the page")
	}
}

func TestServerConfigTemplateRendersEveryConfiguredField(t *testing.T) {
	content := npsconf.DefaultNpsConf + "\ncustom_deployment_flag=keep-me\n"
	sections, unknown, err := serverConfigSections(content)
	if err != nil {
		t.Fatalf("serverConfigSections() error = %v", err)
	}
	tmpl, err := template.ParseFiles("../views/global/config.html")
	if err != nil {
		t.Fatalf("parse server config template: %v", err)
	}
	var output bytes.Buffer
	err = tmpl.Execute(&output, map[string]interface{}{
		"config_path":             "/tmp/nps/conf/nps.conf",
		"config_sections":         sections,
		"config_unknown":          unknown,
		"config_summary":          summarizeServerConfigSections(sections, unknown),
		"config_error":            "",
		"config_validation_error": "",
		"web_base_url":            "",
	})
	if err != nil {
		t.Fatalf("render server config template: %v", err)
	}
	rendered := output.String()
	for _, section := range sections {
		for _, field := range section.Fields {
			if !strings.Contains(rendered, field.Key) {
				t.Fatalf("configuration key %q is missing from rendered output", field.Key)
			}
			if field.Editable && !strings.Contains(rendered, `data-config-input="`+field.Key+`"`) {
				t.Fatalf("editable configuration field %q has no rendered control", field.Key)
			}
			if field.Editable && !field.Sensitive && (field.Type == "text" || field.Type == "int") {
				expected := `name="config_` + field.Key + `" value="` + html.EscapeString(field.Value) + `"`
				if !strings.Contains(rendered, expected) {
					t.Fatalf("configuration field %q did not echo value %q", field.Key, field.Value)
				}
			}
		}
	}
	for _, field := range unknown {
		if !strings.Contains(rendered, html.EscapeString(field.Key)) {
			t.Fatalf("custom configuration key %q is missing from rendered output", field.Key)
		}
	}
}

func TestSystemSettingsPagesExposeSharedContentTabs(t *testing.T) {
	for _, path := range []string{
		"../views/global/index.html",
		"../views/global/registration.html",
		"../views/global/config.html",
		"../views/global/certificate.html",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		template := string(content)
		for _, marker := range []string{
			`class="system-settings-tabs"`,
			`class="system-settings-tab is-active"`,
			`data-console-navigate`,
			`/global/index`,
			`/global/registration`,
			`/global/config`,
			`/global/certificate`,
		} {
			if !strings.Contains(template, marker) {
				t.Fatalf("%s misses shared settings tab marker %q", path, marker)
			}
		}
	}
}

func TestRegistrationSettingsTemplateRendersBenefitControls(t *testing.T) {
	content := strings.Join([]string{
		"allow_user_register=true",
		"register_captcha=true",
		"register_email_verify=true",
		"register_max_clients=1",
		"register_max_tunnels=2",
		"register_max_conn=3",
		"register_upload_limit=128",
		"register_download_limit=256",
		"register_expire_days=0",
		"register_rate_window=60",
		"register_rate_limit=5",
		"register_ip_rate_limit=3",
		"register_email_code_ttl=600",
		"register_email_code_cooldown=60",
		"smtp_host=smtp.example.com",
		"smtp_port=587",
		"smtp_security=starttls",
		"smtp_username=nps@example.com",
		"smtp_password=secret-password",
		"smtp_from=nps@example.com",
	}, "\n")
	sections, summary, err := registrationConfigSections(content)
	if err != nil {
		t.Fatalf("registrationConfigSections() error = %v", err)
	}
	tpl, err := template.ParseFiles("../views/global/registration.html")
	if err != nil {
		t.Fatalf("registration template does not parse: %v", err)
	}
	var output bytes.Buffer
	err = tpl.Execute(&output, map[string]interface{}{
		"web_base_url":                         "",
		"config_path":                          "/tmp/nps/conf/nps.conf",
		"registration_config_sections":         sections,
		"registration_config_summary":          summary,
		"registration_config_error":            "",
		"registration_config_validation_error": "",
	})
	if err != nil {
		t.Fatalf("execute registration template: %v", err)
	}
	page := output.String()
	for _, marker := range []string{
		"/global/index", "/global/registration", "/global/config", "/global/certificate",
		`name="config_allow_user_register"`,
		`name="config_register_max_clients" value="1"`,
		`name="config_register_max_tunnels" value="2"`,
		`name="config_register_max_conn" value="3"`,
		`name="config_register_upload_limit" value="128"`,
		`name="config_register_download_limit" value="256"`,
		`name="config_register_expire_days" value="0"`,
		`name="config_smtp_password"`,
		`data-registration-field`,
		"/global/registration/save",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("registration settings page is missing %q", marker)
		}
	}
	if strings.Contains(page, "secret-password") {
		t.Fatal("SMTP password must not be rendered into the registration page")
	}
}

func TestRegistrationScopedUpdatesPreserveUnrelatedSettings(t *testing.T) {
	content := strings.Join([]string{
		"web_port=8081",
		"allow_user_register=false",
		"register_captcha=true",
		"register_email_verify=false",
		"register_max_clients=0",
		"register_max_tunnels=0",
		"register_max_conn=0",
		"register_upload_limit=0",
		"register_download_limit=0",
		"register_expire_days=0",
		"register_rate_window=60",
		"register_rate_limit=5",
		"register_ip_rate_limit=3",
		"register_email_code_ttl=600",
		"register_email_code_cooldown=60",
		"smtp_password=existing-secret",
		"custom_deployment_flag=keep-me",
	}, "\n")
	form := url.Values{
		"config_allow_user_register":          {"true"},
		"config_register_captcha":             {"true"},
		"config_register_email_verify":        {"false"},
		"config_register_max_clients":         {"0"},
		"config_register_max_tunnels":         {"0"},
		"config_register_max_conn":            {"12"},
		"config_register_upload_limit":        {"128"},
		"config_register_download_limit":      {"256"},
		"config_register_expire_days":         {"30"},
		"config_register_rate_window":         {"60"},
		"config_register_rate_limit":          {"5"},
		"config_register_ip_rate_limit":       {"3"},
		"config_register_email_code_ttl":      {"600"},
		"config_register_email_code_cooldown": {"60"},
		"config_enabled_smtp_password":        {"1"},
		"config_smtp_password":                {""},
		"config_web_port":                     {"9999"},
	}
	request := httptest.NewRequest(http.MethodPost, "/global/registration/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updates, err := parseServerConfigUpdatesForKeys(request, content, registrationConfigKeys())
	if err != nil {
		t.Fatal(err)
	}
	effective := effectiveServerConfigValues(serverConfigKeyValues(content), updates)
	if effective["allow_user_register"] != "true" ||
		effective["register_max_conn"] != "12" ||
		effective["register_upload_limit"] != "128" ||
		effective["register_download_limit"] != "256" ||
		effective["register_expire_days"] != "30" {
		t.Fatalf("registration updates were not applied: %#v", effective)
	}
	if effective["smtp_password"] != "existing-secret" {
		t.Fatal("leaving an enabled masked SMTP password blank should preserve the current value")
	}
	if effective["web_port"] != "8081" || effective["custom_deployment_flag"] != "keep-me" {
		t.Fatalf("registration-scoped save modified unrelated settings: %#v", effective)
	}
}

func TestServerConfigVisualSectionsExcludeCertificateSettings(t *testing.T) {
	sections, _, err := serverConfigSections(npsconf.DefaultNpsConf)
	if err != nil {
		t.Fatal(err)
	}
	filtered := filterServerConfigSections(sections, certificateConfigKeySet)
	fields := make(map[string]serverConfigFieldView)
	for _, section := range filtered {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}
	for _, key := range []string{
		"https_default_cert_file", "https_default_key_file", "force_auto_ssl",
		"ssl_path", "ssl_email", "ssl_ca", "ssl_zerossl_api",
		"ssl_cache_max", "ssl_cache_reload", "ssl_cache_idle",
		"bridge_cert_file", "bridge_key_file",
		"web_open_ssl", "web_cert_file", "web_key_file",
		"ip_register_cert_file", "ip_register_key_file",
		"tls_enable", "tls_bridge_port",
	} {
		if _, exists := fields[key]; exists {
			t.Fatalf("server configuration visual editor still contains certificate setting %q", key)
		}
	}
	for _, key := range []string{"http_proxy_port", "bridge_port", "web_password", "allow_ports"} {
		if _, exists := fields[key]; !exists {
			t.Fatalf("server configuration visual editor unexpectedly lost %q", key)
		}
	}
	filtered = filterServerConfigSections(sections, generalServerConfigExcludedKeys())
	fields = make(map[string]serverConfigFieldView)
	for _, section := range filtered {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}
	for _, key := range registrationConfigKeys() {
		if _, exists := fields[key]; exists {
			t.Fatalf("server configuration visual editor still contains registration setting %q", key)
		}
	}
	for _, key := range []string{"http_proxy_port", "bridge_port", "web_password", "allow_ports"} {
		if _, exists := fields[key]; !exists {
			t.Fatalf("general server configuration unexpectedly lost %q after registration filtering", key)
		}
	}
}

func TestCertificateScopedUpdatesDoNotRemoveUnrelatedOptionalSettings(t *testing.T) {
	content := strings.Join([]string{
		"http_proxy_port=30111",
		"force_auto_ssl=false",
		"web_open_ssl=false",
		"web_cert_file=conf/server.pem",
		"web_key_file=conf/server.key",
		"custom_deployment_flag=keep-me",
	}, "\n")
	form := url.Values{
		"config_force_auto_ssl":         {"true"},
		"config_enabled_force_auto_ssl": {"1"},
		"config_web_open_ssl":           {"false"},
		"config_tls_enable":             {"false"},
		"config_http_proxy_port":        {"9999"},
	}
	request := httptest.NewRequest(http.MethodPost, "/global/certificate/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	keys := make([]string, 0)
	for _, group := range certificateConfigGroups {
		keys = append(keys, group.Keys...)
	}
	updates, err := parseServerConfigUpdatesForKeys(request, content, keys)
	if err != nil {
		t.Fatal(err)
	}
	effective := effectiveServerConfigValues(serverConfigKeyValues(content), updates)
	if effective["force_auto_ssl"] != "true" {
		t.Fatalf("certificate update did not apply force_auto_ssl: %#v", effective)
	}
	if effective["http_proxy_port"] != "30111" {
		t.Fatalf("certificate-scoped save modified unrelated http_proxy_port: %#v", effective)
	}
	if effective["custom_deployment_flag"] != "keep-me" {
		t.Fatalf("certificate-scoped save modified custom setting: %#v", effective)
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
	for _, key := range []string{
		"bridge_tcp_show", "bridge_tls_show", "bridge_kcp_show",
		"bridge_ws_show", "bridge_wss_show", "bridge_quic_show",
	} {
		if fields[key].Type != "bool" || fields[key].Optional {
			t.Fatalf("%s should be a persistent boolean display switch: %+v", key, fields[key])
		}
	}
	for _, key := range []string{
		"bridge_tcp_show_ip", "bridge_tls_show_port", "bridge_ws_show_ip",
		"bridge_wss_show_port", "bridge_quic_show_alpn", "bridge_show_path",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("Bridge command display setting %q is missing", key)
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
	if serverConfigSensitive("https_default_key_file") {
		t.Fatal("private key file path should be visible for configuration auditing")
	}
	if fields["https_default_key_file"].Sensitive || fields["https_default_key_file"].Type == "secret" {
		t.Fatal("private key file path must not use a password input")
	}
	for _, key := range []string{"auth_crypt_key", "public_register_key", "ssl_zerossl_api", "x_nps_http_only"} {
		if fields[key].Type != "secret" {
			t.Fatalf("%s must render as a secret input, got type %q", key, fields[key].Type)
		}
	}
	if first := sections[0].Fields[0].Key; first != "http_proxy_ip" {
		t.Fatalf("HTTP settings should put http_proxy_ip first, got %q", first)
	}
}

func TestServerConfigMissingRequiredFieldRemainsEditable(t *testing.T) {
	sections, _, err := serverConfigSections("web_port=8081\n")
	if err != nil {
		t.Fatalf("serverConfigSections() error = %v", err)
	}
	for _, section := range sections {
		for _, field := range section.Fields {
			if field.Key != "web_ip" {
				continue
			}
			if field.Present {
				t.Fatal("web_ip unexpectedly reported as present")
			}
			if !field.Editable || !field.Enabled {
				t.Fatalf("missing required field must stay editable and enabled: %+v", field)
			}
			return
		}
	}
	t.Fatal("web_ip field was not found")
}

func TestValidateServerConfigSourceAcceptsDefaultTemplate(t *testing.T) {
	if err := validateServerConfigSource(npsconf.DefaultNpsConf); err != nil {
		t.Fatalf("default nps.conf source rejected: %v", err)
	}
}

func TestValidateServerConfigSourceRejectsMalformedAndInvalidValues(t *testing.T) {
	for _, content := range []string{
		"web_port=8081\ninvalid line\n",
		"web_port=not-a-port\n",
		"web_port=8081\nweb_port=8082\n",
		"# no settings\n",
	} {
		if err := validateServerConfigSource(content); err == nil {
			t.Fatalf("invalid source was accepted: %q", content)
		}
	}
}

func TestSaveServerConfigSourceUsesRevisionAndPreservesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.conf")
	original := "# keep this comment\nweb_port=8081\ncustom_flag=yes\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	revision := serverConfigSourceRevision([]byte(original))
	updated := "# keep this comment\nweb_port=18081\ncustom_flag=yes\n"
	changed, nextRevision, err := saveServerConfigSource(path, updated, revision)
	if err != nil {
		t.Fatalf("saveServerConfigSource() error = %v", err)
	}
	if !changed || nextRevision == revision {
		t.Fatalf("source save did not report a changed revision: changed=%v revision=%q", changed, nextRevision)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != updated {
		t.Fatalf("source content was not preserved exactly:\n%s", data)
	}
	if _, _, err := saveServerConfigSource(path, updated+"\n", revision); !errors.Is(err, errServerConfigSourceConflict) {
		t.Fatalf("stale revision error = %v, want conflict", err)
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

	form.Set("config_bridge_tcp_show", "false")
	request = httptest.NewRequest("POST", "/global/config/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updates, err = parseServerConfigUpdates(request, npsconf.DefaultNpsConf)
	if err != nil {
		t.Fatalf("parseServerConfigUpdates(display=false) error = %v", err)
	}
	foundDisabledDisplay := false
	for _, update := range updates {
		if update.Key != "bridge_tcp_show" {
			continue
		}
		if update.Omit || update.Value != "false" {
			t.Fatalf("bridge_tcp_show=false was not persisted: %+v", update)
		}
		foundDisabledDisplay = true
	}
	if !foundDisabledDisplay {
		t.Fatal("bridge_tcp_show=false update is missing")
	}
}

func TestParseGeneralServerConfigUpdatesIgnoresDedicatedPageSettingsAndRelationships(t *testing.T) {
	defaults := serverConfigKeyValues(npsconf.DefaultNpsConf)
	form := url.Values{}
	excluded := generalServerConfigExcludedKeys()
	for _, key := range generalServerConfigKeys() {
		meta := serverConfigMetaFor(key, defaults[key])
		if meta.Optional {
			continue
		}
		form.Set("config_"+key, defaults[key])
	}

	// These are the values that produced the false "cannot be empty" errors
	// when the general editor submitted only its own fields.
	form.Del("config_web_open_ssl")
	form.Del("config_register_rate_window")

	// Invalid relationships owned by the certificate and registration pages
	// must not block an otherwise valid general configuration save.
	current := strings.Join([]string{
		"register_email_verify=true",
		"bridge_cert_file=conf/ssl/bridge.pem",
	}, "\n")
	request := httptest.NewRequest(http.MethodPost, "/global/config/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updates, err := parseServerConfigUpdates(request, current)
	if err != nil {
		t.Fatalf("parseServerConfigUpdates() error = %v", err)
	}
	for _, update := range updates {
		if _, skip := excluded[update.Key]; skip {
			t.Fatalf("general config update unexpectedly touched dedicated-page setting %q", update.Key)
		}
	}
}

func TestScopedServerConfigRelationshipsDoNotBlockOtherPages(t *testing.T) {
	current := map[string]string{
		"register_email_verify": "true",
		"bridge_cert_file":      "conf/ssl/bridge.pem",
		"web_open_ssl":          "true",
	}
	if err := validateServerConfigUpdateSetForKeys(current, []serverConfigUpdate{
		{Key: "http_proxy_port", Value: "80"},
	}, generalServerConfigKeys()); err != nil {
		t.Fatalf("general configuration was blocked by unrelated relationships: %v", err)
	}
	if err := validateServerConfigUpdateSetForKeys(current, []serverConfigUpdate{
		{Key: "allow_user_register", Value: "true"},
	}, registrationConfigKeys()); err == nil {
		t.Fatal("registration email relationship should still be validated on the registration page")
	}
	if err := validateServerConfigUpdateSetForKeys(current, []serverConfigUpdate{
		{Key: "web_open_ssl", Value: "false"},
	}, []string{"web_open_ssl", "web_cert_file", "web_key_file"}); err != nil {
		t.Fatalf("certificate configuration was blocked by unrelated registration relationship: %v", err)
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
		"bridge_cert_file": "bridge.pem",
	}, nil); err == nil {
		t.Fatal("expected Bridge TLS certificate pair validation error")
	}
	if err := validateServerConfigUpdateSet(map[string]string{
		"bridge_cert_file": "bridge.pem", "bridge_key_file": "bridge.key",
	}, nil); err != nil {
		t.Fatalf("valid Bridge TLS certificate pair rejected: %v", err)
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
