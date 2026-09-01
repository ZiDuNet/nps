package proxy

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

type whitelistTestBridge struct {
	calls atomic.Int32
}

func (b *whitelistTestBridge) SendLinkInfo(int, *conn.Link, *file.Tunnel) (net.Conn, error) {
	b.calls.Add(1)
	return nil, errors.New("unexpected upstream connection")
}

type whitelistRemoteConn struct {
	net.Conn
	remote net.Addr
}

func (c *whitelistRemoteConn) RemoteAddr() net.Addr {
	return c.remote
}

// installWhitelistTestHost replaces only the in-memory JsonDb used by this
// package test. Tests in this package are serial, so restoring the pointer is
// sufficient and avoids writing to the repository's runtime data files.
func installWhitelistTestHost(t *testing.T, host *file.Host) {
	t.Helper()
	db := file.GetDb()
	old := db.JsonDb
	testDB := file.NewJsonDb(t.TempDir())
	db.JsonDb = testDB
	testDB.Hosts.Store(host.Id, host)
	t.Cleanup(func() {
		db.JsonDb = old
	})
}

func newWhitelistTestClient() *file.Client {
	client := file.NewClient("test-vkey", true, true)
	client.Id = 1
	client.IpWhite = true
	client.IpWhitePass = "secret"
	client.IpWhiteList = []string{"203.0.113.7"}
	return client
}

func TestIPWhiteBlockDecision(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.IpWhite = true
	client.IpWhitePass = "secret"
	client.IpWhiteList = []string{"203.0.113.7"}

	if !isIPWhiteBlocked(client, "203.0.113.8:443") {
		t.Fatal("an address outside the allowlist must be blocked")
	}
	if isIPWhiteBlocked(client, "203.0.113.7:443") {
		t.Fatal("an address in the allowlist must be accepted")
	}
	client.IpWhite = false
	if isIPWhiteBlocked(client, "203.0.113.8:443") {
		t.Fatal("a disabled allowlist must not block traffic")
	}
}

func TestIPWhiteBlockDecisionSupportsIPv6(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.IpWhite = true
	client.IpWhitePass = "secret"
	client.IpWhiteList = []string{"2001:db8::7"}

	if !isIPWhiteBlocked(client, "[2001:db8::8]:443") {
		t.Fatal("an IPv6 address outside the allowlist must be blocked")
	}
	if isIPWhiteBlocked(client, "[2001:db8::7]:443") {
		t.Fatal("an IPv6 address in the allowlist must be accepted")
	}
}

func TestIPWhiteSnapshotIsSafeDuringConcurrentUpdates(t *testing.T) {
	client := newWhitelistTestClient()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = isIPWhiteBlocked(client, "203.0.113.8:443")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			client.Lock()
			if i%2 == 0 {
				client.IpWhiteList = []string{"203.0.113.7"}
			} else {
				client.IpWhiteList = []string{"203.0.113.8"}
			}
			client.Unlock()
		}
	}()
	wg.Wait()
}

func TestDealClientBlocksUnauthorizedIPBeforeBridge(t *testing.T) {
	db := file.GetDb()
	oldDB := db.JsonDb
	db.JsonDb = file.NewJsonDb(t.TempDir())
	t.Cleanup(func() { db.JsonDb = oldDB })

	client := newWhitelistTestClient()
	bridge := &whitelistTestBridge{}
	server := &BaseServer{bridge: bridge}
	peer, handler := net.Pipe()
	defer peer.Close()
	handlerConn := &whitelistRemoteConn{
		Conn:   handler,
		remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.8"), Port: 4321},
	}

	if err := server.DealClient(conn.NewConn(handlerConn), client, "127.0.0.1:22", nil, "tcp", nil, nil, false, nil, nil); err != nil {
		t.Fatalf("DealClient returned unexpected error: %v", err)
	}
	if calls := bridge.calls.Load(); calls != 0 {
		t.Fatalf("blocked TCP opened %d upstream connections", calls)
	}
}

func TestDealClientRejectsDisabledLocalProxyBeforeBridge(t *testing.T) {
	previous := beego.AppConfig.String("allow_local_proxy")
	if err := beego.AppConfig.Set("allow_local_proxy", "false"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = beego.AppConfig.Set("allow_local_proxy", previous) })

	client := file.NewClient("test-vkey", false, false)
	bridge := &whitelistTestBridge{}
	server := &BaseServer{bridge: bridge}
	peer, handler := net.Pipe()
	defer peer.Close()
	handlerConn := &whitelistRemoteConn{Conn: handler, remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 4321}}

	err := server.DealClient(conn.NewConn(handlerConn), client, "127.0.0.1:22", nil, "tcp", nil, nil, true, nil, nil)
	if err == nil || err.Error() != "local proxy is disabled" {
		t.Fatalf("DealClient error = %v, want disabled local proxy rejection", err)
	}
	if calls := bridge.calls.Load(); calls != 0 {
		t.Fatalf("disabled local proxy opened %d upstream connections", calls)
	}
}

func TestIPWhiteChallengeEscapesRemoteAddress(t *testing.T) {
	got := ipWhiteChallengeIP("<script>alert(1)</script>:443")
	want := "&lt;script&gt;alert(1)&lt;/script&gt;"
	if got != want {
		t.Fatalf("escaped challenge IP = %q, want %q", got, want)
	}
}

func TestHTTPReverseProxyBlocksWebSocketBeforeUpstream(t *testing.T) {
	client := newWhitelistTestClient()
	host := &file.Host{
		Id:       1,
		Host:     "socket.example.test",
		Location: "/",
		Scheme:   "http",
		Client:   client,
		Target:   &file.Target{TargetStr: "127.0.0.1:8080"},
	}
	installWhitelistTestHost(t, host)

	bridge := &whitelistTestBridge{}
	server := &httpServer{BaseServer: BaseServer{bridge: bridge}}
	proxy := NewHttpReverseProxy(server)
	req := httptest.NewRequest(http.MethodGet, "http://socket.example.test/socket", nil)
	req.RequestURI = "/socket"
	req.RemoteAddr = "203.0.113.8:4321"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("WebSocket status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if calls := bridge.calls.Load(); calls != 0 {
		t.Fatalf("blocked WebSocket opened %d upstream connections", calls)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("blocked WebSocket changed connection count to %d", got)
	}
}

func TestHTTPSProxyBlocksBeforeUpstream(t *testing.T) {
	client := newWhitelistTestClient()
	host := &file.Host{
		Id:       1,
		Host:     "secure.example.test",
		Location: "/",
		Scheme:   "https",
		Client:   client,
		Target:   &file.Target{TargetStr: "127.0.0.1:8443"},
	}
	installWhitelistTestHost(t, host)

	bridge := &whitelistTestBridge{}
	server := &HttpsServer{httpServer: httpServer{BaseServer: BaseServer{bridge: bridge}}}
	peer, handler := net.Pipe()
	defer peer.Close()
	handlerConn := &whitelistRemoteConn{
		Conn:   handler,
		remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.8"), Port: 4321},
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	server.handleHttps2(handlerConn, host.Host, nil, buildHttpsRequest(host.Host))

	if calls := bridge.calls.Load(); calls != 0 {
		t.Fatalf("blocked HTTPS opened %d upstream connections", calls)
	}
	buf := make([]byte, 1)
	if n, err := peer.Read(buf); n != 0 || err == nil {
		t.Fatalf("HTTPS peer was not closed after allowlist rejection: n=%d err=%v", n, err)
	}
}
