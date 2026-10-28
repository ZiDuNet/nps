package controllers

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
)

const (
	usageGuideModeDisabled = "disabled"
	usageGuideModeURL      = "url"
	usageGuideModeFile     = "file"
	usageGuideModeHTML     = "html"

	usageGuideHTMLLocalPath = "conf/qa.html"
	usageGuideMaxBytes      = 8 << 20
)

type usageGuideUpdate struct {
	Mode     string
	Source   string
	rollback func()
}

// prepareUsageGuideUpdate detects the administrator's input without asking
// them to choose a storage mode. URLs and paths are stored in global.json;
// pasted HTML is written to conf/qa.html so the help page can be updated
// independently of a binary release.
func prepareUsageGuideUpdate(raw string) (*usageGuideUpdate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &usageGuideUpdate{Mode: usageGuideModeDisabled}, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, errors.New("使用说明 URL 无效，只支持 http:// 或 https:// 地址")
		}
		if strings.ContainsAny(parsed.Host, "\r\n\t ") {
			return nil, errors.New("使用说明 URL 包含无效字符")
		}
		return &usageGuideUpdate{Mode: usageGuideModeURL, Source: parsed.String()}, nil
	}
	if strings.HasPrefix(raw, "<") {
		if len(raw) > usageGuideMaxBytes {
			return nil, fmt.Errorf("粘贴的 HTML 不能超过 %d MiB", usageGuideMaxBytes>>20)
		}
		path, err := resolveUsageGuidePath(usageGuideHTMLLocalPath)
		if err != nil {
			return nil, err
		}
		rollback, err := writeUsageGuideHTML(path, []byte(raw))
		if err != nil {
			return nil, err
		}
		return &usageGuideUpdate{
			Mode:     usageGuideModeHTML,
			Source:   usageGuideHTMLLocalPath,
			rollback: rollback,
		}, nil
	}
	if _, err := resolveUsageGuidePath(raw); err != nil {
		return nil, err
	}
	return &usageGuideUpdate{Mode: usageGuideModeFile, Source: normalizeUsageGuideSource(raw)}, nil
}

func normalizeUsageGuideSource(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.TrimPrefix(raw, "/")
	raw = strings.TrimPrefix(raw, "conf/")
	return "conf/" + filepath.ToSlash(filepath.Clean(raw))
}

// resolveUsageGuidePath restricts help files to HTML documents beneath the
// active conf directory. This prevents an ordinary authenticated user from
// using the embedded viewer to read nps.conf, credentials, or a symlink target
// outside the configuration tree.
func resolveUsageGuidePath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", errors.New("使用说明文件路径不能为空")
	}
	if strings.Contains(raw, "\x00") {
		return "", errors.New("使用说明文件路径无效")
	}
	if strings.HasPrefix(raw, "/") {
		// Keep accepting the documented /conf/... form, but do not silently
		// turn arbitrary absolute paths into paths below the conf directory.
		if !strings.HasPrefix(raw, "/conf/") {
			return "", errors.New("使用说明文件必须位于 NPS 运行目录的 conf 下")
		}
		raw = strings.TrimPrefix(raw, "/")
	}
	if strings.HasPrefix(raw, "conf/") {
		raw = strings.TrimPrefix(raw, "conf/")
	}
	if filepath.IsAbs(filepath.FromSlash(raw)) {
		return "", errors.New("使用说明文件必须位于 NPS 运行目录的 conf 下")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("使用说明文件不能离开 conf 目录")
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".html", ".htm":
	default:
		return "", errors.New("使用说明文件必须是 .html 或 .htm")
	}
	confRoot, err := filepath.Abs(filepath.Join(common.GetRunPath(), "conf"))
	if err != nil {
		return "", fmt.Errorf("解析 conf 目录失败: %w", err)
	}
	target, err := filepath.Abs(filepath.Join(confRoot, clean))
	if err != nil {
		return "", fmt.Errorf("解析使用说明文件失败: %w", err)
	}
	if err := ensurePathWithin(confRoot, target); err != nil {
		return "", err
	}
	// Existing symlinks must not turn an otherwise harmless path into an
	// arbitrary file reader. Missing targets remain valid so an administrator
	// can configure a path before deploying the document.
	if err := ensureResolvedPathWithin(confRoot, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensurePathWithin(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("使用说明文件不能离开 conf 目录")
	}
	return nil
}

func ensureResolvedPathWithin(root, target string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			resolvedRoot = root
		} else {
			return fmt.Errorf("解析 conf 目录失败: %w", err)
		}
	}
	candidate := target
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return ensurePathWithin(resolvedRoot, resolved)
		}
		if !os.IsNotExist(resolveErr) {
			return fmt.Errorf("解析使用说明文件失败: %w", resolveErr)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	return nil
}

func writeUsageGuideHTML(path string, content []byte) (func(), error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, fmt.Errorf("创建使用说明目录失败: %w", err)
	}
	previous, previousErr := os.ReadFile(path)
	previousExisted := previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		return nil, fmt.Errorf("读取现有使用说明失败: %w", previousErr)
	}
	temporary, err := os.CreateTemp(directory, ".qa-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("写入使用说明失败: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("写入使用说明失败: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("写入使用说明失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("写入使用说明失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("写入使用说明失败: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return nil, fmt.Errorf("替换使用说明失败: %w", err)
	}
	return func() {
		if previousExisted {
			_ = os.WriteFile(path, previous, 0644)
			return
		}
		_ = os.Remove(path)
	}, nil
}

func readUsageGuideHTML(global *file.Glob) ([]byte, string, error) {
	if global == nil {
		return nil, "", errors.New("使用说明尚未配置")
	}
	mode := strings.ToLower(strings.TrimSpace(global.UsageGuideMode))
	if mode != usageGuideModeFile && mode != usageGuideModeHTML {
		return nil, "", errors.New("当前使用说明不是本地 HTML 文件")
	}
	path, err := resolveUsageGuidePath(global.UsageGuideSource)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, errors.New("使用说明文件不存在")
		}
		return nil, path, fmt.Errorf("读取使用说明失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, path, errors.New("使用说明不是普通 HTML 文件")
	}
	if info.Size() > usageGuideMaxBytes {
		return nil, path, fmt.Errorf("使用说明文件不能超过 %d MiB", usageGuideMaxBytes>>20)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("读取使用说明失败: %w", err)
	}
	return content, path, nil
}

func usageGuideEditorValue(global *file.Glob) (string, string) {
	if global == nil {
		return "", ""
	}
	mode := strings.ToLower(strings.TrimSpace(global.UsageGuideMode))
	source := strings.TrimSpace(global.UsageGuideSource)
	if mode != usageGuideModeHTML {
		return source, ""
	}
	content, _, err := readUsageGuideHTML(global)
	if err != nil {
		return "", err.Error()
	}
	return string(content), ""
}

func trustedUsageGuideURL(raw string) (template.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("使用说明 URL 无效")
	}
	return template.URL(parsed.String()), nil
}

func usageGuideContentHeaders(header http.Header) {
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	// The embedded document is untrusted administrator content. The sandbox
	// directive forces an opaque origin even if a browser ignores the iframe's
	// sandbox attribute, so it cannot read console cookies or parent DOM data.
	header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; frame-ancestors 'self'; default-src 'self' https: http: data: blob: 'unsafe-inline' 'unsafe-eval'")
}
