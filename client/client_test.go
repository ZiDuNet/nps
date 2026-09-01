package client

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
)

type capturedClientLogger struct {
	entries []string
}

func (l *capturedClientLogger) Info(format string, v ...interface{}) {
	l.entries = append(l.entries, "info: "+fmt.Sprintf(format, v...))
}

func (l *capturedClientLogger) Error(format string, v ...interface{}) {
	l.entries = append(l.entries, "error: "+fmt.Sprintf(format, v...))
}

func (l *capturedClientLogger) Warn(format string, v ...interface{}) {
	l.entries = append(l.entries, "warn: "+fmt.Sprintf(format, v...))
}

func (l *capturedClientLogger) Trace(format string, v ...interface{}) {
	l.entries = append(l.entries, "trace: "+fmt.Sprintf(format, v...))
}

type trackedSession struct {
	net.Conn
	remote net.Addr
	closed bool
}

func (s *trackedSession) RemoteAddr() net.Addr { return s.remote }

func (s *trackedSession) Read([]byte) (int, error)  { return 0, io.EOF }
func (s *trackedSession) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func (s *trackedSession) LocalAddr() net.Addr { return &net.UDPAddr{} }

func (s *trackedSession) SetDeadline(time.Time) error      { return nil }
func (s *trackedSession) SetReadDeadline(time.Time) error  { return nil }
func (s *trackedSession) SetWriteDeadline(time.Time) error { return nil }

func (s *trackedSession) Close() error {
	s.closed = true
	return nil
}

func TestMatchesP2PRemoteClosesUnexpectedSession(t *testing.T) {
	session := &trackedSession{remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}}
	if matchesP2PRemote(session, "127.0.0.1:10002") {
		t.Fatal("unexpected P2P session was accepted")
	}
	if !session.closed {
		t.Fatal("unexpected P2P session was not closed")
	}
}

func TestMatchesP2PRemoteAcceptsExpectedSession(t *testing.T) {
	session := &trackedSession{remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}}
	if !matchesP2PRemote(session, "127.0.0.1:10001") {
		t.Fatal("expected P2P session was rejected")
	}
	if session.closed {
		t.Fatal("expected P2P session was closed")
	}
}

func TestTRPClientUsesInstanceLogger(t *testing.T) {
	client := NewRPClient("", "", "", "", nil, 0)
	logger := &capturedClientLogger{}
	client.SetLogger(logger)

	client.logInfo("connected to %s", "server")
	client.logError("failed: %d", 1)
	client.logWarn("retrying")
	client.logTrace("attempt %d", 2)

	want := []string{
		"info: connected to server",
		"error: failed: 1",
		"warn: retrying",
		"trace: attempt 2",
	}
	if len(logger.entries) != len(want) {
		t.Fatalf("logged %d entries, want %d: %#v", len(logger.entries), len(want), logger.entries)
	}
	for i, entry := range want {
		if logger.entries[i] != entry {
			t.Errorf("entry %d = %q, want %q", i, logger.entries[i], entry)
		}
	}
}

func TestHandleChanHTTPPreservesPipelinedRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer listener.Close()

	serverConn, clientConn := net.Pipe()
	client := NewRPClient("", "", "", "", nil, 0)
	done := make(chan struct{})
	go func() {
		client.handleChan(serverConn)
		close(done)
	}()
	defer func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	}()

	link := conn.NewLink("http", listener.Addr().String(), false, false, "", false, "")
	sendDone := make(chan error, 1)
	go func() {
		_, sendErr := conn.NewConn(clientConn).SendInfo(link, "")
		sendDone <- sendErr
	}()

	targetConn, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept target: %v", err)
	}
	defer targetConn.Close()
	if err := <-sendDone; err != nil {
		t.Fatalf("send link info: %v", err)
	}

	// Both requests are written in one stream write. The first reader may
	// prefetch the second request, so the forwarding side must retain one
	// bufio.Reader for the lifetime of the HTTP tunnel.
	requests := "POST /one HTTP/1.1\r\nHost: example.test\r\nContent-Length: 3\r\n\r\nabc" +
		"GET /two HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n"
	if _, err := clientConn.Write([]byte(requests)); err != nil {
		t.Fatalf("write pipelined requests: %v", err)
	}

	_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	targetReader := bufio.NewReader(targetConn)
	first, err := http.ReadRequest(targetReader)
	if err != nil {
		t.Fatalf("read first forwarded request: %v", err)
	}
	body, err := io.ReadAll(first.Body)
	if err != nil {
		t.Fatalf("read first request body: %v", err)
	}
	if string(body) != "abc" {
		t.Fatalf("first request body = %q, want %q", body, "abc")
	}
	second, err := http.ReadRequest(targetReader)
	if err != nil {
		t.Fatalf("read second forwarded request: %v", err)
	}
	if second.URL.Path != "/two" {
		t.Fatalf("second request path = %q, want %q", second.URL.Path, "/two")
	}

	_ = targetConn.Close()
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP tunnel did not shut down")
	}
}
