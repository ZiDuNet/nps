package bridge

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"github.com/pires/go-proxyproto"
)

func TestSecureClientHandshakeAuthenticatesClientAndServer(t *testing.T) {
	const verifyKey = "secure-handshake-test-key"
	db := &file.DbUtils{JsonDb: &file.JsonDb{}}
	clientRecord := &file.Client{Id: 17, VerifyKey: verifyKey, Status: true}
	db.JsonDb.Clients.Store(clientRecord.Id, clientRecord)
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverResult := make(chan struct {
		id  int
		err error
	}, 1)
	go func() {
		id, err := (&Bridge{}).authenticateClient(conn.NewConn(serverConn), "1.1.29", db)
		serverResult <- struct {
			id  int
			err error
		}{id: id, err: err}
	}()

	client := conn.NewConn(clientConn)
	marker, err := client.GetShortContent(32)
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != string(crypt.SecureHandshakeMarker()) {
		t.Fatalf("server marker = %q, want secure marker", marker)
	}
	if err := client.WriteLenContent(crypt.SecureHandshakeKeyID(verifyKey)); err != nil {
		t.Fatal(err)
	}
	challenge, err := client.GetShortLenContent()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := crypt.SecureHandshakeClientPayload(verifyKey, version.GetVersion(), "1.1.29", time.Now().Unix(), challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteLenContent(payload); err != nil {
		t.Fatal(err)
	}
	flag, err := client.ReadFlag()
	if err != nil {
		t.Fatal(err)
	}
	if flag != common.VERIFY_SUCCESS {
		t.Fatalf("server verification result = %q, want success", flag)
	}
	serverProof, err := client.GetShortContent(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := crypt.VerifySecureHandshakeServerProof(verifyKey, payload, challenge, serverProof); err != nil {
		t.Fatalf("server proof verification failed: %v", err)
	}
	// Modern clients also receive the Bridge certificate fingerprint and its
	// key-bound proof before the connection type is read.
	fingerprint, err := client.GetShortLenContent()
	if err != nil {
		t.Fatal(err)
	}
	fingerprintProof, err := client.GetShortContent(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := crypt.VerifySecureHandshakeFingerprintProof(verifyKey, fingerprint, fingerprintProof); err != nil {
		t.Fatalf("bridge fingerprint proof verification failed: %v", err)
	}
	result := <-serverResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.id != clientRecord.Id {
		t.Fatalf("authenticated client ID = %d, want %d", result.id, clientRecord.Id)
	}
}

func TestAuthenticateClientKeepsLegacyHandshakeThrough1128(t *testing.T) {
	const verifyKey = "legacy-cutoff-test-key"
	for _, clientVersion := range []string{"1.1.27", "1.1.28", "v1.1.28"} {
		t.Run(clientVersion, func(t *testing.T) {
			db := &file.DbUtils{JsonDb: &file.JsonDb{}}
			clientRecord := &file.Client{Id: 29, VerifyKey: verifyKey, Status: true}
			db.JsonDb.Clients.Store(clientRecord.Id, clientRecord)
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()

			serverResult := make(chan struct {
				id  int
				err error
			}, 1)
			go func() {
				id, err := (&Bridge{}).authenticateClient(conn.NewConn(serverConn), clientVersion, db)
				serverResult <- struct {
					id  int
					err error
				}{id: id, err: err}
			}()

			client := conn.NewConn(clientConn)
			serverHandshake, err := client.GetShortContent(32)
			if err != nil {
				t.Fatal(err)
			}
			if want := crypt.Md5(version.GetVersion()); string(serverHandshake) != want {
				t.Fatalf("legacy server response = %q, want %q", serverHandshake, want)
			}
			if _, err := client.Write([]byte(common.Getverifyval(verifyKey))); err != nil {
				t.Fatal(err)
			}
			flag, err := client.ReadFlag()
			if err != nil {
				t.Fatal(err)
			}
			if flag != common.VERIFY_SUCCESS {
				t.Fatalf("verification result = %q, want success", flag)
			}
			result := <-serverResult
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.id != clientRecord.Id {
				t.Fatalf("authenticated client ID = %d, want %d", result.id, clientRecord.Id)
			}
		})
	}
}

func TestAuthenticateClientRejectsUnknownRelease(t *testing.T) {
	db := &file.DbUtils{JsonDb: &file.JsonDb{}}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	if _, err := (&Bridge{}).authenticateClient(conn.NewConn(serverConn), "1.1.29-beta", db); err == nil {
		t.Fatal("a prerelease/unknown client version must not be treated as legacy")
	}
}

func TestWriteLocalProxyProtocolHeader(t *testing.T) {
	for _, test := range []struct {
		name    string
		version string
		want    byte
	}{
		{name: "v1", version: "V1", want: 1},
		{name: "v2", version: "V2", want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			type readResult struct {
				header *proxyproto.Header
				err    error
			}
			resultCh := make(chan readResult, 1)
			go func() {
				accepted, acceptErr := listener.Accept()
				if acceptErr != nil {
					resultCh <- readResult{err: acceptErr}
					return
				}
				defer accepted.Close()
				_ = accepted.SetReadDeadline(time.Now().Add(time.Second))
				header, readErr := proxyproto.Read(bufio.NewReader(accepted))
				resultCh <- readResult{header: header, err: readErr}
			}()

			backend, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()

			const source = "198.51.100.23:45678"
			if err := writeLocalProxyProtocolHeader(backend, source, test.version); err != nil {
				t.Fatal(err)
			}
			result := <-resultCh
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.header == nil || result.header.Version != test.want {
				t.Fatalf("header version = %#v, want %d", result.header, test.want)
			}
			if got := result.header.SourceAddr.String(); got != source {
				t.Fatalf("source address = %q, want %q", got, source)
			}
			if got, want := result.header.DestinationAddr.String(), backend.RemoteAddr().String(); got != want {
				t.Fatalf("destination address = %q, want %q", got, want)
			}
		})
	}
}

func TestWriteLocalProxyProtocolHeaderRejectsInvalidInput(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	acceptedCh := make(chan net.Conn, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptedCh <- accepted
		}
	}()
	backend, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	accepted := <-acceptedCh
	defer accepted.Close()

	for _, test := range []struct {
		name    string
		remote  string
		version string
	}{
		{name: "unknown version", remote: "198.51.100.23:45678", version: "V3"},
		{name: "malformed source address", remote: "invalid", version: "V1"},
		{name: "mixed address families", remote: "[2001:db8::1]:45678", version: "V2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := writeLocalProxyProtocolHeader(backend, test.remote, test.version); err == nil {
				t.Fatal("expected invalid PROXY Protocol input to be rejected")
			}
		})
	}
}

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

func TestSelectSessionRoundRobinUsesEveryLiveSignal(t *testing.T) {
	if err := SetClientSelectMode("roundrobin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetClientSelectMode("primary") })
	rootServer, rootPeer := net.Pipe()
	defer rootPeer.Close()
	variantServer, variantPeer := net.Pipe()
	defer variantPeer.Close()
	root := NewClient(nil, nil, conn.NewConn(rootServer), "1.1.29")
	variant := NewClient(nil, nil, conn.NewConn(variantServer), "1.1.30")
	variant.parent = root
	root.sessions = []*Client{root, variant}
	b := &Bridge{}
	b.Client.Store(9, root)
	first := b.selectSession(9, "signal")
	second := b.selectSession(9, "signal")
	if first != root || second != variant {
		t.Fatalf("round-robin sessions = %p, %p; want root then variant", first, second)
	}
}

func TestSelectSessionRandomCanChooseAllLiveSignals(t *testing.T) {
	if err := SetClientSelectMode("random"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetClientSelectMode("primary") })
	rootServer, rootPeer := net.Pipe()
	defer rootPeer.Close()
	variantServer, variantPeer := net.Pipe()
	defer variantPeer.Close()
	root := NewClient(nil, nil, conn.NewConn(rootServer), "1.1.29")
	variant := NewClient(nil, nil, conn.NewConn(variantServer), "1.1.30")
	variant.parent = root
	root.sessions = []*Client{root, variant}
	b := &Bridge{}
	b.Client.Store(10, root)
	seen := map[*Client]bool{}
	for i := 0; i < 128; i++ {
		seen[b.selectSession(10, "signal")] = true
	}
	if !seen[root] || !seen[variant] {
		t.Fatalf("random selection did not visit both sessions: %#v", seen)
	}
}

func TestDelClientClosesAllDuplicateSignals(t *testing.T) {
	rootServer, rootPeer := net.Pipe()
	defer rootPeer.Close()
	variantServer, variantPeer := net.Pipe()
	defer variantPeer.Close()
	root := NewClient(nil, nil, conn.NewConn(rootServer), "1.1.29")
	variant := NewClient(nil, nil, conn.NewConn(variantServer), "1.1.30")
	variant.parent = root
	root.sessions = []*Client{root, variant}
	b := &Bridge{CloseClient: make(chan int, 1)}
	b.Client.Store(11, root)
	b.DelClient(11)
	if _, ok := b.Client.Load(11); ok {
		t.Fatal("duplicate client entry survived DelClient")
	}
	for name, peer := range map[string]net.Conn{"root": rootPeer, "variant": variantPeer} {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		var one [1]byte
		if _, err := peer.Read(one[:]); err == nil {
			t.Fatalf("%s signal remained open after DelClient", name)
		}
	}
}

func TestSessionForLockedPairsDataWithNewestSignalWithoutData(t *testing.T) {
	if err := SetClientSelectMode("roundrobin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetClientSelectMode("primary") })
	root := NewClient(nil, nil, nil, "1.1.29")
	root.sessions = []*Client{root}
	firstServer, firstPeer := net.Pipe()
	defer firstPeer.Close()
	secondServer, secondPeer := net.Pipe()
	defer secondPeer.Close()
	first := NewClient(nil, nil, conn.NewConn(firstServer), "1.1.29")
	second := NewClient(nil, nil, conn.NewConn(secondServer), "1.1.29")
	first.parent, second.parent = root, root
	root.sessions = []*Client{first, second}
	b := &Bridge{}
	b.clientsMu.Lock()
	selected := b.sessionForLocked(root, "tunnel", "1.1.29")
	b.clientsMu.Unlock()
	if selected != second {
		t.Fatalf("data channel paired with %p, want newest signal %p", selected, second)
	}
}
