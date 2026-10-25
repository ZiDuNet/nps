package pmux

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

type captureConn struct{ bytes.Buffer }

func (c *captureConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *captureConn) Write(value []byte) (int, error)  { return c.Buffer.Write(value) }
func (c *captureConn) Close() error                     { return nil }
func (c *captureConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *captureConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *captureConn) SetDeadline(time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }

type readerConn struct{ *bytes.Reader }

func (c *readerConn) Write(value []byte) (int, error)  { return len(value), nil }
func (c *readerConn) Close() error                     { return nil }
func (c *readerConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *readerConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *readerConn) SetDeadline(time.Time) error      { return nil }
func (c *readerConn) SetReadDeadline(time.Time) error  { return nil }
func (c *readerConn) SetWriteDeadline(time.Time) error { return nil }

func TestPortConnReadPreservesBufferedPrefix(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	conn := newPortConn(server, []byte("abcdef"), true)
	first := make([]byte, 2)
	if n, err := conn.Read(first); err != nil || n != 2 || string(first) != "ab" {
		t.Fatalf("first read = %d/%q, %v", n, first, err)
	}

	go func() { _, _ = client.Write([]byte("ghi")) }()
	second := make([]byte, 7)
	if n, err := conn.Read(second); err != nil || n != 7 || string(second) != "cdefghi" {
		t.Fatalf("second read = %d/%q, %v", n, second, err)
	}
}

func TestPortMuxStartIsIdempotentAndCloseUnblocksListeners(t *testing.T) {
	mux := NewPortMux(0, "")
	if err := mux.Start(); err != nil {
		t.Fatalf("second Start failed: %v", err)
	}
	addr := mux.listenerAddr()
	if addr == nil {
		t.Fatal("port mux did not expose a listener address")
	}

	listener := mux.GetHttpListener()
	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()

	if err := mux.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("closed listener returned a connection")
		}
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed listener error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener Accept did not unblock after Close")
	}
	if err := mux.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if _, err := mux.GetHttpListener().Accept(); err == nil {
		t.Fatal("listener created after Close unexpectedly accepted")
	}
}

func TestPortMuxRoutesClientConnection(t *testing.T) {
	mux := NewPortMux(0, "")
	defer mux.Close()
	addr := mux.listenerAddr()
	if addr == nil {
		t.Fatal("port mux did not expose a listener address")
	}

	listener := mux.GetClientListener()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial mux: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("TSTpayload")); err != nil {
		t.Fatalf("write protocol marker: %v", err)
	}
	select {
	case err := <-acceptErr:
		t.Fatalf("accept client connection: %v", err)
	case conn := <-accepted:
		defer conn.Close()
		buf := make([]byte, 3)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read buffered marker: %v", err)
		}
		if string(buf) != "TST" {
			t.Fatalf("marker = %q", buf)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for client connection")
	}
}

func TestPortMuxBindsConfiguredAddress(t *testing.T) {
	mux := NewPortMuxWithAddress(0, "", "127.0.0.1")
	defer mux.Close()

	addr, ok := mux.listenerAddr().(*net.TCPAddr)
	if !ok || addr == nil {
		t.Fatalf("listener address = %T %v, want *net.TCPAddr", mux.listenerAddr(), mux.listenerAddr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener IP = %v, want loopback", addr.IP)
	}
}

func TestPortMuxRoutesHTTPByHostAndPreservesCompleteRequest(t *testing.T) {
	mux := NewPortMuxWithHosts(0, "admin.example", "bridge.example", "127.0.0.1")
	t.Cleanup(func() { _ = mux.Close() })
	listeners := map[string]net.Listener{
		"admin.example":   mux.GetManagerListener(),
		"bridge.example":  mux.GetClientWSListener(),
		"backend.example": mux.GetHttpListener(),
	}
	addr := mux.listenerAddr().String()

	for host, listener := range listeners {
		t.Run(host, func(t *testing.T) {
			accepted := make(chan net.Conn, 1)
			acceptErr := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					acceptErr <- err
					return
				}
				accepted <- conn
			}()

			request := "GET /route HTTP/1.1\r\nHost: " + host + ":8080\r\nX-After-Host: preserved\r\nConnection: close\r\n\r\n"
			client, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial shared listener: %v", err)
			}
			defer client.Close()
			if _, err := io.WriteString(client, request); err != nil {
				t.Fatalf("write request: %v", err)
			}

			select {
			case err := <-acceptErr:
				t.Fatalf("accept routed request: %v", err)
			case routed := <-accepted:
				defer routed.Close()
				got := make([]byte, len(request))
				if _, err := io.ReadFull(routed, got); err != nil {
					t.Fatalf("read routed request: %v", err)
				}
				if string(got) != request {
					t.Fatalf("routed request was modified or truncated:\n got %q\nwant %q", got, request)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for routed request")
			}
		})
	}
}

func TestPortMuxRoutesTLSBySNIOnlyToRegisteredListeners(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		register   func(*PortMux)
		want       listenerKind
		inspectSNI bool
	}{
		{
			name:       "Bridge SNI",
			serverName: "bridge.example",
			register: func(mux *PortMux) {
				_ = mux.GetClientTLSListener()
				_ = mux.GetHttpsListener()
			},
			want:       listenerClientTLS,
			inspectSNI: true,
		},
		{
			name:       "HTTPS fallback",
			serverName: "site.example",
			register: func(mux *PortMux) {
				_ = mux.GetClientTLSListener()
				_ = mux.GetHttpsListener()
			},
			want:       listenerHTTPS,
			inspectSNI: true,
		},
		{
			name:       "management TLS",
			serverName: "admin.example",
			register: func(mux *PortMux) {
				_ = mux.GetManagerTLSListener()
				_ = mux.GetHttpsListener()
			},
			want:       listenerManagerTLS,
			inspectSNI: true,
		},
		{
			name:       "WSS SNI",
			serverName: "bridge.example",
			register: func(mux *PortMux) {
				_ = mux.GetClientWSSListener()
				_ = mux.GetHttpsListener()
			},
			want:       listenerClientWSS,
			inspectSNI: true,
		},
		{
			name:       "unregistered Bridge listener is ignored",
			serverName: "bridge.example",
			register: func(mux *PortMux) {
				_ = mux.GetHttpsListener()
			},
			want:       listenerHTTPS,
			inspectSNI: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mux := NewPortMuxWithHosts(0, "admin.example", "bridge.example", "127.0.0.1")
			t.Cleanup(func() { _ = mux.Close() })
			test.register(mux)
			server, client := net.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- tls.Client(client, &tls.Config{ServerName: test.serverName}).Handshake()
			}()
			prefix := make([]byte, 3)
			if _, err := io.ReadFull(server, prefix); err != nil {
				_ = server.Close()
				_ = client.Close()
				t.Fatalf("read TLS prefix: %v", err)
			}
			target, raw, readMore := mux.classifyTLS(server, prefix)
			if target != mux.listener(test.want) {
				_ = server.Close()
				_ = client.Close()
				t.Fatalf("TLS route = %p, want listener kind %d", target, test.want)
			}
			if (test.inspectSNI && len(raw) <= len(prefix)) || !readMore {
				_ = server.Close()
				_ = client.Close()
				t.Fatalf("TLS route did not preserve the inspected ClientHello: bytes=%d readMore=%t", len(raw), readMore)
			}
			_ = server.Close()
			_ = client.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("TLS client handshake did not stop after listener close")
			}
		})
	}
}

func TestPortMuxDropsUnknownHostWhenNoHTTPFallbackExists(t *testing.T) {
	mux := NewPortMuxWithHosts(0, "admin.example", "bridge.example", "127.0.0.1")
	t.Cleanup(func() { _ = mux.Close() })
	admin := mux.GetManagerListener()
	ws := mux.GetClientWSListener()
	adminAccept := make(chan net.Conn, 1)
	wsAccept := make(chan net.Conn, 1)
	go func() { conn, _ := admin.Accept(); adminAccept <- conn }()
	go func() { conn, _ := ws.Accept(); wsAccept <- conn }()
	client, err := net.Dial("tcp", mux.listenerAddr().String())
	if err != nil {
		t.Fatalf("dial shared listener: %v", err)
	}
	defer client.Close()
	_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: unknown.example\r\n\r\n")
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("unknown Host should be closed instead of routed to the management listener")
	}
	select {
	case routed := <-adminAccept:
		if routed != nil {
			_ = routed.Close()
			t.Fatal("unknown Host was routed to the management listener")
		}
	case routed := <-wsAccept:
		if routed != nil {
			_ = routed.Close()
			t.Fatal("unknown Host was routed to the WebSocket listener")
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSameHostNormalizesPortAndCase(t *testing.T) {
	if !sameHost("BRIDGE.EXAMPLE:8044", "bridge.example.") {
		t.Fatal("host normalization did not ignore case, trailing dot, and port")
	}
}

func TestReadClientHelloServerNameDoesNotConsumeFollowingBytes(t *testing.T) {
	capture := new(captureConn)
	_ = tls.Client(capture, &tls.Config{ServerName: "bridge.example"}).Handshake()
	hello := append([]byte(nil), capture.Bytes()...)
	if len(hello) <= 3 {
		t.Fatalf("captured ClientHello has only %d bytes", len(hello))
	}
	following := []byte("NEXT-RECORD")
	server := &readerConn{Reader: bytes.NewReader(append(append([]byte(nil), hello[3:]...), following...))}
	serverName, raw, err := readClientHelloServerName(server, hello[:3])
	if err != nil {
		t.Fatalf("read ClientHello: %v", err)
	}
	if serverName != "bridge.example" {
		t.Fatalf("ClientHello SNI = %q, want bridge.example", serverName)
	}
	if !bytes.Equal(raw, hello) {
		t.Fatalf("captured ClientHello differs from original: got %d bytes, want %d", len(raw), len(hello))
	}
	got := make([]byte, len(following))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("read bytes after ClientHello: %v", err)
	}
	if !bytes.Equal(got, following) {
		t.Fatalf("bytes after ClientHello = %q, want %q", got, following)
	}
}
