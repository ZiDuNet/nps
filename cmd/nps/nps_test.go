package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRotateInsecureConfig(t *testing.T) {
	input := "web_password=123\nauth_key=CHANGE_ME\nauth_crypt_key =213\npublic_vkey=123\n"
	output, secrets := rotateInsecureConfig(input)

	for _, value := range []string{"web_password=123", "auth_key=CHANGE_ME", "auth_crypt_key =213", "public_vkey=123"} {
		if strings.Contains(output, value) {
			t.Fatalf("insecure config value %q remains in output %q", value, output)
		}
	}
	if len(secrets["web_password"]) != 8 || len(secrets["auth_key"]) != 8 || len(secrets["auth_crypt_key"]) != 16 {
		t.Fatalf("generated secret lengths are incorrect: %#v", secrets)
	}
	if !strings.Contains(output, "public_vkey=\n") {
		t.Fatalf("legacy public_vkey should be disabled: %q", output)
	}
}

func TestRotateInsecureConfigPreservesExplicitEmptyValues(t *testing.T) {
	input := "web_password=\nauth_key=\nauth_crypt_key=\npublic_vkey=custom\n"
	output, secrets := rotateInsecureConfig(input)
	if output != input {
		t.Fatalf("explicit empty values should remain unchanged: got %q", output)
	}
	if len(secrets) != 0 {
		t.Fatalf("unexpected rotations for explicit values: %#v", secrets)
	}
}

func TestDefaultNpsConfMatchesCheckedInTemplate(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate nps test source")
	}
	templatePath := filepath.Join(filepath.Dir(sourcePath), "..", "..", "conf", "nps.conf")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read checked-in nps.conf: %v", err)
	}
	if string(content) != defaultNpsConf {
		t.Fatalf("embedded default config differs from %s", templatePath)
	}
}

func TestMergeMissingConfigFieldsPreservesValuesCommentsAndEmptyValues(t *testing.T) {
	config := "# old config\r\nweb_port=19001\r\ncustom_setting=keep\r\ntotp_secret=\r\n#pow_bits=5\r\n"
	template := "# Web\nweb_port=8081\n# TOTP\ntotp_secret=default\n# New option description\nnew_option=true\n# PoW\npow_bits=20\n"

	merged, added := mergeMissingConfigFields(config, template)
	if strings.Contains(merged, "web_port=8081") || !strings.Contains(merged, "web_port=19001\r\n") {
		t.Fatalf("existing non-default value was not preserved: %q", merged)
	}
	if strings.Count(merged, "totp_secret=") != 1 || !strings.Contains(merged, "totp_secret=\r\n") {
		t.Fatalf("explicit empty value was not preserved: %q", merged)
	}
	if !strings.Contains(merged, "#pow_bits=5\r\n") || !strings.Contains(merged, "pow_bits=20") {
		t.Fatalf("commented key should not satisfy the active setting: %q", merged)
	}
	if !strings.Contains(merged, "new_option=true") || !strings.Contains(merged, "# New option description") {
		t.Fatalf("missing setting and its documentation were not appended: %q", merged)
	}
	if !strings.Contains(merged, "custom_setting=keep\r\n") {
		t.Fatalf("unknown existing setting was not preserved: %q", merged)
	}
	if len(added) != 2 || added[0] != "new_option" || added[1] != "pow_bits" {
		t.Fatalf("added keys = %#v, want new_option and pow_bits", added)
	}

	again, addedAgain := mergeMissingConfigFields(merged, template)
	if again != merged || len(addedAgain) != 0 {
		t.Fatalf("migration is not idempotent: added=%#v", addedAgain)
	}
}

func TestMergeMissingConfigFieldsDoesNotAppendCommentedTemplateOptions(t *testing.T) {
	merged, added := mergeMissingConfigFields("existing=true\n", "# optional\n#optional_setting=false\n")
	if merged != "existing=true\n" || len(added) != 0 {
		t.Fatalf("comment-only options should not be migrated: %q, %#v", merged, added)
	}
}

func TestMergeMissingConfigFieldsDoesNotReaddDisabledOptionalSettings(t *testing.T) {
	template := "# required\nweb_port=8081\n# optional listener\nhttp_proxy_port=80\n# optional debug\npprof_port=9999\n"
	merged, added := mergeMissingConfigFields("web_port=19001\n", template)
	if strings.Contains(merged, "http_proxy_port=") || strings.Contains(merged, "pprof_port=") {
		t.Fatalf("disabled optional settings were re-added: %q", merged)
	}
	if !strings.Contains(merged, "web_port=19001\n") || len(added) != 0 {
		t.Fatalf("required existing setting should remain untouched: %q, %#v", merged, added)
	}
}

func TestMergeMissingConfigFieldsAddsRegistrationRuntimeLimits(t *testing.T) {
	template := strings.Join([]string{
		"allow_user_register=false",
		"register_max_clients=0",
		"register_max_tunnels=0",
		"register_max_conn=0",
		"register_upload_limit=0",
		"register_download_limit=0",
		"register_expire_days=0",
	}, "\n")
	existing := "allow_user_register=true\nregister_max_clients=2\nregister_max_tunnels=4\nregister_expire_days=7\n"
	merged, added := mergeMissingConfigFields(existing, template)
	for _, key := range []string{"register_max_conn", "register_upload_limit", "register_download_limit"} {
		if !strings.Contains(merged, key+"=0") {
			t.Fatalf("missing registration runtime limit %q was not migrated: %q", key, merged)
		}
		if !containsTestString(added, key) {
			t.Fatalf("migration did not report added key %q: %#v", key, added)
		}
	}
	for _, preserved := range []string{
		"allow_user_register=true", "register_max_clients=2",
		"register_max_tunnels=4", "register_expire_days=7",
	} {
		if !strings.Contains(merged, preserved) {
			t.Fatalf("existing registration setting %q was not preserved: %q", preserved, merged)
		}
	}
}

func containsTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestMigrateConfigCompatibilityMapsUpstreamAliases(t *testing.T) {
	config := "bridge_tcp_port=18024\r\nbridge_tls_port=18025\r\nbridge_tcp_ip=127.0.0.1\r\n"
	merged, added := migrateConfigCompatibility(config)

	for _, expected := range []string{
		"bridge_port=18024\r\n",
		"tls_bridge_port=18025\r\n",
		"bridge_ip=127.0.0.1\r\n",
	} {
		if !strings.Contains(merged, expected) {
			t.Fatalf("canonical compatibility setting %q missing from %q", expected, merged)
		}
	}
	for _, source := range []string{"bridge_tcp_port=18024", "bridge_tls_port=18025", "bridge_tcp_ip=127.0.0.1"} {
		if !strings.Contains(merged, source) {
			t.Fatalf("source setting %q was not preserved: %q", source, merged)
		}
	}
	if len(added) != 3 || added[0] != "bridge_port" || added[1] != "tls_bridge_port" || added[2] != "bridge_ip" {
		t.Fatalf("compatibility settings added = %#v", added)
	}

	again, addedAgain := migrateConfigCompatibility(merged)
	if again != merged || len(addedAgain) != 0 {
		t.Fatalf("compatibility migration is not idempotent: added=%#v", addedAgain)
	}
}

func TestMigrateConfigCompatibilityKeepsCanonicalValues(t *testing.T) {
	config := "bridge_port=19024\nbridge_tcp_port=18024\n"
	merged, added := migrateConfigCompatibility(config)
	if merged != config || len(added) != 0 {
		t.Fatalf("canonical value should win over alias: %q, %#v", merged, added)
	}
}

func TestMigrateConfigCompatibilityPrefersBridgePortForSelectedTransport(t *testing.T) {
	tests := []struct {
		name       string
		bridgeType string
		input      string
		want       string
	}{
		{
			name:       "tcp selected when both upstream ports exist",
			bridgeType: "tcp",
			input:      "bridge_type=tcp\nbridge_tcp_port=18024\nbridge_kcp_port=18025\n",
			want:       "bridge_port=18024",
		},
		{
			name:       "kcp selected when both upstream ports exist",
			bridgeType: "kcp",
			input:      "bridge_type=kcp\nbridge_tcp_port=18024\nbridge_kcp_port=18025\n",
			want:       "bridge_port=18025",
		},
		{
			name:       "selected alias absent falls back to available port",
			bridgeType: "kcp",
			input:      "bridge_type=kcp\nbridge_tcp_port=18024\n",
			want:       "bridge_port=18024",
		},
		{
			name:       "explicit tcp does not consume the parallel kcp port",
			bridgeType: "tcp",
			input:      "bridge_type=tcp\nbridge_kcp_port=18025\n",
			want:       "bridge_kcp_port=18025",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			merged, added := migrateConfigCompatibility(test.input)
			if !strings.Contains(merged, test.want) {
				t.Fatalf("migrated config missing %q: %q", test.want, merged)
			}
			if test.name == "explicit tcp does not consume the parallel kcp port" {
				if len(added) != 0 || strings.Contains(merged, "bridge_port=") {
					t.Fatalf("parallel KCP port was incorrectly migrated as the TCP port: %q, %#v", merged, added)
				}
			} else if len(added) == 0 || added[0] != "bridge_port" {
				t.Fatalf("added aliases = %#v, want bridge_port", added)
			}
		})
	}
}

func TestMigrateConfigCompatibilityPreservesTransportSpecificBridgeIPs(t *testing.T) {
	input := "bridge_type=tcp\nbridge_kcp_ip=127.0.0.2\n"
	merged, added := migrateConfigCompatibility(input)
	if merged != input || len(added) != 0 {
		t.Fatalf("explicit TCP config should not consume the parallel KCP IP: %q, %#v", merged, added)
	}

	input = "bridge_type=kcp\nbridge_kcp_ip=127.0.0.3\n"
	merged, added = migrateConfigCompatibility(input)
	if !strings.Contains(merged, "bridge_ip=127.0.0.3") || len(added) != 1 || added[0] != "bridge_ip" {
		t.Fatalf("selected KCP listener IP was not migrated: %q, %#v", merged, added)
	}
}

func TestParseConfiguredLogLevelSupportsNumericAndUpstreamNames(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{value: "0", want: 0},
		{value: "7", want: 7},
		{value: "trace", want: 7},
		{value: "DEBUG", want: 7},
		{value: "info", want: 6},
		{value: "warning", want: 4},
		{value: "error", want: 3},
		{value: "fatal", want: 2},
		{value: "panic", want: 1},
		{value: "off", want: -1},
	}
	for _, test := range tests {
		got, ok := parseConfiguredLogLevel(test.value)
		if !ok || got != test.want {
			t.Errorf("parseConfiguredLogLevel(%q) = (%d, %v), want (%d, true)", test.value, got, ok, test.want)
		}
	}
	for _, value := range []string{"", "verbose", "8", "-2"} {
		if got, ok := parseConfiguredLogLevel(value); ok {
			t.Errorf("parseConfiguredLogLevel(%q) = (%d, true), want invalid", value, got)
		}
	}
}

func TestWriteConfigAtomicallyPreservesModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.conf")
	link := filepath.Join(dir, "nps.conf")
	if err := os.WriteFile(target, []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigAtomically(link, []byte("new")); err != nil {
		t.Fatalf("atomic config write: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("target contents = %q, want new", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Fatalf("target mode = %o, want 640", got)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced instead of its target")
	}
}

func TestMigrateConfigFileIsIdempotentAndPreservesExistingValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nps.conf")
	old := "# deployment values\r\nweb_port=19001\r\nweb_password=custom-secret\r\ncustom_option=keep\r\n"
	if err := os.WriteFile(path, []byte(old), 0640); err != nil {
		t.Fatal(err)
	}
	template := "# panel\nweb_port=8081\n# generated\ntotp_secret=\n# new\ndisconnect_timeout=60\n"
	changed, added, secrets, err := migrateConfigFile(path, template)
	if err != nil {
		t.Fatalf("migrate config: %v", err)
	}
	if !changed || len(secrets) != 0 || len(added) != 1 || added[0] != "disconnect_timeout" {
		t.Fatalf("migration result changed=%v added=%#v secrets=%#v", changed, added, secrets)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "web_port=19001\r\n") || !strings.Contains(got, "web_password=custom-secret\r\n") || !strings.Contains(got, "custom_option=keep\r\n") {
		t.Fatalf("existing values were not preserved: %q", got)
	}
	if strings.Contains(got, "totp_secret=") || !strings.Contains(got, "disconnect_timeout=60") {
		t.Fatalf("missing options were not appended exactly once: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	changed, added, secrets, err = migrateConfigFile(path, template)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if changed || len(added) != 0 || len(secrets) != 0 {
		t.Fatalf("migration is not idempotent: changed=%v added=%#v secrets=%#v", changed, added, secrets)
	}
}

func TestMigrateConfigFileSupportsLegacyBridgePortNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nps.conf")
	old := "bridge_tcp_port=18024\nbridge_tls_port=18025\nbridge_tcp_ip=127.0.0.1\nweb_password=custom-secret\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	template := "# bridge\nbridge_port=8024\nbridge_ip=0.0.0.0\ntls_bridge_port=8025\n# security\ntls_enable=true\n"

	changed, added, secrets, err := migrateConfigFile(path, template)
	if err != nil {
		t.Fatalf("migrate upstream config: %v", err)
	}
	if !changed || len(secrets) != 0 {
		t.Fatalf("migration result changed=%v secrets=%#v", changed, secrets)
	}
	for _, key := range []string{"bridge_port=18024", "tls_bridge_port=18025", "bridge_ip=127.0.0.1", "tls_enable=true"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(data), key) {
			t.Fatalf("migrated config missing %q: %q", key, data)
		}
	}
	if len(added) != 4 {
		t.Fatalf("added settings = %#v, want three aliases and tls_enable", added)
	}

	changed, added, secrets, err = migrateConfigFile(path, template)
	if err != nil {
		t.Fatalf("second upstream migration: %v", err)
	}
	if changed || len(added) != 0 || len(secrets) != 0 {
		t.Fatalf("upstream migration is not idempotent: changed=%v added=%#v secrets=%#v", changed, added, secrets)
	}
}

func TestMigrateConfigFileAddsPersistentBridgeDisplayBooleans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nps.conf")
	old := "bridge_port=8024\nweb_password=custom-secret\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	template := "bridge_port=8024\nbridge_tcp_show=true\nbridge_tcp_show_ip=\nbridge_ws_show=true\nbridge_quic_show_alpn=\n"

	changed, added, _, err := migrateConfigFile(path, template)
	if err != nil {
		t.Fatalf("migrate config: %v", err)
	}
	if !changed || strings.Join(added, ",") != "bridge_tcp_show,bridge_ws_show" {
		t.Fatalf("persistent display booleans were not added: changed=%v added=%#v", changed, added)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bridge_tcp_show=true", "bridge_ws_show=true"} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("migrated config missing %q: %q", key, data)
		}
	}
	for _, key := range []string{"bridge_tcp_show_ip", "bridge_quic_show_alpn"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("optional display override %q should remain omitted: %q", key, data)
		}
	}
}
