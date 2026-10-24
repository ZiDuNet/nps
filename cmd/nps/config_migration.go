package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrateConfigFile applies the release template to an existing configuration
// file without replacing user settings.  Keeping this operation at the file
// boundary makes the startup path easy to test and, more importantly, keeps a
// failed write from being reported as a successful upgrade.
//
// The returned added keys and rotated secrets are informational.  A caller can
// use them for logs without having to parse the resulting file again.
func migrateConfigFile(path, template string) (changed bool, added []string, secrets map[string]string, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return false, nil, nil, err
	}
	original := string(content)
	compatibility, compatibilityAdded := migrateConfigCompatibility(original)
	merged, templateAdded := mergeMissingConfigFields(compatibility, template)
	added = append(compatibilityAdded, templateAdded...)
	updated, secrets := rotateInsecureConfig(merged)
	if updated == original {
		return false, nil, nil, nil
	}
	if err := writeConfigAtomically(path, []byte(updated)); err != nil {
		return false, added, secrets, err
	}
	return true, added, secrets, nil
}

// configCompatibilityAliases describes historical Bridge setting names that
// differ from the canonical names used by the current configuration. The old
// assignment is intentionally kept in place so an upgrade remains reversible.
type configCompatibilityRule struct {
	canonical string
	aliases   []string
}

var configCompatibilityAliases = []configCompatibilityRule{
	{canonical: "bridge_port", aliases: []string{"bridge_tcp_port", "bridge_kcp_port"}},
	{canonical: "tls_bridge_port", aliases: []string{"bridge_tls_port"}},
	{canonical: "bridge_ip", aliases: []string{"bridge_tcp_ip", "bridge_kcp_ip"}},
}

// configMigrationOptionalKeys are feature switches, listener endpoints, and
// optional paths that the Web configuration panel can remove from nps.conf.
// Do not re-add them on every startup when an operator deliberately disabled
// them. A fresh installation still receives the complete checked-in template;
// this list only affects upgrades of an existing file.
var configMigrationOptionalKeys = map[string]struct{}{
	"http_proxy_port": {}, "https_proxy_port": {},
	"http_proxy_access_port": {}, "https_proxy_access_port": {},
	"bridge_kcp_port": {}, "bridge_ws_port": {}, "bridge_wss_port": {},
	"bridge_quic_port": {}, "bridge_quic_ip": {}, "quic_alpn": {},
	"quic_keep_alive_period": {}, "quic_max_idle_timeout": {},
	"quic_max_incoming_streams": {}, "bridge_path": {},
	"public_vkey": {}, "ip_limit": {}, "public_register_key": {},
	"ip_register_ip": {}, "ip_register_port": {},
	"ip_register_cert_file": {}, "ip_register_key_file": {},
	"flow_store_interval": {}, "log_path": {}, "log_max_size": {},
	"log_max_files": {}, "log_max_days": {}, "log_compress": {},
	"log_rotate_daily": {}, "audit_log_path": {},
	"p2p_ip": {}, "p2p_port": {}, "totp_secret": {},
	"web_host": {}, "web_base_url": {}, "instance_key_file": {},
	"web_cert_file": {}, "web_key_file": {}, "allow_ports": {},
	"http_cache_length": {}, "pprof_ip": {}, "pprof_port": {},
	"tls_bridge_port": {},
}

// migrateConfigCompatibility adds canonical keys when an existing
// configuration only contains a historical alias. Existing canonical values
// always win, including explicit empty values.  The function is deliberately
// independent from Beego so it can be tested before the config is loaded.
func migrateConfigCompatibility(content string) (string, []string) {
	values := configValues(content)
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	bridgeType := strings.ToLower(strings.TrimSpace(values["bridge_type"]))
	_, bridgeTypeSet := values["bridge_type"]

	var blocks []string
	var added []string
	for _, rule := range configCompatibilityAliases {
		if _, exists := values[rule.canonical]; exists {
			continue
		}
		aliases := preferredConfigAliases(rule, bridgeType, bridgeTypeSet, values)
		for _, alias := range aliases {
			value, exists := values[alias]
			if !exists {
				continue
			}
			blocks = append(blocks,
				"# Added automatically to keep the Bridge listener configuration compatible (alias: "+alias+")."+newline+
					rule.canonical+"="+value,
			)
			values[rule.canonical] = value
			added = append(added, rule.canonical)
			break
		}
	}
	if len(blocks) == 0 {
		return content, nil
	}
	return appendConfigBlocks(content, blocks, newline), added
}

func preferredConfigAliases(rule configCompatibilityRule, bridgeType string, bridgeTypeSet bool, values map[string]string) []string {
	// bridge_kcp_port is also a current, valid secondary-listener option. It is
	// only a legacy alias for bridge_port when KCP is the selected primary
	// transport (or when the old file has no transport setting and KCP is its
	// only port). In an explicitly TCP configuration, never consume the KCP
	// listener as the primary TCP port.
	if rule.canonical == "bridge_port" {
		switch bridgeType {
		case "kcp":
			return []string{"bridge_kcp_port", "bridge_tcp_port"}
		case "tcp":
			return []string{"bridge_tcp_port"}
		default:
			if _, hasTCP := values["bridge_tcp_port"]; hasTCP {
				return []string{"bridge_tcp_port", "bridge_kcp_port"}
			}
			if !bridgeTypeSet {
				return []string{"bridge_kcp_port", "bridge_tcp_port"}
			}
			return []string{"bridge_tcp_port"}
		}
	}
	if rule.canonical == "bridge_ip" {
		switch bridgeType {
		case "kcp":
			return []string{"bridge_kcp_ip", "bridge_tcp_ip"}
		case "tcp":
			return []string{"bridge_tcp_ip"}
		default:
			if _, hasTCP := values["bridge_tcp_ip"]; hasTCP {
				return []string{"bridge_tcp_ip", "bridge_kcp_ip"}
			}
			if !bridgeTypeSet {
				return []string{"bridge_kcp_ip", "bridge_tcp_ip"}
			}
			return []string{"bridge_tcp_ip"}
		}
	}
	return append([]string(nil), rule.aliases...)
}

// configValues returns active INI assignments.  The last assignment wins,
// matching the behavior of the Beego INI loader and allowing operators to
// override a copied setting later in the file.
func configValues(content string) map[string]string {
	values := make(map[string]string)
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator < 0 {
			continue
		}
		key := strings.TrimSpace(line[:separator])
		if key == "" || strings.ContainsAny(key, " \t[]") {
			continue
		}
		values[key] = strings.TrimSpace(line[separator+1:])
	}
	return values
}

func appendConfigBlocks(content string, blocks []string, newline string) string {
	merged := content
	if merged != "" {
		if !strings.HasSuffix(merged, "\n") && !strings.HasSuffix(merged, "\r") {
			merged += newline
		}
		merged += newline
	}
	merged += strings.Join(blocks, newline+newline) + newline
	return merged
}

// mergeMissingConfigFields appends active settings present in the current
// release template but absent from an existing config. Existing assignments,
// including explicit empty values, and all existing comments are left intact.
func mergeMissingConfigFields(content, template string) (string, []string) {
	existing := configKeys(content)
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}

	var blocks []string
	var added []string
	var pending []string
	for _, rawLine := range strings.Split(template, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if key, ok := configEntryKey(line); ok {
			if _, optional := configMigrationOptionalKeys[key]; optional {
				// An omitted optional setting is intentional. Do not carry its
				// template comments into a later block either.
				pending = pending[:0]
				continue
			}
			if _, ok := existing[key]; !ok {
				start := 0
				for start < len(pending) && strings.TrimSpace(pending[start]) == "" {
					start++
				}
				blockLines := append(append([]string(nil), pending[start:]...), line)
				block := strings.TrimRight(strings.Join(blockLines, newline), "\r\n")
				if block != "" {
					blocks = append(blocks, block)
				}
				added = append(added, key)
				existing[key] = struct{}{}
			}
			pending = pending[:0]
			continue
		}
		pending = append(pending, line)
	}
	if len(added) == 0 {
		return content, nil
	}

	blocks = append([]string{"# The following settings were added automatically by this NPS release."}, blocks...)
	return appendConfigBlocks(content, blocks, newline), added
}

func configKeys(content string) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		if key, ok := configEntryKey(line); ok {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func configEntryKey(line string) (string, bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	line = strings.TrimPrefix(line, "\ufeff")
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", false
	}
	separator := strings.IndexByte(line, '=')
	if separator < 0 {
		return "", false
	}
	key := strings.TrimSpace(line[:separator])
	if key == "" || strings.ContainsAny(key, " \t[]") {
		return "", false
	}
	return key, true
}

// writeConfigAtomically replaces a regular config file through a temporary
// file in the same directory. If nps.conf is a symlink, its target is updated
// instead of replacing the link itself.
func writeConfigAtomically(path string, data []byte) error {
	target := path
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve config symlink: %w", err)
		}
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config target is not a regular file: %s", target)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
