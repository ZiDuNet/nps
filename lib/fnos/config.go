// Package fnos implements NPC's native fnOS configuration. It has no HTTP
// listener and never uses NPC's server-side WORK_CONFIG channel.
package fnos

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/quickcommand"
)

const (
	Version        = 1
	MaxConnections = 256 // A resource guard, not a fixed number of wizard slots.
	maxConfigBytes = 1 << 20
	MainID         = "0000000000000001"
)

var identifier = regexp.MustCompile(`^[a-f0-9]{16}$`)

type Entry struct {
	ID      string
	Mode    string
	Enabled bool
	Command quickcommand.Command
	Raw     string // The real npc.conf, including unmanaged settings and comments.
	Sync    bool   // Rule sections require the existing WORK_CONFIG startup path.
}

type Config struct {
	Version     int
	Revision    string
	Connections []Entry
}

func NewConfig() Config {
	return Config{Version: Version, Revision: "initial", Connections: []Entry{}}
}

func newID() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func RelativePath(id string) string {
	if id == MainID {
		return "npc.conf"
	}
	return filepath.Join("connections", id, "npc.conf")
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return nil, errors.New("npc.conf 必须是小于 1 MiB 的普通文件，不能是符号链接")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Configuration contains VKeys; repair overly permissive legacy files.
	if info.Mode().Perm() != 0600 {
		if err := f.Chmod(0600); err != nil {
			return nil, fmt.Errorf("无法保护连接配置权限：%w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if len(data) > maxConfigBytes {
		return nil, errors.New("npc.conf 超过 1 MiB")
	}
	return data, err
}

// Read derives settings and the stale-form token from the actual INI files.
// There is no second JSON store and manual edits need no revision bookkeeping.
func Read(root string) (Config, error) {
	config := NewConfig()
	ids := []string{MainID}
	if info, err := os.Lstat(filepath.Join(root, "connections")); err == nil && !info.IsDir() {
		return Config{}, errors.New("connections 必须是实际目录，不能是符号链接")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	dirs, err := os.ReadDir(filepath.Join(root, "connections"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	for _, dir := range dirs {
		if !identifier.MatchString(dir.Name()) || dir.Name() == MainID {
			continue
		}
		if !dir.IsDir() || dir.Type()&os.ModeSymlink != 0 {
			return Config{}, errors.New("连接目录不能是符号链接或特殊文件")
		}
		ids = append(ids, dir.Name())
	}
	if len(ids) > MaxConnections+1 {
		return Config{}, fmt.Errorf("最多允许 %d 条连接", MaxConnections)
	}
	for _, id := range ids {
		data, err := readPrivate(filepath.Join(root, RelativePath(id)))
		if errors.Is(err, os.ErrNotExist) {
			continue // Empty directories left by an explicit deletion are harmless.
		}
		if err != nil {
			return Config{}, fmt.Errorf("%s：%w", RelativePath(id), err)
		}
		entry, err := ParseEntry(id, string(data))
		if err != nil {
			return Config{}, fmt.Errorf("%s：%w", RelativePath(id), err)
		}
		config.Connections = append(config.Connections, entry)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	config.Revision = Revision(config)
	return config, nil
}

func Revision(config Config) string {
	if len(config.Connections) == 0 {
		return "initial"
	}
	hash := sha256.New()
	entries := append([]Entry(nil), config.Connections...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00%d\x00%s", entry.ID, len(entry.Raw), entry.Raw)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// ParseEntry validates INI structure and the connection fields without invoking
// the legacy parser's side effects (pprof, account file reads, etc.). The child
// process uses the existing full parser for actual NPC rule startup.
func ParseEntry(id, raw string) (Entry, error) {
	entry := Entry{ID: id, Raw: raw, Mode: "manual", Enabled: true}
	expanded, err := common.ParseStr(raw)
	if err != nil {
		return entry, errors.New("npc.conf 环境变量模板无效")
	}
	values := map[string]string{}
	sections := map[string]bool{}
	section := ""
	for index, line := range strings.Split(strings.ReplaceAll(expanded, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			if line != trimmed || !strings.HasSuffix(trimmed, "]") || strings.ContainsAny(trimmed[1:len(trimmed)-1], "[]") {
				return entry, fmt.Errorf("npc.conf 第 %d 行节名称无效，节名称应单独顶格书写", index+1)
			}
			section = trimmed[1 : len(trimmed)-1]
			if section == "" || sections[section] {
				return entry, fmt.Errorf("npc.conf 第 %d 行节名称为空或重复", index+1)
			}
			sections[section] = true
			entry.Sync = entry.Sync || section != "common"
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || section == "" || strings.ContainsRune(trimmed, '\x00') {
			return entry, fmt.Errorf("npc.conf 第 %d 行需要位于配置节内并使用 key=value", index+1)
		}
		if section == "common" {
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			switch key {
			case "server":
				key = "server_addr"
			case "tp":
				key = "conn_type"
			}
			values[key] = value
		}
	}
	if !sections["common"] {
		return entry, errors.New("npc.conf 缺少 [common] 配置节")
	}
	if mode := values["fnos_mode"]; mode != "" {
		entry.Mode = mode
	}
	for _, key := range []string{"tls_enable", "tls_insecure_skip_verify", "fnos_enabled", "fnos_config_sync"} {
		if value := values[key]; value != "" && value != "true" && value != "false" {
			return entry, fmt.Errorf("[common] 中 %s 只能为 true 或 false", key)
		}
	}
	entry.Enabled = values["fnos_enabled"] != "false"
	entry.Sync = entry.Sync || values["fnos_config_sync"] == "true"
	entry.Command, err = validateCommand(quickcommand.Command{
		Name: values["remark"], Server: values["server_addr"], VKey: values["vkey"], Transport: values["conn_type"],
		TLS: values["tls_enable"] == "true", BridgePath: values["bridge_path"], QuicALPN: values["quic_alpn"],
		TLSServerName: values["tls_server_name"], TLSFingerprint: values["tls_fingerprint"], TLSCAFile: values["tls_ca_file"],
		TLSInsecureSkipVerify: values["tls_insecure_skip_verify"] == "true",
	})
	if err == nil && entry.Mode != "quick" && entry.Mode != "manual" {
		err = errors.New("fnos_mode 只能为 quick 或 manual")
	}
	return entry, err
}

func (config *Config) Validate() error {
	if config.Version != Version || (config.Revision != "initial" && !identifier.MatchString(config.Revision)) {
		return errors.New("不支持的飞牛连接配置版本或修订号")
	}
	if len(config.Connections) > MaxConnections {
		return fmt.Errorf("最多允许 %d 条连接", MaxConnections)
	}
	ids, connections := map[string]bool{}, map[string]bool{}
	for index := range config.Connections {
		entry := &config.Connections[index]
		if !identifier.MatchString(entry.ID) || ids[entry.ID] {
			return errors.New("连接编号无效或重复")
		}
		ids[entry.ID] = true
		if entry.Mode != "quick" && entry.Mode != "manual" {
			return errors.New("请选择快捷指令或手动填写")
		}
		command, err := validateCommand(entry.Command)
		if err != nil {
			return fmt.Errorf("第 %d 条连接：%w", index+1, err)
		}
		entry.Command = command
		parsed, err := ParseEntry(entry.ID, entry.Raw)
		if err != nil {
			return fmt.Errorf("第 %d 份 npc.conf：%w", index+1, err)
		}
		if parsed != *entry {
			return errors.New("连接信息与 npc.conf 内容不一致，拒绝保存")
		}
		key := strings.ToLower(command.Server) + "\x00" + command.VKey
		if connections[key] {
			return errors.New("同一服务端地址与 VKey 不能重复添加；修改协议请编辑已有连接")
		}
		connections[key] = true
	}
	return nil
}

func validateCommand(input quickcommand.Command) (quickcommand.Command, error) {
	command, err := quickcommand.Normalize(input)
	if err != nil {
		// Do not echo a malformed quick command or its secret-bearing fields.
		return quickcommand.Command{}, errors.New("连接参数无效，请检查地址、VKey、协议及协议参数")
	}
	host, port, err := net.SplitHostPort(command.Server)
	if err != nil || !validHost(host) {
		return quickcommand.Command{}, errors.New("服务端应为域名或 IP 加端口；IPv6 地址使用 [地址]:端口")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return quickcommand.Command{}, errors.New("服务端端口必须为 1–65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	command.Server = net.JoinHostPort(strings.ToLower(host), strconv.Itoa(portNumber))
	if len(command.Name) > 128 || len(command.VKey) > 1024 || len(command.BridgePath) > 1024 || len(command.QuicALPN) > 255 ||
		len(command.TLSServerName) > 255 || len(command.TLSFingerprint) > 256 || len(command.TLSCAFile) > 1024 {
		return quickcommand.Command{}, errors.New("连接参数过长")
	}
	for _, field := range []string{command.Name, command.VKey, command.BridgePath, command.QuicALPN, command.TLSServerName, command.TLSFingerprint, command.TLSCAFile} {
		if strings.ContainsAny(field, "\x00\r\n") {
			return quickcommand.Command{}, errors.New("连接参数不能包含换行或控制字符")
		}
	}
	if command.TLSInsecureSkipVerify {
		return quickcommand.Command{}, errors.New("飞牛包不允许跳过 TLS 证书校验，请使用可信证书、CA 或证书指纹")
	}
	return command, nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

type LookupEnv func(string) (string, bool)

func FieldPrefix(id, revision string) string { return "wizard_" + id + "_" + revision + "_" }

// Apply applies a single native wizard submission to a copy. All existing
// entries are kept unless the user explicitly selects update or delete.
// Revision-scoped names prevent fnOS's remembered form values replaying an add
// or delete on the next visit, so stale submissions are safe no-ops. Password
// blanks retain the existing secret.
func Apply(old Config, lookup LookupEnv, templates ...string) (Config, error) {
	config := old
	config.Connections = append([]Entry{}, old.Connections...)
	result := make([]Entry, 0, len(old.Connections)+1)
	for _, entry := range config.Connections {
		prefix := FieldPrefix(entry.ID, old.Revision)
		action, _ := lookup(prefix + "action")
		switch action {
		case "", "keep":
		case "update":
			updated, err := entryFromFields(prefix, &entry, lookup)
			if err != nil {
				return Config{}, err
			}
			entry = updated
		case "delete":
			confirm, _ := lookup(prefix + "confirm_delete")
			if confirm != "yes" {
				return Config{}, errors.New("删除连接前请选择确认删除")
			}
			continue
		default:
			return Config{}, errors.New("连接操作无效，请重新打开应用设置")
		}
		result = append(result, entry)
	}
	prefix := FieldPrefix("new", old.Revision)
	action, _ := lookup(prefix + "action")
	switch action {
	case "", "skip":
	case "add":
		entry, err := entryFromFields(prefix, nil, lookup, templates...)
		if err != nil {
			return Config{}, err
		}
		if len(old.Connections) == 0 {
			entry.ID = MainID
		}
		result = append(result, entry)
	default:
		return Config{}, errors.New("新增连接操作无效，请重新打开应用设置")
	}
	config.Connections = result
	config.Revision = Revision(config)
	return config, config.Validate()
}

func entryFromFields(prefix string, previous *Entry, lookup LookupEnv, templates ...string) (Entry, error) {
	get := func(name string) string { value, _ := lookup(prefix + name); return strings.TrimSpace(value) }
	entry := Entry{Mode: get("mode"), Enabled: true}
	if previous != nil {
		entry = *previous
		entry.Mode = get("mode")
	} else {
		var err error
		entry.ID, err = newID()
		if err != nil {
			return Entry{}, err
		}
		if len(templates) > 0 && templates[0] != "" {
			entry.Raw = templates[0]
		}
	}
	switch get("enabled") {
	case "true":
		entry.Enabled = true
	case "false":
		entry.Enabled = false
	default:
		return Entry{}, errors.New("请选择连接启用状态")
	}
	switch entry.Mode {
	case "quick":
		encoded := get("quick")
		if encoded == "" {
			if previous == nil || previous.Mode != "quick" {
				return Entry{}, errors.New("请粘贴完整快捷启动码")
			}
		} else {
			if len(encoded) > 16384 {
				return Entry{}, errors.New("快捷启动码过长")
			}
			command, err := quickcommand.DecodeBase64(encoded)
			if err != nil {
				return Entry{}, errors.New("快捷启动码无效，请重新从 NPS 面板复制完整内容")
			}
			entry.Command = command
		}
	case "manual":
		vkey := get("vkey")
		if vkey == "" && previous != nil {
			vkey = previous.Command.VKey
		}
		host := strings.Trim(get("host"), "[]")
		entry.Command = quickcommand.Command{
			Server:         net.JoinHostPort(host, get("port")),
			VKey:           vkey,
			Transport:      get("protocol"),
			BridgePath:     get("path"),
			QuicALPN:       get("alpn"),
			TLSServerName:  get("sni"),
			TLSFingerprint: get("fingerprint"),
			TLSCAFile:      get("ca"),
		}
		// Do not carry irrelevant defaults into TCP/KCP quick commands.
		if entry.Command.Transport != "ws" && entry.Command.Transport != "wss" {
			entry.Command.BridgePath = ""
		}
		if entry.Command.Transport != "quic" {
			entry.Command.QuicALPN = ""
		}
	default:
		return Entry{}, errors.New("请选择快捷指令或手动填写")
	}
	if name := get("name"); name != "" {
		entry.Command.Name = name
	}
	command, err := validateCommand(entry.Command)
	entry.Command = command
	if err != nil {
		return Entry{}, err
	}
	entry.Raw = MergeCommon(entry.Raw, map[string]string{
		"server_addr": command.Server, "vkey": command.VKey, "conn_type": command.Transport,
		"remark": command.Name, "tls_enable": strconv.FormatBool(command.TLS),
		"bridge_path": command.BridgePath, "quic_alpn": command.QuicALPN,
		"tls_server_name": command.TLSServerName, "tls_fingerprint": command.TLSFingerprint, "tls_ca_file": command.TLSCAFile,
		"fnos_mode": entry.Mode, "fnos_enabled": strconv.FormatBool(entry.Enabled),
	})
	return ParseEntry(entry.ID, entry.Raw)
}

// MergeCommon edits only wizard-owned fields. Other common options, rule
// sections and comments remain intact, so hand-written npc.conf is first-class.
func MergeCommon(raw string, values map[string]string) string {
	if raw == "" {
		raw = "# NPC 配置，由飞牛安装向导生成；可手动编辑。\n# 每份配置对应一个 VKey；修改后自动应用，错误配置不会覆盖运行中的连接。\n[common]\nauto_reconnection=true\ndisconnect_timeout=60\ndisable_remote_update=true\n"
	}
	remaining := make(map[string]string, len(values))
	for key, value := range values {
		remaining[key] = value
	}
	var output []string
	section := ""
	flush := func() {
		keys := make([]string, 0, len(remaining))
		for key := range remaining {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			output = append(output, key+"="+remaining[key])
			delete(remaining, key)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(strings.ReplaceAll(raw, "\r\n", "\n"), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if section == "common" {
				flush()
			}
			section = trimmed[1 : len(trimmed)-1]
		}
		if section == "common" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, ";") {
			if key, _, ok := strings.Cut(trimmed, "="); ok {
				key = strings.TrimSpace(key)
				if key == "server" {
					key = "server_addr"
				} else if key == "tp" {
					key = "conn_type"
				}
				if value, managed := values[key]; managed {
					if _, first := remaining[key]; first {
						output = append(output, key+"="+value)
						delete(remaining, key)
					}
					continue
				}
			}
		}
		output = append(output, line)
	}
	if section == "common" {
		flush()
	}
	return strings.Join(output, "\n") + "\n"
}

// AtomicWrite refuses symlink targets and uses a same-directory, fsynced
// temporary file. The caller must serialize multi-file wizard transactions.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("目标文件不是普通文件，拒绝覆盖")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".npc-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err == nil {
		_, err = io.Copy(f, bytes.NewReader(data))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// SaveWithWizard stages the non-secret native form before committing runtime
// configuration. If committing fails, it restores the previous form.
func SaveWithWizard(root, wizardPath string, config Config, logTail ...string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	old, err := Read(root)
	if err != nil {
		return err
	}
	wizard, err := Wizard(config, false, logTail...)
	if err != nil {
		return err
	}
	oldWizard, err := os.ReadFile(wizardPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := AtomicWrite(wizardPath, wizard, 0644); err != nil {
		return fmt.Errorf("无法更新飞牛原生设置表单：%w", err)
	}
	previous, wanted := map[string]string{}, map[string]string{}
	for _, entry := range old.Connections {
		previous[entry.ID] = entry.Raw
	}
	for _, entry := range config.Connections {
		wanted[entry.ID] = entry.Raw
	}
	changed := []string{}
	rollback := func(cause error) error {
		failed := false
		for _, id := range changed {
			path := filepath.Join(root, RelativePath(id))
			if raw, exists := previous[id]; exists {
				failed = AtomicWrite(path, []byte(raw), 0600) != nil || failed
			} else {
				failed = os.Remove(path) != nil || failed
			}
		}
		failed = AtomicWrite(wizardPath, oldWizard, 0644) != nil || failed
		if failed {
			return fmt.Errorf("配置保存与回滚失败，请检查磁盘权限后重新打开设置：%w", cause)
		}
		return cause
	}
	for id, raw := range wanted {
		if raw == previous[id] {
			continue
		}
		if err := AtomicWrite(filepath.Join(root, RelativePath(id)), []byte(raw), 0600); err != nil {
			return rollback(err)
		}
		changed = append(changed, id)
	}
	for id := range previous {
		if _, exists := wanted[id]; exists {
			continue
		}
		if err := os.Remove(filepath.Join(root, RelativePath(id))); err != nil {
			return rollback(err)
		}
		changed = append(changed, id)
	}
	return nil
}

// Save commits only the npc.conf files. It is used by the embedded settings
// page, which has no native wizard file to stage. The running manager detects
// the revision change on its next poll and applies it without restarting.
func Save(root string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	old, err := Read(root)
	if err != nil {
		return err
	}
	previous, wanted := map[string]string{}, map[string]string{}
	for _, entry := range old.Connections {
		previous[entry.ID] = entry.Raw
	}
	for _, entry := range config.Connections {
		wanted[entry.ID] = entry.Raw
	}
	changed := []string{}
	rollback := func(cause error) error {
		failed := false
		for _, id := range changed {
			path := filepath.Join(root, RelativePath(id))
			if raw, exists := previous[id]; exists {
				failed = AtomicWrite(path, []byte(raw), 0600) != nil || failed
			} else {
				failed = os.Remove(path) != nil || failed
			}
		}
		if failed {
			return fmt.Errorf("配置保存与回滚失败，请检查磁盘权限后重新打开设置：%w", cause)
		}
		return cause
	}
	for id, raw := range wanted {
		if raw == previous[id] {
			continue
		}
		if err := AtomicWrite(filepath.Join(root, RelativePath(id)), []byte(raw), 0600); err != nil {
			return rollback(err)
		}
		changed = append(changed, id)
	}
	for id := range previous {
		if _, exists := wanted[id]; exists {
			continue
		}
		if err := os.Remove(filepath.Join(root, RelativePath(id))); err != nil {
			return rollback(err)
		}
		changed = append(changed, id)
	}
	return nil
}
