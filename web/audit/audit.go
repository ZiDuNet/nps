// Package audit provides a small, local operation-audit journal for the
// management plane. It is deliberately independent from the runtime traffic
// logger: audit records describe control-plane actions and never contain
// credentials or proxied request/response bodies.
package audit

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
)

const (
	ResultSuccess = "success"
	ResultFailure = "failure"
	defaultLimit  = 100
	maxLimit      = 500
)

// Event is the persisted, non-sensitive description of one management-plane
// operation. OwnerUserID is used to enforce normal-user visibility; zero means
// the resource is global or currently unassigned.
type Event struct {
	ID           string            `json:"id"`
	Time         time.Time         `json:"time"`
	ActorType    string            `json:"actor_type"`
	ActorID      int               `json:"actor_id,omitempty"`
	ActorName    string            `json:"actor_name,omitempty"`
	SourceIP     string            `json:"source_ip,omitempty"`
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path,omitempty"`
	Action       string            `json:"action"`
	ResourceType string            `json:"resource_type,omitempty"`
	ResourceID   int               `json:"resource_id,omitempty"`
	OwnerUserID  int               `json:"owner_user_id,omitempty"`
	Result       string            `json:"result"`
	Error        string            `json:"error,omitempty"`
	Details      map[string]string `json:"details,omitempty"`
}

// Filter controls an audit query. OwnerUserID is enforced by the controller,
// while the package also applies it so callers cannot accidentally widen a
// scoped query after authorization has been decided.
type Filter struct {
	Offset       int
	Limit        int
	Search       string
	Action       string
	ResourceType string
	Result       string
	OwnerUserID  *int
}

// Page contains a newest-first page and the total number of matching records.
type Page struct {
	Events []*Event
	Total  int
}

var journal = struct {
	sync.RWMutex
	path string
}{
	path: filepath.Join(common.GetRunPath(), "audit.log"),
}

// Configure changes the journal location. Relative paths are resolved against
// the NPS run directory, matching the behavior of the other local files.
func Configure(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = filepath.Join(common.GetRunPath(), "audit.log")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(common.GetRunPath(), path)
	}
	journal.Lock()
	journal.path = filepath.Clean(path)
	journal.Unlock()
}

// Path returns the current journal path. It is mainly useful for diagnostics
// and tests; callers should not write the file directly.
func Path() string {
	journal.RLock()
	defer journal.RUnlock()
	return journal.path
}

// Append appends one record atomically with respect to other audit writers.
// Management operations call this after their state change; a journal error
// should be reported to the runtime log by the caller but must not interrupt
// an already completed tunnel or client operation.
func Append(event Event) error {
	if strings.TrimSpace(event.Action) == "" {
		return errors.New("audit action is required")
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	if event.ID == "" {
		event.ID = newID(event.Time)
	}
	if event.Result == "" {
		event.Result = ResultSuccess
	}
	// Keep the record compact and avoid accidental line breaks in the table.
	event.Error = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(event.Error, "\r", " "), "\n", " "))

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}

	journal.Lock()
	defer journal.Unlock()
	if err := os.MkdirAll(filepath.Dir(journal.path), 0700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(journal.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open audit journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write audit journal: %w", err)
	}
	return nil
}

// List reads the append-only journal and returns a bounded newest-first page.
// A malformed line is ignored so one interrupted write cannot hide all later
// audit records.
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
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	action := strings.TrimSpace(filter.Action)
	resourceType := strings.TrimSpace(filter.ResourceType)
	result := strings.TrimSpace(filter.Result)

	journal.RLock()
	path := journal.path
	journal.RUnlock()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Page{Events: []*Event{}, Total: 0}, nil
	}
	if err != nil {
		return Page{}, fmt.Errorf("open audit journal: %w", err)
	}
	defer file.Close()

	matched := make([]*Event, 0)
	scanner := bufio.NewScanner(file)
	// Details are intentionally small, but permit a generous line size so a
	// future safe metadata field cannot make the entire journal unreadable.
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if filter.OwnerUserID != nil && event.OwnerUserID != *filter.OwnerUserID {
			continue
		}
		if action != "" && event.Action != action {
			continue
		}
		if resourceType != "" && event.ResourceType != resourceType {
			continue
		}
		if result != "" && event.Result != result {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(event.Action+" "+event.ResourceType+" "+event.ActorName+" "+event.SourceIP+" "+event.Error), search) {
			continue
		}
		matched = append(matched, &event)
	}
	if err := scanner.Err(); err != nil {
		return Page{}, fmt.Errorf("read audit journal: %w", err)
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

func newID(t time.Time) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("%d", t.UnixNano())
	}
	return t.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:])
}
