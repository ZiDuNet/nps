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
