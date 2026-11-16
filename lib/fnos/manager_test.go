package fnos

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeConnection struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
	kicked  bool
}

func (f *fakeConnection) Start() {
	close(f.started)
	if !f.kicked {
		<-f.stopped
	}
}
func (f *fakeConnection) Close()       { f.once.Do(func() { close(f.stopped) }) }
func (f *fakeConnection) Kicked() bool { return f.kicked }

func receive(t *testing.T, ch <-chan *fakeConnection) *fakeConnection {
	t.Helper()
	select {
	case f := <-ch:
		select {
		case <-f.started:
		case <-time.After(time.Second):
			t.Fatal("connection was not started")
		}
		return f
	case <-time.After(time.Second):
		t.Fatal("connection was not created")
		return nil
	}
}

func TestManagerOnlyRestartsChangedProfile(t *testing.T) {
	created := make(chan *fakeConnection, 8)
	m := Manager{Factory: func(Entry) Connection {
		f := &fakeConnection{started: make(chan struct{}), stopped: make(chan struct{})}
		created <- f
		return f
	}}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewConfig()
	c.Connections = []Entry{entry(t, MainID, "one")}
	m.Apply(ctx, c)
	one := receive(t, created)
	c.Connections = append(c.Connections, entry(t, "abcdef0123456789", "two"))
	m.Apply(ctx, c)
	two := receive(t, created)
	c.Connections[0].Raw += "# edit\n"
	m.Apply(ctx, c)
	_ = receive(t, created)
	select {
	case <-one.stopped:
	default:
		t.Fatal("edited connection stayed running")
	}
	select {
	case <-two.stopped:
		t.Fatal("editing another connection stopped this one")
	default:
	}
	c.Connections[1].Enabled = false
	m.Apply(ctx, c)
	select {
	case <-two.stopped:
	default:
		t.Fatal("disabled connection stayed running")
	}
	m.Apply(ctx, NewConfig())
	c.Connections[0].Enabled = true
	m.Apply(ctx, c)
	_ = receive(t, created)
}

func TestManagerKickDoesNotRestartOrStopOthers(t *testing.T) {
	created := make(chan *fakeConnection, 8)
	m := Manager{Retry: time.Millisecond, Factory: func(e Entry) Connection {
		f := &fakeConnection{started: make(chan struct{}), stopped: make(chan struct{}), kicked: e.ID == MainID}
		created <- f
		return f
	}}
	defer m.Close()
	c := NewConfig()
	c.Connections = []Entry{entry(t, MainID, "one"), entry(t, "abcdef0123456789", "two")}
	m.Apply(context.Background(), c)
	_ = receive(t, created)
	_ = receive(t, created)
	select {
	case <-m.slots[MainID].done:
	case <-time.After(time.Second):
		t.Fatal("kicked slot did not finish")
	}
	m.Apply(context.Background(), c)
	if len(created) != 0 {
		t.Fatal("unchanged kicked slot was restarted")
	}
	select {
	case <-m.slots["abcdef0123456789"].done:
		t.Fatal("other connection stopped on kick")
	default:
	}
}

func TestManagerCleanupAndResetLog(t *testing.T) {
	created := make(chan *fakeConnection, 8)
	var cleaned, reset int
	m := Manager{
		Retry: time.Millisecond,
		Factory: func(Entry) Connection {
			f := &fakeConnection{started: make(chan struct{}), stopped: make(chan struct{})}
			created <- f
			return f
		},
		Cleanup:  func(Entry) { cleaned++ },
		ResetLog: func(Entry) { reset++ },
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewConfig()
	c.Connections = []Entry{entry(t, MainID, "one")}
	m.Apply(ctx, c)
	_ = receive(t, created)

	renamed := entry(t, MainID, "one")
	renamed.Command.Name = "renamed"
	c.Connections = []Entry{renamed}
	m.Apply(ctx, c)
	_ = receive(t, created)
	if reset != 1 || cleaned != 0 {
		t.Fatalf("rename should reset the log once and never clean up: reset=%d cleaned=%d", reset, cleaned)
	}

	m.Apply(ctx, NewConfig())
	if cleaned != 1 || reset != 1 {
		t.Fatalf("delete should clean up once: reset=%d cleaned=%d", reset, cleaned)
	}
}
