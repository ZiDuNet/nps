package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/store"
)

func openSettingsFixture(t *testing.T, conf string) (string, *store.Store) {
	t.Helper()
	runPath := t.TempDir()
	confDir := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if conf != "" {
		if err := os.WriteFile(filepath.Join(confDir, "nps.conf"), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sqlite, err := store.Open(filepath.Join(confDir, "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	return runPath, sqlite
}

func resetSettingsManager(t *testing.T) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
}

func TestInitImportsBusinessKeysAndLeavesBootKeys(t *testing.T) {
	resetSettingsManager(t)
	runPath, sqlite := openSettingsFixture(t, strings.Join([]string{
		"web_port=8081",
		"allow_user_login=true",
		"register_rate_limit=7",
		"allow_flow_limit=true",
	}, "\n"))
	defer sqlite.Close()
	if err := Init(runPath, sqlite); err != nil {
		t.Fatal(err)
	}
	values, err := sqlite.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if values["allow_user_login"] != "true" || values["register_rate_limit"] != "7" {
		t.Fatalf("business settings not imported: %#v", values)
	}
	if _, exists := values["allow_flow_limit"]; exists {
		t.Fatalf("legacy feature gate was imported: %#v", values)
	}
	content, err := os.ReadFile(filepath.Join(runPath, "conf", "nps.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "web_port=8081") {
		t.Fatalf("boot setting was removed from nps.conf: %s", text)
	}
	for _, key := range []string{"allow_user_login", "register_rate_limit", "allow_flow_limit"} {
		if strings.Contains(text, key+"=") {
			t.Fatalf("%s was left in nps.conf: %s", key, text)
		}
	}
	if err := Init(runPath, sqlite); err != nil {
		t.Fatalf("second settings.Init() error = %v", err)
	}
}

func TestSaveRejectsBootKeyAndUpdatesBeego(t *testing.T) {
	resetSettingsManager(t)
	runPath, sqlite := openSettingsFixture(t, "web_port=8081\nallow_user_login=false\n")
	defer sqlite.Close()
	if err := Init(runPath, sqlite); err != nil {
		t.Fatal(err)
	}
	if err := Save(map[string]string{"web_port": "9090"}, nil); err == nil {
		t.Fatal("Save accepted a boot-only key")
	}
	if err := Save(map[string]string{"allow_user_login": "true"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := Get("allow_user_login"); !ok || got != "true" {
		t.Fatalf("Get(allow_user_login) = %q, %v", got, ok)
	}
}

func TestApplyClearsDeletedManagedKey(t *testing.T) {
	resetSettingsManager(t)
	runPath, sqlite := openSettingsFixture(t, "allow_user_login=true\n")
	defer sqlite.Close()
	if err := Init(runPath, sqlite); err != nil {
		t.Fatal(err)
	}
	if err := Save(nil, []string{"allow_user_login"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := Get("allow_user_login"); exists {
		t.Fatal("deleted setting remained in manager snapshot")
	}
	values, err := sqlite.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := values["allow_user_login"]; exists {
		t.Fatalf("deleted setting remained in SQLite: %#v", values)
	}
}
