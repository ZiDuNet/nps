package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestGUIAssetNameFor(t *testing.T) {
	tests := []struct {
		goos string
		arch string
		want string
	}{
		{goos: "windows", arch: "arm64", want: "npc-gui-windows-arm64.zip"},
		{goos: "darwin", arch: "amd64", want: "npc-gui-darwin-amd64.zip"},
		{goos: "linux", arch: "amd64", want: "npc-gui-linux-amd64.zip"},
	}

	for _, tt := range tests {
		if got := guiAssetNameFor(tt.goos, tt.arch); got != tt.want {
			t.Fatalf("guiAssetNameFor(%q, %q) = %q, want %q", tt.goos, tt.arch, got, tt.want)
		}
	}
}

func TestMacAppBundlePath(t *testing.T) {
	path := "/Applications/NPS Client.app/Contents/MacOS/NPS Client"
	bundle, ok := macAppBundlePath(path)
	if !ok {
		t.Fatal("macAppBundlePath did not recognize a valid app executable")
	}
	if bundle != "/Applications/NPS Client.app" {
		t.Fatalf("bundle = %q, want %q", bundle, "/Applications/NPS Client.app")
	}
	if _, ok := macAppBundlePath("/usr/local/bin/npc-gui"); ok {
		t.Fatal("macAppBundlePath accepted a standalone executable")
	}
}

func TestReplaceMacAppBundle(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "NPS Client.app")
	executablePath := filepath.Join(appPath, "Contents", "MacOS", "NPS Client")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executablePath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(root, "update.zip")
	writeUpdateZip(t, archivePath, map[string]string{
		"NPS Client.app/Contents/MacOS/NPS Client": "new binary",
		"NPS Client.app/Contents/Info.plist":       "new metadata",
	})

	if err := replaceMacAppBundle(archivePath, executablePath, filepath.Join(root, "temporary")); err != nil {
		t.Fatalf("replaceMacAppBundle returned error: %v", err)
	}
	content, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" {
		t.Fatalf("updated executable = %q, want new binary", content)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("updated executable mode = %o, want executable", info.Mode())
	}
	backupContent, err := os.ReadFile(filepath.Join(appPath+".old", "Contents", "MacOS", "NPS Client"))
	if err != nil {
		t.Fatal(err)
	}
	if string(backupContent) != "old binary" {
		t.Fatalf("backup executable = %q, want old binary", backupContent)
	}
}

func writeUpdateZip(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
}
