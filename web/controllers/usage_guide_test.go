package controllers

import (
	"bytes"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
)

func useUsageGuideTestConf(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0755); err != nil {
		t.Fatal(err)
	}
	previous := common.ConfPath
	common.ConfPath = root
	t.Cleanup(func() { common.ConfPath = previous })
	return root
}

func TestPrepareUsageGuideWritesPastedHTMLToConf(t *testing.T) {
	root := useUsageGuideTestConf(t)
	update, err := prepareUsageGuideUpdate("<html><body><h1>QA</h1></body></html>")
	if err != nil {
		t.Fatal(err)
	}
	if update.Mode != usageGuideModeHTML || update.Source != usageGuideHTMLLocalPath {
		t.Fatalf("HTML guide update = %#v", update)
	}
	content, err := os.ReadFile(filepath.Join(root, "conf", "qa.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<h1>QA</h1>") {
		t.Fatalf("saved HTML = %q", content)
	}
	if update.rollback != nil {
		update.rollback()
	}
	if _, err := os.Stat(filepath.Join(root, "conf", "qa.html")); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove new qa.html: %v", err)
	}
}

func TestPrepareUsageGuideSupportsURLAndConfPaths(t *testing.T) {
	useUsageGuideTestConf(t)
	urlUpdate, err := prepareUsageGuideUpdate("https://docs.example.com/nps?id=1#start")
	if err != nil {
		t.Fatal(err)
	}
	if urlUpdate.Mode != usageGuideModeURL || urlUpdate.Source != "https://docs.example.com/nps?id=1#start" {
		t.Fatalf("URL guide update = %#v", urlUpdate)
	}

	fileUpdate, err := prepareUsageGuideUpdate("/conf/help/QA.html")
	if err != nil {
		t.Fatal(err)
	}
	if fileUpdate.Mode != usageGuideModeFile || fileUpdate.Source != "conf/help/QA.html" {
		t.Fatalf("file guide update = %#v", fileUpdate)
	}
}

func TestUsageGuideRejectsTraversalAndSensitiveFiles(t *testing.T) {
	useUsageGuideTestConf(t)
	for _, raw := range []string{"../secret.html", "conf/../../secret.html", "conf/nps.conf", "/etc/passwd.html"} {
		if _, err := prepareUsageGuideUpdate(raw); err == nil {
			t.Errorf("prepareUsageGuideUpdate(%q) succeeded, want rejection", raw)
		}
	}
}

func TestUsageGuideRejectsSymlinkOutsideConf(t *testing.T) {
	root := useUsageGuideTestConf(t)
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "conf", "outside.html")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	readUsageGuidePath, err := resolveUsageGuidePath("conf/outside.html")
	if err == nil {
		t.Fatalf("resolveUsageGuidePath(%q) = %q, want rejection", link, readUsageGuidePath)
	}
}

func TestUsageGuideEditorAndFileMissingState(t *testing.T) {
	root := useUsageGuideTestConf(t)
	global := &file.Glob{UsageGuideMode: usageGuideModeHTML, UsageGuideSource: usageGuideHTMLLocalPath}
	if value, editorErr := usageGuideEditorValue(global); value != "" || editorErr == "" {
		t.Fatalf("missing HTML editor value = %q, error=%q", value, editorErr)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "qa.html"), []byte("<p>hello</p>"), 0644); err != nil {
		t.Fatal(err)
	}
	if value, editorErr := usageGuideEditorValue(global); value != "<p>hello</p>" || editorErr != "" {
		t.Fatalf("HTML editor value = %q, error=%q", value, editorErr)
	}
}

func TestHelpTemplateRendersReadyAndMissingStates(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/index/help.html"))
	for _, data := range []map[string]interface{}{
		{
			"web_base_url":        "",
			"isAdmin":             true,
			"help_state":          "ready",
			"help_mode":           usageGuideModeHTML,
			"help_source":         usageGuideHTMLLocalPath,
			"help_embed_url":      template.URL("/index/help/content"),
			"help_external_url":   template.URL("/index/help/content"),
			"help_iframe_sandbox": "allow-scripts allow-forms",
			"help_mixed_content":  false,
		},
		{
			"web_base_url": "",
			"isAdmin":      false,
			"help_state":   "missing",
			"help_mode":    usageGuideModeFile,
			"help_source":  "conf/missing.html",
		},
	} {
		var rendered bytes.Buffer
		if err := tpl.Execute(&rendered, data); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rendered.String(), "help-document") {
			t.Fatalf("help template did not render its page shell: %s", rendered.String())
		}
	}
}

func TestHelpActionsShareToolbarAndRespectAdminPermissions(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/index/help.html"))
	for _, ready := range []bool{true, false} {
		for _, admin := range []bool{true, false} {
			state := "missing"
			if ready {
				state = "ready"
			}
			var rendered bytes.Buffer
			if err := tpl.Execute(&rendered, map[string]interface{}{
				"isAdmin": admin, "help_state": state, "help_mode": usageGuideModeURL,
				"help_source": "https://docs.example.test/guide", "web_base_url": "/panel",
				"help_embed_url":      template.URL("https://docs.example.test/guide"),
				"help_external_url":   template.URL("https://docs.example.test/guide"),
				"help_iframe_sandbox": "allow-scripts allow-same-origin",
			}); err != nil {
				t.Fatal(err)
			}
			html := rendered.String()
			configureCount := strings.Count(html, `href="/panel/global/index#usage-guide-settings"`)
			if (admin && configureCount != 1) || (!admin && configureCount != 0) {
				t.Fatalf("ready=%v admin=%v configure buttons=%d", ready, admin, configureCount)
			}
			if !ready {
				if strings.Contains(html, `<iframe id="help-document-frame"`) {
					t.Fatal("missing guide rendered an iframe")
				}
				continue
			}
			header := html[:strings.Index(html, "</header>")]
			if strings.Contains(header, "help-document-actions") {
				t.Fatal("ready guide kept a second row for toolbar actions")
			}
			if strings.Count(html, `id="help-document-refresh"`) != 1 ||
				!strings.Contains(html, `sandbox="allow-scripts allow-same-origin"`) ||
				!strings.Contains(html, `role="group" aria-label="说明操作"`) {
				t.Fatal("guide toolbar or iframe isolation settings changed")
			}
		}
	}
}

func TestUsageGuideContentHeadersUseSandbox(t *testing.T) {
	header := make(http.Header)
	usageGuideContentHeaders(header)
	if !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("CSP = %q, want sandbox", header.Get("Content-Security-Policy"))
	}
	if header.Get("Cache-Control") == "" || header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("content headers = %#v", header)
	}
}
