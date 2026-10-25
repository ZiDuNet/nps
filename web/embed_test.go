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

func TestExtractWebFilesRemovesObsoleteZUIAssets(t *testing.T) {
	runPath := t.TempDir()
	stale := filepath.Join(runPath, "web", "static", "js", "zui-3.0.0.js")
	if err := os.MkdirAll(filepath.Dir(stale), 0755); err != nil {
		t.Fatalf("create stale asset dir: %v", err)
	}
	if err := os.WriteFile(stale, []byte("stale zui"), 0644); err != nil {
		t.Fatalf("write stale asset: %v", err)
	}

	ExtractWebFiles(runPath)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("obsolete ZUI asset still exists after extraction: %v", err)
	}
}

func TestLoginAndRegistrationTemplatesFailClosedWithoutPasswordEncryption(t *testing.T) {
	tests := []struct {
		path         string
		encryptCall  string
		failureCheck string
	}{
		{path: "views/login/index.html", encryptCall: "var encryptedPassword = npsEncryptedPassword(data.password)", failureCheck: "if (!encryptedPassword)"},
		{path: "views/login/register.html", encryptCall: "data.password = npsRSAEncryptor.encrypt(payload)", failureCheck: "if (!data.password)"},
	}
	for _, test := range tests {
		content, err := ViewsFS.ReadFile(test.path)
		if err != nil {
			t.Fatalf("read embedded template %s: %v", test.path, err)
		}
		template := string(content)
		for _, marker := range []string{
			"typeof JSEncrypt === 'function' ? new JSEncrypt() : null",
			test.encryptCall,
			test.failureCheck,
			"npsNotify('error'",
			"return false;",
		} {
			if !strings.Contains(template, marker) {
				t.Errorf("%s must fail closed when password encryption is unavailable; missing %q", test.path, marker)
			}
		}
		for _, unsafeFallback := range []string{"return password", "|| password", "|| data.password"} {
			if strings.Contains(template, unsafeFallback) {
				t.Errorf("%s must not fall back to submitting a plaintext password via %q", test.path, unsafeFallback)
			}
		}
		if test.path == "views/login/index.html" && !strings.Contains(template, "data.password = encryptedPassword") {
			t.Errorf("%s must submit the encrypted password after encryption succeeds", test.path)
		}
	}
}

func TestJSEncryptEmbeddedAssetIncludesLicense(t *testing.T) {
	if _, err := ReadStaticFile("js/jsencrypt.min.js"); err != nil {
		t.Fatalf("read embedded JSEncrypt bundle: %v", err)
	}
	license, err := ReadStaticFile("js/jsencrypt.min.js.LICENSE.txt")
	if err != nil {
		t.Fatalf("read embedded JSEncrypt license: %v", err)
	}
	if !strings.Contains(string(license), "MIT License") {
		t.Fatal("embedded JSEncrypt license does not include its MIT license text")
	}
}
