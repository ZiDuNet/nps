package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/store"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/config"
	"github.com/astaxie/beego/logs"
)

// Manager owns the runtime-editable business configuration. The values in
// this package are authoritative after startup: nps.conf is only the bootstrap
// source for listener, TLS and other process-lifetime settings.
type Manager struct {
	mu       sync.RWMutex
	store    *store.Store
	confPath string
	runPath  string
	values   map[string]string
}

var (
	activeMu sync.RWMutex
	active   *Manager
)

const startupConfigImportedKey = "startup_settings_imported"

// StartupConfigImported distinguishes settings that have already moved to
// SQLite from an old store that has not yet initialised its settings.
func StartupConfigImported(meta map[string]string) bool {
	return meta[startupConfigImportedKey] == "1"
}

// managedKeys are the business keys stored in SQLite. Keep this list explicit:
// treating every unknown key as managed would silently move third-party
// deployment settings out of the file and break rollback.
var managedKeys = map[string]struct{}{
	"allow_user_login":                     {},
	"allow_user_vkey_login":                {},
	"allow_user_register":                  {},
	"open_captcha":                         {},
	"register_captcha":                     {},
	"register_email_verify":                {},
	"register_max_clients":                 {},
	"register_max_tunnels":                 {},
	"register_max_hosts":                   {},
	"register_max_conn":                    {},
	"register_bandwidth_limit":             {},
	"register_upload_limit":                {},
	"register_download_limit":              {},
	"register_expire_days":                 {},
	"register_rate_window":                 {},
	"register_rate_limit":                  {},
	"register_ip_rate_limit":               {},
	"register_email_code_ttl":              {},
	"register_email_code_cooldown":         {},
	"smtp_host":                            {},
	"smtp_port":                            {},
	"smtp_security":                        {},
	"smtp_username":                        {},
	"smtp_password":                        {},
	"smtp_from":                            {},
	"allow_user_change_username":           {},
	"allow_user_local":                     {},
	"allow_secret_link":                    {},
	"allow_secret_local":                   {},
	"allow_remote_update":                  {},
	"allow_time_limit":                     {},
	"allow_x_real_ip":                      {},
	"trusted_proxy_ips":                    {},
	"flow_limit_type":                      {},
	"disconnect_timeout":                   {},
	"acl_max_rules":                        {},
	"geoip_path":                           {},
	"geosite_path":                         {},
	"geo_reload_interval":                  {},
	"dns_server":                           {},
	"ntp_server":                           {},
	"ntp_interval":                         {},
	"timezone":                             {},
	"allow_local_proxy":                    {},
	"allow_multi_ip":                       {},
	"system_info_display":                  {},
	"http_add_origin_header":               {},
	"http_forwarded_header_mode":           {},
	"http_forwarded_trust_mode":            {},
	"http_cache":                           {},
	"http_cache_length":                    {},
	"http_proxy_response_timeout":          {},
	"http_client_upload_stall_timeout":     {},
	"http_client_response_stall_timeout":   {},
	"http_client_upload_total_timeout":     {},
	"http_client_response_total_timeout":   {},
	"http_backend_connect_timeout":         {},
	"http_backend_tls_handshake_timeout":   {},
	"http_backend_request_start_timeout":   {},
	"http_backend_upload_stall_timeout":    {},
	"http_backend_read_stall_timeout":      {},
	"http_backend_response_total_timeout":  {},
	"http_backend_keepalive":               {},
	"http_backend_max_idle_conns":          {},
	"http_backend_max_idle_conns_per_host": {},
	"http_backend_idle_conn_timeout":       {},
	"http_backend_expect_continue_timeout": {},
	"http_backend_max_pools":               {},
	"usage_history_enabled":                {},
	"usage_history_path":                   {},
	"usage_history_interval":               {},
	"usage_history_1m_hours":               {},
	"usage_history_5m_days":                {},
	"usage_history_save_every":             {},
	"usage_history_max_resources":          {},
	"client_history_limit":                 {},
	"show_http_proxy_port":                 {},
	"http_proxy_access_port":               {},
	"https_proxy_access_port":              {},
	"mcp_enabled":                          {},
	"api_hmac_enabled":                     {},
	"api_hmac_time_window":                 {},
	"api_legacy_md5_enabled":               {},
	"allow_ports":                          {},
	"flow_store_interval":                  {},
	"audit_log_path":                       {},
	"connection_log_path":                  {},
	"login_ban_time":                       {},
	"login_ip_ban_time":                    {},
	"login_user_ban_time":                  {},
	"login_max_fail_times":                 {},
	"login_max_body":                       {},
	"login_max_skew":                       {},
	"force_pow":                            {},
	"pow_bits":                             {},
	"totp_secret":                          {},
	"public_register_key":                  {},
	"public_vkey":                          {},
	"ip_limit":                             {},
	"ip_register_max_hours":                {},
	"auth_time_window":                     {},
	"x_nps_http_only":                      {},
}

// legacyRemovedKeys are feature gates from the old client-level quota model.
// Their limits are now always enforced, so keeping any value would be a
// misleading switch. They are deleted from nps.conf during the same migration
// that moves business settings to SQLite, including deployments that upgrade
// from a configuration file which still contains them.
var legacyRemovedKeys = map[string]struct{}{
	"allow_flow_limit":           {},
	"allow_rate_limit":           {},
	"allow_tunnel_num_limit":     {},
	"allow_connection_num_limit": {},
}

// IsManagedKey reports whether a key belongs to the runtime business settings
// table rather than nps.conf.
func IsManagedKey(key string) bool {
	_, ok := managedKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

// IsLegacyRemovedKey reports whether a configuration key is obsolete and must
// not be displayed or re-added by the configuration migration.
func IsLegacyRemovedKey(key string) bool {
	_, ok := legacyRemovedKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

// ManagedKeySet returns a defensive copy for migration and tests.
func ManagedKeySet() map[string]struct{} {
	keys := make(map[string]struct{}, len(managedKeys))
	for key := range managedKeys {
		keys[key] = struct{}{}
	}
	return keys
}

// Init initializes settings for callers that manage their own storage.
// Production startup uses ImportStartupConfig before publishing its database.
func Init(runPath string, sqlite *store.Store) error {
	confPath := filepath.Join(runPath, "conf", "nps.conf")
	ini, err := ReadStartupConfig(confPath, "")
	if err != nil {
		return err
	}
	values, err := ImportStartupConfig(sqlite, ini.Values)
	if err != nil {
		return err
	}
	manager := &Manager{store: sqlite, confPath: confPath, runPath: runPath, values: values}
	if err := manager.removeManagedKeysFromFile(ini); err != nil {
		return err
	}
	setActive(manager)
	manager.Apply()
	return nil
}

// ImportStartupConfig writes absent business settings once and reads them back.
// On first startup the caller uses an unpublished database, so a failure
// cannot leave an empty or partially migrated nps.db in the live directory.
func ImportStartupConfig(sqlite *store.Store, fileValues map[string]string) (map[string]string, error) {
	if sqlite == nil {
		return nil, errors.New("settings store is unavailable")
	}
	meta, err := sqlite.LoadMeta()
	if err != nil {
		return nil, fmt.Errorf("读取 SQLite 业务配置迁移状态: %w", err)
	}
	values, err := sqlite.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("读取 SQLite 业务配置: %w", err)
	}
	if StartupConfigImported(meta) {
		return values, nil
	}

	imported := make(map[string]string)
	for key, value := range fileValues {
		if !IsManagedKey(key) {
			continue
		}
		if _, exists := values[key]; exists {
			continue
		}
		// Older nps.conf files used zero for every registration quota to mean
		// "unlimited". The current three-state contract reserves zero for
		// "not included", so normalize that historical convention exactly once
		// while importing. Later explicit zeros saved from the panel are kept.
		if isLegacyUnlimitedRegistrationQuota(key, value) {
			value = ""
		}
		// Historically the packaged default public VKey was shared across
		// installations. Preserve startup's existing disable-on-import rule.
		if key == "public_vkey" && value == "123" {
			value = ""
		}
		imported[key] = value
	}
	if len(imported) > 0 {
		if err := sqlite.UpsertSettings(imported); err != nil {
			return nil, fmt.Errorf("迁移 nps.conf 业务配置到 SQLite: %w", err)
		}
		values, err = sqlite.LoadSettings()
		if err != nil {
			return nil, fmt.Errorf("核对已写入的业务配置: %w", err)
		}
		for key, expected := range imported {
			if actual, ok := values[key]; !ok || actual != expected {
				return nil, fmt.Errorf("SQLite 业务配置 %s 写入后校验不一致", key)
			}
		}
	}
	if err := sqlite.SetMeta(startupConfigImportedKey, "1"); err != nil {
		return nil, fmt.Errorf("记录 SQLite 业务配置已导入: %w", err)
	}
	return values, nil
}

// Activate applies the settings after Beego has loaded the retained boot
// configuration. It does not read or rewrite nps.conf.
func Activate(runPath string, sqlite *store.Store, values map[string]string) {
	manager := &Manager{store: sqlite, runPath: runPath, values: values}
	setActive(manager)
	manager.Apply()
}

func isLegacyUnlimitedRegistrationQuota(key, value string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "register_max_clients", "register_max_tunnels", "register_max_hosts",
		"register_max_conn", "register_bandwidth_limit",
		"register_upload_limit", "register_download_limit":
		return strings.TrimSpace(value) == "0"
	default:
		return false
	}
}

// Snapshot returns a defensive copy of the current effective settings.
func Snapshot() map[string]string {
	manager := current()
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	values := make(map[string]string, len(manager.values))
	for key, value := range manager.values {
		values[key] = value
	}
	return values
}

// Enabled reports whether the settings manager has been initialized for this
// run path. Callers that also work in JSON-only/test mode use it to decide
// whether a business key can be moved out of nps.conf without losing it.
func Enabled(runPath string) bool {
	manager := current()
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return filepath.Clean(manager.runPath) == filepath.Clean(runPath)
}

// ManagedValues returns the current settings in INI form. It lets the Web
// configuration view render business keys that no longer exist in nps.conf,
// and keeps that rendering identical to the values injected into Beego.
func ManagedValues() string {
	values := Snapshot()
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(values[key])
		builder.WriteByte('\n')
	}
	return builder.String()
}

// Get returns one effective business setting.
func Get(key string) (string, bool) {
	manager := current()
	if manager == nil {
		return "", false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	value, ok := manager.values[strings.ToLower(strings.TrimSpace(key))]
	return value, ok
}

// Save commits a related group of business settings and injects them into the
// running process. Empty values are stored as explicit empty strings so an
// optional feature can be disabled without deleting the row; Delete removes a
// key entirely when a page omits an optional field.
func Save(values map[string]string, deleteKeys []string) error {
	manager := current()
	if manager == nil {
		return errors.New("settings manager is not initialized")
	}
	if len(values) == 0 && len(deleteKeys) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.ToLower(strings.TrimSpace(key))
		if !IsManagedKey(key) {
			return fmt.Errorf("配置 %s 属于启动参数，不能写入 SQLite 业务配置", key)
		}
		normalized[key] = strings.TrimSpace(value)
	}
	deleted := make([]string, 0, len(deleteKeys))
	for _, key := range deleteKeys {
		key = strings.ToLower(strings.TrimSpace(key))
		if !IsManagedKey(key) {
			return fmt.Errorf("配置 %s 属于启动参数，不能从 SQLite 业务配置删除", key)
		}
		deleted = append(deleted, key)
	}
	if err := manager.store.ReplaceSettings(normalized, deleted); err != nil {
		return err
	}
	manager.mu.Lock()
	for key, value := range normalized {
		manager.values[key] = value
	}
	for _, key := range deleted {
		delete(manager.values, key)
	}
	manager.mu.Unlock()
	manager.Apply()
	return nil
}

// Apply copies all effective business values into Beego's AppConfig. Beego is
// retained as the read interface so the hundreds of existing call sites do not
// need a risky repository-wide rewrite.
func (m *Manager) Apply() {
	if m == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for key, value := range m.values {
		if err := beego.AppConfig.Set(key, value); err != nil {
			logs.Warn("apply setting %s failed: %v", key, err)
		}
	}
	// Beego keeps values in process memory. If an optional setting was deleted
	// from the settings table, a stale AppConfig value would otherwise remain
	// effective until restart. Explicitly clear every managed key that is no
	// longer present.
	for key := range managedKeys {
		if _, exists := m.values[key]; exists {
			continue
		}
		if err := beego.AppConfig.Set(key, ""); err != nil {
			logs.Warn("clear setting %s failed: %v", key, err)
		}
	}
}

func (m *Manager) removeManagedKeysFromFile(ini StartupINI) error {
	filtered, changed := SlimStartupConfig(ini.Content, m.values, ini.RunMode)
	if !changed {
		return nil
	}
	backup := m.confPath + ".pre-settings.bak." + version.VERSION + "." + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, []byte(ini.Content), 0o600); err != nil {
		return fmt.Errorf("备份 nps.conf 失败: %w", err)
	}
	if err := writeFileAtomically(m.confPath, []byte(filtered)); err != nil {
		return fmt.Errorf("从 nps.conf 移除已迁移业务配置失败: %w", err)
	}
	logs.Info("removed migrated business settings from nps.conf; backup: %s", backup)
	return nil
}

func current() *Manager {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

func setActive(manager *Manager) {
	activeMu.Lock()
	active = manager
	activeMu.Unlock()
}

// ResetForTest clears the package singleton. Tests that exercise startup
// migration need to start from a clean manager.
func ResetForTest() {
	activeMu.Lock()
	active = nil
	activeMu.Unlock()
}

// StartupINI contains the main file and its parsed, effective configuration.
// RunMode is passed to the rewriter so it never removes inactive sections
// based on a second, different interpretation of the original text.
type StartupINI struct {
	Content string
	RunMode string
	Values  map[string]string
}

// ReadStartupConfig uses the same INI parser and run-mode precedence as Beego,
// including quoted values, includes and the distinction between absent and
// explicitly empty settings. The default text is used only for a missing file.
func ReadStartupConfig(path, defaults string) (StartupINI, error) {
	info, statErr := os.Stat(path)
	if statErr == nil && !info.Mode().IsRegular() {
		return StartupINI{}, errors.New("nps.conf 必须是可读取的普通文件")
	}
	content, err := os.ReadFile(path)
	var parsed config.Configer
	switch {
	case err == nil:
		parsed, err = config.NewConfig("ini", path)
	case errors.Is(err, os.ErrNotExist):
		if _, linkErr := os.Lstat(path); !errors.Is(linkErr, os.ErrNotExist) {
			return StartupINI{}, fmt.Errorf("无法读取 nps.conf 的链接目标: %w", err)
		}
		if defaults == "" {
			return StartupINI{}, nil
		}
		return ParseStartupConfig(defaults)
	}
	if err != nil {
		// The upstream parser can include the entire offending line in its
		// error. Do not copy it into startup logs or migration reports.
		return StartupINI{}, errors.New("无法读取或解析 nps.conf，请检查 INI 格式、权限及 include 文件")
	}
	return resolveStartupConfig(string(content), parsed), nil
}

// ParseStartupConfig parses a standalone embedded template. Real files are
// read by ReadStartupConfig so relative include paths resolve correctly.
func ParseStartupConfig(content string) (StartupINI, error) {
	parsed, err := config.NewConfigData("ini", []byte(content))
	if err != nil {
		return StartupINI{}, errors.New("无法解析内置 NPS 配置")
	}
	return resolveStartupConfig(content, parsed), nil
}

func resolveStartupConfig(content string, parsed config.Configer) StartupINI {
	mode := startupRunMode(parsed)
	return StartupINI{Content: content, RunMode: mode, Values: effectiveINIValues(parsed, mode)}
}

func startupRunMode(parsed config.Configer) string {
	if mode := os.Getenv("BEEGO_RUNMODE"); mode != "" {
		return strings.ToLower(mode)
	}
	mode := beego.BConfig.RunMode
	// LoadAppConfig reads RunMode during struct assignment and then again
	// before accessing the rest of the configuration.
	for range 2 {
		mode = parsed.DefaultString(mode+"::runmode", parsed.DefaultString("runmode", mode))
	}
	return strings.ToLower(mode)
}

func effectiveINIValues(parsed config.Configer, mode string) map[string]string {
	values := make(map[string]string)
	base, _ := parsed.GetSection("default")
	for key, value := range base {
		values[key] = value
	}
	overrides, _ := parsed.GetSection(mode)
	for key, value := range overrides {
		if _, exists := base[key]; value != "" || !exists {
			values[key] = value
		}
	}
	return values
}

// MainFileValues is used when adopting a pre-existing SQLite installation.
// Earlier versions copied business settings from the main file only, so a
// newly parsed include must not enable settings that were never in use.
func (ini StartupINI) MainFileValues() (map[string]string, error) {
	var content strings.Builder
	hasIncludes := false
	for _, line := range strings.SplitAfter(ini.Content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		fields := strings.Fields(trimmed)
		if len(fields) == 2 && strings.EqualFold(fields[0], "include") && !strings.Contains(trimmed, "=") {
			hasIncludes = true
			continue
		}
		content.WriteString(line)
	}
	if !hasIncludes {
		return ini.Values, nil
	}
	parsed, err := config.NewConfigData("ini", []byte(content.String()))
	if err != nil {
		return nil, errors.New("无法解析 nps.conf 的独立设置")
	}
	return effectiveINIValues(parsed, ini.RunMode), nil
}

// SlimStartupConfig removes migrated keys and their preceding descriptions.
// Preserve unrelated comments, unknown keys, includes and inactive sections.
func SlimStartupConfig(content string, stored map[string]string, mode string) (string, bool) {
	remove := func(key string) bool {
		_, present := stored[key]
		return IsLegacyRemovedKey(key) || IsManagedKey(key) && present
	}
	bom := ""
	if strings.HasPrefix(content, "\ufeff") {
		bom, content = "\ufeff", strings.TrimPrefix(content, "\ufeff")
	}
	lines := strings.SplitAfter(content, "\n")
	result := make([]string, 0, len(lines))
	section := "default"
	commentStart := 0
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result = append(result, line)
			commentStart = len(result)
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(trimmed[1 : len(trimmed)-1])
		}
		key, _, assignment := strings.Cut(strings.TrimSpace(strings.TrimLeft(trimmed, "#;")), "=")
		key = strings.ToLower(strings.TrimSpace(key))
		comment := strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";")
		if assignment && (section == "default" || section == mode) && remove(key) {
			changed = true
			result = result[:commentStart]
			continue
		}
		result = append(result, line)
		if !comment {
			commentStart = len(result)
		}
	}
	return bom + strings.Join(result, ""), changed
}

func writeFileAtomically(path string, content []byte) error {
	target := path
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".settings-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return err
	}
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
	return os.Rename(tempPath, target)
}
