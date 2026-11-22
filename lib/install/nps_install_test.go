package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareNpsInstallConfigAcceptsBinaryOnlyRelease(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "nps")
	if err := prepareNpsInstallConfig(source, destination); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(destination, "conf"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("install should leave an empty conf directory for first startup: %v", err)
	}
}

func TestPrepareNpsInstallConfigKeepsExistingConfig(t *testing.T) {
	source, destination := t.TempDir(), filepath.Join(t.TempDir(), "nps")
	if err := os.Mkdir(filepath.Join(source, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "conf", "nps.conf"), []byte("web_port=1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareNpsInstallConfig(source, destination); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, "conf", "nps.conf")
	if data, err := os.ReadFile(path); err != nil || string(data) != "web_port=1234\n" {
		t.Fatalf("install did not keep the supplied config: %v", err)
	}
	if err := os.WriteFile(path, []byte("web_port=4321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareNpsInstallConfig(source, destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "web_port=4321\n" {
		t.Fatalf("install overwrote the deployed config: %v", err)
	}
}
