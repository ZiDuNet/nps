package controllers

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	beecontext "github.com/astaxie/beego/context"
)

func TestHostCertificateFormsSubmitMaterialUnderResolverFieldNames(t *testing.T) {
	for _, templatePath := range []string{"../views/index/hadd.html", "../views/index/hedit.html"} {
		content, err := os.ReadFile(templatePath)
		if err != nil {
			t.Fatal(err)
		}
		page := string(content)
		for _, marker := range []string{
			`name="cert" data-cert-data`,
			`name="key" data-key-data`,
		} {
			if !strings.Contains(page, marker) {
				t.Fatalf("%s is missing resolver field %q", templatePath, marker)
			}
		}
		for _, legacy := range []string{`name="cert_data"`, `name="key_data"`} {
			if strings.Contains(page, legacy) {
				t.Fatalf("%s still submits unsupported resolver field %q", templatePath, legacy)
			}
		}
		if !strings.Contains(page, `data-certificate-modes="upload,paste"`) {
			t.Fatalf("%s must expose upload and paste modes only", templatePath)
		}
		if strings.Contains(page, `name="cert_mode"`) && strings.Contains(page, `<option value="path"`) {
			t.Fatalf("%s still exposes a server-path certificate mode", templatePath)
		}
	}
}

func TestCertificateTemplatesKeepAdministratorPathModeAndValidationGate(t *testing.T) {
	globalPage, err := os.ReadFile("../views/global/certificate.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(globalPage)
	if got := strings.Count(page, `data-certificate-modes="path,upload,paste"`); got < 3 {
		t.Fatalf("certificate settings page has %d explicit administrator mode declarations, want at least 3", got)
	}
	if !strings.Contains(page, `bridgeWidget.prepareForSubmit()`) {
		t.Fatal("Bridge certificate save must use the validation gate")
	}
	if strings.Contains(page, `bridgeWidget.prepareCertificate()`) {
		t.Fatal("Bridge certificate save bypasses validation")
	}
}

func TestResolveHostCertificatePathsPersistsUploadedPair(t *testing.T) {
	previousRunPath := common.ConfPath
	common.ConfPath = t.TempDir()
	t.Cleanup(func() {
		common.ConfPath = previousRunPath
	})
	if err := os.MkdirAll(filepath.Join(common.ConfPath, "conf"), 0755); err != nil {
		t.Fatal(err)
	}

	certPath, keyPath := writeControllerTestCertificate(t, []string{"app.example.com"})
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"cert_mode": {"upload"},
		"cert":      {base64.StdEncoding.EncodeToString(certPEM)},
		"key":       {base64.StdEncoding.EncodeToString(keyPEM)},
	}
	request := httptest.NewRequest(http.MethodPost, "http://console.test/index/addhost", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := beecontext.NewContext()
	ctx.Reset(httptest.NewRecorder(), request)
	controller := &IndexController{}
	controller.Init(ctx, "IndexController", "AddHost", controller)

	storedCertPath, storedKeyPath, err := controller.resolveHostCertificatePaths(42, "app.example.com", request, "", "", false)
	if err != nil {
		t.Fatalf("persist uploaded certificate pair: %v", err)
	}
	if storedCertPath != "conf/certs/host_42.pem" || storedKeyPath != "conf/certs/host_42.key" {
		t.Fatalf("managed certificate paths = %q, %q", storedCertPath, storedKeyPath)
	}
	for path, want := range map[string][]byte{
		storedCertPath: certPEM,
		storedKeyPath:  keyPEM,
	} {
		got, readErr := os.ReadFile(filepath.Join(common.ConfPath, filepath.FromSlash(path)))
		if readErr != nil {
			t.Fatalf("read persisted certificate %q: %v", path, readErr)
		}
		if string(got) != string(want) {
			t.Fatalf("persisted certificate %q content changed", path)
		}
	}
}

func TestResolveHostCertificatePathsRejectsCustomPathChangesAndPreservesLegacy(t *testing.T) {
	newRequest := func(values url.Values) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "http://console.test/index/edithost", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}
	controller := &IndexController{}

	submittedPath := newRequest(url.Values{
		"cert_mode":      {"path"},
		"cert_file_path": {"/etc/nps/new.pem"},
		"key_file_path":  {"/etc/nps/new.key"},
	})
	if _, _, err := controller.resolveHostCertificatePaths(7, "app.example.com", submittedPath, "conf/certs/old.pem", "conf/certs/old.key", false); err == nil {
		t.Fatal("custom domain with changed server paths must be rejected")
	}

	legacyPaths := newRequest(url.Values{
		"cert_mode":      {"path"},
		"cert_file_path": {"conf/certs/old.pem"},
		"key_file_path":  {"conf/certs/old.key"},
	})
	certPath, keyPath, err := controller.resolveHostCertificatePaths(7, "app.example.com", legacyPaths, "conf/certs/old.pem", "conf/certs/old.key", false)
	if err != nil {
		t.Fatalf("unchanged legacy path pair should be preserved: %v", err)
	}
	if certPath != "conf/certs/old.pem" || keyPath != "conf/certs/old.key" {
		t.Fatalf("legacy paths changed: %q, %q", certPath, keyPath)
	}

	platformJump := newRequest(url.Values{
		"cert_mode":      {"path"},
		"cert_file_path": {"/etc/passwd"},
		"key_file_path":  {"/etc/shadow"},
	})
	certPath, keyPath, err = controller.resolveHostCertificatePaths(7, "managed.example.com", platformJump, "conf/certs/old.pem", "conf/certs/old.key", true)
	if err != nil {
		t.Fatalf("platform-managed host should ignore host certificate paths: %v", err)
	}
	if certPath != "" || keyPath != "" {
		t.Fatalf("platform-managed host persisted host certificate paths: %q, %q", certPath, keyPath)
	}
}
