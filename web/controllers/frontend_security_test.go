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
