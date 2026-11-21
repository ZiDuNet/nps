package file

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/version"
)

const (
	jsonGatewaySchema           = "nps-json-gateway-v1"
	jsonGatewayFile             = ".json-gateway"
	jsonGatewayRecordVersionKey = "_NPSGatewayVersion"
	// jsonGatewayMinVersion is the first release that writes the canonical
	// bps/three-state layout. A marker from an older binary is not proof that
	// 0.26.x credentials and flow accounting have been normalized.
	jsonGatewayMinVersion = "1.1.49"
)

// JSONGatewayFiles records which JSON files were normalized. A record file can
// be rewritten in place before a later file fails, so the marker must also
// prevent a retry from applying the KB/s multiplier twice.
type jsonGatewayMarker struct {
	Schema     string   `json:"schema"`
	Version    string   `json:"version"`
	MigratedAt string   `json:"migrated_at"`
	Files      []string `json:"files,omitempty"`
}

// JSONGatewayResult reports what the one-time compatibility pass changed.
// The pass is intentionally explicit: legacy JSON fields cannot be inferred
// safely after a process has already been upgraded to bps semantics.
type JSONGatewayResult struct {
	MarkerPath string
	Clients    int
	Users      int
	Tunnels    int
	Hosts      int
}

const (
	gatewayFileClients = "clients.json"
	gatewayFileUsers   = "users.json"
	gatewayFileTasks   = "tasks.json"
	gatewayFileHosts   = "hosts.json"
	gatewayFileGlobal  = "global.json"
)

// bundledJSONSampleDigests are the exact LF-normalized contents shipped in
// conf/. They are demo records rather than an installation's data. Any edit,
// including appending a record, changes the digest and makes the file migrate.
var bundledJSONSampleDigests = map[string]string{
	gatewayFileClients: "1ebc869822b34c18be336975f03e97e4e09a981de9cea3228c81e22c24c82209",
	gatewayFileTasks:   "f8e5dd5dc818cf5fecdfc0c2e366a2c75d7a9f7a6050d1fb6c0624f897034a92",
	gatewayFileHosts:   "1adc2a35d096622bc5e8b42cd07427bf87a570be1f3be46176ee0af9ddeabbe4",
	gatewayFileGlobal:  "e4026b065d364c80d639a26123c3f82812886f9e414130daba995658aeab9b90",
}

func jsonDataFiles(runPath string) []string {
	confPath := filepath.Join(runPath, "conf")
	return []string{
		filepath.Join(confPath, "clients.json"),
		filepath.Join(confPath, "users.json"),
		filepath.Join(confPath, "tasks.json"),
		filepath.Join(confPath, "hosts.json"),
		filepath.Join(confPath, "global.json"),
	}
}

// JSONDataExists reports whether conf contains data that must pass through the
// JSON gateway. Empty skeletons and the exact sample files bundled with the
// repository do not represent an installation. Unreadable or modified files
// remain data so migration can fail closed instead of silently dropping them.
func JSONDataExists(runPath string) bool {
	for _, path := range jsonDataFiles(runPath) {
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !isPlaceholderJSONData(filepath.Base(path), content) {
			return true
		}
	}
	return false
}

func isPlaceholderJSONData(name string, content []byte) bool {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	if digest, ok := bundledJSONSampleDigests[name]; ok {
		sum := sha256.Sum256(normalized)
		if fmt.Sprintf("%x", sum) == digest {
			return true
		}
	}
	trimmed := bytes.TrimSpace(normalized)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if name == gatewayFileGlobal {
		return bytes.Equal(trimmed, []byte("{}"))
	}
	for _, record := range bytes.Split(normalized, []byte("\n"+common.CONN_DATA_SEQ)) {
		record = bytes.TrimSpace(record)
		if len(record) > 0 && !bytes.Equal(record, []byte("null")) {
			return false
		}
	}
	return true
}

func jsonDataFileContentsExist(runPath string) bool {
	for _, path := range jsonDataFiles(runPath) {
		content, err := os.ReadFile(path)
		if err == nil && !isPlaceholderJSONData(filepath.Base(path), content) {
			return true
		}
	}
	return false
}

func JSONGatewayMarkerPath(runPath string) string {
	return filepath.Join(runPath, "conf", jsonGatewayFile)
}

func HasJSONGatewayMarker(runPath string) bool {
	marker, err := validateJSONGatewayMarker(runPath)
	if err != nil {
		return false
	}
	return gatewayMarkerComplete(runPath, marker)
}

func ReadJSONGatewayMarker(runPath string) (jsonGatewayMarker, error) {
	return readJSONGatewayMarkerPath(JSONGatewayMarkerPath(runPath))
}

// ReadJSONGatewayMarkerFromPath reads a marker that was archived with a conf
// backup. The migration report uses it to preserve the exact gateway source
// version after the original conf/.json-gateway has been renamed.
func ReadJSONGatewayMarkerFromPath(path string) (jsonGatewayMarker, error) {
	return readJSONGatewayMarkerPath(path)
}

func readJSONGatewayMarkerPath(path string) (jsonGatewayMarker, error) {
	var marker jsonGatewayMarker
	content, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	if err := json.Unmarshal(content, &marker); err != nil {
		return marker, err
	}
	if marker.Schema != jsonGatewaySchema {
		return marker, fmt.Errorf("JSON 迁移标记 schema 无效: %q", marker.Schema)
	}
	return marker, nil
}

func validateJSONGatewayMarker(runPath string) (jsonGatewayMarker, error) {
	marker, err := ReadJSONGatewayMarker(runPath)
	if err != nil {
		return marker, err
	}
	if strings.TrimSpace(marker.Version) == "" {
		return marker, errors.New("JSON 迁移标记缺少网关版本")
	}
	comparison, err := version.Compare(marker.Version, jsonGatewayMinVersion)
	if err != nil {
		return marker, fmt.Errorf("JSON 迁移标记版本无效: %q", marker.Version)
	}
	if comparison < 0 {
		return marker, fmt.Errorf("JSON 迁移标记版本 %s 低于网关版最低版本 %s", marker.Version, jsonGatewayMinVersion)
	}
	return marker, nil
}

// gatewayMarkerComplete reports whether every JSON data file still present
// has been recorded. A marker is written after each file is normalized so an
// interrupted run can resume instead of multiplying the KB/s conversion.
func gatewayMarkerComplete(runPath string, marker jsonGatewayMarker) bool {
	for _, path := range jsonDataFiles(runPath) {
		content, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && isPlaceholderJSONData(filepath.Base(path), content)) {
			continue
		}
		if readErr != nil {
			return false
		}
		name := filepath.Base(path)
		if !containsString(marker.Files, name) {
			return false
		}
	}
	return true
}

// RunJSONGateway normalizes legacy NPS JSON data once. It is safe to invoke
// from startup or from the dedicated -json-gateway flag: an existing marker
// means the files already use the canonical bps/three-state representation.
func RunJSONGateway(runPath string) (JSONGatewayResult, error) {
	result := JSONGatewayResult{MarkerPath: JSONGatewayMarkerPath(runPath)}
	if !JSONDataExists(runPath) {
		if err := writeJSONGatewayMarker(result.MarkerPath, normalizedJSONDataFiles(nil)); err != nil {
			return result, err
		}
		return result, nil
	}
	if marker, err := validateJSONGatewayMarker(runPath); err == nil && gatewayMarkerComplete(runPath, marker) {
		return result, nil
	}
	var completed []string
	marker, markerErr := ReadJSONGatewayMarker(runPath)
	if markerErr == nil {
		validated, err := validateJSONGatewayMarker(runPath)
		if err != nil {
			return result, err
		}
		marker = validated
		completed = append(completed, marker.Files...)
		// Records without a file list may have been partially rewritten by an
		// interrupted older gateway. Rewriting them again would multiply the
		// KB/s conversion, so an incomplete legacy marker requires a restart from
		// the pre-gateway backup instead of guessing.
		if len(completed) == 0 && jsonDataFileContentsExist(runPath) {
			return result, errors.New("检测到不完整的 JSON 迁移标记；请从 .pre-gateway.bak 备份恢复后重新运行网关")
		}
	} else if errors.Is(markerErr, os.ErrNotExist) {
		if err := backupJSONDataFiles(runPath); err != nil {
			return result, fmt.Errorf("备份 JSON 数据失败: %w", err)
		}
	} else {
		return result, fmt.Errorf("读取 JSON 迁移标记失败: %w", markerErr)
	}
	if err := normalizeJSONDataFiles(runPath, &result, completed); err != nil {
		return result, err
	}
	if marker, err := validateJSONGatewayMarker(runPath); err == nil && gatewayMarkerComplete(runPath, marker) {
		return result, nil
	}
	if err := writeJSONGatewayMarker(result.MarkerPath, existingJSONDataFileNames(runPath)); err != nil {
		return result, err
	}
	return result, nil
}

func existingJSONDataFileNames(runPath string) []string {
	names := make([]string, 0, len(jsonDataFiles(runPath)))
	for _, path := range jsonDataFiles(runPath) {
		content, err := os.ReadFile(path)
		if err == nil && !isPlaceholderJSONData(filepath.Base(path), content) {
			names = append(names, filepath.Base(path))
		}
	}
	return names
}

func backupJSONDataFiles(runPath string) error {
	for _, path := range jsonDataFiles(runPath) {
		if !common.FileExists(path) {
			continue
		}
		backup := path + ".pre-gateway.bak." + version.VERSION
		if common.FileExists(backup) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeFileAtomically(backup, data); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONGatewayMarker(path string, files []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	marker := jsonGatewayMarker{
		Schema:     jsonGatewaySchema,
		Version:    version.VERSION,
		MigratedAt: time.Now().Format(time.RFC3339),
		Files:      files,
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".json-gateway.*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Rename(tempPath, path)
}

func normalizeJSONDataFiles(runPath string, result *JSONGatewayResult, completed []string) error {
	confPath := filepath.Join(runPath, "conf")
	normalized := normalizedJSONDataFiles(completed)
	normalizeFile := func(name string, convert func(string) (string, bool, error), count *int) error {
		path := filepath.Join(confPath, name)
		if !common.FileExists(path) || containsString(normalized, name) {
			return nil
		}
		if err := normalizeJSONRecords(path, convert, count); err != nil {
			return err
		}
		normalized = append(normalized, name)
		return writeJSONGatewayMarker(JSONGatewayMarkerPath(runPath), normalized)
	}
	if err := normalizeFile(gatewayFileClients, func(raw string) (string, bool, error) {
		client, changed, err := normalizeLegacyClientJSON(raw)
		if err != nil {
			return "", false, err
		}
		if client == nil {
			return "", false, nil
		}
		data, err := json.Marshal(client)
		return string(data), changed, err
	}, &result.Clients); err != nil {
		return fmt.Errorf("迁移客户端 JSON: %w", err)
	}
	if err := normalizeFile(gatewayFileUsers, func(raw string) (string, bool, error) {
		user, changed, err := normalizeLegacyUserJSON(raw)
		if err != nil {
			return "", false, err
		}
		if user == nil {
			return "", false, nil
		}
		data, err := json.Marshal(user)
		return string(data), changed, err
	}, &result.Users); err != nil {
		return fmt.Errorf("迁移用户 JSON: %w", err)
	}
	if err := normalizeFile(gatewayFileTasks, func(raw string) (string, bool, error) {
		var tunnel Tunnel
		if err := json.Unmarshal([]byte(raw), &tunnel); err != nil {
			return "", false, fmt.Errorf("隧道记录不是有效 JSON: %w", err)
		}
		changed := legacyResourceLimitsFromJSON(raw, &tunnel)
		data, err := json.Marshal(&tunnel)
		return string(data), changed, err
	}, &result.Tunnels); err != nil {
		return fmt.Errorf("迁移隧道 JSON: %w", err)
	}
	if err := normalizeFile(gatewayFileHosts, func(raw string) (string, bool, error) {
		var host Host
		if err := json.Unmarshal([]byte(raw), &host); err != nil {
			return "", false, fmt.Errorf("Host 记录不是有效 JSON: %w", err)
		}
		changed := legacyResourceLimitsFromJSON(raw, &host)
		data, err := json.Marshal(&host)
		return string(data), changed, err
	}, &result.Hosts); err != nil {
		return fmt.Errorf("迁移 Host JSON: %w", err)
	}
	globalPath := filepath.Join(confPath, gatewayFileGlobal)
	if common.FileExists(globalPath) && !containsString(normalized, gatewayFileGlobal) {
		content, err := os.ReadFile(globalPath)
		if err != nil {
			return err
		}
		var global Glob
		if err := json.Unmarshal(content, &global); err != nil {
			return fmt.Errorf("全局配置不是有效 JSON: %w", err)
		}
		normalizedRecord, alreadyNormalized, err := checkJSONGatewayRecord(content)
		if err != nil {
			return fmt.Errorf("全局配置不是有效对象: %w", err)
		}
		if !alreadyNormalized {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(content, &raw); err != nil {
				return fmt.Errorf("全局配置不是有效对象: %w", err)
			}
			if value, ok := rawInt64(raw, "RateLimit"); ok {
				global.RateLimit = rate.BpsFromKBPerSecond(value)
			}
			data, err := json.Marshal(&global)
			if err != nil {
				return err
			}
			normalizedRecord, err = stampJSONGatewayRecord(data)
			if err != nil {
				return err
			}
		}
		if err := writeFileAtomically(globalPath, normalizedRecord); err != nil {
			return err
		}
		normalized = append(normalized, gatewayFileGlobal)
		if err := writeJSONGatewayMarker(JSONGatewayMarkerPath(runPath), normalized); err != nil {
			return err
		}
	}
	return nil
}

func normalizedJSONDataFiles(files []string) []string {
	known := []string{
		gatewayFileClients, gatewayFileUsers, gatewayFileTasks,
		gatewayFileHosts, gatewayFileGlobal,
	}
	normalized := make([]string, 0, len(known))
	for _, name := range known {
		if containsString(files, name) {
			normalized = append(normalized, name)
		}
	}
	return normalized
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeJSONRecords(path string, convert func(string) (string, bool, error), count *int) error {
	if !common.FileExists(path) {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	records := strings.Split(string(content), "\n"+common.CONN_DATA_SEQ)
	output := make([]string, 0, len(records))
	for _, record := range records {
		trimmed := strings.TrimSpace(record)
		if trimmed == "" || trimmed == "null" {
			continue
		}
		if _, alreadyNormalized, versionErr := checkJSONGatewayRecord([]byte(record)); versionErr != nil {
			return fmt.Errorf("%s 记录无效: %w", filepath.Base(path), versionErr)
		} else if alreadyNormalized {
			output = append(output, record)
			if count != nil {
				*count = *count + 1
			}
			continue
		}
		converted, changed, convertErr := convert(record)
		if convertErr != nil {
			return convertErr
		}
		if converted == "" {
			continue
		}
		marked, markErr := stampJSONGatewayRecord([]byte(converted))
		if markErr != nil {
			return markErr
		}
		output = append(output, string(marked))
		if count != nil {
			*count = *count + 1
		}
		_ = changed
	}
	return writeFileAtomically(path, []byte(strings.Join(output, "\n"+common.CONN_DATA_SEQ)))
}

func checkJSONGatewayRecord(content []byte) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, false, err
	}
	raw, exists := fields[jsonGatewayRecordVersionKey]
	if !exists {
		return content, false, nil
	}
	var recordedVersion string
	if err := json.Unmarshal(raw, &recordedVersion); err != nil {
		return nil, false, fmt.Errorf("网关记录版本字段无效: %w", err)
	}
	comparison, err := version.Compare(recordedVersion, jsonGatewayMinVersion)
	if err != nil {
		return nil, false, fmt.Errorf("网关记录版本 %q 无效: %w", recordedVersion, err)
	}
	if comparison < 0 {
		return nil, false, fmt.Errorf("网关记录版本 %s 低于最低版本 %s", recordedVersion, jsonGatewayMinVersion)
	}
	return content, true, nil
}

func stampJSONGatewayRecord(content []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, err
	}
	recordedVersion, err := json.Marshal(jsonGatewayMinVersion)
	if err != nil {
		return nil, err
	}
	fields[jsonGatewayRecordVersionKey] = recordedVersion
	return json.Marshal(fields)
}

func normalizeLegacyUserJSON(raw string) (*User, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, false, fmt.Errorf("用户记录不是有效 JSON: %w", err)
	}
	var user User
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return nil, false, fmt.Errorf("用户记录字段无效: %w", err)
	}
	changed := false
	if _, exists := fields["MaxClientNum"]; exists {
		if value, ok := rawInt64(fields, "MaxClientNum"); ok {
			if value <= 0 {
				user.MaxClientNum = nil
				changed = true
			}
		}
	}
	if _, exists := fields["MaxTunnelNum"]; exists {
		if value, ok := rawInt64(fields, "MaxTunnelNum"); ok {
			if value <= 0 {
				user.MaxTunnelNum = nil
				changed = true
			}
		}
	}
	if _, exists := fields["MaxHostNum"]; !exists {
		user.MaxHostNum = nil
	}
	if _, exists := fields["MaxConn"]; exists {
		if value, ok := rawInt64(fields, "MaxConn"); ok {
			if value <= 0 {
				user.MaxConn = nil
				changed = true
			}
		}
	}
	upload, hasUpload := rawInt64(fields, "UploadLimit")
	download, hasDownload := rawInt64(fields, "DownloadLimit")
	if _, canonical := fields["BandwidthLimit"]; !canonical && (hasUpload || hasDownload) {
		user.BandwidthLimit = legacyDirectionalBandwidth(upload, hasUpload, download, hasDownload)
		changed = true
	}
	return &user, changed, nil
}

func legacyDirectionalBandwidth(upload int64, hasUpload bool, download int64, hasDownload bool) *int64 {
	if !hasUpload && !hasDownload {
		return nil
	}
	if (hasUpload && upload <= 0) || (hasDownload && download <= 0) {
		return nil
	}
	total := upload + download
	if total <= 0 {
		return nil
	}
	return LimitInt64(rate.BpsFromKBPerSecond(total))
}

func normalizeLegacyClientJSON(raw string) (*Client, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, false, fmt.Errorf("客户端记录不是有效 JSON: %w", err)
	}
	var client Client
	if err := json.Unmarshal([]byte(raw), &client); err != nil {
		return nil, false, fmt.Errorf("客户端记录字段无效: %w", err)
	}
	changed := false
	if value, ok := rawInt64(fields, "RateLimit"); ok && value > 0 {
		client.RateLimit = rate.BpsFromKBPerSecond(value)
		changed = true
	}
	return &client, changed, nil
}

func legacyResourceLimitsFromJSON(raw string, target interface{}) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return false
	}
	upload, hasUpload := rawInt64(fields, "UploadLimit")
	download, hasDownload := rawInt64(fields, "DownloadLimit")
	if !hasUpload && !hasDownload {
		return false
	}
	var uploadBps, downloadBps int64
	if hasUpload && upload > 0 {
		uploadBps = rate.BpsFromKBPerSecond(upload)
	}
	if hasDownload && download > 0 {
		downloadBps = rate.BpsFromKBPerSecond(download)
	}
	switch value := target.(type) {
	case *Tunnel:
		value.SetRateLimits(uploadBps, downloadBps)
	case *Host:
		value.SetRateLimits(uploadBps, downloadBps)
	default:
		return false
	}
	return true
}

func rawInt64(fields map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := fields[key]
	if !ok {
		return 0, false
	}
	if normalized := strings.TrimSpace(string(raw)); normalized == "" || normalized == "null" {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		var number float64
		if json.Unmarshal(raw, &number) != nil {
			return 0, false
		}
		return int64(number), true
	}
	return value, true
}

func writeFileAtomically(path string, content []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// EnsureGatewayForSQLite enforces the documented upgrade path. SQLite import
// must never accept raw 0.26.x JSON silently.
func EnsureGatewayForSQLite(runPath string) error {
	if !JSONDataExists(runPath) {
		return nil
	}
	marker, err := validateJSONGatewayMarker(runPath)
	if err != nil {
		return fmt.Errorf("未检测到可用的 JSON 网关迁移标记（conf/.json-gateway）：%v。请先启动 JSON 网关版完成数据整形，再升级到 SQLite 版本；不要直接导入旧 JSON", err)
	}
	// The marker is written after each file, so a partial run must not be
	// accepted even when the named files exist. Optional files (notably
	// users.json in legacy layouts) are absent from both the directory and the
	// list; the ownership check below decides whether that is safe.
	if !gatewayMarkerComplete(runPath, marker) {
		return errors.New("JSON 网关迁移标记未覆盖全部现有数据文件，请先重新运行 JSON 网关版完成整形，再升级到 SQLite 版本")
	}
	if !common.FileExists(filepath.Join(runPath, "conf", "users.json")) && jsonDataUsesLegacyClientCredentials(runPath) {
		return errors.New("缺少 users.json，但客户端仍包含 WebUserName/WebPassword 或 UserId；请先启动 JSON 网关版完成用户归属修复")
	}
	return nil
}

func jsonDataUsesLegacyClientCredentials(runPath string) bool {
	content, err := os.ReadFile(filepath.Join(runPath, "conf", "clients.json"))
	if err != nil {
		return false
	}
	for _, record := range strings.Split(string(content), "\n"+common.CONN_DATA_SEQ) {
		record = strings.TrimSpace(record)
		if record == "" || record == "null" {
			continue
		}
		var client Client
		if err := json.Unmarshal([]byte(record), &client); err != nil {
			continue
		}
		if strings.TrimSpace(client.WebUserName) != "" || strings.TrimSpace(client.WebPassword) != "" || client.UserId != 0 {
			return true
		}
	}
	return false
}
