package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	updateRepo   = version.GitHubRepository
	updateAPIURL = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
)

// UpdateInfo 版本检查结果，供前端展示
type UpdateInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseNotes    string `json:"releaseNotes"`
	PublishedAt     string `json:"publishedAt"`
	DownloadURL     string `json:"downloadUrl"`
	AssetName       string `json:"assetName"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// guiAssetName 当前平台对应的 GUI 发布包文件名
func guiAssetName() string {
	return guiAssetNameFor(runtime.GOOS, runtime.GOARCH)
}

func guiAssetNameFor(goos, goarch string) string {
	return fmt.Sprintf("npc-gui-%s-%s.zip", goos, goarch)
}

// fetchLatestRelease 拉取 GitHub 最新 release 信息
func fetchLatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, updateAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取版本信息失败: HTTP %d", resp.StatusCode)
	}
	rl := new(ghRelease)
	if err := json.NewDecoder(resp.Body).Decode(rl); err != nil {
		return nil, err
	}
	if rl.TagName == "" {
		return nil, errors.New("无法解析最新版本号")
	}
	return rl, nil
}

func findAsset(rl *ghRelease, name string) (*ghAsset, error) {
	for i := range rl.Assets {
		if rl.Assets[i].Name == name {
			return &rl.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("发布资源中未找到 %s，当前平台暂不支持自动更新", name)
}

func updateAvailable(current, latest string) (bool, error) {
	comparison, err := version.Compare(current, latest)
	if err != nil {
		return false, fmt.Errorf("最新 Release 标签 %q 无效: %w", latest, err)
	}
	return comparison < 0, nil
}

func releaseAssetChecksum(rl *ghRelease, assetName string) (string, error) {
	checksums, err := findAsset(rl, "checksums.txt")
	if err != nil {
		return "", fmt.Errorf("Release %s 缺少 checksums.txt: %w", rl.TagName, err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, checksums.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 checksums.txt 失败: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || filepath.Base(strings.TrimPrefix(fields[len(fields)-1], "*")) != assetName {
			continue
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("%s 的 SHA-256 格式无效", assetName)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("%s 的 SHA-256 格式无效: %w", assetName, err)
		}
		return strings.ToLower(fields[0]), nil
	}
	return "", fmt.Errorf("checksums.txt 中未找到 %s", assetName)
}

// CheckForUpdate 检查是否有新版本可用（检测升级）
func (a *App) CheckForUpdate() (*UpdateInfo, error) {
	rl, err := fetchLatestRelease()
	if err != nil {
		return nil, err
	}
	latest := strings.TrimPrefix(rl.TagName, "v")
	current := version.VERSION
	available, err := updateAvailable(current, rl.TagName)
	if err != nil {
		return nil, err
	}

	assetName := guiAssetName()
	downloadURL := ""
	if asset, err := findAsset(rl, assetName); err == nil {
		downloadURL = asset.BrowserDownloadURL
	} else if available {
		return nil, err
	}

	return &UpdateInfo{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: available,
		ReleaseNotes:    rl.Body,
		PublishedAt:     rl.PublishedAt,
		DownloadURL:     downloadURL,
		AssetName:       assetName,
	}, nil
}

// DownloadAndInstallUpdate 下载最新版本并热替换当前可执行文件
func (a *App) DownloadAndInstallUpdate() error {
	rl, err := fetchLatestRelease()
	if err != nil {
		return err
	}
	available, err := updateAvailable(version.VERSION, rl.TagName)
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("当前已是最新版本 %s，无需更新", version.VERSION)
	}

	assetName := guiAssetName()
	asset, err := findAsset(rl, assetName)
	if err != nil {
		return err
	}
	checksum, err := releaseAssetChecksum(rl, assetName)
	if err != nil {
		return err
	}

	slog.Info("开始下载更新包", "url", asset.BrowserDownloadURL)

	exePath, err := getExecutablePath()
	if err != nil {
		return fmt.Errorf("获取当前程序路径失败: %w", err)
	}

	tempParent := ""
	if runtime.GOOS == "darwin" {
		if appBundle, ok := macAppBundlePath(exePath); ok {
			tempParent = filepath.Dir(appBundle)
		}
	}
	tempDir, err := os.MkdirTemp(tempParent, ".npc-gui-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	zipPath := filepath.Join(tempDir, assetName)
	if err := downloadFile(asset.BrowserDownloadURL, zipPath, checksum); err != nil {
		return err
	}

	if err := replaceFromZip(zipPath, exePath, tempDir); err != nil {
		return err
	}

	slog.Info("更新完成，请重启程序生效")
	return nil
}

// RestartApp 重启当前程序（更新完成后调用）
func (a *App) RestartApp() error {
	exePath, err := getExecutablePath()
	if err != nil {
		return fmt.Errorf("获取当前程序路径失败: %w", err)
	}
	cmd := exec.Command(exePath)
	if runtime.GOOS == "darwin" {
		if appBundle, ok := macAppBundlePath(exePath); ok {
			cmd = exec.Command("open", "-n", appBundle)
		}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动新版本失败: %w", err)
	}
	// 延迟退出当前进程，确保新进程已启动
	go func() {
		time.Sleep(800 * time.Millisecond)
		if a.app != nil {
			a.app.Quit()
		}
	}()
	return nil
}

func downloadFile(url, dest, checksum string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新包失败: HTTP %d", resp.StatusCode)
	}
	temporaryDest := dest + ".part"
	_ = os.Remove(temporaryDest)
	out, err := os.Create(temporaryDest)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(out, hash), resp.Body); err != nil {
		out.Close()
		_ = os.Remove(temporaryDest)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(temporaryDest)
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(actual, checksum) {
		_ = os.Remove(temporaryDest)
		return fmt.Errorf("更新包的 SHA-256 校验失败")
	}
	if err := os.Rename(temporaryDest, dest); err != nil {
		_ = os.Remove(temporaryDest)
		return err
	}
	return nil
}

// replaceFromZip 从 zip 更新包中取出可执行文件并热替换正在运行的程序
func replaceFromZip(zipPath, exePath, tempDir string) error {
	if runtime.GOOS == "darwin" {
		return replaceMacAppBundle(zipPath, exePath, tempDir)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开更新包失败: %w", err)
	}
	defer zr.Close()

	exeBase := filepath.Base(exePath)
	var found *zip.File
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if strings.EqualFold(name, exeBase) || strings.EqualFold(name, "npc-gui") || strings.EqualFold(name, "npc-gui.exe") {
			found = f
			break
		}
	}
	if found == nil {
		return fmt.Errorf("更新包中未找到可执行文件 %s", exeBase)
	}

	rc, err := found.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	newBin := filepath.Join(tempDir, "npc-gui"+guiExeExt())
	out, err := os.Create(newBin)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	out.Close()
	chmodExecutable(newBin, 0o755)

	return replaceExecutableFile(newBin, exePath)
}

func macAppBundlePath(exePath string) (string, bool) {
	marker := string(filepath.Separator) + "Contents" + string(filepath.Separator) + "MacOS" + string(filepath.Separator)
	index := strings.Index(exePath, marker)
	if index <= 0 {
		return "", false
	}
	bundlePath := exePath[:index]
	if !strings.HasSuffix(strings.ToLower(bundlePath), ".app") {
		return "", false
	}
	return bundlePath, true
}

func replaceMacAppBundle(zipPath, exePath, tempDir string) error {
	destination, ok := macAppBundlePath(exePath)
	if !ok {
		return fmt.Errorf("当前程序不在 macOS .app 包内，无法安全替换")
	}

	source, err := extractMacAppBundle(zipPath, filepath.Join(tempDir, "bundle"))
	if err != nil {
		return err
	}
	backup := destination + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("无法备份当前应用 %s: %w", destination, err)
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("替换 macOS 应用失败: %w", err)
	}
	return nil
}

func extractMacAppBundle(zipPath, destinationRoot string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("打开更新包失败: %w", err)
	}
	defer zr.Close()

	appRoot := ""
	for _, file := range zr.File {
		cleanName := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(file.Name)), "./")
		parts := strings.Split(cleanName, "/")
		if len(parts) > 0 && strings.HasSuffix(strings.ToLower(parts[0]), ".app") {
			appRoot = parts[0]
			break
		}
	}
	if appRoot == "" {
		return "", errors.New("更新包中未找到 macOS .app 应用包")
	}

	if err := os.MkdirAll(destinationRoot, 0o755); err != nil {
		return "", err
	}
	for _, file := range zr.File {
		cleanName := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(file.Name)), "./")
		if cleanName != appRoot && !strings.HasPrefix(cleanName, appRoot+"/") {
			continue
		}
		if strings.HasPrefix(cleanName, "../") || filepath.IsAbs(cleanName) {
			return "", fmt.Errorf("更新包包含非法路径 %q", file.Name)
		}
		destinationPath := filepath.Join(destinationRoot, filepath.FromSlash(cleanName))
		relativePath, err := filepath.Rel(destinationRoot, destinationPath)
		if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("更新包包含非法路径 %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destinationPath, file.Mode()); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return "", err
		}
		reader, err := file.Open()
		if err != nil {
			return "", err
		}
		writer, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			reader.Close()
			return "", err
		}
		_, copyErr := io.Copy(writer, reader)
		closeErr := writer.Close()
		reader.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if strings.HasPrefix(cleanName, appRoot+"/Contents/MacOS/") {
			if err := os.Chmod(destinationPath, 0o755); err != nil {
				return "", err
			}
		}
	}

	bundlePath := filepath.Join(destinationRoot, appRoot)
	if info, err := os.Stat(bundlePath); err != nil || !info.IsDir() {
		return "", fmt.Errorf("更新包中的 macOS 应用包无效")
	}
	return bundlePath, nil
}

func guiExeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// replaceExecutableFile 用新文件替换正在运行的可执行文件。
// Windows 下运行中的 exe 不能直接覆盖，先改名备份再替换。
func replaceExecutableFile(srcBin, destBin string) error {
	if srcFi, err := os.Stat(srcBin); err == nil {
		if dstFi, err := os.Stat(destBin); err == nil {
			if os.SameFile(srcFi, dstFi) {
				return nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(destBin), 0o755); err != nil {
		return err
	}

	// 将当前程序改名为 .old 备份（Windows 上运行中的文件允许改名）
	if _, err := os.Stat(destBin); err == nil {
		bak := destBin + ".old"
		_ = os.Remove(bak)
		if err := os.Rename(destBin, bak); err != nil {
			return fmt.Errorf("无法备份当前程序 %s: %w", destBin, err)
		}
	}

	// 同文件系统 rename 原子替换；跨卷回退为拷贝
	if err := os.Rename(srcBin, destBin); err != nil {
		if _, copyErr := copyFileForUpdate(srcBin, destBin); copyErr != nil {
			// 尽力恢复旧程序
			bak := destBin + ".old"
			if _, statErr := os.Stat(bak); statErr == nil {
				_ = os.Rename(bak, destBin)
			}
			return fmt.Errorf("替换可执行文件失败: %w", copyErr)
		}
		_ = os.Remove(srcBin)
	}
	return nil
}

func copyFileForUpdate(src, dest string) (int64, error) {
	srcF, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer srcF.Close()
	dstF, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer dstF.Close()
	return io.Copy(dstF, srcF)
}

func chmodExecutable(path string, mode os.FileMode) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, mode)
	}
}
