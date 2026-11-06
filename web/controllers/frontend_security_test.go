package controllers

import (
	"os"
	"strings"
	"testing"
)

func TestLanguageSubmitUsesSameOriginReturnUrl(t *testing.T) {
	content, err := os.ReadFile("../static/js/language.js")
	if err != nil {
		t.Fatalf("read language script: %v", err)
	}
	script := string(content)
	if !strings.Contains(script, "function npsSafeReturnUrl(referrer)") {
		t.Fatal("language script must validate referrer URLs")
	}
	if strings.Contains(script, "window.location.href = document.referrer") {
		t.Fatal("language script must not redirect directly to an arbitrary referrer")
	}
	for _, marker := range []string{
		"function resolveLanguageCode (value)",
		"function lookupLanguageValue (index, current)",
		"Missing or malformed entries must not erase existing",
		"var runtime = window.nps || {};",
		"var pendingLanguage = null;",
		"npsLanguageChanged",
		"click.npsLanguage",
		"registration_captcha_invalid",
		"registration_email_sent",
		"registration_success",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("language script misses regression guard %q", marker)
		}
	}
}

func TestConsoleTemplatesUseBootstrapWithoutZUI(t *testing.T) {
	paths := []string{
		"../views/public/layout.html",
		"../views/login/index.html",
		"../views/login/register.html",
		"../views/login/ipregister.html",
	}
	disallowed := []string{
		"static/css/style.css",
		"bootstrap.min.css",
		"bootstrap-table",
		"bootstrap.min.js",
		"popper.min.js",
		"zui-3.0.0",
		"zui-console.css",
		"data-toggle=",
		"data-dismiss=",
		"icon icon-",
		"window.zui",
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read console template %s: %v", path, err)
		}
		template := string(content)
		for _, required := range []string{
			"bootstrap-5.3.3.min.css",
			"bootstrap-5.3.3.bundle.min.js",
			"nps-console.css",
			"bootstrap-nps.css",
		} {
			if !strings.Contains(template, required) {
				t.Fatalf("console template %s must load %q", path, required)
			}
		}
		for _, token := range disallowed {
			if strings.Contains(template, token) {
				t.Fatalf("console template %s still references removed asset or legacy contract %q", path, token)
			}
		}
		if strings.Contains(template, `onclick="return switchLanguage(`) {
			t.Fatalf("console template %s must use the namespaced delegated language handler", path)
		}
	}
}

func TestLoginKeepsIPRegistrationEntryAndForcesRequestedBackgroundMotion(t *testing.T) {
	loginContent, err := os.ReadFile("../views/login/index.html")
	if err != nil {
		t.Fatalf("read login template: %v", err)
	}
	loginTemplate := string(loginContent)
	for _, marker := range []string{
		`class="auth-login-panel__ip-link"`,
		`href="{{.web_base_url}}/ip/register"`,
		`data-i18n-zh="IP 登记入口"`,
		"ui=20261106-5&motion=on",
		"static/css/nps-login-v2.css",
		"版权所有 <strong>NPS-Pro</strong> &copy; 2018-2026 By:WuShuo",
	} {
		if !strings.Contains(loginTemplate, marker) {
			t.Fatalf("login template misses IP registration or branding marker %q", marker)
		}
	}

	ipRegisterContent, err := os.ReadFile("../views/login/ipregister.html")
	if err != nil {
		t.Fatalf("read IP registration template: %v", err)
	}
	if !strings.Contains(string(ipRegisterContent), "ui=20261106-5&motion=on") {
		t.Fatal("IP registration template must use the animated background override")
	}

	accountRegisterContent, err := os.ReadFile("../views/login/register.html")
	if err != nil {
		t.Fatalf("read account registration template: %v", err)
	}
	accountRegisterTemplate := string(accountRegisterContent)
	for _, marker := range []string{
		`id="auth-network-background"`,
		"ui=20261106-5&motion=on",
		"syncRegisterBackgroundTheme",
		"版权所有 <strong>NPS-Pro</strong> &copy; 2018-2026 By:WuShuo",
	} {
		if !strings.Contains(accountRegisterTemplate, marker) {
			t.Fatalf("account registration template misses animated background marker %q", marker)
		}
	}

	backgroundContent, err := os.ReadFile("../static/background/nps-network-background.html")
	if err != nil {
		t.Fatalf("read network background: %v", err)
	}
	background := string(backgroundContent)
	for _, marker := range []string{
		`document.documentElement.setAttribute('data-motion', 'on')`,
		`document.documentElement.getAttribute('data-motion') !== 'on'`,
		`:root[data-motion="on"] #ambient`,
		`:root[data-motion="on"] #sweep`,
		`:root[data-motion="on"] #halo`,
		`id="cursorGlow"`,
		`setPointer:function(x,y,active)`,
		`data-pointer-active`,
	} {
		if !strings.Contains(background, marker) {
			t.Fatalf("network background misses explicit motion override marker %q", marker)
		}
	}
}

func TestLoginRememberControlSubmitsWithLoginForm(t *testing.T) {
	content, err := os.ReadFile("../views/login/index.html")
	if err != nil {
		t.Fatalf("read login template: %v", err)
	}
	template := string(content)

	formStart := strings.Index(template, `<form id="login-form"`)
	if formStart < 0 {
		t.Fatal("login template must expose the submitted #login-form")
	}
	formEndOffset := strings.Index(template[formStart:], "</form>")
	if formEndOffset < 0 {
		t.Fatal("login form is not closed")
	}
	formEnd := formStart + formEndOffset
	remember := strings.Index(template, `id="auth-remember"`)
	if remember < formStart || remember > formEnd {
		t.Fatal("remember_login checkbox must be inside #login-form so it is submitted")
	}
	for _, marker := range []string{
		`document.getElementById('login-form')`,
		`$('#login-form').serializeArray()`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("login script must use the scoped login form marker %q", marker)
		}
	}
	if strings.Contains(template, `$("form").serializeArray()`) {
		t.Fatal("login script must not serialize every form on the page")
	}
}

func TestLoginPoWStatusIsActionableAndLocalized(t *testing.T) {
	content, err := os.ReadFile("../views/login/index.html")
	if err != nil {
		t.Fatalf("read login template: %v", err)
	}
	template := string(content)
	for _, marker := range []string{
		`id="auth-pow-status" data-state="preparing"`,
		`role="status" aria-live="polite"`,
		`data-pow-message="preparing"`,
		`data-pow-message="calculating"`,
		`data-pow-message="ready"`,
		`data-pow-message="error"`,
		`id="auth-pow-retry" type="button" hidden`,
		`function setLoginPowState(state)`,
		`window.npsPowReady = false;`,
		`window.npsPowRunning = false;`,
		`window.npsPowUnsupported = false;`,
		`id="auth-pow-compat"`,
		`function checkLoginPowCompatibility()`,
		`!window.fetch || !window.Worker`,
		`window.isSecureContext === false`,
		`!window.crypto || !window.crypto.subtle`,
		`submit.disabled = working;`,
		`npsPowRetry.addEventListener('click', prepareLoginPow)`,
		`new Worker(base + '/static/js/nps-pow-worker.js`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("login PoW status misses actionable UI contract %q", marker)
		}
	}

	cssContent, err := os.ReadFile("../static/css/nps-login-v2.css")
	if err != nil {
		t.Fatalf("read login styles: %v", err)
	}
	css := string(cssContent)
	for _, marker := range []string{
		`.auth-login-pow-status[data-state="ready"]`,
		`.auth-login-pow-status[data-state="error"]`,
		`.auth-login-pow-status__retry`,
		`.auth-login-pow-compat`,
		`.auth-login-pow-compat.is-error`,
		`@keyframes auth-login-pow-sweep`,
	} {
		if !strings.Contains(css, marker) {
			t.Fatalf("login PoW styles miss visual state contract %q", marker)
		}
	}

	workerContent, err := os.ReadFile("../static/js/nps-pow-worker.js")
	if err != nil {
		t.Fatalf("read login PoW worker: %v", err)
	}
	worker := string(workerContent)
	for _, marker := range []string{
		`function runSelfTest()`,
		`function solveChallenge(nonce, bits)`,
		`sha256SingleBlock(block, words, state)`,
		`self.onmessage = function`,
		`type: 'progress'`,
		`type: 'result'`,
	} {
		if !strings.Contains(worker, marker) {
			t.Fatalf("login PoW worker misses performance contract %q", marker)
		}
	}
}

func TestAccountManagementCentralizesSessionsAndAdminTOTP(t *testing.T) {
	content, err := os.ReadFile("../views/user/api.html")
	if err != nil {
		t.Fatalf("read account management template: %v", err)
	}
	template := string(content)
	for _, marker := range []string{
		`data-i18n-zh="账户管理"`,
		`id="totp-start"`,
		`id="totp-confirm"`,
		`id="totp-disable"`,
		`id="api-session-list"`,
		`id="session-revoke-all"`,
		`post('/security/revoke-all')`,
		`refreshSessionDevices()`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("account management template misses security marker %q", marker)
		}
	}

	layoutContent, err := os.ReadFile("../views/public/layout.html")
	if err != nil {
		t.Fatalf("read console layout: %v", err)
	}
	layout := string(layoutContent)
	if !strings.Contains(layout, `data-i18n-zh="账户管理"`) ||
		strings.Contains(layout, `data-i18n-zh="API 接入"`) {
		t.Fatal("console navigation must call /user/api Account management")
	}
}

func TestConsolePageContextUsesPersistentTopbar(t *testing.T) {
	layoutContent, err := os.ReadFile("../views/public/layout.html")
	if err != nil {
		t.Fatalf("read console layout: %v", err)
	}
	layout := string(layoutContent)
	for _, marker := range []string{
		`id="nps-page-context"`,
		`function npsSyncPageContext(root)`,
		`npsSyncPageContext(content);`,
		`#nps-page-context h1, #nps-content h1`,
	} {
		if !strings.Contains(layout, marker) {
			t.Fatalf("console layout misses persistent page context contract %q", marker)
		}
	}

	styleContent, err := os.ReadFile("../static/css/nps-console.css")
	if err != nil {
		t.Fatalf("read console stylesheet: %v", err)
	}
	style := string(styleContent)
	for _, marker := range []string{
		`body.nps-bootstrap .nps-page-context {`,
		`body.nps-bootstrap .dashboard-page-header.has-context-in-topbar`,
	} {
		if !strings.Contains(style, marker) {
			t.Fatalf("console stylesheet misses persistent page context contract %q", marker)
		}
	}
}

func TestGlobalSecurityTabAndPoWExclusiveForm(t *testing.T) {
	for _, path := range []string{
		"../views/global/index.html",
		"../views/global/registration.html",
		"../views/global/config.html",
		"../views/global/certificate.html",
		"../views/global/security.html",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read global settings template %s: %v", path, err)
		}
		if !strings.Contains(string(content), `href="{{.web_base_url}}/global/security"`) {
			t.Fatalf("global settings template %s misses Security tab", path)
		}
	}

	securityContent, err := os.ReadFile("../views/global/security.html")
	if err != nil {
		t.Fatalf("read security settings template: %v", err)
	}
	securityTemplate := string(securityContent)
	for _, marker := range []string{
		`id="security-pow-enabled"`,
		`id="security-pow-bits"`,
		`/security/pow/save`,
		`打开账户管理`,
	} {
		if !strings.Contains(securityTemplate, marker) {
			t.Fatalf("security settings template misses marker %q", marker)
		}
	}

	configContent, err := os.ReadFile("../controllers/server_config.go")
	if err != nil {
		t.Fatalf("read server config controller: %v", err)
	}
	configSource := string(configContent)
	for _, marker := range []string{
		`var securityConfigKeySet = map[string]struct{}`,
		`"force_pow": {}`,
		`"pow_bits":  {}`,
		`for key := range securityConfigKeySet`,
	} {
		if !strings.Contains(configSource, marker) {
			t.Fatalf("server configuration must exclude Security tab fields; missing %q", marker)
		}
	}
}

func TestAuthenticationPagesShareGlassShellAndSpotlight(t *testing.T) {
	for _, path := range []string{
		"../views/login/index.html",
		"../views/login/register.html",
		"../views/login/ipregister.html",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read authentication template %s: %v", path, err)
		}
		template := string(content)
		for _, marker := range []string{
			`class="auth-login-shell`,
			"static/css/nps-login-v2.css",
			"static/js/nps-auth-shell.js",
		} {
			if !strings.Contains(template, marker) {
				t.Fatalf("authentication template %s misses shared shell marker %q", path, marker)
			}
		}
	}

	scriptContent, err := os.ReadFile("../static/js/nps-auth-shell.js")
	if err != nil {
		t.Fatalf("read authentication shell script: %v", err)
	}
	script := string(scriptContent)
	for _, marker := range []string{
		"contentWindow.NPS_BG",
		"setPointer",
		"pointermove",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("authentication shell script misses spotlight marker %q", marker)
		}
	}

	ipContent, err := os.ReadFile("../views/login/ipregister.html")
	if err != nil {
		t.Fatalf("read IP registration template: %v", err)
	}
	ipTemplate := string(ipContent)
	for _, marker := range []string{
		`id="ip-register-form"`,
		`$('#ip-register-form').serializeArray()`,
		`class="auth-login-ip-meta"`,
	} {
		if !strings.Contains(ipTemplate, marker) {
			t.Fatalf("IP registration template misses preserved form marker %q", marker)
		}
	}

	styleContent, err := os.ReadFile("../static/css/nps-login-v2.css")
	if err != nil {
		t.Fatalf("read authentication stylesheet: %v", err)
	}
	stylesheet := string(styleContent)
	for _, marker := range []string{
		"--auth-brand-bg: rgba(246, 250, 254, .50)",
		"--auth-brand-bg: rgba(4, 13, 28, .52)",
		`[data-theme="dark"] body.auth-login-page .auth-login-captcha__challenge`,
		"backdrop-filter: blur(11px) saturate(112%)",
	} {
		if !strings.Contains(stylesheet, marker) {
			t.Fatalf("authentication stylesheet misses translucent or captcha marker %q", marker)
		}
	}
}

func TestRegistrationUsesLocalizedAuthMessages(t *testing.T) {
	content, err := os.ReadFile("../views/login/register.html")
	if err != nil {
		t.Fatalf("read registration template: %v", err)
	}
	template := string(content)
	for _, marker := range []string{
		`npsNotify('success', npsAuthErrorMessage(response));`,
		`npsNotify('error', npsAuthErrorMessage(response));`,
		`npsNotify('error', npsAuthErrorMessage(xhr.responseJSON));`,
		`npsNotify(res.status ? 'success' : 'error', npsAuthErrorMessage(res));`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("registration template must localize auth responses via %q", marker)
		}
	}
	for _, raw := range []string{
		`npsNotify('success', response.msg`,
		`npsNotify('error', (response && response.msg)`,
		`npsNotify('error', (xhr.responseJSON && xhr.responseJSON.msg)`,
		`npsNotify(res.status ? 'success' : 'error', res.msg)`,
	} {
		if strings.Contains(template, raw) {
			t.Fatalf("registration template still renders an unlocalized server message via %q", raw)
		}
	}
}

func TestLanguageDropdownKeepsCompactViewportBoundaries(t *testing.T) {
	content, err := os.ReadFile("../static/css/nps-console.css")
	if err != nil {
		t.Fatalf("read console stylesheet: %v", err)
	}
	stylesheet := string(content)
	for _, marker := range []string{
		"#languagemenu + .dropdown-menu",
		"width: max-content !important",
		"left: auto !important",
		"max-width: calc(100vw - 24px) !important",
	} {
		if !strings.Contains(stylesheet, marker) {
			t.Fatalf("console stylesheet misses language dropdown guard %q", marker)
		}
	}
}

func TestListToolbarsUseUnifiedRightAlignedControlOrder(t *testing.T) {
	scriptContent, err := os.ReadFile("../static/js/nps-table.js")
	if err != nil {
		t.Fatalf("read table script: %v", err)
	}
	script := string(scriptContent)
	if !strings.Contains(script, "this.$toolbar.append(this.$bars, this.$search, this.$filters, this.$columns, this.$barActions)") {
		t.Fatal("table utility controls must be ordered search, filters, columns, refresh")
	}
	if !strings.Contains(script, "if ($item.is('.nps-table-filter')) self.$filters.append($item)") {
		t.Fatal("table filters must be moved into their dedicated right-aligned group")
	}

	styleContent, err := os.ReadFile("../static/css/nps-console.css")
	if err != nil {
		t.Fatalf("read console stylesheet: %v", err)
	}
	stylesheet := string(styleContent)
	for _, marker := range []string{
		"body.nps-bootstrap .nps-table__toolbar {\n    justify-content: flex-end;",
		"body.nps-bootstrap .nps-table .nps-table__toolbar > .nps-table__bars {\n    margin-right: auto !important;",
		"body.nps-bootstrap .nps-table .nps-table__toolbar > .nps-table__columns,",
		"body.nps-bootstrap .nps-table__search {\n    order: 1;",
		"body.nps-bootstrap .nps-table__filters {\n    display: flex;\n    order: 2;",
		"body.nps-bootstrap .nps-table__columns {\n    order: 3;",
		"body.nps-bootstrap .nps-table__bar-actions {\n    order: 4;",
	} {
		if !strings.Contains(stylesheet, marker) {
			t.Fatalf("console stylesheet misses unified toolbar contract %q", marker)
		}
	}
}

func TestResourceListsExposeCascadingIdentityFilters(t *testing.T) {
	scriptContent, err := os.ReadFile("../static/js/nps-table.js")
	if err != nil {
		t.Fatalf("read table script: %v", err)
	}
	for _, marker := range []string{
		"initCascadingFilters",
		"refreshOwnerClientOptions",
		"refreshFilterTitles",
		"data-filter-owner-id",
		"[data-nps-table-filter=\"filter_client_id\"]",
	} {
		if !strings.Contains(string(scriptContent), marker) {
			t.Fatalf("table script misses cascading filter marker %q", marker)
		}
	}
	for _, path := range []string{"../views/index/list.html", "../views/index/hlist.html"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read resource list template %s: %v", path, readErr)
		}
		template := string(content)
		for _, marker := range []string{"nps-table-filter--identity", `data-filter-owner-id="{{.UserId}}"`, "未命名客户端（ID"} {
			if !strings.Contains(template, marker) {
				t.Fatalf("resource list template %s misses cascading filter marker %q", path, marker)
			}
		}
	}
	tunnelListContent, err := os.ReadFile("../views/index/list.html")
	if err != nil {
		t.Fatalf("read tunnel list template: %v", err)
	}
	for _, marker := range []string{
		"function tunnelTargetItems",
		"tunnel-target-entry",
		"tunnelAccessAddressMarkup",
		"tunnel-access-actions",
		"data-clipboard-text",
		"fa-external-link-alt",
		"currentconnections: false",
	} {
		if !strings.Contains(string(tunnelListContent), marker) {
			t.Fatalf("tunnel list template misses access display marker %q", marker)
		}
	}
	hostListContent, err := os.ReadFile("../views/index/hlist.html")
	if err != nil {
		t.Fatalf("read host list template: %v", err)
	}
	for _, marker := range []string{`'<span>' + label + '</span>`, "fa-external-link-alt", "host-client__line", "client.UserRemark"} {
		if !strings.Contains(string(hostListContent), marker) {
			t.Fatalf("host list template misses endpoint action marker %q", marker)
		}
	}
	styleContent, err := os.ReadFile("../static/css/nps-console.css")
	if err != nil {
		t.Fatalf("read console stylesheet: %v", err)
	}
	for _, marker := range []string{
		".client-flow-heading",
		".host-domain-badge--custom",
		".host-endpoint",
		".host-client__line",
		".tunnel-access-cell",
		".tunnel-target-entry",
		".client-list__address",
		".client-list__owner",
		".client-list__remark",
		".client-owner-cell__remark",
		".user-identity-remark",
	} {
		if !strings.Contains(string(styleContent), marker) {
			t.Fatalf("console stylesheet misses resource identity marker %q", marker)
		}
	}
	clientListContent, err := os.ReadFile("../views/client/list.html")
	if err != nil {
		t.Fatalf("read client list template: %v", err)
	}
	for _, marker := range []string{
		"storageKey: 'client-v4'",
		"class: 'client-list__address',\n                // Compact desktop keeps both flow columns visible",
		"class: 'client-list__inlet-flow',\n                visible: true",
		"class: 'client-list__export-flow',\n                visible: true",
		"align: 'center',\n                class: 'client-list__remark'",
		"align: 'center',\n                class: 'client-list__owner'",
	} {
		if !strings.Contains(string(clientListContent), marker) {
			t.Fatalf("client list template misses visible flow marker %q", marker)
		}
	}
}

func TestClientFiltersExposeOnlineStatusThroughAccessibleListbox(t *testing.T) {
	scriptContent, err := os.ReadFile("../static/js/nps-client-filter.js")
	if err != nil {
		t.Fatalf("read client filter script: %v", err)
	}
	script := string(scriptContent)
	for _, marker := range []string{
		"aria-haspopup",
		"role', 'listbox",
		"role', 'option",
		"data-client-online",
		"ArrowDown",
		"Escape",
		"NpsClientFilter",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("client filter script misses accessible status marker %q", marker)
		}
	}

	styleContent, err := os.ReadFile("../static/css/nps-client-filter.css")
	if err != nil {
		t.Fatalf("read client filter stylesheet: %v", err)
	}
	stylesheet := string(styleContent)
	for _, marker := range []string{
		".nps-client-filter__status.is-online",
		".nps-client-filter__status.is-offline",
		".nps-client-filter__trigger:focus-visible",
		".nps-client-filter__menu",
	} {
		if !strings.Contains(stylesheet, marker) {
			t.Fatalf("client filter stylesheet misses status marker %q", marker)
		}
	}

	for _, path := range []string{"../views/index/list.html", "../views/index/hlist.html"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read client filter template %s: %v", path, readErr)
		}
		template := string(content)
		for _, marker := range []string{`data-client-online="{{.IsConnect}}"`, `aria-labelledby=`} {
			if !strings.Contains(template, marker) {
				t.Fatalf("client filter template %s misses online state marker %q", path, marker)
			}
		}
	}

	layoutContent, err := os.ReadFile("../views/public/layout.html")
	if err != nil {
		t.Fatalf("read console layout: %v", err)
	}
	for _, marker := range []string{"nps-client-filter.css", "nps-client-filter.js"} {
		if !strings.Contains(string(layoutContent), marker) {
			t.Fatalf("console layout misses client filter asset %q", marker)
		}
	}

	overviewContent, err := os.ReadFile("../views/index/overview-admin.html")
	if err != nil {
		t.Fatalf("read overview template: %v", err)
	}
	for _, marker := range []string{"nps-client-filter.css", "nps-client-filter.js"} {
		if !strings.Contains(string(overviewContent), marker) {
			t.Fatalf("overview template misses client filter asset %q", marker)
		}
	}
}

func TestPublicLayoutKeepsShellAndNavigatesContentOnly(t *testing.T) {
	content, err := os.ReadFile("../views/public/layout.html")
	if err != nil {
		t.Fatalf("read public layout: %v", err)
	}
	template := string(content)
	for _, marker := range []string{
		`id="side-menu"`,
		`id="nps-content"`,
		`window.nps = { "web_base_url": "{{.web_base_url}}"`,
		`window.fetch(url.href`,
		`history.pushState`,
		`window.addEventListener('popstate'`,
		`executeContentScripts(content)`,
		`X-Requested-With`,
		`a[data-console-navigate][href]`,
		`window.npsBeforeNavigate`,
		`window.npsNavigate = navigate`,
		`data-i18n-zh="打开"`,
		`basePath = new URL(basePath, window.location.origin).pathname`,
		`url = window.location.origin + basePath + '/overview#access_key='`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("public layout misses content navigation marker %q", marker)
		}
	}
	if strings.Contains(template, `window.location.href = url.href`) {
		t.Fatal("menu navigation must not replace the complete console document")
	}
	if !strings.Contains(template, `"repository": "{{.github_repository}}" };`) {
		t.Fatal("public layout must terminate the runtime config statement before the version check")
	}
	for _, marker := range []string{
		`id="nps-version-indicator"`,
		`releases/latest`,
		`function checkConsoleVersion()`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("public layout misses console version marker %q", marker)
		}
	}
}

func TestConsoleContentNavigationStylesKeepPreviousContentVisible(t *testing.T) {
	content, err := os.ReadFile("../static/css/nps-console.css")
	if err != nil {
		t.Fatalf("read console stylesheet: %v", err)
	}
	stylesheet := string(content)
	for _, marker := range []string{
		`#nps-content.is-loading`,
		`#nps-content.is-loading::before`,
		`@keyframes nps-content-progress`,
	} {
		if !strings.Contains(stylesheet, marker) {
			t.Fatalf("console stylesheet misses navigation loading marker %q", marker)
		}
	}
}

func TestVendoredBootstrapReplacesLegacyUIStacks(t *testing.T) {
	assets := []string{
		"../static/css/bootstrap.min.css",
		"../static/css/bootstrap-table.min.css",
		"../static/css/datatables.css",
		"../static/css/style.css",
		"../static/js/bootstrap.min.js",
		"../static/js/bootstrap-table.min.js",
		"../static/js/bootstrap-table-locale-all.min.js",
		"../static/js/popper.min.js",
		"../static/css/zui-3.0.0.css",
		"../static/js/zui-3.0.0.js",
		"../static/webfonts/zenicon.woff",
	}
	for _, asset := range assets {
		if _, err := os.Stat(asset); !os.IsNotExist(err) {
			t.Errorf("legacy UI asset must not be shipped: %s (err: %v)", asset, err)
		}
	}
	for _, asset := range []string{
		"../static/css/bootstrap-5.3.3.min.css",
		"../static/js/bootstrap-5.3.3.bundle.min.js",
		"../static/css/nps-console.css",
		"../static/css/bootstrap-nps.css",
	} {
		if _, err := os.Stat(asset); err != nil {
			t.Errorf("vendored Bootstrap asset must be shipped: %s (err: %v)", asset, err)
		}
	}
}
