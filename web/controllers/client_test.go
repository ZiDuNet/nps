package controllers

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestClientListRowsExposeUserName(t *testing.T) {
	rows := newClientListRows([]*file.Client{
		{
			Id:          1,
			UserId:      10,
			UserName:    "alice",
			VerifyKey:   "client-vkey",
			WebPassword: "web-secret",
			IpWhitePass: "allowlist-secret",
			Cnf: &file.Config{
				U: "basic-user",
				P: "basic-secret",
			},
		},
	})

	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal client list rows: %v", err)
	}

	got := string(b)
	if !strings.Contains(got, `"UserName":"alice"`) {
		t.Fatalf("expected client list row to expose UserName, got %s", got)
	}
	for _, secret := range []string{"web-secret", "allowlist-secret", "basic-user", "basic-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("client list row must not expose credential %q: %s", secret, got)
		}
	}
}

func TestClientFormTemplatesExposeUserSelect(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "add", path: "../views/client/add.html"},
		{name: "edit", path: "../views/client/edit.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := readClientTemplateForTest(t, tt.path)
			for _, expected := range []string{
				`id="user_id"`,
				`所属用户`,
				`name="user_id"`,
				`{{range .users}}`,
				`{{.UserName}}`,
			} {
				if !strings.Contains(content, expected) {
					t.Fatalf("%s template misses user select marker: %s", tt.name, expected)
				}
			}
			workflowName := map[string]string{"add": "client-create", "edit": "client-edit"}[tt.name]
			workflow := `data-form-workflow="` + workflowName + `"`
			if !strings.Contains(content, workflow) {
				t.Fatalf("%s template misses workflow marker: %s", tt.name, workflow)
			}
			for _, expected := range []string{
				`class="form-page form-page-client`,
				`form-page-back`,
				`form-workflow-card`,
			} {
				if !strings.Contains(content, expected) {
					t.Fatalf("%s template misses shared form layout marker: %s", tt.name, expected)
				}
			}
		})
	}
}

func TestClientListTemplateUsesSafeAccessibleControls(t *testing.T) {
	content := readClientTemplateForTest(t, "../views/client/list.html")
	for _, marker := range []string{
		`$('body').setLang('#table');`,
		`npsBooleanMarkup(!!row.ConfigConnAllow)`,
		`npsApplyTableState(this, 'client')`,
		`type="button"`,
		`data-title-en="Disable client"`,
		`data-title-en="Copy command"`,
		`npsNotify('success', npsIsEnglish() ? 'Copied' : '复制成功')`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("client list misses safety/accessibility marker: %s", marker)
		}
	}
	for _, credentialField := range []string{"row.WebPassword", "row.IpWhitePass", "config.U", "config.P"} {
		if strings.Contains(content, credentialField) {
			t.Fatalf("client list must not render credential field: %s", credentialField)
		}
	}
}

func TestClientListTemplateQuotesBridgeType(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/client/list.html"))
	data := map[string]interface{}{
		"ip":           "127.0.0.1",
		"p":            8024,
		"tls_p":        8025,
		"win":          "./npc",
		"bridgeType":   "kcp",
		"isAdmin":      true,
		"web_base_url": "",
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute client list template: %v", err)
	}
	if !strings.Contains(rendered.String(), "-type=kcp'") {
		t.Fatalf("rendered client list must keep bridge type inside the command string: %s", rendered.String())
	}
}

func readClientTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(b)
}
