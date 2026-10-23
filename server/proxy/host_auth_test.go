package proxy

import (
	"bufio"
	"encoding/base64"
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
)

type hostAuthBridge struct {
	calls atomic.Int32
}

func (b *hostAuthBridge) SendLinkInfo(int, *conn.Link, *file.Tunnel) (net.Conn, error) {
	b.calls.Add(1)
	clientSide, upstreamSide := net.Pipe()
	go func() {
		defer upstreamSide.Close()
		request, err := http.ReadRequest(bufio.NewReader(upstreamSide))
		if err != nil {
			return
		}
		if request.Body != nil {
			_, _ = io.Copy(io.Discard, request.Body)
			_ = request.Body.Close()
		}
		_, _ = io.WriteString(upstreamSide, "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nupstream")
	}()
	return clientSide, nil
}

func hostAuthRequest(t *testing.T, authorization string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://secure.example.test/resource", nil)
	req.RequestURI = "/resource"
	req.RemoteAddr = "203.0.113.7:4321"
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

func basicAuthorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestHostBasicAuthRejectsBeforeOpeningTunnel(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	host := &file.Host{
		Id:       73,
		Host:     "secure.example.test",
		Location: "/",
		Scheme:   "http",
		Auth:     "operator:correct-horse",
		Client:   client,
		Target:   &file.Target{TargetStr: "127.0.0.1:8080"},
	}
	installWhitelistTestHost(t, host)
	bridge := &hostAuthBridge{}
	proxy := NewHttpReverseProxy(&httpServer{BaseServer: BaseServer{bridge: bridge}})
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, hostAuthRequest(t, basicAuthorization("operator", "wrong")))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("401 response did not include a Basic authentication challenge")
	}
	if calls := bridge.calls.Load(); calls != 0 {
		t.Fatalf("failed authentication opened %d upstream tunnels", calls)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("failed authentication reserved %d client connections", got)
	}
}

func TestHostBasicAuthOverridesClientAuthForStandardHTTP(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.Cnf.U, client.Cnf.P = "legacy", "client-password"
	host := &file.Host{
		Id:       74,
		Host:     "secure.example.test",
		Location: "/",
		Scheme:   "http",
		Auth:     "operator:correct-horse",
		Client:   client,
		Target:   &file.Target{TargetStr: "127.0.0.1:8080"},
	}
	installWhitelistTestHost(t, host)
	bridge := &hostAuthBridge{}
	proxy := NewHttpReverseProxy(&httpServer{BaseServer: BaseServer{bridge: bridge}})
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, hostAuthRequest(t, basicAuthorization("operator", "correct-horse")))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", response.Code, http.StatusOK, response.Body.String())
	}
	if got := strings.TrimSpace(response.Body.String()); got != "upstream" {
		t.Fatalf("body = %q, want upstream response", got)
	}
	if calls := bridge.calls.Load(); calls != 1 {
		t.Fatalf("successful request opened %d upstream tunnels, want 1", calls)
	}
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("completed request leaked %d client connections", got)
	}
}

func TestHostBasicAuthRawFailureWritesChallengeAndCloses(t *testing.T) {
	server := &BaseServer{}
	host := &file.Host{Auth: "operator:correct-horse"}
	peer, raw := net.Pipe()
	defer peer.Close()
	request := hostAuthRequest(t, "")

	result := make(chan error, 1)
	go func() { result <- server.authHostRequest(request, conn.NewConn(raw), host, "", "") }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	response, err := io.ReadAll(peer)
	if err != nil {
		t.Fatalf("read unauthorized response: %v", err)
	}
	if !strings.Contains(string(response), "401 Unauthorized") || !strings.Contains(string(response), "WWW-Authenticate: Basic") {
		t.Fatalf("raw response = %q, want 401 Basic challenge", response)
	}
	if err := <-result; err == nil {
		t.Fatal("authHostRequest accepted a missing credential")
	}
}
