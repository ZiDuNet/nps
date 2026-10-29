// Package connectionlog persists bounded connection metadata for the
// management console. It never stores payload bytes, credentials, private
// keys, or authorization headers.
package connectionlog

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
)

const (
	KindBridge = "bridge"
	KindTunnel = "tunnel"
	KindHost   = "host"

	ResultSuccess = "success"
	ResultFailure = "failure"

	ActionBridgeConnected    = "bridge.connected"
	ActionBridgeDisconnected = "bridge.disconnected"
	ActionBridgeAuthFailed   = "bridge.auth_failed"
	ActionProxyConnection    = "proxy.connection"

	defaultLimit  = 100
	maxLimit      = 500
	queueCapacity = 4096
)

// Event is the non-sensitive description of one Bridge or proxy connection.
// StartedAt and EndedAt describe the observed session. Bridge authentication
// failures use one timestamp and normally have an empty client ID.
type Event struct {
	ID              string    `json:"id"`
	Time            time.Time `json:"time"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	Kind            string    `json:"kind"`
	Action          string    `json:"action"`
	Result          string    `json:"result"`
	UserID          int       `json:"user_id,omitempty"`
	UserName        string    `json:"user_name,omitempty"`
	ClientID        int       `json:"client_id,omitempty"`
	ClientRemark    string    `json:"client_remark,omitempty"`
	ResourceType    string    `json:"resource_type,omitempty"`
	ResourceID      int       `json:"resource_id,omitempty"`
	ResourceRemark  string    `json:"resource_remark,omitempty"`
	BridgeTransport string    `json:"bridge_transport,omitempty"`
	ProxyProtocol   string    `json:"proxy_protocol,omitempty"`
	SourceIP        string    `json:"source_ip,omitempty"`
	Target          string    `json:"target,omitempty"`
	ClientVersion   string    `json:"client_version,omitempty"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	BytesIn         int64     `json:"bytes_in,omitempty"`
	BytesOut        int64     `json:"bytes_out,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// Filter controls a newest-first connection-log query. OwnerUserID and
// AllowedClientIDs are applied inside the package so a scoped controller
// cannot accidentally widen a query after authorization.
type Filter struct {
	Offset           int
	Limit            int
	Search           string
	Kind             string
	Action           string
	Result           string
	BridgeTransport  string
	ProxyProtocol    string
	OwnerUserID      *int
	ClientID         *int
	AllowedClientIDs map[int]struct{}
}

// Page is one bounded page of connection events.
type Page struct {
	Events []*Event
	Total  int
}

type job struct {
	path    string
	payload []byte
	barrier chan struct{}
}

var state = struct {
	sync.RWMutex
	path string
}{}

var (
	startOnce sync.Once
	jobs      = make(chan job, queueCapacity)
	dropped   atomic.Uint64
)

func startWriter() {
	startOnce.Do(func() {
		go func() {
			for item := range jobs {
				if item.barrier != nil {
					close(item.barrier)
					continue
				}
				if item.path == "" || len(item.payload) == 0 {
					continue
				}
				if err := write(item.path, item.payload); err != nil {
					log.Printf("connection log write failed: %v", err)
				}
			}
		}()
	})
}

// Configure changes the JSONL destination. An empty path disables writes.
// Relative paths are resolved against the NPS runtime directory.
func Configure(path string) {
	startWriter()
	path = strings.TrimSpace(path)
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(common.GetRunPath(), path)
	}
	if path != "" {
		path = filepath.Clean(path)
	}
	state.Lock()
	state.path = path
	state.Unlock()
}

// Path returns the active destination. An empty result means logging is off.
func Path() string {
	state.RLock()
	defer state.RUnlock()
	return state.path
}

// Append queues one event without applying disk backpressure to a forwarding
// goroutine. A full queue drops the event and increments the diagnostic
// counter returned by Dropped.
func Append(event Event) error {
	state.RLock()
	path := state.path
	state.RUnlock()
	if path == "" {
		return nil
	}
	if strings.TrimSpace(event.Action) == "" {
		return errors.New("connection log action is required")
	}
	now := time.Now().UTC()
	if event.Time.IsZero() {
		event.Time = now
	} else {
		event.Time = event.Time.UTC()
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = event.Time
	} else {
		event.StartedAt = event.StartedAt.UTC()
	}
	if event.EndedAt.IsZero() {
		event.EndedAt = event.Time
	} else {
		event.EndedAt = event.EndedAt.UTC()
	}
	if event.ID == "" {
		event.ID = newID(event.Time)
	}
	if event.Result == "" {
		event.Result = ResultSuccess
	}
	event.Error = oneLine(event.Error)
	event.ClientRemark = oneLine(event.ClientRemark)
	event.ResourceRemark = oneLine(event.ResourceRemark)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal connection event: %w", err)
	}
	startWriter()
	select {
	case jobs <- job{path: path, payload: append(payload, '\n')}:
	default:
		dropped.Add(1)
	}
	return nil
}

// Flush waits until all events queued before the call have reached the writer.
// It is primarily used by tests and orderly shutdown paths.
func Flush() {
	startWriter()
	done := make(chan struct{})
	jobs <- job{barrier: done}
	<-done
}

// Dropped reports events discarded because the asynchronous queue was full.
func Dropped() uint64 {
	return dropped.Load()
}

// List returns a bounded newest-first page and ignores malformed or partial
// JSONL lines so one interrupted write cannot hide later records.
func List(filter Filter) (Page, error) {
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	if filter.Limit <= 0 {
		filter.Limit = defaultLimit
	}
	if filter.Limit > maxLimit {
		filter.Limit = maxLimit
	}
	state.RLock()
	path := state.path
	state.RUnlock()
	if path == "" {
		return Page{Events: []*Event{}, Total: 0}, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Page{Events: []*Event{}, Total: 0}, nil
	}
	if err != nil {
		return Page{}, fmt.Errorf("open connection log: %w", err)
	}
	defer file.Close()

	search := strings.ToLower(strings.TrimSpace(filter.Search))
	kind := strings.TrimSpace(filter.Kind)
	action := strings.TrimSpace(filter.Action)
	result := strings.TrimSpace(filter.Result)
	bridgeTransport := strings.TrimSpace(filter.BridgeTransport)
	proxyProtocol := strings.TrimSpace(filter.ProxyProtocol)
	matched := make([]*Event, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if filter.OwnerUserID != nil && event.UserID != *filter.OwnerUserID {
			continue
		}
		if filter.ClientID != nil && event.ClientID != *filter.ClientID {
			continue
		}
		if filter.AllowedClientIDs != nil {
			if _, ok := filter.AllowedClientIDs[event.ClientID]; !ok {
				continue
			}
		}
		if kind != "" && event.Kind != kind {
			continue
		}
		if action != "" && event.Action != action {
			continue
		}
		if result != "" && event.Result != result {
			continue
		}
		if bridgeTransport != "" && event.BridgeTransport != bridgeTransport {
			continue
		}
		if proxyProtocol != "" && event.ProxyProtocol != proxyProtocol {
			continue
		}
		if search != "" {
			haystack := strings.Join([]string{
				event.Action, event.Result, event.UserName, event.ClientRemark,
				event.ResourceType, event.ResourceRemark, event.BridgeTransport,
				event.ProxyProtocol, event.SourceIP, event.Target,
				event.ClientVersion, event.Error,
			}, " ")
			if !strings.Contains(strings.ToLower(haystack), search) {
				continue
			}
		}
		matched = append(matched, &event)
	}
	if err := scanner.Err(); err != nil {
		return Page{}, fmt.Errorf("read connection log: %w", err)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Time.Equal(matched[j].Time) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].Time.After(matched[j].Time)
	})
	total := len(matched)
	if filter.Offset >= total {
		return Page{Events: []*Event{}, Total: total}, nil
	}
	end := filter.Offset + filter.Limit
	if end > total {
		end = total
	}
	return Page{Events: matched[filter.Offset:end], Total: total}, nil
}

func write(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create connection log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open connection log: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write connection log: %w", err)
	}
	return nil
}

func oneLine(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
}

func newID(t time.Time) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("%d", t.UnixNano())
	}
	return t.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:])
}
