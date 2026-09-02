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
}

func TestConsoleTemplatesUseZUIWithoutBootstrap(t *testing.T) {
	paths := []string{
		"../views/public/layout.html",
		"../views/login/index.html",
		"../views/login/register.html",
	}
	disallowed := []string{
		"static/css/style.css",
		"bootstrap.min.css",
		"bootstrap-table",
		"bootstrap.min.js",
		"popper.min.js",
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read console template %s: %v", path, err)
		}
		template := string(content)
		if !strings.Contains(template, "zui-3.0.0") {
			t.Fatalf("console template %s must load ZUI", path)
		}
		for _, token := range disallowed {
			if strings.Contains(template, token) {
				t.Fatalf("console template %s still references removed Bootstrap asset %q", path, token)
			}
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
		`window.fetch(url.href`,
		`history.pushState`,
		`window.addEventListener('popstate'`,
		`executeContentScripts(content)`,
		`X-Requested-With`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("public layout misses content navigation marker %q", marker)
		}
	}
	if strings.Contains(template, `window.location.href = url.href`) {
		t.Fatal("menu navigation must not replace the complete console document")
	}
}

func TestConsoleContentNavigationStylesKeepPreviousContentVisible(t *testing.T) {
	content, err := os.ReadFile("../static/css/zui-console.css")
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

func TestBootstrapAssetsAreNotShipped(t *testing.T) {
	assets := []string{
		"../static/css/bootstrap.min.css",
		"../static/css/bootstrap-table.min.css",
		"../static/css/datatables.css",
		"../static/css/style.css",
		"../static/js/bootstrap.min.js",
		"../static/js/bootstrap-table.min.js",
		"../static/js/bootstrap-table-locale-all.min.js",
		"../static/js/popper.min.js",
	}
	for _, asset := range assets {
		if _, err := os.Stat(asset); !os.IsNotExist(err) {
			t.Errorf("legacy Bootstrap asset must not be shipped: %s (err: %v)", asset, err)
		}
	}
}
