package fnos

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	return &Server{Root: root, LogPath: filepath.Join(root, "npc.log"), Prefix: "/app/NPC-Pro"}, root
}

func testServerWithLogs(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	server := &Server{Root: root, LogPath: filepath.Join(root, "npc.log"), LogDir: logDir, Prefix: "/app/NPC-Pro"}
	server.Status = func(id string) (bool, bool) {
		return id == MainID, id == MainID
	}
	return server, root
}

func doJSON(t *testing.T, server *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func TestEmbeddedSettingsRoundTrip(t *testing.T) {
	server, root := testServer(t)
	rec := doJSON(t, server, http.MethodGet, "/app/NPC-Pro/api/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config status %d", rec.Code)
	}
	var initial configView
	if err := json.Unmarshal(rec.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Revision != "initial" || len(initial.Connections) != 0 {
		t.Fatalf("unexpected initial config: %+v", initial)
	}

	rec = doJSON(t, server, http.MethodPost, "/app/NPC-Pro/api/config", configSubmit{
		Revision: "initial",
		Add: &connectionSubmit{
			Action: "add", Mode: "manual", Enabled: boolPtr(true),
			Host: "example.com", Port: "8024", VKey: "secret-key", Protocol: "tcp",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST config status %d body %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, server, http.MethodGet, "/app/NPC-Pro/api/config", nil)
	var after configView
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Connections) != 1 || after.Connections[0].Host != "example.com" || after.Connections[0].Protocol != "tcp" {
		t.Fatalf("unexpected saved config: %+v", after)
	}
	if _, err := Read(root); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedRawEditor(t *testing.T) {
	server, root := testServer(t)
	content := "[common]\nserver_addr=example.com:8024\nvkey=secret-key\nconn_type=tcp\n"
	rec := doJSON(t, server, http.MethodPost, "/app/NPC-Pro/api/raw", rawSubmit{ID: MainID, Content: content})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST raw status %d body %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, http.MethodGet, "/app/NPC-Pro/api/raw?id="+MainID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET raw status %d", rec.Code)
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Content != content {
		t.Fatalf("raw round trip mismatch: %v %q", err, out.Content)
	}
	if _, err := Read(root); err != nil {
		t.Fatal(err)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestPerConnectionLogAndStatus(t *testing.T) {
	server, _ := testServerWithLogs(t)
	rec := doJSON(t, server, http.MethodPost, "/app/NPC-Pro/api/config", configSubmit{
		Revision: "initial",
		Add: &connectionSubmit{
			Action: "add", Mode: "manual", Enabled: boolPtr(true),
			Host: "example.com", Port: "8024", VKey: "secret-key", Protocol: "tcp",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST config status %d body %s", rec.Code, rec.Body.String())
	}

	if err := os.MkdirAll(server.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenLog(ConnectionLogPath(server.LogDir, MainID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("demo log line\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, server, http.MethodGet, "/app/NPC-Pro/api/config", nil)
	var view configView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Connections) != 1 || !view.Connections[0].Running || view.Connections[0].VKey != "secret-key" {
		t.Fatalf("unexpected running/vkey view: %+v", view.Connections)
	}

	rec = doJSON(t, server, http.MethodGet, "/app/NPC-Pro/api/logs?id="+MainID, nil)
	var logs map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil || logs["log"] != "demo log line" {
		t.Fatalf("unexpected per-connection log: %v %q", err, logs["log"])
	}
}
