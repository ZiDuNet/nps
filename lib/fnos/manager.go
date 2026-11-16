package fnos

import (
	"context"
	"sync"
	"time"
)

// Connection is satisfied by client.TRPClient. Each entry owns a separate
// instance; native configuration must never stop the generic Npc system service.
type Connection interface {
	Start()
	Close()
	Kicked() bool
}

type memoryReporter interface {
	Memory() int64
}

type slot struct {
	entry  Entry
	cancel context.CancelFunc
	done   chan struct{}
	mem    func() int64
}

type stoppedSlot struct {
	running     *slot
	deleted     bool
	nameChanged bool
}

type Manager struct {
	mu      sync.Mutex
	slots   map[string]*slot
	Factory func(Entry) Connection
	Event   func(Entry, string)
	Status  func(Entry, bool)
	Cleanup func(Entry)
	ResetLog func(Entry)
	Retry   time.Duration
}

func (m *Manager) event(entry Entry, message string) {
	if m.Event != nil {
		m.Event(entry, message)
	}
}

func (m *Manager) setStatus(entry Entry, running bool) {
	if m.Status != nil {
		m.Status(entry, running)
	}
}

func (m *Manager) setMemory(id string, getter func() int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slots == nil {
		return
	}
	if running, ok := m.slots[id]; ok {
		running.mem = getter
	}
}

// Memory returns the resident memory reported by the connection currently
// owned by id, in bytes. A stopped or unknown connection reports 0.
func (m *Manager) Memory(id string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slots == nil {
		return 0
	}
	running, ok := m.slots[id]
	if !ok || running.mem == nil {
		return 0
	}
	return running.mem()
}

// Apply is called serially by the file watcher. Only changed/disabled/deleted
// entries are stopped, and the old loop exits before its replacement starts.
func (m *Manager) Apply(ctx context.Context, config Config) {
	m.mu.Lock()
	if m.slots == nil {
		m.slots = map[string]*slot{}
	}
	wanted := make(map[string]Entry)
	for _, entry := range config.Connections {
		if entry.Enabled {
			wanted[entry.ID] = entry
		}
	}
	stopped := []stoppedSlot{}
	for id, running := range m.slots {
		entry, exists := wanted[id]
		if !exists || entry != running.entry {
			running.cancel()
			nameChanged := exists && entry.Command.Name != running.entry.Command.Name
			stopped = append(stopped, stoppedSlot{running: running, deleted: !exists, nameChanged: nameChanged})
			delete(m.slots, id)
		}
	}
	m.mu.Unlock()

	// Wait for replaced or removed slots to fully exit before starting their
	// replacements, preserving the old-before-new ordering across retries.
	for _, stoppedSlot := range stopped {
		<-stoppedSlot.running.done
		if stoppedSlot.deleted {
			if m.Cleanup != nil {
				m.Cleanup(stoppedSlot.running.entry)
			}
		} else if stoppedSlot.nameChanged && m.ResetLog != nil {
			m.ResetLog(stoppedSlot.running.entry)
		}
	}

	m.mu.Lock()
	for id, entry := range wanted {
		if _, exists := m.slots[id]; exists || ctx.Err() != nil {
			continue
		}
		child, cancel := context.WithCancel(ctx)
		running := &slot{entry: entry, cancel: cancel, done: make(chan struct{})}
		m.slots[id] = running
		go m.run(child, running)
	}
	m.mu.Unlock()
}

func (m *Manager) Close() {
	m.mu.Lock()
	slots := make([]*slot, 0, len(m.slots))
	for id, running := range m.slots {
		running.cancel()
		slots = append(slots, running)
		delete(m.slots, id)
	}
	m.mu.Unlock()
	for _, running := range slots {
		<-running.done
	}
}

func (m *Manager) run(ctx context.Context, running *slot) {
	defer close(running.done)
	entry := running.entry
	m.setStatus(entry, true)
	defer m.setStatus(entry, false)
	m.event(entry, "启动独立连接")
	defer m.event(entry, "连接已停止")
	for ctx.Err() == nil {
		connection := m.Factory(entry)
		if reporter, ok := connection.(memoryReporter); ok {
			m.setMemory(entry.ID, reporter.Memory)
		} else {
			m.setMemory(entry.ID, nil)
		}
		finished := make(chan struct{})
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			select {
			case <-ctx.Done():
				connection.Close()
			case <-finished:
			}
		}()
		connection.Start()
		close(finished)
		m.setMemory(entry.ID, nil)
		<-closed
		connection.Close()
		if ctx.Err() != nil {
			return
		}
		if connection.Kicked() {
			m.event(entry, "服务端已删除此客户端，停止本条重连；其他连接不受影响。需要恢复时请在应用设置中停用再启用此条连接")
			return
		}
		delay := m.Retry
		if delay <= 0 {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
