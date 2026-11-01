package install

import (
	"os"
	"path/filepath"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/version"
)

func TestReleaseAssetName(t *testing.T) {
	tests := []struct {
		name string
		goos string
		arch string
		bin  string
		want string
	}{
		{name: "linux arm client", goos: "linux", arch: "arm", bin: "client", want: "linux_arm_client.tar.gz"},
		{name: "windows arm64 server", goos: "windows", arch: "arm64", bin: "server", want: "windows_arm64_server.tar.gz"},
		{name: "darwin amd64 client", goos: "darwin", arch: "amd64", bin: "client", want: "darwin_amd64_client.tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseAssetName(tt.goos, tt.arch, tt.bin); got != tt.want {
				t.Fatalf("releaseAssetName(%q, %q, %q) = %q, want %q", tt.goos, tt.arch, tt.bin, got, tt.want)
			}
		})
	}
}

func TestFindReleaseAsset(t *testing.T) {
	rl := &release{
		TagName: "v1.1.3",
		Assets: []releaseAsset{
			{Name: "linux_amd64_client.tar.gz", BrowserDownloadURL: "https://example.test/client"},
		},
	}

	asset, err := findReleaseAsset(rl, "linux_amd64_client.tar.gz")
	if err != nil {
		t.Fatalf("findReleaseAsset returned error: %v", err)
	}
	if asset.BrowserDownloadURL != "https://example.test/client" {
		t.Fatalf("unexpected asset URL %q", asset.BrowserDownloadURL)
	}
	if _, err := findReleaseAsset(rl, "windows_arm64_client.tar.gz"); err == nil {
		t.Fatal("findReleaseAsset accepted a missing asset")
	}
}

func TestUpdateAvailable(t *testing.T) {
	available, err := updateAvailable(version.VERSION)
	if err != nil {
		t.Fatalf("updateAvailable returned error: %v", err)
	}
	if available {
		t.Fatal("current version should not have an update")
	}
	if _, err := updateAvailable("master"); err == nil {
		t.Fatal("non-semantic release tag should be rejected")
	}
}

func TestReplaceBinFromPackageUsesExplicitTarget(t *testing.T) {
	packageDir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "npc-renamed")
	sourceName := "npc"
	if common.IsWindows() {
		sourceName += ".exe"
		target += ".exe"
	}
	if err := os.WriteFile(filepath.Join(packageDir, sourceName), []byte("new-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := copyStaticFileReplaceNpcAt(packageDir, targetDir, target); err != nil {
		t.Fatalf("copyStaticFileReplaceNpcAt: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary" {
		t.Fatalf("target content = %q, want new-binary", content)
	}
}

func TestStableExecutablePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "regular", path: "/opt/npc", want: "/opt/npc"},
		{name: "backup", path: "/opt/npc.old", want: "/opt/npc"},
		{name: "deleted inode", path: "/opt/npc (deleted)", want: "/opt/npc"},
		{name: "nested backup", path: "/opt/client/npc.old", want: "/opt/client/npc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stableExecutablePath(tt.path); got != tt.want {
				t.Fatalf("stableExecutablePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestReplaceExecutableDoesNotCreateUnixBackup(t *testing.T) {
	if common.IsWindows() {
		t.Skip("Windows requires the .old backup while the executable is running")
	}

	root := t.TempDir()
	src := filepath.Join(root, "new")
	dest := filepath.Join(root, "npc")
	if err := os.WriteFile(src, []byte("new-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable(src, dest); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	content, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary" {
		t.Fatalf("target content = %q, want new-binary", content)
	}
	if _, err := os.Stat(dest + ".old"); !os.IsNotExist(err) {
		t.Fatalf("unexpected Unix backup %s (err=%v)", dest+".old", err)
	}
}
