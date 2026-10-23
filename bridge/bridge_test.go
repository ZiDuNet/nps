package bridge

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
)

func bridgeHealthProfileKey(health file.Health) string {
	snapshot := file.HealthConfigSnapshot(&health)
	if err := file.NormalizeHealth(&snapshot); err != nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s",
		snapshot.HealthCheckType,
		snapshot.HealthCheckTarget,
		snapshot.HealthCheckTimeout,
		snapshot.HealthMaxFail,
		snapshot.HealthCheckInterval,
		snapshot.HttpHealthUrl,
	)
}

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

func TestRequestClientUpdateWritesSignal(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	b := &Bridge{}
	b.Client.Store(7, NewClient(nil, nil, conn.NewConn(serverConn), "test"))
	read := make(chan string, 1)
	go func() {
		buf := make([]byte, len(common.CLIENT_UPDATE))
		_, _ = io.ReadFull(clientConn, buf)
		read <- string(buf)
	}()
	if err := b.RequestClientUpdate(7); err != nil {
		t.Fatalf("RequestClientUpdate: %v", err)
	}
	select {
	case got := <-read:
		if got != common.CLIENT_UPDATE {
			t.Fatalf("signal = %q, want %q", got, common.CLIENT_UPDATE)
		}
	case <-time.After(time.Second):
		t.Fatal("update signal was not written")
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

func TestHealthProfilesForClientFiltersDeduplicatesAndSnapshots(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	client := &file.Client{Id: 7}
	other := &file.Client{Id: 8}
	db.Clients.Store(client.Id, client)
	db.Clients.Store(other.Id, other)

	first := &file.Tunnel{
		Id: 1, Mode: "tcp", Status: true, Client: client,
		Health: file.Health{HealthCheckType: "HTTP", HealthCheckTarget: "127.0.0.1:8080", HealthCheckTimeout: 2, HealthMaxFail: 3, HealthCheckInterval: 10, HttpHealthUrl: "/healthz"},
	}
	duplicate := &file.Tunnel{
		Id: 2, Mode: "tcp", Status: true, Client: client,
		Health: file.Health{HealthCheckType: "http", HealthCheckTarget: "127.0.0.1:8080", HealthCheckTimeout: 2, HealthMaxFail: 3, HealthCheckInterval: 10, HttpHealthUrl: "/healthz"},
	}
	// Inactive, non-TCP and another client's rules must not be sent.
	inactive := &file.Tunnel{Id: 3, Mode: "tcp", Status: false, Client: client, Health: first.Health}
	udp := &file.Tunnel{Id: 4, Mode: "udp", Status: true, Client: client, Health: first.Health}
	foreign := &file.Tunnel{Id: 5, Mode: "tcp", Status: true, Client: other, Health: first.Health}
	for _, tunnel := range []*file.Tunnel{first, duplicate, inactive, udp, foreign} {
		db.Tasks.Store(tunnel.Id, tunnel)
	}
	db.Hosts.Store(6, &file.Host{
		Id: 6, Client: client, IsClose: false,
		Health: file.Health{HealthCheckType: "tcp", HealthCheckTarget: "127.0.0.1:22", HealthCheckTimeout: 1, HealthMaxFail: 2, HealthCheckInterval: 5},
	})

	profiles := healthProfilesForClientFromDB(&file.DbUtils{JsonDb: db}, client.Id)
	if len(profiles) != 2 {
		t.Fatalf("profiles = %d, want deduplicated TCP + Host profiles: %#v", len(profiles), profiles)
	}
	seen := make(map[string]bool)
	for _, profile := range profiles {
		if profile.HealthNextTime.IsZero() == false || profile.HealthMap != nil || len(profile.HealthRemoveArr) != 0 {
			t.Fatalf("runtime fields leaked into distributed profile: %#v", profile)
		}
		seen[bridgeHealthProfileKey(profile)] = true
	}
	if !seen[bridgeHealthProfileKey(first.Health)] || !seen[bridgeHealthProfileKey(file.Health{HealthCheckType: "tcp", HealthCheckTarget: "127.0.0.1:22", HealthCheckTimeout: 1, HealthMaxFail: 2, HealthCheckInterval: 5})] {
		t.Fatalf("unexpected profiles: %#v", profiles)
	}
}

func TestRequestClientHealthConfigIsVersionGatedAndWireCompatible(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	clientRecord := &file.Client{Id: 7}
	db.Clients.Store(clientRecord.Id, clientRecord)
	db.Tasks.Store(1, &file.Tunnel{
		Id: 1, Mode: "tcp", Status: true, Client: clientRecord,
		Health: file.Health{HealthCheckType: "tcp", HealthCheckTarget: "127.0.0.1:22", HealthCheckTimeout: 1, HealthMaxFail: 2, HealthCheckInterval: 5},
	})

	testCases := []struct {
		name       string
		version    string
		wantSignal bool
	}{
		{name: "new NPC receives additive frame", version: "1.1.29", wantSignal: true},
		{name: "old NPC keeps legacy stream", version: "1.1.28", wantSignal: false},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()
			bridge := &Bridge{}
			bridge.Client.Store(7, NewClient(nil, nil, conn.NewConn(serverConn), test.version))

			errCh := make(chan error, 1)
			go func() { errCh <- bridge.requestClientHealthConfig(7, &file.DbUtils{JsonDb: db}) }()
			if test.wantSignal {
				flag := make([]byte, 4)
				if _, err := io.ReadFull(clientConn, flag); err != nil {
					t.Fatalf("read health flag: %v", err)
				}
				if string(flag) != common.HEALTH_CONFIG {
					t.Fatalf("flag = %q, want %q", flag, common.HEALTH_CONFIG)
				}
				var length int32
				if err := binary.Read(clientConn, binary.LittleEndian, &length); err != nil {
					t.Fatalf("read health payload length: %v", err)
				}
				if length <= 0 {
					t.Fatalf("health payload length = %d, want positive", length)
				}
				payload := make([]byte, length)
				if _, err := io.ReadFull(clientConn, payload); err != nil {
					t.Fatalf("read health payload: %v", err)
				}
				var profiles []file.Health
				if err := json.Unmarshal(payload, &profiles); err != nil {
					t.Fatalf("decode health payload: %v", err)
				}
				if len(profiles) != 1 || profiles[0].HealthCheckTarget != "127.0.0.1:22" {
					t.Fatalf("profiles = %#v", profiles)
				}
			} else {
				if err := clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				one := make([]byte, 1)
				if _, err := clientConn.Read(one); err == nil {
					t.Fatalf("old NPC unexpectedly received health configuration")
				}
			}
			if err := <-errCh; err != nil {
				t.Fatalf("requestClientHealthConfig: %v", err)
			}
		})
	}
}
