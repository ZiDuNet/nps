package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	npsconf "ehang.io/nps/conf"
)

const serverConfigSourceMaxBytes = 1 << 20

var errServerConfigSourceConflict = errors.New("配置文件已被其他窗口修改，请重新加载源码后再保存")

func serverConfigSourceRevision(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func readServerConfigSource(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("配置文件不是普通文件: %s", path)
	}
	if info.Size() > serverConfigSourceMaxBytes {
		return nil, fmt.Errorf("配置文件超过 %d KiB，无法在源码视图中编辑", serverConfigSourceMaxBytes/1024)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) > serverConfigSourceMaxBytes {
		return nil, fmt.Errorf("配置文件超过 %d KiB，无法在源码视图中编辑", serverConfigSourceMaxBytes/1024)
	}
	return content, nil
}

func validateServerConfigSource(content string) error {
	if len(content) > serverConfigSourceMaxBytes {
		return fmt.Errorf("配置文件不能超过 %d KiB", serverConfigSourceMaxBytes/1024)
	}
	if !utf8.ValidString(content) {
		return errors.New("配置文件必须是有效的 UTF-8 文本")
	}
	if strings.ContainsRune(content, '\x00') {
		return errors.New("配置文件不能包含 NUL 字符")
	}

	defaults := serverConfigKeyValues(npsconf.DefaultNpsConf)
	values := make(map[string]string)
	seen := make(map[string]struct{})
	section := "default"
	keyCount := 0
	for index, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		lineNumber := index + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || len(strings.TrimSpace(line[1:len(line)-1])) == 0 {
				return fmt.Errorf("第 %d 行配置节格式无效", lineNumber)
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("第 %d 行缺少等号，应为 key=value", lineNumber)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" || strings.ContainsAny(key, " \t[]") {
			return fmt.Errorf("第 %d 行的配置键无效", lineNumber)
		}
		normalizedKey := strings.ToLower(key)
		identity := section + "\x00" + normalizedKey
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("第 %d 行重复配置 %s", lineNumber, key)
		}
		seen[identity] = struct{}{}
		values[normalizedKey] = value
		keyCount++
		if meta, ok := serverConfigMetaByKey[normalizedKey]; ok {
			meta = serverConfigMetaFor(normalizedKey, defaults[normalizedKey])
			meta.Editable = true
			if err := validateServerConfigValue(normalizedKey, meta, value); err != nil {
				return fmt.Errorf("第 %d 行：%w", lineNumber, err)
			}
		}
	}
	if keyCount == 0 {
		return errors.New("配置源码中没有任何 key=value 配置项")
	}
	if err := validateServerConfigUpdateSet(values, nil); err != nil {
		return err
	}
	return nil
}

func saveServerConfigSource(path, content, expectedRevision string) (bool, string, error) {
	current, err := readServerConfigSource(path)
	if err != nil {
		return false, "", err
	}
	currentRevision := serverConfigSourceRevision(current)
	if strings.TrimSpace(expectedRevision) == "" || expectedRevision != currentRevision {
		return false, currentRevision, errServerConfigSourceConflict
	}
	if content == string(current) {
		return false, currentRevision, nil
	}
	if err := validateServerConfigSource(content); err != nil {
		return false, currentRevision, err
	}
	if err := writeServerConfigAtomically(path, []byte(content)); err != nil {
		return false, currentRevision, err
	}
	return true, serverConfigSourceRevision([]byte(content)), nil
}

func (s *GlobalController) ConfigSource() {
	if !s.RequireAdmin() {
		return
	}
	setNoStore(s.Ctx)
	content, err := readServerConfigSource(serverConfigPath())
	if err != nil {
		s.configSourceError(http.StatusInternalServerError, "读取 nps.conf 源码失败: "+err.Error())
		return
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":   1,
		"msg":      "ok",
		"path":     serverConfigPath(),
		"content":  string(content),
		"revision": serverConfigSourceRevision(content),
		"size":     len(content),
		"lines":    strings.Count(string(content), "\n") + 1,
	})
	s.ServeJSON()
}

func (s *GlobalController) SaveConfigSource() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	maxFormBytes := int64(serverConfigSourceMaxBytes*3 + 64*1024)
	if s.Ctx.Request.ContentLength > maxFormBytes {
		s.configSourceError(http.StatusRequestEntityTooLarge, "配置源码过大")
		return
	}
	s.Ctx.Request.Body = http.MaxBytesReader(s.Ctx.ResponseWriter, s.Ctx.Request.Body, maxFormBytes)
	if err := s.Ctx.Request.ParseForm(); err != nil {
		s.configSourceError(http.StatusRequestEntityTooLarge, "读取配置源码失败: "+err.Error())
		return
	}
	content := s.GetString("content")
	revision := strings.TrimSpace(s.GetString("revision"))
	changed, nextRevision, err := saveServerConfigSource(serverConfigPath(), content, revision)
	if err != nil {
		if errors.Is(err, errServerConfigSourceConflict) {
			s.configSourceError(http.StatusConflict, err.Error())
			return
		}
		s.configSourceError(http.StatusBadRequest, "保存 nps.conf 源码失败: "+err.Error())
		return
	}
	s.auditMutation("server_config.source_update", "nps.conf", 0, 0, "", nil, map[string]string{
		"changed": fmt.Sprintf("%t", changed),
		"bytes":   fmt.Sprintf("%d", len(content)),
	})
	message := "源码未发生变化，无需重启 NPS"
	if changed {
		message = "配置源码已保存，需要重启 NPS 才会生效"
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":           1,
		"msg":              message,
		"changed":          changed,
		"restart_required": changed,
		"revision":         nextRevision,
		"size":             len(content),
		"lines":            strings.Count(content, "\n") + 1,
	})
	s.ServeJSON()
}

func (s *GlobalController) configSourceError(status int, message string) {
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 0,
		"msg":    message,
	})
	s.ServeJSON()
	s.StopRun()
}
