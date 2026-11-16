package fnos

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"ehang.io/nps/lib/common"
)

const LogSize = 5 << 20
const LogBackups = 3

// LogTailBytes is the read window used to render the Web UI log view, and
// LogTailLines caps how many recent lines are returned. The on-disk file is
// never truncated by these values; rotation is handled separately by LogSize.
const LogTailBytes = 1 << 20
const LogTailLines = 2000

var ansiSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// LogWriter bounds disk use to one active file and three backups. A full set
// of backups must not disable further rotation (Beego's maxfiles does that).
type LogWriter struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	limit   int64
	secrets []string
}

func OpenLog(path string) (*LogWriter, error) {
	w := &LogWriter{path: path, limit: LogSize}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	release, err := lockLog(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer release()
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func regularOrMissing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("日志文件不能是符号链接或特殊文件")
	}
	return nil
}

func (w *LogWriter) open() error {
	if err := regularOrMissing(w.path); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	w.file = f
	return nil
}

func (w *LogWriter) SetSecrets(config Config) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.secrets = Secrets(config)
}

// ConnectionLogPath returns the per-VKey log file below the shared log
// directory. Each connection owns one log file so the embedded UI can show a
// clean per-tab view without filtering a shared, interleaved stream.
func ConnectionLogPath(logDir, id string) string {
	return filepath.Join(logDir, id+".log")
}

// Secrets also covers native npc.conf passwords and proxy credentials. The
// child keeps its own filter even after its entry is removed from the parent.
func Secrets(config Config) []string {
	seen := map[string]bool{}
	for _, entry := range config.Connections {
		seen[entry.Command.VKey] = true
		raw := entry.Raw
		if expanded, err := common.ParseStr(raw); err == nil {
			raw = expanded
		}
		for _, line := range strings.Split(raw, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || strings.HasPrefix(key, "#") || strings.HasPrefix(key, ";") {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			if strings.Contains(key, "password") || strings.Contains(key, "token") || strings.Contains(key, "secret") || key == "vkey" || key == "auth" || key == "proxy_url" {
				seen[strings.TrimSpace(value)] = true
			}
		}
	}
	delete(seen, "")
	result := make([]string, 0, len(seen))
	for secret := range seen {
		result = append(result, secret)
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

func Redact(message string, secrets []string) string {
	message = ansiSequence.ReplaceAllString(message, "")
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[密钥已隐藏]")
		}
	}
	return message
}

func (w *LogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	release, err := lockLog(w.path + ".lock")
	if err != nil {
		return 0, err
	}
	defer release()
	// Reopen under the cross-process lock: clearing or rotating a log from
	// native settings cannot leave the runtime writing an unlinked inode.
	if err := w.file.Close(); err != nil {
		return 0, err
	}
	w.file = nil
	if err := w.open(); err != nil {
		return 0, err
	}
	message := Redact(string(data), w.secrets)
	if len(message) > 16384 {
		message = message[:16384] + "…\n"
	}
	info, err := w.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size()+int64(len(message)) > w.limit {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	if _, err := io.WriteString(w.file, message); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *LogWriter) rotate() error {
	for _, suffix := range []string{".1", ".2", ".3"} {
		if err := regularOrMissing(w.path + suffix); err != nil {
			return err
		}
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	if err := os.Remove(w.path + ".3"); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = w.open()
		return err
	}
	for _, pair := range [][2]string{{".2", ".3"}, {".1", ".2"}, {"", ".1"}} {
		if err := os.Rename(w.path+pair[0], w.path+pair[1]); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = w.open()
			return err
		}
	}
	return w.open()
}

func (w *LogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *LogWriter) Sync() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Sync()
	}
}

// LogTail is a bounded snapshot, refreshed by reopening native settings.
func LogTail(path string) string {
	release, err := lockLog(path + ".lock")
	if err != nil {
		return "日志读取失败，请检查应用用户权限。"
	}
	defer release()
	if err := regularOrMissing(path); err != nil {
		return "日志不可读取：目标不是普通文件。"
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "尚无运行日志。"
	}
	if err != nil {
		return "日志读取失败，请检查应用用户权限。"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "日志状态读取失败。"
	}
	length := min(info.Size(), int64(LogTailBytes))
	data := make([]byte, length)
	if _, err := f.ReadAt(data, info.Size()-length); err != nil && err != io.EOF {
		return "日志内容读取失败。"
	}
	if info.Size() > length {
		if offset := bytes.IndexByte(data, '\n'); offset >= 0 {
			data = data[offset+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > LogTailLines {
		lines = lines[len(lines)-LogTailLines:]
	}
	return ansiSequence.ReplaceAllString(strings.Join(lines, "\n"), "")
}

// ClearLogs truncates the active inode so the running writer remains attached.
// Only the three precise regular-file backup names are removed.
func ClearLogs(path string) error {
	release, err := lockLog(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	paths := []string{path, path + ".1", path + ".2", path + ".3"}
	for _, target := range paths {
		if err := regularOrMissing(target); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, target := range paths[1:] {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RemoveLogs deletes the active log and its three rotation backups. It is used
// after a connection is removed so a later re-created MainID does not inherit
// stale history from the previous VKey.
func RemoveLogs(path string) error {
	release, err := lockLog(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	paths := []string{path, path + ".1", path + ".2", path + ".3"}
	for _, target := range paths {
		if err := regularOrMissing(target); err != nil {
			return err
		}
	}
	for _, target := range paths {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
