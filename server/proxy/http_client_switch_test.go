package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server/traffic"
)

type hostSwitchTestBridge struct {
	upstream map[int]net.Conn
	dialed   chan int
}

// Some stream readers return a final chunk and io.EOF in one call.
type hostSwitchFinalRead struct {
	net.Conn
	final string
}

func (c *hostSwitchFinalRead) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err == nil && string(p[:n]) == c.final {
		return n, io.EOF
	}
	return n, err
}

func (b *hostSwitchTestBridge) SendLinkInfo(clientID int, _ *conn.Link, _ *file.Tunnel) (net.Conn, error) {
	b.dialed <- clientID
	target := b.upstream[clientID]
	if target == nil {
		return nil, fmt.Errorf("test upstream %d not configured", clientID)
	}
	return target, nil
}

func hostSwitchTestPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	deadline := time.Now().Add(3 * time.Second)
	if err := right.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	return left, right
}

func TestHTTPKeepAliveSwitchesHostClientAfterSave(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pending       bool
		finalReadEOF  bool
		responseClose bool
	}{
		{name: "after completed response"},
		{name: "with pending pipelined responses", pending: true},
		{name: "final response bytes and EOF", pending: true, finalReadEOF: true},
		{name: "close-delimited final response", pending: true, responseClose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldClient, host := installStreamingHost(t)
			oldClient.Lock()
			oldClient.Status = true
			oldClient.NoStore = false
			oldClient.Unlock()
			newClient := file.NewClient("test-new-host-client", false, false)
			newClient.Id = 2
			newClient.Status = true
			newClient.Cnf = &file.Config{}
			file.GetDb().JsonDb.Clients.Store(newClient.Id, newClient)

			oldUpstream, oldPeer := hostSwitchTestPipe(t)
			newUpstream, newPeer := hostSwitchTestPipe(t)
			serverConn, visitor := hostSwitchTestPipe(t)
			if tc.finalReadEOF {
				oldUpstream = &hostSwitchFinalRead{Conn: oldUpstream, final: "still old"}
			}
			bridge := &hostSwitchTestBridge{
				upstream: map[int]net.Conn{oldClient.Id: oldUpstream, newClient.Id: newUpstream},
				dialed:   make(chan int, 16),
			}
			server := &httpServer{BaseServer: BaseServer{bridge: bridge}}
			first := httptest.NewRequest(http.MethodGet, "http://"+host.Host+"/first", nil)
			first.RequestURI = "/first"
			first.RemoteAddr = "198.51.100.27:12345"
			hostName, target := host.Host, host.Target.TargetStr
			done := make(chan struct{})
			go func() {
				defer close(done)
				server.handleHttp(conn.NewConn(serverConn), first, bufio.NewReader(serverConn))
			}()
			t.Cleanup(func() {
				_ = visitor.Close()
				_ = oldPeer.Close()
				_ = newPeer.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("HTTP handler did not release the test connection")
				}
			})

			oldReader, newReader, visitorReader := bufio.NewReader(oldPeer), bufio.NewReader(newPeer), bufio.NewReader(visitor)
			expectRequest := func(reader *bufio.Reader, path, method string) {
				t.Helper()
				req, err := http.ReadRequest(reader)
				if err != nil {
					t.Fatalf("upstream read %s: %v", path, err)
				}
				_ = req.Body.Close()
				if req.URL.Path != path || req.Method != method {
					t.Fatalf("upstream received %s %q, want %s %q", req.Method, req.URL.Path, method, path)
				}
			}
			sendRequest := func(path, method string) {
				t.Helper()
				if _, err := fmt.Fprintf(visitor, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\n", method, path, hostName); err != nil {
					t.Fatalf("send %s: %v", path, err)
				}
			}
			sendResponse := func(upstream net.Conn, body string) {
				t.Helper()
				if _, err := fmt.Fprintf(upstream, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
					t.Fatalf("write upstream %q: %v", body, err)
				}
			}
			expectResponse := func(want string) {
				t.Helper()
				res, err := http.ReadResponse(visitorReader, nil)
				if err != nil {
					t.Fatalf("read response %q: %v", want, err)
				}
				body, err := io.ReadAll(res.Body)
				_ = res.Body.Close()
				if err != nil || res.StatusCode != http.StatusOK || string(body) != want {
					t.Fatalf("read response: status=%d body=%q err=%v; want 200 %q", res.StatusCode, body, err, want)
				}
			}
			expectDial := func(want int) {
				t.Helper()
				select {
				case id := <-bridge.dialed:
					if id != want {
						t.Fatalf("dialed client %d, want %d", id, want)
					}
				case <-time.After(time.Second):
					t.Fatalf("no dial for client %d", want)
				}
			}

			expectDial(oldClient.Id)
			expectRequest(oldReader, "/first", http.MethodGet)
			if tc.pending {
				// Pipeline two more requests before receiving the first response.
				sendRequest("/still-old", http.MethodHead)
				expectRequest(oldReader, "/still-old", http.MethodHead)
				sendRequest("/last-old", http.MethodGet)
				expectRequest(oldReader, "/last-old", http.MethodGet)
				// Start returning a chunked response but hold back its last bytes.
				if _, err := io.WriteString(oldPeer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nold\r\n"); err != nil {
					t.Fatal(err)
				}
			} else {
				sendResponse(oldPeer, "old")
				expectResponse("old")
			}

			replacement := &file.Host{
				Id: host.Id, Host: hostName, Location: "/", Scheme: "http",
				Client: newClient, Target: &file.Target{TargetStr: target},
			}
			if err := file.GetDb().UpdateHostWithOwnerChange(replacement, oldClient, true); err != nil {
				t.Fatalf("switch host from A to B: %v", err)
			}
			sendRequest("/on-new-client", http.MethodGet)
			if tc.pending {
				res, err := http.ReadResponse(visitorReader, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(oldPeer, "5\r\n tail\r\n0\r\n\r\n"); err != nil {
					t.Fatalf("the first old response was cut off at hand-off: %v", err)
				}
				body, err := io.ReadAll(res.Body)
				_ = res.Body.Close()
				if err != nil || string(body) != "old tail" {
					t.Fatalf("old chunked response was cut off: %q, %v", body, err)
				}
				if _, err := io.WriteString(oldPeer, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				head, err := http.ReadResponse(visitorReader, &http.Request{Method: http.MethodHead})
				if err != nil {
					t.Fatalf("read old HEAD response: %v", err)
				}
				_ = head.Body.Close()
				if head.StatusCode != http.StatusOK || head.ContentLength != 100 {
					t.Fatalf("old HEAD response: status=%d length=%d", head.StatusCode, head.ContentLength)
				}
				switch {
				case tc.finalReadEOF:
					if _, err := io.WriteString(oldPeer, "HTTP/1.1 200 OK\r\nContent-Length: 9\r\n\r\n"); err != nil {
						t.Fatal(err)
					}
					writeDone := make(chan error, 1)
					go func() {
						_, err := io.WriteString(oldPeer, "still old")
						writeDone <- err
					}()
					expectResponse("still old")
					if err := <-writeDone; err != nil {
						t.Fatal(err)
					}
				case tc.responseClose:
					if _, err := io.WriteString(oldPeer, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nstill old"); err != nil {
						t.Fatal(err)
					}
					_ = oldPeer.Close()
					expectResponse("still old")
				default:
					sendResponse(oldPeer, "still old")
					expectResponse("still old")
				}
				if tc.responseClose {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("proxy did not close after the final response")
					}
					if active := atomic.LoadInt32(&newClient.NowConn); active != 0 || len(bridge.dialed) != 0 {
						t.Fatal("a close-delimited response must not be followed by a response from B on the same connection")
					}
					return
				}
			}

			expectDial(newClient.Id)
			expectRequest(newReader, "/on-new-client", http.MethodGet)
			if _, err := oldReader.Peek(1); !errors.Is(err, io.EOF) {
				t.Fatalf("old upstream was not released: %v", err)
			}
			if old, new := atomic.LoadInt32(&oldClient.NowConn), atomic.LoadInt32(&newClient.NowConn); old != 0 || new != 1 {
				t.Fatalf("connection slots after hand-off: A=%d, B=%d; want 0, 1", old, new)
			}
			oldIn, oldOut, _ := oldClient.Flow.Snapshot()
			sendResponse(newPeer, "new")
			expectResponse("new")
			// Further requests on the unchanged rule reuse the new upstream.
			sendRequest("/still-new", http.MethodGet)
			expectRequest(newReader, "/still-new", http.MethodGet)
			sendResponse(newPeer, "still new")
			expectResponse("still new")
			_ = newPeer.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("proxy did not finish after B closed the upstream")
			}
			select {
			case id := <-bridge.dialed:
				t.Fatalf("unexpected extra dial to client %d", id)
			default:
			}
			if connections := atomic.LoadInt32(&newClient.NowConn); connections != 0 {
				t.Fatalf("new client kept %d connection slots after close", connections)
			}
			if in, out, _ := oldClient.Flow.Snapshot(); oldIn == 0 || oldOut == 0 || in != oldIn || out != oldOut {
				t.Fatalf("old client flow changed after hand-off: before=%d/%d, after=%d/%d", oldIn, oldOut, in, out)
			}
			if in, out, _ := newClient.Flow.Snapshot(); in == 0 || out == 0 {
				t.Fatalf("new client did not receive its flow counts: %d/%d", in, out)
			}
		})
	}
}

func TestHTTPHostSwitchWaitsForBlockedResponseWrite(t *testing.T) {
	responses := &rawHTTPResponses{responseInspector: newResponseInspector(
		strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nbody"), traffic.Resource{}, newRequestQueue(), nil,
	)}
	responses.addRequest(traffic.Event{Method: http.MethodGet})
	// The inspector has read the entire response but the downstream write
	// has not completed. This is not a safe point to open another upstream.
	buf := make([]byte, 256)
	if n, err := responses.Read(buf); err != nil || n == 0 {
		t.Fatalf("read response before downstream write: n=%d err=%v", n, err)
	}
	responses.mu.Lock()
	pending := responses.pending
	responses.mu.Unlock()
	if !pending {
		t.Fatal("declared the route drained before the response was written")
	}
	// The copy loop only reads again after the write succeeds.
	if _, err := responses.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	done := make(chan struct{})
	close(done)
	if !responses.waitForDrain(done) {
		t.Fatal("fully delivered response did not allow hand-off")
	}
}
