package install

import (
	"os"
	"path/filepath"
	"strings"
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

func TestStaticFileDestinationsUseInstallPathOnWindows(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "Program Files", "nps")
	got := staticFileDestinations("npc", installDir, true)
	want := filepath.Join(installDir, "npc.exe")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("staticFileDestinations() = %v, want [%s]", got, want)
	}
}

func TestCopyStaticFileReplaceNpcTargetsUpdatesEveryTarget(t *testing.T) {
	packageDir := t.TempDir()
	sourceName := "npc"
	if common.IsWindows() {
		sourceName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(packageDir, sourceName), []byte("new-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	targets := []string{
		filepath.Join(targetDir, "first-npc"),
		filepath.Join(targetDir, "second-npc"),
	}
	for _, target := range targets {
		if err := os.WriteFile(target, []byte("old-binary"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	updated, err := copyStaticFileReplaceNpcTargets(packageDir, targets)
	if err != nil {
		t.Fatalf("copyStaticFileReplaceNpcTargets: %v", err)
	}
	if len(updated) != len(targets) {
		t.Fatalf("updated targets = %v, want %v", updated, targets)
	}
	for _, target := range targets {
		content, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "new-binary" {
			t.Fatalf("%s content = %q, want new-binary", target, content)
		}
	}
}

func TestCopyStaticFileReplaceNpcTargetsContinuesAfterOneFailure(t *testing.T) {
	packageDir := t.TempDir()
	sourceName := "npc"
	if common.IsWindows() {
		sourceName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(packageDir, sourceName), []byte("new-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0644); err != nil {
		t.Fatal(err)
	}
	invalidTarget := filepath.Join(blocker, "npc")
	validTarget := filepath.Join(root, "npc")
	if err := os.WriteFile(validTarget, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	updated, err := copyStaticFileReplaceNpcTargets(packageDir, []string{invalidTarget, validTarget})
	if err == nil {
		t.Fatal("partial update should report the failed target")
	}
	if len(updated) != 1 || updated[0] != validTarget {
		t.Fatalf("updated targets = %v, want [%s]", updated, validTarget)
	}
	content, readErr := os.ReadFile(validTarget)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "new-binary" {
		t.Fatalf("valid target content = %q, want new-binary", content)
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
		{name: "unique backup", path: "/opt/npc.old-1762312345678900000", want: "/opt/npc"},
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

func TestExecutableBackupPathFallsBackWhenLegacyBackupIsBusy(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "npc.exe")
	legacy := dest + ".old"
	if err := os.MkdirAll(filepath.Join(legacy, "locked"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := executableBackupPath(dest)
	if err != nil {
		t.Fatalf("executableBackupPath: %v", err)
	}
	if got == legacy || !strings.HasPrefix(got, legacy+"-") {
		t.Fatalf("executableBackupPath() = %q, want a unique backup below %q", got, legacy)
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
	if content, err := os.ReadFile(src); err != nil || string(content) != "new-binary" {
		t.Fatalf("source executable was removed or changed: content=%q err=%v", content, err)
	}
}
