package controllers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestTunnelSidebarMenu(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{mode: "tcp", want: "tcp"},
		{mode: "udp", want: "udp"},
		{mode: "httpProxy", want: "http"},
		{mode: "socks5", want: "socks5"},
		{mode: "secret", want: "secret"},
		{mode: "p2p", want: "p2p"},
		{mode: "file", want: "file"},
		{mode: "", want: "tcp"},
		{mode: "unknown", want: "tcp"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := tunnelSidebarMenu(tt.mode); got != tt.want {
				t.Fatalf("tunnelSidebarMenu(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestTunnelModeTemplateEntrypointsIncludeSharedViews(t *testing.T) {
	tests := []struct {
		mode   string
		action string
		parent string
	}{
		{mode: "tcp", action: "add", parent: "index/add.html"},
		{mode: "tcp", action: "edit", parent: "index/edit.html"},
		{mode: "tcp", action: "list", parent: "index/list.html"},
		{mode: "udp", action: "add", parent: "index/add.html"},
		{mode: "udp", action: "edit", parent: "index/edit.html"},
		{mode: "udp", action: "list", parent: "index/list.html"},
		{mode: "httpProxy", action: "add", parent: "index/add.html"},
		{mode: "httpProxy", action: "edit", parent: "index/edit.html"},
		{mode: "httpProxy", action: "list", parent: "index/list.html"},
		{mode: "socks5", action: "add", parent: "index/add.html"},
		{mode: "socks5", action: "edit", parent: "index/edit.html"},
		{mode: "socks5", action: "list", parent: "index/list.html"},
		{mode: "secret", action: "add", parent: "index/add.html"},
		{mode: "secret", action: "edit", parent: "index/edit.html"},
		{mode: "secret", action: "list", parent: "index/list.html"},
		{mode: "p2p", action: "add", parent: "index/add.html"},
		{mode: "p2p", action: "edit", parent: "index/edit.html"},
		{mode: "p2p", action: "list", parent: "index/list.html"},
		{mode: "file", action: "add", parent: "index/add.html"},
		{mode: "file", action: "edit", parent: "index/edit.html"},
		{mode: "file", action: "list", parent: "index/list.html"},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.action, func(t *testing.T) {
			path := "../views/index/tunnel/" + tt.mode + "/" + tt.action + ".html"
			content := readTemplateForTest(t, path)
			want := `{{template "` + tt.parent + `" .}}`
			if strings.TrimSpace(content) != want {
				t.Fatalf("%s must include shared view %s, got %q", path, tt.parent, strings.TrimSpace(content))
			}
		})
	}
}

func TestTunnelListIdentificationKeyScopedToCredentialModes(t *testing.T) {
	content := readTemplateForTest(t, "../views/index/list.html")
	if !strings.Contains(content, "row.Mode === 'secret' || row.Mode === 'p2p'") {
		t.Fatal("tunnel detail should gate identification key on secret/p2p modes")
	}
	if strings.Contains(content, `+ '<div><b langtag="word-identificationkey"></b><span>' + tunnelText(row.Password || '-') + '</span></div>'`) {
		t.Fatal("tunnel detail must not render identification key unconditionally")
	}
}

func TestTunnelModeFieldStylesAvoidFirstPaintFlash(t *testing.T) {
	content := readTemplateForTest(t, "../static/css/zui-console.css")
	for _, marker := range []string{
		`.tunnel-form.tunnel-type-tcp [data-tunnel-field="password"]`,
		`.tunnel-form.tunnel-type-httpProxy [data-tunnel-field="target"]`,
		`.tunnel-form.tunnel-type-secret [data-tunnel-field="port"]`,
		`.tunnel-form.tunnel-type-file [data-tunnel-field="target"]`,
		`.tunnel-form.tunnel-type-udp [data-tunnel-section="security"]`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("mode-aware CSS misses first-paint rule %s", marker)
		}
	}
}

func TestSidebarTemplateContainsAddEditMenuBindings(t *testing.T) {
	content := readTemplateForTest(t, "../views/public/layout.html")
	for _, menu := range []string{"index", "client", "user", "host", "tcp", "udp", "http", "socks5", "secret", "p2p", "file", "global", "help"} {
		marker := `{{if eq "` + menu + `" .menu}}active{{end}}`
		if !strings.Contains(content, marker) {
			t.Fatalf("sidebar template misses active binding for menu %q", menu)
		}
	}
}

func TestLayoutSupportsContentOnlyNavigation(t *testing.T) {
	content := readTemplateForTest(t, "../views/public/layout.html")
	for _, marker := range []string{
		`<main id="nps-content"`,
		`menu.addEventListener('click'`,
		`window.fetch(url.href`,
		`history.pushState({ npsConsole: true }`,
		`window.addEventListener('popstate'`,
		`X-Requested-With`,
		`content.classList.toggle('is-loading'`,
		`$(window).triggerHandler('pagehide')`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("layout misses content-only navigation marker: %s", marker)
		}
	}
}

func TestTunnelFormTemplatesExposeTypeSpecificFields(t *testing.T) {
	addExpected := map[string]string{
		"tcp":       `tcp: ["client_id", "server_ip", "port", "target", "local_proxy", "proto_version"]`,
		"udp":       `udp: ["client_id", "server_ip", "port", "target", "local_proxy"]`,
		"socks5":    `socks5: ["client_id", "server_ip", "port"]`,
		"httpProxy": `httpProxy: ["client_id", "server_ip", "port"]`,
		"secret":    `secret: ["client_id", "server_ip", "target", "password"]`,
		"p2p":       `p2p: ["client_id", "server_ip", "target", "password"]`,
		"file":      `file: ["client_id", "server_ip", "port", "local_path", "strip_pre"]`,
	}
	editExpected := map[string]string{
		"tcp":       `tcp: ["client_id", "server_ip", "port", "target", "local_proxy", "proto_version"]`,
		"udp":       `udp: ["client_id", "server_ip", "port", "target", "local_proxy"]`,
		"socks5":    `socks5: ["client_id", "server_ip", "port"]`,
		"httpProxy": `httpProxy: ["client_id", "server_ip", "port"]`,
		"secret":    `secret: ["client_id", "server_ip", "target", "password"]`,
		"p2p":       `p2p: ["client_id", "server_ip", "target", "password"]`,
		"file":      `file: ["client_id", "server_ip", "port", "local_path", "strip_pre"]`,
	}

	tests := []struct {
		name      string
		path      string
		expected  map[string]string
		forbidden []string
	}{
		{name: "add", path: "../views/index/add.html", expected: addExpected},
		{
			name:      "edit",
			path:      "../views/index/edit.html",
			expected:  editExpected,
			forbidden: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := readTemplateForTest(t, tt.path)
			if !strings.Contains(content, `<option value="file" langtag="scheme-file"></option>`) {
				t.Fatalf("%s template does not expose file tunnel option", tt.name)
			}
			for tunnelType, fields := range tt.expected {
				if !strings.Contains(content, fields) {
					t.Fatalf("%s template misses %s field map: %s", tt.name, tunnelType, fields)
				}
			}
			for _, fields := range tt.forbidden {
				if strings.Contains(content, fields) {
					t.Fatalf("%s template contains non-upstream field map: %s", tt.name, fields)
				}
			}
		})
	}
}

func TestTunnelListCopyControlsAreLocalized(t *testing.T) {
	content := readTemplateForTest(t, "../views/index/list.html")
	for _, marker := range []string{
		`data-aria-label-zh="复制命令" data-aria-label-en="Copy command"`,
		`data-title-zh="复制命令" data-title-en="Copy command"`,
		`npsNotify('success', npsIsEnglish() ? 'Copied' : '复制成功')`,
		`npsNotify('error', npsIsEnglish() ? 'Copy failed' : '复制失败')`,
		`var tunnelListLanguageKey = tunnelListType === 'host' ? 'page-hostlist' : 'page-list' + tunnelListType;`,
		`$('body').setLang('#table');`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("tunnel list misses localized copy marker: %s", marker)
		}
	}
}

func TestHostListTemplateUsesSafeDynamicOutput(t *testing.T) {
	content := readTemplateForTest(t, "../views/index/hlist.html")
	for _, marker := range []string{
		`rel="noopener noreferrer"`,
		`npsEscapeHtml((row.Target || {}).TargetStr || '-')`,
		`type="button"`,
		`$('body').setLang('#table');`,
		`PlatformManaged`,
		`cardView: window.matchMedia && window.matchMedia('(max-width: 768px)').matches`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("host list misses safety/accessibility marker: %s", marker)
		}
	}
	for _, forbidden := range []string{"CertFilePath", "KeyFilePath", "WebUserName", "WebPassword"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("host list must not render sensitive host fields: %s", forbidden)
		}
	}
}

func TestHostFormTemplatesRenderPlatformDomainFields(t *testing.T) {
	platformDomains := []platformDomainOption{{ID: "managed-example", Wildcard: "*.example.com"}}
	host := map[string]interface{}{
		"Id":               8,
		"Host":             "portal.example.com",
		"PlatformDomainID": "managed-example",
		"Remark":           "portal",
		"Location":         "/",
		"Scheme":           "all",
		"AutoHttps":        true,
		"HeaderChange":     "",
		"HostChange":       "",
		"CertFilePath":     "/server/private.pem",
		"KeyFilePath":      "/server/private.key",
		"Client":           map[string]interface{}{"Id": 3},
		"Target":           map[string]interface{}{"TargetStr": "127.0.0.1:8080", "LocalProxy": false},
	}
	tests := []struct {
		name string
		path string
		data map[string]interface{}
	}{
		{
			name: "create",
			path: "../views/index/hadd.html",
			data: map[string]interface{}{
				"platformDomains":       platformDomains,
				"platformDefaultPrefix": "abcd1234",
				"allow_local_proxy":     true,
				"web_base_url":          "",
				"client_id":             3,
			},
		},
		{
			name: "edit platform host",
			path: "../views/index/hedit.html",
			data: map[string]interface{}{
				"platformDomains":   platformDomains,
				"hostIsPlatform":    true,
				"platformPrefix":    "portal",
				"allow_local_proxy": true,
				"web_base_url":      "",
				"h":                 host,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := template.Must(template.ParseFiles(tt.path))
			var rendered bytes.Buffer
			if err := tpl.Execute(&rendered, tt.data); err != nil {
				t.Fatalf("execute host form template: %v", err)
			}
			content := rendered.String()
			for _, marker := range []string{
				"platform_domain_id",
				"platform_prefix",
				`<input id="platform-prefix"`,
				`<select id="platform-domain-id"`,
				"data-certificate-configured",
				"platformhostavailable",
				"custom-domain-server-host",
				"window.location.hostname",
			} {
				if !strings.Contains(content, marker) {
					t.Fatalf("rendered host form misses platform-domain control %q", marker)
				}
			}
			if tt.name == "edit platform host" && strings.Contains(content, "/server/private.pem") {
				t.Fatal("managed host edit form must not expose the administrator certificate path")
			}
		})
	}
}

func TestTunnelFormTemplatesRenderInitialTypeVisibility(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		typeClass      string
		expectedRules  []string
		forbiddenRules []string
	}{
		{
			name:          "add",
			path:          "../views/index/add.html",
			typeClass:     `class="form-page-grid tunnel-form tunnel-type-{{if .type}}{{.type}}{{else}}tcp{{end}}"`,
			expectedRules: nil,
		},
		{
			name:           "edit",
			path:           "../views/index/edit.html",
			typeClass:      `class="form-page-grid tunnel-form tunnel-type-{{.t.Mode}}"`,
			forbiddenRules: nil,
		},
	}

	requiredCSS := []string{
		`.tunnel-form .tunnel-field-hidden`,
		`.tunnel-form .tunnel-section-hidden`,
		`data-tunnel-field`,
		`data-tunnel-section`,
		`data-tunnel-case`,
		`tunnelSectionFields`,
		`setTunnelFieldVisibility(fieldID, visible)`,
		`.prop("disabled", !visible)`,
		`fields.indexOf(fieldID)`,
		`tunnelTypeClasses`,
		`applyTypeClass(type);`,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := readTemplateForTest(t, tt.path)
			if !strings.Contains(content, tt.typeClass) {
				t.Fatalf("%s template does not set initial tunnel type class", tt.name)
			}
			for _, rule := range requiredCSS {
				if !strings.Contains(content, rule) {
					t.Fatalf("%s template misses initial visibility rule: %s", tt.name, rule)
				}
			}
			for _, rule := range tt.expectedRules {
				if !strings.Contains(content, rule) {
					t.Fatalf("%s template misses initial visibility rule: %s", tt.name, rule)
				}
			}
			for _, rule := range tt.forbiddenRules {
				if strings.Contains(content, rule) {
					t.Fatalf("%s template contains non-upstream visibility rule: %s", tt.name, rule)
				}
			}
			if strings.Contains(content, `.css("display"`) {
				t.Fatalf("%s template directly mutates display instead of state classes", tt.name)
			}
		})
	}
}

func TestTunnelFormTemplatesExecuteWithInitialTypeClass(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		data      map[string]interface{}
		wantClass string
	}{
		{
			name: "add udp",
			path: "../views/index/add.html",
			data: map[string]interface{}{
				"type":              "udp",
				"allow_multi_ip":    true,
				"allow_local_proxy": true,
				"web_base_url":      "",
			},
			wantClass: `tunnel-type-udp`,
		},
		{
			name: "add default",
			path: "../views/index/add.html",
			data: map[string]interface{}{
				"type":              "",
				"allow_multi_ip":    true,
				"allow_local_proxy": true,
				"web_base_url":      "",
			},
			wantClass: `tunnel-type-tcp`,
		},
		{
			name: "edit file",
			path: "../views/index/edit.html",
			data: map[string]interface{}{
				"t": map[string]interface{}{
					"Id":           1,
					"Mode":         "file",
					"Client":       map[string]interface{}{"Id": 1},
					"ServerIp":     "0.0.0.0",
					"Port":         8080,
					"Target":       map[string]interface{}{"LocalProxy": false, "TargetStr": ""},
					"LocalPath":    "/tmp",
					"StripPre":     "",
					"Password":     "",
					"ProtoVersion": "",
					"Remark":       "",
				},
				"allow_multi_ip":    true,
				"allow_local_proxy": true,
				"web_base_url":      "",
			},
			wantClass: `tunnel-type-file`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := template.Must(template.ParseFiles(tt.path))
			var buf bytes.Buffer
			if err := tpl.Execute(&buf, tt.data); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			if !strings.Contains(buf.String(), tt.wantClass) {
				t.Fatalf("rendered template misses %s", tt.wantClass)
			}
		})
	}
}

func TestDedicatedTunnelFormsRenderOnlyModeFields(t *testing.T) {
	type modeCase struct {
		mode      string
		present   []string
		forbidden []string
	}
	cases := []modeCase{
		{mode: "tcp", present: []string{"tunnel-port", "tunnel-target", "ProtoVersion"}, forbidden: []string{"tunnel-local-path", "tunnel-password"}},
		{mode: "udp", present: []string{"tunnel-port", "tunnel-target"}, forbidden: []string{"tunnel-local-path", "tunnel-password", "ProtoVersion"}},
		{mode: "httpProxy", present: []string{"tunnel-port"}, forbidden: []string{"tunnel-target", "tunnel-password", "ProtoVersion"}},
		{mode: "socks5", present: []string{"tunnel-port"}, forbidden: []string{"tunnel-target", "tunnel-password", "ProtoVersion"}},
		{mode: "secret", present: []string{"tunnel-target", "tunnel-password"}, forbidden: []string{"tunnel-port", "tunnel-local-path", "ProtoVersion"}},
		{mode: "p2p", present: []string{"tunnel-target", "tunnel-password"}, forbidden: []string{"tunnel-port", "tunnel-local-path", "ProtoVersion"}},
		{mode: "file", present: []string{"tunnel-port", "tunnel-local-path", "tunnel-strip-pre"}, forbidden: []string{"tunnel-target", "tunnel-password", "ProtoVersion"}},
	}
	for _, tc := range cases {
		t.Run("add/"+tc.mode, func(t *testing.T) {
			tpl := template.Must(template.ParseFiles("../views/index/add.html"))
			var rendered bytes.Buffer
			data := map[string]interface{}{
				"type":              tc.mode,
				"dedicated":         true,
				"allow_multi_ip":    true,
				"allow_local_proxy": true,
				"web_base_url":      "",
			}
			if err := tpl.Execute(&rendered, data); err != nil {
				t.Fatalf("execute dedicated add template: %v", err)
			}
			assertTemplateFieldPresence(t, rendered.String(), tc.present, tc.forbidden)
		})

		t.Run("edit/"+tc.mode, func(t *testing.T) {
			tpl := template.Must(template.ParseFiles("../views/index/edit.html"))
			var rendered bytes.Buffer
			data := map[string]interface{}{
				"dedicated":         true,
				"allow_multi_ip":    true,
				"allow_local_proxy": true,
				"web_base_url":      "",
				"t": map[string]interface{}{
					"Mode":         tc.mode,
					"Id":           1,
					"Remark":       "test",
					"ServerIp":     "0.0.0.0",
					"Port":         8080,
					"Password":     "secret",
					"LocalPath":    "/tmp",
					"StripPre":     "",
					"ProtoVersion": "",
					"Client":       map[string]interface{}{"Id": 1, "Remark": "client"},
					"Target":       map[string]interface{}{"TargetStr": "127.0.0.1:8080", "LocalProxy": false},
				},
			}
			if err := tpl.Execute(&rendered, data); err != nil {
				t.Fatalf("execute dedicated edit template: %v", err)
			}
			assertTemplateFieldPresence(t, rendered.String(), tc.present, tc.forbidden)
		})
	}
}

func assertTemplateFieldPresence(t *testing.T, content string, present, forbidden []string) {
	t.Helper()
	for _, field := range present {
		if !strings.Contains(content, `id="`+field+`"`) {
			t.Errorf("rendered template misses expected field %q", field)
		}
	}
	for _, field := range forbidden {
		if strings.Contains(content, `id="`+field+`"`) {
			t.Errorf("rendered template contains forbidden field %q", field)
		}
	}
}

func TestTunnelListTemplateQuotesRuntimeJavaScriptValues(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/index/list.html"))
	data := map[string]interface{}{
		"type":         "tcp');window.__npsTemplateInjected=true;//",
		"client_id":    "1');window.__npsTemplateInjected=true;//",
		"ip":           "127.0.0.1",
		"p":            8024,
		"tls_p":        8025,
		"win":          "./npc",
		"bridgeType":   "kcp",
		"tls_enable":   true,
		"web_base_url": "",
		"isAdmin":      true,
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute tunnel list template: %v", err)
	}
	content := rendered.String()
	for _, marker := range []string{
		`var tunnelListMode = '`,
		`var tunnelListClientId = Number('`,
		`var address = "127.0.0.1" + ":"`,
		`-type=kcp -password=`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("rendered tunnel list misses safely quoted runtime value %q", marker)
		}
	}
	if strings.Contains(content, `');window.__npsTemplateInjected=true;//`) {
		t.Fatal("tunnel list must escape route values embedded in JavaScript strings")
	}
}

func TestHostListTemplateEscapesRuntimeJavaScriptValues(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/index/hlist.html"))
	data := map[string]interface{}{
		"client_id": "1');window.__npsTemplateInjected=true;//",
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute host list template: %v", err)
	}
	content := rendered.String()
	if !strings.Contains(content, `var hostListClientId = Number('`) {
		t.Fatal("host list must wrap client ID in a JavaScript string before conversion")
	}
	if strings.Contains(content, `');window.__npsTemplateInjected=true;//`) {
		t.Fatal("host list must escape route values embedded in JavaScript strings")
	}
}

func TestClientSelectorTemplatesUseTextNodes(t *testing.T) {
	for _, path := range []string{"../views/index/add.html", "../views/index/hadd.html"} {
		content := readTemplateForTest(t, path)
		for _, marker := range []string{`document.createElement('option')`, `option.textContent =`, `option.selected =`} {
			if !strings.Contains(content, marker) {
				t.Fatalf("%s must construct client options with DOM text nodes: %s", path, marker)
			}
		}
		if strings.Contains(content, `option += ">" + data.rows[i].Id + '-' + data.rows[i].Remark`) {
			t.Fatalf("%s must not concatenate a client remark into option HTML", path)
		}
	}
}

func TestListTemplatesNormalizeClientIDBeforeAjax(t *testing.T) {
	tests := []struct {
		path     string
		required []string
		forbid   string
	}{
		{
			path: "../views/index/list.html",
			required: []string{
				`var tunnelListMode = '{{.type}}';`,
				`var tunnelListClientId = Number('{{.client_id}}');`,
				`"type": tunnelListMode,`,
				`"client_id": tunnelListClientId,`,
			},
			forbid: `"type":{{.type}}`,
		},
		{
			path: "../views/index/hlist.html",
			required: []string{
				`var hostListClientId = Number('{{.client_id}}');`,
				`client_id: hostListClientId`,
			},
			forbid: `"client_id": {{if .client_id}}{{.client_id}}{{else}}0{{end}}`,
		},
	}

	for _, tt := range tests {
		content := readTemplateForTest(t, tt.path)
		for _, marker := range tt.required {
			if !strings.Contains(content, marker) {
				t.Fatalf("%s must normalize client ID before loading data: %s", tt.path, marker)
			}
		}
		if strings.Contains(content, tt.forbid) {
			t.Fatalf("%s still injects a template value directly into JavaScript", tt.path)
		}
	}
}

func TestDashboardTemplateUsesScalarLoadValues(t *testing.T) {
	content := readTemplateForTest(t, "../views/index/index.html")
	if strings.Contains(content, "JSON.parse({{.data.load}})") {
		t.Fatal("dashboard must not pass an unquoted JSON object to JSON.parse")
	}
	if !strings.Contains(content, `$("#overview_load").text("{{.data.load1}} / {{.data.load5}} / {{.data.load15}}");`) {
		t.Fatal("dashboard should render load averages from scalar status fields")
	}
}

func TestSharedTemplatesQuoteRuntimeConfig(t *testing.T) {
	for _, path := range []string{
		"../views/login/index.html",
		"../views/login/register.html",
		"../views/public/layout.html",
	} {
		content := readTemplateForTest(t, path)
		if strings.Contains(content, `"web_base_url": {{.web_base_url}}`) {
			t.Fatalf("%s injects web_base_url as an unquoted JavaScript value", path)
		}
		if !strings.Contains(content, `"web_base_url": "{{.web_base_url}}"`) {
			t.Fatalf("%s must quote web_base_url in the runtime JavaScript object", path)
		}
	}
}

func readTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(b)
}
