package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ehang.io/nps/lib/version"
)

const (
	selfUpdateCommand   = "__self-update"
	selfUpdateInterval  = 24 * time.Hour
	selfUpdateRepo      = "ZiDuNet/nps"
	selfUpdateStateName = "npsctl-update.json"
	selfUpdateCheckAPI  = "https://api.github.com/repos/" + selfUpdateRepo + "/releases/latest"
)

type selfUpdateState struct {
	LastCheck int64 `json:"last_check"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// maybeScheduleSelfUpdate starts a detached best-effort updater. It must never
// block, fail or change the exit code of the command the user actually ran.
func maybeScheduleSelfUpdate() {
	if strings.TrimSpace(os.Getenv("NPSCTL_NO_UPDATE_CHECK")) == "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	applyPendingUpdate(exe)
	if !selfUpdateDue() {
		return
	}
	cmd := exec.Command(exe, selfUpdateCommand)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

func selfUpdateStatePath() string {
	dir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "nps", selfUpdateStateName)
}

func selfUpdateDue() bool {
	path := selfUpdateStatePath()
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var state selfUpdateState
	if json.Unmarshal(raw, &state) != nil {
		return true
	}
	return time.Since(time.Unix(state.LastCheck, 0)) >= selfUpdateInterval
}

func markSelfUpdateChecked() {
	path := selfUpdateStatePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(selfUpdateState{LastCheck: time.Now().Unix()})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}

func selfUpdateAssetName() (string, bool) {
	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64", "386":
	case "arm":
		arch = "armv7"
	default:
		return "", false
	}
	name := "npsctl_" + runtime.GOOS + "_" + arch
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name, true
}

// runSelfUpdate performs one update attempt. Every failure is swallowed: the
// caller is a detached helper, and an unreachable GitHub or a read-only
// install directory must never affect the user's command.
func runSelfUpdate() (string, error) {
	markSelfUpdateChecked()
	if strings.TrimSpace(version.VERSION) == "" {
		return "", errors.New("unknown local version")
	}
	assetName, ok := selfUpdateAssetName()
	if !ok {
		return "", errors.New("unsupported platform")
	}
	release, err := fetchLatestRelease()
	if err != nil {
		return "", err
	}
	comparison, err := version.Compare(release.TagName, version.VERSION)
	if err != nil || comparison <= 0 {
		return "", nil
	}
	assetURL, checksumURL := "", ""
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			assetURL = asset.URL
		case "checksums.txt":
			checksumURL = asset.URL
		}
	}
	if assetURL == "" || checksumURL == "" {
		return "", errors.New("release assets missing")
	}
	expected, err := fetchChecksum(checksumURL, assetName)
	if err != nil {
		return "", err
	}
	payload, err := downloadFile(assetURL)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expected) {
		return "", errors.New("checksum mismatch")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := writePendingUpdate(exe, payload); err != nil {
		return "", err
	}
	applyPendingUpdate(exe)
	return strings.TrimPrefix(release.TagName, "v"), nil
}

func fetchLatestRelease() (*githubRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, selfUpdateCheckAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "npsctl/"+version.VERSION)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release lookup failed: HTTP %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, errors.New("release tag missing")
	}
	return &release, nil
}

func fetchChecksum(url, assetName string) (string, error) {
	body, err := downloadFile(url)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "./"), "/")
		if name == assetName {
			return fields[0], nil
		}
	}
	return "", errors.New("checksum entry missing")
}

func downloadFile(url string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "npsctl/"+version.VERSION)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func writePendingUpdate(exe string, payload []byte) error {
	target := exe + ".new"
	if err := os.WriteFile(target, payload, 0o755); err != nil {
		return err
	}
	return os.Chmod(target, 0o755)
}

// applyPendingUpdate swaps a previously downloaded binary into place. Renaming
// the running executable is allowed on every supported platform, so this also
// works on Windows where the file cannot be overwritten directly.
func applyPendingUpdate(exe string) {
	pending := exe + ".new"
	if _, err := os.Stat(pending); err != nil {
		return
	}
	backup := exe + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		return
	}
	if err := os.Rename(pending, exe); err != nil {
		_ = os.Rename(backup, exe)
		return
	}
	_ = os.Remove(backup)
}

// selfUpdateCommandRun is the visible `npsctl self-update` entry point. It
// reports the outcome but never fails the process for network problems.
func selfUpdateCommandRun(out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	latest, err := runSelfUpdate()
	switch {
	case err != nil:
		fmt.Fprintln(out, "自更新检查失败（不影响当前操作）:", err)
	case latest == "":
		fmt.Fprintln(out, "已是最新版本", version.VERSION)
	default:
		fmt.Fprintf(out, "已更新到 %s（下次运行生效）\n", latest)
	}
	return nil
}
