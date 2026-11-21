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

// Init loads the settings table, imports any business keys still present in
// nps.conf, injects the effective values into Beego and removes the migrated
// keys from the file. It is safe to call repeatedly; a partial migration is
// resumed by the next startup.
func Init(runPath string, sqlite *store.Store) error {
	if sqlite == nil {
		return errors.New("settings store is unavailable")
	}
	// settings.Init is also called by tests on a process whose storage may be
	// a JSON-only fixture. Never claim to be active without a settings table.
	if err := sqlite.CheckSettingsTable(); err != nil {
		return fmt.Errorf("检查 SQLite 业务配置表: %w", err)
	}
	confPath := filepath.Join(runPath, "conf", "nps.conf")
	values, err := sqlite.LoadSettings()
	if err != nil {
		return fmt.Errorf("读取 SQLite 业务配置: %w", err)
	}
	fileValues, content, err := readConfigValues(confPath)
	if err != nil {
		return err
	}

	imported := make(map[string]string)
	for key, value := range fileValues {
		if IsLegacyRemovedKey(key) {
			continue
		}
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
		imported[key] = value
		values[key] = value
	}
	if len(imported) > 0 {
		if err := sqlite.UpsertSettings(imported); err != nil {
			return fmt.Errorf("迁移 nps.conf 业务配置到 SQLite: %w", err)
		}
	}

	manager := &Manager{
		store:    sqlite,
		confPath: confPath,
		runPath:  runPath,
		values:   values,
	}
	if err := manager.removeManagedKeysFromFile(content); err != nil {
		return err
	}
	setActive(manager)
	manager.Apply()
	if len(imported) > 0 {
		logs.Info("migrated %d business settings from nps.conf to SQLite", len(imported))
	}
	return nil
}

// firstFileToSQLiteImport reports whether this is the moment a business
// setting is first copied out of the legacy configuration file. Existing rows
// in the settings table always win, so the normalization cannot rewrite a
// value that an administrator explicitly saved after upgrading.
func firstFileToSQLiteImport(key, value string, stored map[string]string) bool {
	_, exists := stored[key]
	return !exists && value != ""
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

func (m *Manager) removeManagedKeysFromFile(content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	filtered, changed := stripManagedKeyLines(content)
	if !changed {
		return nil
	}
	backup := m.confPath + ".pre-settings.bak." + version.VERSION + "." + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, []byte(content), 0o600); err != nil {
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

func readConfigValues(path string) (map[string]string, string, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("读取 nps.conf: %w", err)
	}
	values := make(map[string]string)
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key == "" || strings.ContainsAny(key, " \t[]") {
			continue
		}
		values[key] = strings.TrimSpace(parts[1])
	}
	return values, string(content), nil
}

func stripManagedKeyLines(content string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	result := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r\n"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			result = append(result, line)
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			result = append(result, line)
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if IsManagedKey(key) || IsLegacyRemovedKey(key) {
			changed = true
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, ""), changed
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
