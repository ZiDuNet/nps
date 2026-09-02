package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
)

type streamingTestBridge struct {
	target net.Conn
	calls  atomic.Int32
}

func (b *streamingTestBridge) SendLinkInfo(int, *conn.Link, *file.Tunnel) (net.Conn, error) {
	b.calls.Add(1)
	return b.target, nil
}

func installStreamingHost(t *testing.T) (*file.Client, *file.Host) {
	t.Helper()
	db := file.GetDb()
	old := db.JsonDb
	testDB := file.NewJsonDb(t.TempDir())
	db.JsonDb = testDB
	t.Cleanup(func() { db.JsonDb = old })

	client := file.NewClient("stream-vkey", true, true)
	client.Id = 1
	client.Cnf = &file.Config{}
	host := &file.Host{
		Id:       1,
		Host:     "stream.example.test",
		Location: "/",
		Scheme:   "http",
		Flow:     &file.Flow{},
		Client:   client,
		Target:   &file.Target{TargetStr: "127.0.0.1:19090"},
	}
	testDB.Clients.Store(client.Id, client)
	testDB.Hosts.Store(host.Id, host)
	return client, host
}

func readStreamingResponse(t *testing.T, c net.Conn, req *http.Request) *http.Response {
	t.Helper()
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatalf("read streaming response: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestHostSSEStaysOpenPastRequestIdleWindow(t *testing.T) {
	client, _ := installStreamingHost(t)
	upstream, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()
	bridge := &streamingTestBridge{target: upstream}
	server := &httpServer{BaseServer: BaseServer{bridge: bridge}}

	oldTimeout := hostRequestReadTimeout
	hostRequestReadTimeout = 40 * time.Millisecond
	t.Cleanup(func() { hostRequestReadTimeout = oldTimeout })

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	req := httptest.NewRequest(http.MethodGet, "http://stream.example.test/events", nil)
	req.RequestURI = "/events"
	req.URL.Scheme = "http"
	req.RemoteAddr = "198.51.100.20:12345"
	done := make(chan struct{})
	go func() {
		server.handleHttp(conn.NewConn(serverConn), req, bufio.NewReader(serverConn))
		close(done)
	}()

	if deadlineErr := upstreamPeer.SetReadDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	upstreamReader := bufio.NewReader(upstreamPeer)
	for {
		line, readErr := upstreamReader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read forwarded request: %v", readErr)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if deadlineErr := upstreamPeer.SetReadDeadline(time.Time{}); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	responseHeaders := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nConnection: keep-alive\r\n\r\ndata: first\n\n")
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := upstreamPeer.Write(responseHeaders)
		writeDone <- writeErr
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write SSE headers: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the first response read")
	}

	response := readStreamingResponse(t, clientConn, req)
	firstEvent := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, firstEvent); err != nil {
		t.Fatalf("read first SSE event: %v", err)
	}
	if !bytes.Equal(firstEvent, []byte("data: first\n\n")) {
		t.Fatalf("first SSE event = %q", firstEvent)
	}

	// The old handler returned as soon as the 40ms request deadline elapsed.
	// A live stream must remain usable and deliver data well after that window.
	time.Sleep(120 * time.Millisecond)
	secondEvent := []byte("data: second\n\n")
	writeDone = make(chan error, 1)
	go func() {
		_, writeErr := upstreamPeer.Write(secondEvent)
		writeDone <- writeErr
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write second SSE event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE stream was closed at the request idle deadline")
	}
	gotSecond := make([]byte, len(secondEvent))
	if _, err := io.ReadFull(response.Body, gotSecond); err != nil {
		t.Fatalf("read second SSE event: %v", err)
	}
	if !bytes.Equal(gotSecond, secondEvent) {
		t.Fatalf("second SSE event = %q", gotSecond)
	}

	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := upstreamPeer.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// A final upstream write wakes the response copier so it observes the
	// closed client socket. The handler must then release both sides.
	_, _ = upstreamPeer.Write([]byte("data: after-close\n\n"))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Host SSE handler did not release after client interruption")
	}
	if _, err := upstreamPeer.Write([]byte("data: after-release\n\n")); err == nil {
		t.Fatal("upstream remained writable after client interruption")
	}
	if calls := bridge.calls.Load(); calls != 1 {
		t.Fatalf("bridge calls = %d, want one upstream stream", calls)
	}
	inlet, export, _ := client.Flow.Snapshot()
	if inlet < int64(len(responseHeaders)+len(secondEvent)) || export < int64(len(responseHeaders)+len(secondEvent)) {
		t.Fatalf("client flow counters = inlet %d export %d, want Host response bytes included", inlet, export)
	}
}

func TestHostFiniteResponseKeepsBoundedIdleCleanup(t *testing.T) {
	installStreamingHost(t)
	upstream, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()
	bridge := &streamingTestBridge{target: upstream}
	server := &httpServer{BaseServer: BaseServer{bridge: bridge}}

	oldTimeout := hostRequestReadTimeout
	hostRequestReadTimeout = 40 * time.Millisecond
	t.Cleanup(func() { hostRequestReadTimeout = oldTimeout })

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	req := httptest.NewRequest(http.MethodGet, "http://stream.example.test/finite", nil)
	req.RequestURI = "/finite"
	req.URL.Scheme = "http"
	req.RemoteAddr = "198.51.100.21:12345"
	done := make(chan struct{})
	go func() {
		server.handleHttp(conn.NewConn(serverConn), req, bufio.NewReader(serverConn))
		close(done)
	}()

	if deadlineErr := upstreamPeer.SetReadDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	upstreamReader := bufio.NewReader(upstreamPeer)
	for {
		line, readErr := upstreamReader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read forwarded request: %v", readErr)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if deadlineErr := upstreamPeer.SetReadDeadline(time.Time{}); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	finite := []byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: keep-alive\r\n\r\nhello")
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := upstreamPeer.Write(finite)
		writeDone <- writeErr
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write finite response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive finite response write")
	}
	response := readStreamingResponse(t, clientConn, req)
	body := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, body); err != nil {
		t.Fatalf("read finite response body: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("finite response body = %q", body)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finite keep-alive response bypassed idle cleanup")
	}
	if _, err := upstreamPeer.Write([]byte("after-close")); err == nil {
		t.Fatal("upstream remained writable after finite response cleanup")
	}
}

func TestJoinWithFlowStopsBothDirectionsOnPeerDisconnect(t *testing.T) {
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	flow := &file.Flow{}
	done := make(chan struct{})
	go func() {
		joinWithFlow(client, upstream, flow)
		close(done)
	}()

	first := []byte("client-to-upstream")
	writeDone := make(chan error, 1)
	go func() {
		_, err := clientPeer.Write(first)
		writeDone <- err
	}()
	if err := upstreamPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(first))
	if _, err := io.ReadFull(upstreamPeer, got); err != nil {
		t.Fatalf("read client payload: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Fatalf("client payload = %q", got)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write client payload: %v", err)
	}

	second := []byte("upstream-to-client")
	writeDone = make(chan error, 1)
	go func() {
		_, err := upstreamPeer.Write(second)
		writeDone <- err
	}()
	if err := clientPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got = make([]byte, len(second))
	if _, err := io.ReadFull(clientPeer, got); err != nil {
		t.Fatalf("read upstream payload: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("upstream payload = %q", got)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write upstream payload: %v", err)
	}

	if err := clientPeer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WebSocket join did not stop after client disconnect")
	}
	inlet, export, _ := flow.Snapshot()
	if inlet == 0 || export == 0 {
		t.Fatalf("flow counters = inlet %d export %d, want both directions", inlet, export)
	}
}
