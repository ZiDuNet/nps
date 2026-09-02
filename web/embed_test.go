package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractWebFilesSyncsEmbeddedViews(t *testing.T) {
	runPath := t.TempDir()
	addTemplatePath := filepath.Join(runPath, "web", "views", "index", "add.html")
	if err := os.MkdirAll(filepath.Dir(addTemplatePath), 0755); err != nil {
		t.Fatalf("create stale template dir: %v", err)
	}
	if err := os.WriteFile(addTemplatePath, []byte("stale template"), 0644); err != nil {
		t.Fatalf("write stale template: %v", err)
	}

	ExtractWebFiles(runPath)

	content, err := os.ReadFile(addTemplatePath)
	if err != nil {
		t.Fatalf("read extracted template: %v", err)
	}
	got := string(content)
	if got == "stale template" {
		t.Fatal("existing template was not synced")
	}
	if !strings.Contains(got, `display: none !important;`) {
		t.Fatal("synced template does not contain robust hide rule")
	}
	if !strings.Contains(got, `.tunnel-form .tunnel-field-hidden`) || !strings.Contains(got, `data-tunnel-field="local_path"`) {
		t.Fatal("synced template does not contain the mode-aware tunnel field rule")
	}
}
