package proxy

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
)

type socks5TestBridge struct {
	calls  atomic.Int32
	err    error
	target net.Conn
}

func (b *socks5TestBridge) SendLinkInfo(int, *conn.Link, *file.Tunnel) (net.Conn, error) {
	b.calls.Add(1)
	if b.err != nil {
		return nil, b.err
	}
	return b.target, nil
}

type socks5RemoteConn struct {
	net.Conn
	remote net.Addr
}

func (c *socks5RemoteConn) RemoteAddr() net.Addr {
	return c.remote
}

func newSocks5TestServer(client *file.Client, bridge NetBridge) *Sock5ModeServer {
	return NewSock5ModeServer(bridge, &file.Tunnel{
		Id:       1,
		ServerIp: "127.0.0.1",
		Client:   client,
		Flow:     client.Flow,
		Target:   &file.Target{},
	})
}

func withSocks5TestDB(t *testing.T, global *file.Glob) {
	t.Helper()
	db := file.GetDb()
	old := db.JsonDb
	db.JsonDb = file.NewJsonDb(t.TempDir())
	db.JsonDb.Global = global
	t.Cleanup(func() { db.JsonDb = old })
}

func TestSock5UDPAssociateRejectsIPPoliciesBeforeBridge(t *testing.T) {
	tests := []struct {
		name   string
		global *file.Glob
		setup  func(*file.Client)
	}{
		{
			name:   "global blacklist",
			global: &file.Glob{BlackIpList: []string{"203.0.113.8"}},
		},
		{
			name: "client blacklist",
			setup: func(client *file.Client) {
				client.BlackIpList = []string{"203.0.113.8"}
			},
		},
		{
			name: "client IP allowlist",
			setup: func(client *file.Client) {
				client.IpWhite = true
				client.IpWhitePass = "allowlist-secret"
				client.IpWhiteList = []string{"203.0.113.7"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withSocks5TestDB(t, test.global)
			client := file.NewClient("vkey", false, false)
			client.Id = 7
			if test.setup != nil {
				test.setup(client)
			}
			bridge := &socks5TestBridge{err: errors.New("bridge must not be called")}
			server := newSocks5TestServer(client, bridge)
			peer, local := net.Pipe()
			defer peer.Close()
			request := &socks5RemoteConn{
				Conn:   local,
				remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.8"), Port: 4321},
			}

			// The policy check is intentionally before request parsing. A blocked
			// peer must not be able to keep a handler or bridge allocation alive.
			server.handleUDP(request)
			if got := bridge.calls.Load(); got != 0 {
				t.Fatalf("blocked UDP associate opened %d upstream connections", got)
			}
			if got := atomic.LoadInt32(&client.NowConn); got != 0 {
				t.Fatalf("blocked UDP associate changed connection count to %d", got)
			}
		})
	}
}

func TestSock5UDPAssociateHonorsConnectionLimitBeforeReadingRequest(t *testing.T) {
	withSocks5TestDB(t, nil)
	client := file.NewClient("vkey", false, false)
	client.Id = 8
	client.MaxConn = 1
	client.NowConn = 1
	bridge := &socks5TestBridge{err: errors.New("bridge must not be called")}
	server := newSocks5TestServer(client, bridge)
	peer, local := net.Pipe()
	defer peer.Close()
	request := &socks5RemoteConn{
		Conn:   local,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321},
	}

	server.handleUDP(request)
	if got := bridge.calls.Load(); got != 0 {
		t.Fatalf("quota-rejected UDP associate opened %d upstream connections", got)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 1 {
		t.Fatalf("quota-rejected UDP associate changed connection count to %d", got)
	}
}

func TestSock5UDPAssociateReleasesConnectionSlotOnBridgeFailure(t *testing.T) {
	withSocks5TestDB(t, nil)
	client := file.NewClient("vkey", false, false)
	client.Id = 9
	client.MaxConn = 1
	bridge := &socks5TestBridge{err: errors.New("client offline")}
	server := newSocks5TestServer(client, bridge)
	peer, local := net.Pipe()
	defer peer.Close()
	request := &socks5RemoteConn{
		Conn:   local,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321},
	}

	done := make(chan struct{})
	go func() {
		server.handleUDP(request)
		close(done)
	}()
	if _, err := peer.Write([]byte{ipV4, 127, 0, 0, 1, 0, 53}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP associate did not exit after bridge failure")
	}
	if got := bridge.calls.Load(); got != 1 {
		t.Fatalf("bridge call count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("bridge failure leaked connection slot: NowConn=%d", got)
	}
}

func TestSock5UDPAssociateClosesTargetWhenControlConnectionCloses(t *testing.T) {
	withSocks5TestDB(t, nil)
	client := file.NewClient("vkey", false, false)
	client.Id = 10
	client.MaxConn = 1
	targetPeer, target := net.Pipe()
	defer targetPeer.Close()
	bridge := &socks5TestBridge{target: target}
	server := newSocks5TestServer(client, bridge)
	peer, local := net.Pipe()
	request := &socks5RemoteConn{
		Conn:   local,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321},
	}
	done := make(chan struct{})
	go func() {
		server.handleUDP(request)
		close(done)
	}()

	if _, err := peer.Write([]byte{ipV4, 127, 0, 0, 1, 0, 53}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(peer, reply); err != nil {
		t.Fatalf("read UDP associate reply: %v", err)
	}
	if reply[1] != succeeded {
		t.Fatalf("UDP associate reply code = %d, want %d", reply[1], succeeded)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 1 {
		t.Fatalf("active UDP associate count = %d, want 1", got)
	}

	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP associate did not exit after control connection close")
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("closed UDP associate leaked connection slot: NowConn=%d", got)
	}
	var b [1]byte
	targetClosed := make(chan error, 1)
	go func() {
		_, err := targetPeer.Read(b[:])
		targetClosed <- err
	}()
	select {
	case err := <-targetClosed:
		if err == nil {
			t.Fatal("bridge target remained open after UDP associate shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("bridge target remained open after UDP associate shutdown")
	}
}

func TestSock5StartUsesOneConnectionSlotPerRequest(t *testing.T) {
	withSocks5TestDB(t, nil)
	client := file.NewClient("vkey", false, false)
	client.Id = 11
	client.MaxConn = 1
	targetPeer, target := net.Pipe()
	defer targetPeer.Close()
	bridge := &socks5TestBridge{target: target}
	server := newSocks5TestServer(client, bridge)
	server.task.Port = 0

	startDone := make(chan error, 1)
	go func() { startDone <- server.Start() }()
	listener := waitForSocks5Listener(t, server)
	defer func() {
		_ = server.Close()
		select {
		case err := <-startDone:
			if err != nil {
				t.Errorf("socks5 listener returned %v", err)
			}
		case <-time.After(time.Second):
			t.Error("socks5 listener did not stop")
		}
	}()

	peer, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial socks5 listener: %v", err)
	}
	defer peer.Close()
	if _, err := peer.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(peer, greeting[:]); err != nil {
		t.Fatalf("read SOCKS greeting: %v", err)
	}
	if greeting != [2]byte{5, 0} {
		t.Fatalf("greeting = %v, want no-auth success", greeting)
	}
	if _, err := peer.Write([]byte{5, connectMethod, 0, ipV4, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(peer, reply[:]); err != nil {
		t.Fatalf("read SOCKS CONNECT reply: %v", err)
	}
	if reply[1] != succeeded {
		t.Fatalf("SOCKS CONNECT reply = %d, want %d", reply[1], succeeded)
	}
	if got := bridge.calls.Load(); got != 1 {
		t.Fatalf("bridge call count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 1 {
		t.Fatalf("active SOCKS request count = %d, want 1", got)
	}

	_ = peer.Close()
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&client.NowConn) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("closed SOCKS request leaked connection slot: NowConn=%d", got)
	}
}

func TestSock5HandshakeTimesOut(t *testing.T) {
	oldTimeout := socks5HandshakeTimeout
	socks5HandshakeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { socks5HandshakeTimeout = oldTimeout })

	client := file.NewClient("vkey", false, false)
	client.Id = 12
	server := newSocks5TestServer(client, &socks5TestBridge{err: errors.New("bridge must not be called")})
	peer, local := net.Pipe()
	defer peer.Close()

	done := make(chan struct{})
	go func() {
		server.handleConn(local)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 handshake did not time out")
	}
}

func waitForSocks5Listener(t *testing.T, server *Sock5ModeServer) net.Listener {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.listenerMu.RLock()
		listener := server.listener
		server.listenerMu.RUnlock()
		if listener != nil {
			return listener
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("socks5 listener did not start")
	return nil
}
