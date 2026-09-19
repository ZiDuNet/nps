package traffic

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Resource identifies the tunnel-like object whose traffic is being inspected.
// Hosts and ordinary tunnels use separate namespaces so an ID can never leak
// events from one resource type into the other.
type Resource struct {
	Kind string `json:"resource_kind"`
	ID   int    `json:"resource_id"`
}

// Event is deliberately structured instead of being a formatted log line. The
// browser can render request metadata and body chunks without parsing strings,
// while the proxy remains free to discard events when nobody is watching.
type Event struct {
	Type            string            `json:"type"`
	Time            time.Time         `json:"time"`
	Resource        Resource          `json:"resource"`
	RequestID       string            `json:"request_id,omitempty"`
	Method          string            `json:"method,omitempty"`
	Scheme          string            `json:"scheme,omitempty"`
	Host            string            `json:"host,omitempty"`
	Path            string            `json:"path,omitempty"`
	RemoteAddr      string            `json:"remote_addr,omitempty"`
	ForwardedFor    string            `json:"forwarded_for,omitempty"`
	ListenerAddr    string            `json:"listener_addr,omitempty"`
	TargetAddr      string            `json:"target_addr,omitempty"`
	Status          int               `json:"status,omitempty"`
	DurationMS      int64             `json:"duration_ms,omitempty"`
	BytesIn         int64             `json:"bytes_in,omitempty"`
	BytesOut        int64             `json:"bytes_out,omitempty"`
	ContentType     string            `json:"content_type,omitempty"`
	ContentEncoding string            `json:"content_encoding,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Part            string            `json:"part,omitempty"`
	Body            string            `json:"body,omitempty"`
	BodyEncoding    string            `json:"body_encoding,omitempty"`
	BodyTruncated   bool              `json:"body_truncated,omitempty"`
	BodySkipped     bool              `json:"body_skipped,omitempty"`
	Complete        bool              `json:"complete,omitempty"`
	Dropped         uint64            `json:"dropped,omitempty"`
	Error           string            `json:"error,omitempty"`
}

const (
	// MaxBodyBytes bounds the transient body preview per direction. The proxy
	// continues forwarding bytes after this limit is reached.
	MaxBodyBytes = 256 << 10

	subscriberBuffer  = 128
	maxBodyChunkBytes = 16 << 10
)

type subscription struct {
	ch      chan Event
	dropped atomic.Uint64
}

type hub struct {
	mu      sync.RWMutex
	nextID  uint64
	streams map[Resource]map[uint64]*subscription
}

var defaultHub = &hub{streams: make(map[Resource]map[uint64]*subscription)}

// Subscribe starts a best-effort live stream. No event replay is performed;
// this is intentional because traffic previews are transient and never stored.
func Subscribe(resource Resource) (<-chan Event, func()) {
	stream := &subscription{ch: make(chan Event, subscriberBuffer)}
	defaultHub.mu.Lock()
	defaultHub.nextID++
	id := defaultHub.nextID
	if defaultHub.streams[resource] == nil {
		defaultHub.streams[resource] = make(map[uint64]*subscription)
	}
	defaultHub.streams[resource][id] = stream
	defaultHub.mu.Unlock()

	var once sync.Once
	closeStream := func() {
		once.Do(func() {
			defaultHub.mu.Lock()
			if streams := defaultHub.streams[resource]; streams != nil {
				if current, ok := streams[id]; ok && current == stream {
					delete(streams, id)
					close(stream.ch)
				}
				if len(streams) == 0 {
					delete(defaultHub.streams, resource)
				}
			}
			defaultHub.mu.Unlock()
		})
	}
	return stream.ch, closeStream
}

// HasSubscribers lets hot proxy paths avoid creating body copies when the
// debug panel is closed. It is only an optimization; Publish remains safe.
func HasSubscribers(resource Resource) bool {
	defaultHub.mu.RLock()
	active := len(defaultHub.streams[resource]) > 0
	defaultHub.mu.RUnlock()
	return active
}

// Publish never waits for a browser. A full subscriber queue increments its
// drop counter and the next successful event carries an overflow marker.
func Publish(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	defaultHub.mu.RLock()
	streams := defaultHub.streams[event.Resource]
	for _, stream := range streams {
		if dropped := stream.dropped.Swap(0); dropped > 0 {
			overflow := Event{Type: "dropped", Time: event.Time, Resource: event.Resource, Dropped: dropped}
			select {
			case stream.ch <- overflow:
			default:
				stream.dropped.Add(dropped)
			}
		}
		select {
		case stream.ch <- event:
		default:
			stream.dropped.Add(1)
		}
	}
	defaultHub.mu.RUnlock()
}

// NewRequestID creates an opaque short-lived identifier for correlating body
// chunks with their request/response metadata. It contains no user data.
func NewRequestID() string {
	var raw [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(raw[:])
}

// BodyCapture copies only inspectable text and publishes chunks without ever
// placing backpressure on the caller. A nil or non-text capture is cheap.
type BodyCapture struct {
	resource  Resource
	requestID string
	part      string
	limit     int
	seen      int
	truncated bool
	active    bool
	endOnce   sync.Once
}

func NewBodyCapture(resource Resource, requestID, part, contentType, disposition string) *BodyCapture {
	contentType = strings.TrimSpace(strings.ToLower(strings.SplitN(contentType, ";", 2)[0]))
	active := HasSubscribers(resource) && IsInspectableContentType(contentType, disposition)
	return &BodyCapture{
		resource:  resource,
		requestID: requestID,
		part:      part,
		limit:     MaxBodyBytes,
		active:    active,
	}
}

func (c *BodyCapture) Active() bool { return c != nil && c.active }

func (c *BodyCapture) Write(p []byte) {
	if c == nil || !c.active || len(p) == 0 {
		return
	}
	if c.seen >= c.limit {
		c.truncated = true
		return
	}
	remaining := c.limit - c.seen
	if len(p) > remaining {
		p = p[:remaining]
		c.truncated = true
	}
	c.seen += len(p)
	for len(p) > 0 {
		n := len(p)
		if n > maxBodyChunkBytes {
			n = maxBodyChunkBytes
		}
		Publish(Event{Type: "body_chunk", Resource: c.resource, RequestID: c.requestID, Part: c.part, Body: string(p[:n]), BodyEncoding: "utf-8"})
		p = p[n:]
	}
}

func (c *BodyCapture) End() {
	if c == nil || !c.active {
		return
	}
	c.endOnce.Do(func() {
		Publish(Event{Type: "body_end", Resource: c.resource, RequestID: c.requestID, Part: c.part, BodyTruncated: c.truncated, Complete: true})
	})
}

func IsInspectableContentType(contentType, disposition string) bool {
	contentType = strings.TrimSpace(strings.ToLower(strings.SplitN(contentType, ";", 2)[0]))
	disposition = strings.ToLower(disposition)
	if strings.Contains(disposition, "attachment") || strings.HasPrefix(contentType, "multipart/") {
		return false
	}
	if contentType == "" {
		return false
	}
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/ld+json", "application/xml", "text/xml", "application/graphql", "application/x-www-form-urlencoded":
		return true
	default:
		return false
	}
}

// SafeHeaders keeps the complete request/response headers for the live
// inspector. Values are collapsed to one line for SSE; callers explicitly
// opted into an authorized, non-persistent management debug stream.
func SafeHeaders(header http.Header) map[string]string {
	if header == nil {
		return nil
	}
	result := make(map[string]string)
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		result[key] = strings.Join(strings.Fields(strings.Join(values, ", ")), " ")
	}
	return result
}
