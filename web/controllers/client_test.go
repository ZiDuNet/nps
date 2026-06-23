package controllers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestClientListRowsExposeUserName(t *testing.T) {
	rows := newClientListRows([]*file.Client{
		{
			Id:       1,
			UserId:   10,
			UserName: "alice",
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
		})
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
