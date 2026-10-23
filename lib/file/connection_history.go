package file

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// ConnectionEvent pairs one online transition with its confirmed offline
// transition. OfflineTime remains empty while the client is currently online.
type ConnectionEvent struct {
	OnlineTime  string
	OfflineTime string
}

// ConnectionHistory is bounded and safe to update from bridge cleanup and
// reconnect goroutines. Its custom JSON methods keep the mutex out of the
// persisted representation and accept the older event-only format.
type ConnectionHistory struct {
	mu     sync.RWMutex
	events []ConnectionEvent
	online bool
}

var connectionHistoryLimit atomic.Int32

func init() { connectionHistoryLimit.Store(5) }

// SetConnectionHistoryLimit configures the number of recent sessions retained;
// zero disables history while preserving the rest of the client record.
func SetConnectionHistoryLimit(limit int) {
	if limit < 0 {
		limit = 0
	}
	if limit > 100 {
		limit = 100
	}
	connectionHistoryLimit.Store(int32(limit))
}

func getConnectionHistoryLimit() int { return int(connectionHistoryLimit.Load()) }

func (h *ConnectionHistory) trimLocked(limit int) {
	if limit == 0 {
		h.events = nil
		return
	}
	if len(h.events) > limit {
		h.events = append([]ConnectionEvent(nil), h.events[len(h.events)-limit:]...)
	}
}

func (h *ConnectionHistory) record(online bool, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	limit := getConnectionHistoryLimit()
	h.trimLocked(limit)
	if limit == 0 || h.online == online {
		h.online = online
		return
	}
	h.online = online
	stamp := now.Format("2006-01-02 15:04:05")
	if online {
		h.events = append(h.events, ConnectionEvent{OnlineTime: stamp})
	} else if len(h.events) > 0 {
		h.events[len(h.events)-1].OfflineTime = stamp
	}
	h.trimLocked(limit)
}

func (h *ConnectionHistory) MarshalJSON() ([]byte, error) {
	if h == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(h.snapshot())
}

func (h *ConnectionHistory) snapshot() []ConnectionEvent {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	h.trimLocked(getConnectionHistoryLimit())
	events := append([]ConnectionEvent(nil), h.events...)
	h.mu.Unlock()
	return events
}

func (h *ConnectionHistory) UnmarshalJSON(data []byte) error {
	var stored []struct {
		OnlineTime  string
		OfflineTime string
		Time        string
		Online      bool
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	events := make([]ConnectionEvent, 0, len(stored))
	for _, item := range stored {
		switch {
		case item.OnlineTime != "":
			events = append(events, ConnectionEvent{OnlineTime: item.OnlineTime, OfflineTime: item.OfflineTime})
		case item.Time != "" && item.Online:
			events = append(events, ConnectionEvent{OnlineTime: item.Time})
		case item.Time != "" && len(events) > 0:
			events[len(events)-1].OfflineTime = item.Time
		}
	}
	h.mu.Lock()
	h.events = events
	h.trimLocked(getConnectionHistoryLimit())
	// A restart cannot prove that the last persisted session is still online.
	h.online = false
	h.mu.Unlock()
	return nil
}

func (c *Client) RecordConnection(online bool, now time.Time) {
	if c == nil || c.Id <= 0 {
		return
	}
	c.RLock()
	history := c.ConnectionHistory
	c.RUnlock()
	if history != nil {
		history.record(online, now)
	}
}

// ConnectionHistorySnapshot returns a copy for management APIs and templates.
func (c *Client) ConnectionHistorySnapshot() []ConnectionEvent {
	if c == nil {
		return nil
	}
	c.RLock()
	history := c.ConnectionHistory
	c.RUnlock()
	if history == nil {
		return nil
	}
	return history.snapshot()
}
