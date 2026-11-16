package fnos

import (
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"ehang.io/nps/lib/quickcommand"
)

//go:embed ui/index.html
var embeddedIndex []byte

// Server is the fnOS embedded settings service. It listens only on a Unix
// socket, never on a network port. The fnOS gateway authenticates the user and
// forwards requests under the configured prefix; this process still performs
// its own input validation on every write.
type Server struct {
	Root         string
	LogPath      string
	LogDir       string
	TemplatePath string
	Prefix       string
	Status       func(id string) (bool, bool)
	Memory       func(id string) int64
	AppVersion   string
	CoreVersion  string
	mu           sync.Mutex
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, s.Prefix)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	switch {
	case path == "/" || path == "/index.html":
		s.serveIndex(w)
	case path == "/api/config":
		s.handleConfig(w, r)
	case path == "/api/logs":
		s.handleLogs(w, r)
	case path == "/api/raw":
		s.handleRaw(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(embeddedIndex)
}

type connectionView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	VKey        string `json:"vkey"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Mode        string `json:"mode"`
	Host        string `json:"host"`
	Port        string `json:"port"`
	Protocol    string `json:"protocol"`
	Path        string `json:"path"`
	ALPN        string `json:"alpn"`
	SNI         string `json:"sni"`
	Fingerprint string `json:"fingerprint"`
	CA          string `json:"ca"`
	Sync        bool   `json:"sync"`
	Memory      int64  `json:"memory"`
}

type configView struct {
	Version     int              `json:"version"`
	Revision    string           `json:"revision"`
	AppVersion  string           `json:"app_version"`
	CoreVersion string           `json:"core_version"`
	Connections []connectionView `json:"connections"`
}

type connectionSubmit struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	ConfirmDelete string `json:"confirm_delete"`
	Name          string `json:"name"`
	Enabled       *bool  `json:"enabled"`
	Mode          string `json:"mode"`
	Quick         string `json:"quick"`
	Host          string `json:"host"`
	Port          string `json:"port"`
	VKey          string `json:"vkey"`
	Protocol      string `json:"protocol"`
	Path          string `json:"path"`
	ALPN          string `json:"alpn"`
	SNI           string `json:"sni"`
	Fingerprint   string `json:"fingerprint"`
	CA            string `json:"ca"`
}

type configSubmit struct {
	Revision    string             `json:"revision"`
	Connections []connectionSubmit `json:"connections"`
	Add         *connectionSubmit  `json:"add"`
	Logs        *logsSubmit        `json:"logs"`
}

type logsSubmit struct {
	Action string `json:"action"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getConfig(w)
	case http.MethodPost:
		s.postConfig(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getConfig(w http.ResponseWriter) {
	config, err := Read(s.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := configView{
		Version:     config.Version,
		Revision:    config.Revision,
		AppVersion:  s.AppVersion,
		CoreVersion: s.CoreVersion,
		Connections: []connectionView{},
	}
	for _, entry := range config.Connections {
		connection := connectionViewFor(entry)
		if s.Status != nil {
			connection.Running, _ = s.Status(entry.ID)
		}
		if s.Memory != nil {
			connection.Memory = s.Memory(entry.ID)
		}
		view.Connections = append(view.Connections, connection)
	}
	writeJSON(w, view)
}

func connectionViewFor(entry Entry) connectionView {
	host, port, _ := net.SplitHostPort(entry.Command.Server)
	return connectionView{
		ID: entry.ID, Name: entry.Command.Name, VKey: entry.Command.VKey, Enabled: entry.Enabled, Mode: entry.Mode,
		Host: host, Port: port, Protocol: protocolValue(entry.Command),
		Path: entry.Command.BridgePath, ALPN: entry.Command.QuicALPN,
		SNI: entry.Command.TLSServerName, Fingerprint: entry.Command.TLSFingerprint,
		CA: entry.Command.TLSCAFile, Sync: entry.Sync,
	}
}

func protocolValue(command quickcommand.Command) string {
	command, err := quickcommand.Normalize(command)
	if err != nil {
		return "tcp"
	}
	if command.Transport == "tcp" && command.TLS {
		return "tls"
	}
	return command.Transport
}

func (s *Server) postConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取请求")
		return
	}
	var submit configSubmit
	if err := json.Unmarshal(body, &submit); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := Read(s.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if submit.Revision != old.Revision {
		writeError(w, http.StatusConflict, "设置已过期，请刷新页面后重试")
		return
	}
	lookup := buildLookup(old.Revision, submit)
	template := s.readTemplate()
	updated, err := Apply(old, lookup, template)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := Save(s.Root, updated); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if submit.Logs != nil && submit.Logs.Action == "clear" {
		if err := ClearLogs(s.LogPath); err != nil {
			writeError(w, http.StatusInternalServerError, "配置已保存，但日志清理失败："+err.Error())
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ok", "revision": updated.Revision})
}

func buildLookup(revision string, submit configSubmit) LookupEnv {
	values := map[string]string{}
	put := func(prefix, key, value string) {
		if value != "" {
			values[prefix+key] = value
		}
	}
	for _, c := range submit.Connections {
		if c.ID == "" {
			continue
		}
		prefix := FieldPrefix(c.ID, revision)
		put(prefix, "action", c.Action)
		put(prefix, "confirm_delete", c.ConfirmDelete)
		put(prefix, "name", c.Name)
		if c.Enabled != nil {
			put(prefix, "enabled", strconv.FormatBool(*c.Enabled))
		}
		put(prefix, "mode", c.Mode)
		put(prefix, "quick", c.Quick)
		put(prefix, "host", c.Host)
		put(prefix, "port", c.Port)
		put(prefix, "vkey", c.VKey)
		put(prefix, "protocol", c.Protocol)
		put(prefix, "path", c.Path)
		put(prefix, "alpn", c.ALPN)
		put(prefix, "sni", c.SNI)
		put(prefix, "fingerprint", c.Fingerprint)
		put(prefix, "ca", c.CA)
	}
	if submit.Add != nil {
		prefix := FieldPrefix("new", revision)
		put(prefix, "action", submit.Add.Action)
		put(prefix, "name", submit.Add.Name)
		if submit.Add.Enabled != nil {
			put(prefix, "enabled", strconv.FormatBool(*submit.Add.Enabled))
		}
		put(prefix, "mode", submit.Add.Mode)
		put(prefix, "quick", submit.Add.Quick)
		put(prefix, "host", submit.Add.Host)
		put(prefix, "port", submit.Add.Port)
		put(prefix, "vkey", submit.Add.VKey)
		put(prefix, "protocol", submit.Add.Protocol)
		put(prefix, "path", submit.Add.Path)
		put(prefix, "alpn", submit.Add.ALPN)
		put(prefix, "sni", submit.Add.SNI)
		put(prefix, "fingerprint", submit.Add.Fingerprint)
		put(prefix, "ca", submit.Add.CA)
	}
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func (s *Server) readTemplate() string {
	if s.TemplatePath == "" {
		return ""
	}
	data, err := os.ReadFile(s.TemplatePath)
	if err != nil || len(data) > 1<<20 {
		return ""
	}
	return string(data)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			if id != MainID && !identifier.MatchString(id) {
				writeError(w, http.StatusBadRequest, "连接编号无效")
				return
			}
			config, err := Read(s.Root)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			path := s.connectionLogPath(id)
			writeJSON(w, map[string]string{"id": id, "log": Redact(LogTail(path), Secrets(config))})
		} else {
			config, err := Read(s.Root)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, map[string]string{"log": Redact(LogTail(s.LogPath), Secrets(config))})
		}
	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var submit logsSubmit
		if err := json.Unmarshal(body, &submit); err != nil || submit.Action != "clear" {
			writeError(w, http.StatusBadRequest, "请求格式无效")
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		path := s.LogPath
		if id != "" {
			if id != MainID && !identifier.MatchString(id) {
				writeError(w, http.StatusBadRequest, "连接编号无效")
				return
			}
			path = s.connectionLogPath(id)
		}
		if err := ClearLogs(path); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) connectionLogPath(id string) string {
	if s.LogDir == "" {
		return ""
	}
	return ConnectionLogPath(s.LogDir, id)
}

type rawSubmit struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != MainID && !identifier.MatchString(id) {
			writeError(w, http.StatusBadRequest, "连接编号无效")
			return
		}
		data, err := readPrivate(filepath.Join(s.Root, RelativePath(id)))
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到配置文件")
			return
		}
		writeJSON(w, map[string]string{"id": id, "path": RelativePath(id), "content": string(data)})
	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20+1<<20))
		var submit rawSubmit
		if err := json.Unmarshal(body, &submit); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式无效")
			return
		}
		id := strings.TrimSpace(submit.ID)
		if id != MainID && !identifier.MatchString(id) {
			writeError(w, http.StatusBadRequest, "连接编号无效")
			return
		}
		if len(submit.Content) > 1<<20 {
			writeError(w, http.StatusBadRequest, "配置文件超过 1 MiB")
			return
		}
		if _, err := ParseEntry(id, submit.Content); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := AtomicWrite(filepath.Join(s.Root, RelativePath(id)), []byte(submit.Content), 0600); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONWithStatus(w, status, map[string]string{"error": message})
}

func writeJSONWithStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// SocketPath returns the absolute Unix socket path for a target directory.
func SocketPath(target string) string {
	return filepath.Join(target, "app.sock")
}
