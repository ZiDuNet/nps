package bridge

import (
	"net"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
)

func TestSanitizePublicClientOwnsIdentityFields(t *testing.T) {
	candidate := &file.Client{Id: 42, UserId: 9, NoStore: false, NoDisplay: true, Status: false}
	if err := sanitizePublicClient(candidate); err != nil {
		t.Fatalf("sanitize public client: %v", err)
	}
	candidate.RLock()
	id, userID := candidate.Id, candidate.UserId
	noStore, noDisplay, status := candidate.NoStore, candidate.NoDisplay, candidate.Status
	candidate.RUnlock()
	if id != 0 || userID != 0 || !noStore || noDisplay || !status {
		t.Fatalf("identity fields were not normalized: id=%d user=%d noStore=%t noDisplay=%t status=%t", id, userID, noStore, noDisplay, status)
	}
}

func TestHealthDisconnectDoesNotDeleteReplacementSignal(t *testing.T) {
	oldServer, oldPeer := net.Pipe()
	defer oldServer.Close()
	newServer, newPeer := net.Pipe()
	defer newServer.Close()
	defer newPeer.Close()

	b := &Bridge{}
	b.Client.Store(7, NewClient(nil, nil, conn.NewConn(newServer), "test"))
	done := make(chan struct{})
	go func() {
		b.GetHealthFromClient(7, conn.NewConn(oldServer))
		close(done)
	}()

	if err := oldPeer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health reader did not exit after old signal closed")
	}
	if _, ok := b.Client.Load(7); !ok {
		t.Fatal("old health reader removed the replacement client session")
	}
}

func TestPingSnapshotDoesNotDeleteReplacementSession(t *testing.T) {
	b := &Bridge{}
	oldSignal := conn.NewConn(nil)
	newSignal := conn.NewConn(nil)
	cl := NewClient(nil, nil, oldSignal, "test")
	b.Client.Store(7, cl)
	snapshot := clientSessionSnapshot{client: cl, signal: oldSignal}

	cl.mu.Lock()
	cl.signal = newSignal
	cl.mu.Unlock()
	b.delClientIfSnapshot(7, snapshot)

	v, ok := b.Client.Load(7)
	if !ok || v.(*Client) != cl {
		t.Fatal("stale ping snapshot deleted replacement session")
	}
}
