package proxy

import (
	"encoding/base64"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
)

func TestHTTPProxyAuthUsesTunnelAccountsBeforeClientFallback(t *testing.T) {
	server := &BaseServer{task: &file.Tunnel{ProxyAuth: "proxy-user:proxy-pass"}}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-pass")))
	if err := server.proxyAuth(request, nil, "client-user", "client-pass"); err != nil {
		t.Fatalf("dedicated proxy credentials rejected: %v", err)
	}

	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("client-user:client-pass")))
	if err := server.proxyAuth(request, nil, "client-user", "client-pass"); err == nil {
		t.Fatal("client credentials must not override dedicated proxy credentials")
	}

	server.task = &file.Tunnel{}
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("client-user:client-pass")))
	if err := server.proxyAuth(request, nil, "client-user", "client-pass"); err != nil {
		t.Fatalf("client fallback credentials rejected: %v", err)
	}
}

func TestSOCKS5AuthUsesTunnelAccountsBeforeClientFallback(t *testing.T) {
	tests := []struct {
		name        string
		snapshot    socksTaskSnapshot
		username    string
		password    string
		wantSuccess bool
	}{
		{
			name: "dedicated account",
			snapshot: socksTaskSnapshot{
				proxyAuth:    map[string]string{"proxy-user": "proxy-pass"},
				clientConfig: file.Config{U: "client-user", P: "client-pass"},
			},
			username: "proxy-user", password: "proxy-pass", wantSuccess: true,
		},
		{
			name: "client account cannot override dedicated account",
			snapshot: socksTaskSnapshot{
				proxyAuth:    map[string]string{"proxy-user": "proxy-pass"},
				clientConfig: file.Config{U: "client-user", P: "client-pass"},
			},
			username: "client-user", password: "client-pass", wantSuccess: false,
		},
		{
			name: "fallback client account",
			snapshot: socksTaskSnapshot{
				clientConfig: file.Config{U: "client-user", P: "client-pass"},
			},
			username: "client-user", password: "client-pass", wantSuccess: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Sock5ModeServer{}
			client, peer := net.Pipe()
			defer peer.Close()
			result := make(chan error, 1)
			go func() { result <- server.authWithSnapshot(client, test.snapshot) }()

			handshake := []byte{1, byte(len(test.username))}
			handshake = append(handshake, []byte(test.username)...)
			handshake = append(handshake, byte(len(test.password)))
			handshake = append(handshake, []byte(test.password)...)
			if _, err := peer.Write(handshake); err != nil {
				t.Fatalf("write SOCKS auth request: %v", err)
			}
			reply := make([]byte, 2)
			if _, err := io.ReadFull(peer, reply); err != nil {
				t.Fatalf("read SOCKS auth response: %v", err)
			}
			select {
			case err := <-result:
				if (err == nil) != test.wantSuccess {
					t.Fatalf("auth error = %v, wantSuccess %v", err, test.wantSuccess)
				}
			case <-time.After(time.Second):
				t.Fatal("SOCKS auth did not finish")
			}
			wantStatus := byte(authFailure)
			if test.wantSuccess {
				wantStatus = authSuccess
			}
			if reply[1] != wantStatus {
				t.Fatalf("auth status = %d, want %d", reply[1], wantStatus)
			}
		})
	}
}
