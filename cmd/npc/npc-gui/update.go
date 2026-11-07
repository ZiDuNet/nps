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
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ehang.io/nps/client"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/install"
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
	return fetchGUIRelease("")
}

func fetchGUIRelease(tag string) (*ghRelease, error) {
	return fetchGUIReleaseWithClient(tag, nil)
}

func fetchGUIReleaseWithClient(tag string, httpClient *http.Client) (*ghRelease, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := updateAPIURL
	if tag != "" {
		if _, err := version.Compare(tag, tag); err != nil {
			return nil, err
		}
		endpoint = strings.TrimSuffix(updateAPIURL, "latest") + "tags/v" + strings.TrimPrefix(tag, "v")
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("获取版本信息失败：GitHub API 限流（HTTP %d），请稍后重试", resp.StatusCode)
		}
		return nil, fmt.Errorf("获取版本信息失败: HTTP %d", resp.StatusCode)
	}
	rl := new(ghRelease)
	if err := json.NewDecoder(resp.Body).Decode(rl); err != nil {
		return nil, err
	}
	if rl.TagName == "" {
		return nil, errors.New("无法解析最新版本号")
	}
	if tag != "" {
		if cmp, err := version.Compare(tag, rl.TagName); err != nil || cmp != 0 {
			return nil, errors.New("Release 与指定升级版本不符")
		}
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
	return releaseAssetChecksumWithClient(rl, assetName, nil)
}

func releaseAssetChecksumWithClient(rl *ghRelease, assetName string, httpClient *http.Client) (string, error) {
	checksums, err := findAsset(rl, "checksums.txt")
	if err != nil {
		return "", fmt.Errorf("Release %s 缺少 checksums.txt: %w", rl.TagName, err)
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, checksums.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := httpClient.Do(req)
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

// registerRemoteUpdateHandler connects the embedded NPC client to the GUI
// updater.  The client package receives the wire-level CLIENT_UPDATE signal
// asynchronously; this handler only performs the GUI-specific download,
// replacement and restart work.  A GUI process has one executable shared by
// all of its saved connections, so updating once restarts the whole GUI.
func (a *App) registerRemoteUpdateHandler() {
	client.ConfigureRemoteUpdater(common.UpdateCapability{Protocol: 2, Kind: "gui", Mode: "desktop", Enabled: true}, func(ctx client.UpdateContext) (bool, error) {
		if a == nil {
			return false, errors.New("GUI 应用不可用，无法执行远程升级")
		}

		a.updateMu.Lock()
		defer a.updateMu.Unlock()

		ctx.Report("checking", ctx.Version, "正在检查 GUI 正式版本")
		rl, err := fetchGUIReleaseWithClient(ctx.Version, ctx.HTTPClient)
		if err != nil {
			return false, err
		}
		available, err := updateAvailable(version.VERSION, rl.TagName)
		if err != nil {
			return false, err
		}
		if !available {
			slog.Info("收到服务端升级请求，但当前 GUI 已是最新版本", "version", version.VERSION)
			return false, nil
		}

		ctx.Report("downloading", rl.TagName, "正在下载 GUI 更新包，现有连接保持运行")
		if err := a.stageGUIRelease(rl, ctx.HTTPClient); err != nil {
			return false, err
		}
		a.pendingUpdate.Result = common.ClientUpdateProgress{JobID: ctx.JobID, Version: rl.TagName, Stage: "restarted"}
		ctx.Report("verifying", rl.TagName, "更新包已校验，即将保存当前运行的连接")
		if err := a.restartAppLocked(func() { ctx.Report("restarting", rl.TagName, "正在退出旧 GUI 并恢复原有连接") }); err != nil {
			return false, err
		}
		return true, nil
	})
	slog.Info("GUI 客户端远程升级器已注册")
}

// DownloadAndInstallUpdate 下载最新版本并热替换当前可执行文件
func (a *App) DownloadAndInstallUpdate() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.downloadAndInstallUpdate()
}

// downloadAndInstallUpdate performs the replacement without taking updateMu.
// Callers that can run concurrently with the frontend must use the exported
// method above or hold updateMu (as the remote-update handler does).
func (a *App) downloadAndInstallUpdate() error {
	rl, err := fetchLatestRelease()
	if err != nil {
		return err
	}
	return a.downloadAndInstallRelease(rl)
}

func (a *App) downloadAndInstallRelease(rl *ghRelease) error {
	return a.stageGUIRelease(rl, nil)
}

func (a *App) stageGUIRelease(rl *ghRelease, httpClient *http.Client) error {
	if rl == nil || rl.TagName == "" {
		return errors.New("Release 信息为空")
	}
	available, err := updateAvailable(version.VERSION, rl.TagName)
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("当前已是最新版本 %s，无需更新", version.VERSION)
	}
	exePath, err := getExecutablePath()
	if err != nil {
		return fmt.Errorf("获取当前程序路径失败: %w", err)
	}
	target := exePath
	if runtime.GOOS == "darwin" {
		bundle, ok := macAppBundlePath(exePath)
		if !ok || strings.Contains(filepath.ToSlash(exePath), "/AppTranslocation/") || strings.HasPrefix(filepath.ToSlash(exePath), "/Volumes/") {
			return errors.New("请先退出并将应用移至可写的 Applications 目录，再手动打开更新；不在只读磁盘映像或临时转位目录内替换")
		}
		target = bundle
	}
	if err := install.PreflightUpdateTarget(target, guiUpdateReceiptPath()); err != nil {
		return err
	}

	assetName := guiAssetName()
	asset, err := findAsset(rl, assetName)
	if err != nil {
		return err
	}
	checksum, err := releaseAssetChecksumWithClient(rl, assetName, httpClient)
	if err != nil {
		return err
	}

	slog.Info("开始下载更新包", "url", asset.BrowserDownloadURL)

	tempParent := ""
	if runtime.GOOS == "darwin" {
		if appBundle, ok := macAppBundlePath(exePath); ok {
			tempParent = filepath.Dir(appBundle)
		}
	}
	tempDir, err := install.CreatePrivateUpdateDirectory(tempParent, ".npc-gui-update-*")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tempDir)
		}
	}()

	zipPath := filepath.Join(tempDir, assetName)
	if err := downloadFileWithClient(asset.BrowserDownloadURL, zipPath, checksum, httpClient); err != nil {
		return err
	}

	plan := install.RestartPlan{Target: exePath, Version: rl.TagName, ParentPID: os.Getpid(), Receipt: guiUpdateReceiptPath(), RequireStartup: true}
	plan.WorkingDir, _ = os.Getwd()
	if runtime.GOOS == "darwin" {
		bundle, ok := macAppBundlePath(exePath)
		if !ok {
			return errors.New("当前程序不在可更新的 .app 包中")
		}
		source, err := extractMacAppBundle(zipPath, filepath.Join(tempDir, "bundle"))
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bundle, exePath)
		if err != nil {
			return err
		}
		if err = install.VerifyNpcBinary(filepath.Join(source, rel), rl.TagName); err != nil {
			return err
		}
		plan.Target, plan.Source, plan.MacBundle = bundle, source, bundle
		plan.BundleExecutable = rel
	} else {
		source := filepath.Join(tempDir, "npc-gui"+guiExeExt())
		if err := extractGUIExecutable(zipPath, source, filepath.Base(exePath)); err != nil {
			return err
		}
		if err := install.VerifyNpcBinary(source, rl.TagName); err != nil {
			return err
		}
		plan.Source = source
	}
	if a.pendingUpdateDir != "" {
		_ = os.RemoveAll(a.pendingUpdateDir)
	}
	a.pendingUpdate = &plan
	a.pendingUpdateDir = tempDir
	keep = true
	slog.Info("GUI 更新包已校验，等待安全重启")
	return nil
}

// RestartApp 重启当前程序（更新完成后调用）
func (a *App) RestartApp() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.restartAppLocked(func() {})
}

func downloadFile(url, dest, checksum string) error {
	return downloadFileWithClient(url, dest, checksum, nil)
}

func downloadFileWithClient(url, dest, checksum string, httpClient *http.Client) error {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "npc-gui-updater")
	resp, err := httpClient.Do(req)
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
// Windows 下运行中的 exe 不能直接覆盖，先改名备份再替换。Unix 可以
// 原子替换正在执行的 inode，不应创建 .old，否则重启时可能再次执行备份。
func replaceExecutableFile(srcBin, destBin string) error {
	destBin = normalizeExecutablePath(destBin)
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

	// 将当前程序改名为 .old 备份（Windows 上运行中的文件允许改名）。
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(destBin); err == nil {
			bak := destBin + ".old"
			_ = os.Remove(bak)
			if err := os.Rename(destBin, bak); err != nil {
				return fmt.Errorf("无法备份当前程序 %s: %w", destBin, err)
			}
		}
	}

	// 同文件系统 rename 原子替换；跨卷回退为拷贝
	if err := os.Rename(srcBin, destBin); err != nil {
		if copyErr := copyFileForUpdateAtomic(srcBin, destBin); copyErr != nil {
			// 尽力恢复旧程序
			if runtime.GOOS == "windows" {
				bak := destBin + ".old"
				if _, statErr := os.Stat(bak); statErr == nil {
					_ = os.Rename(bak, destBin)
				}
			}
			return fmt.Errorf("替换可执行文件失败: %w", copyErr)
		}
		_ = os.Remove(srcBin)
	}
	return nil
}

func copyFileForUpdateAtomic(src, dest string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	srcF, err := os.Open(src)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, srcF); err != nil {
		_ = srcF.Close()
		_ = tmp.Close()
		return err
	}
	if info, err := os.Stat(src); err == nil {
		if err := tmp.Chmod(info.Mode().Perm()); err != nil {
			_ = srcF.Close()
			_ = tmp.Close()
			return err
		}
	}
	if err := srcF.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func chmodExecutable(path string, mode os.FileMode) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, mode)
	}
}
