package install

import (
	"testing"

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
