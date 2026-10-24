//go:build !npcgui

package proxy

import (
	"testing"

	"ehang.io/nps/lib/file"
)

func TestLooksLikeHTTPProxyRequest(t *testing.T) {
	tests := []struct {
		prefix string
		want   bool
	}{
		{prefix: "GE", want: true},
		{prefix: "CO", want: true},
		{prefix: "PA", want: true},
		{prefix: "PR", want: true}, // HTTP/2 cleartext preface starts with PRI.
		{prefix: "\x05\x01", want: false},
		{prefix: "??", want: false},
	}
	for _, test := range tests {
		if got := looksLikeHTTPProxyRequest([]byte(test.prefix)); got != test.want {
			t.Errorf("looksLikeHTTPProxyRequest(%q) = %v, want %v", test.prefix, got, test.want)
		}
	}
}

func TestEffectiveMixProxyFlags(t *testing.T) {
	tests := []struct {
		name                string
		task                *file.Tunnel
		wantHTTP, wantSOCKS bool
	}{
		{name: "mixed", task: &file.Tunnel{Mode: "mixProxy", HttpProxy: true, Socks5Proxy: true}, wantHTTP: true, wantSOCKS: true},
		{name: "HTTP only", task: &file.Tunnel{Mode: "mixProxy", HttpProxy: true}, wantHTTP: true},
		{name: "SOCKS5 only", task: &file.Tunnel{Mode: "mixProxy", Socks5Proxy: true}, wantSOCKS: true},
		{name: "legacy HTTP", task: &file.Tunnel{Mode: "httpProxy"}, wantHTTP: true},
		{name: "legacy SOCKS5", task: &file.Tunnel{Mode: "socks5"}, wantSOCKS: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotHTTP, gotSOCKS := effectiveMixProxyFlags(test.task)
			if gotHTTP != test.wantHTTP || gotSOCKS != test.wantSOCKS {
				t.Fatalf("effectiveMixProxyFlags() = (%v, %v), want (%v, %v)", gotHTTP, gotSOCKS, test.wantHTTP, test.wantSOCKS)
			}
		})
	}
}

func TestMixProxySOCKSDestinationACLChecksUDPDatagramsNotRelayHint(t *testing.T) {
	task := &file.Tunnel{Mode: "mixProxy"}
	if err := task.SetDestinationACL(file.AclWhitelist, "full:allowed.example"); err != nil {
		t.Fatal(err)
	}

	if !mixProxySOCKSDestinationAllowed(task, associateMethod, "0.0.0.0:0") {
		t.Fatal("UDP ASSOCIATE relay hint should not be treated as a destination")
	}
	if !mixProxySOCKSDestinationAllowed(task, connectMethod, "allowed.example:443") {
		t.Fatal("whitelisted SOCKS5 CONNECT destination was denied")
	}
	if mixProxySOCKSDestinationAllowed(task, connectMethod, "blocked.example:443") {
		t.Fatal("non-whitelisted SOCKS5 CONNECT destination was allowed")
	}
}
