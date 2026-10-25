package proxy

import (
	"strings"
	"testing"

	"github.com/quic-go/quic-go/http3"
)

func TestBridgeHTTP3QUICConfigDisables0RTT(t *testing.T) {
	cfg := bridgeHTTP3QUICConfig()
	if cfg == nil {
		t.Fatal("bridgeHTTP3QUICConfig returned nil")
	}
	if cfg.Allow0RTT {
		t.Fatal("HTTP/3/Bridge must not allow QUIC 0-RTT for replayable requests")
	}
	if cfg.MaxIncomingStreams <= 0 {
		t.Fatalf("MaxIncomingStreams = %d, want positive default", cfg.MaxIncomingStreams)
	}
}

func TestHTTP3ConnectionProtocolClassificationUsesALPNAndBridgeSNI(t *testing.T) {
	tests := []struct {
		name       string
		alpn       string
		serverName string
		bridgeHost string
		want       bool
	}{
		{name: "browser HTTP3", alpn: http3.NextProtoH3, serverName: "site.example", bridgeHost: "bridge.example", want: true},
		{name: "trimmed browser HTTP3", alpn: " h3 ", serverName: "site.example", bridgeHost: "bridge.example", want: true},
		{name: "h3 routed to Bridge by SNI", alpn: http3.NextProtoH3, serverName: "bridge.example", bridgeHost: "bridge.example"},
		{name: "Bridge SNI ignores case and trailing dot", alpn: http3.NextProtoH3, serverName: "BRIDGE.EXAMPLE.", bridgeHost: "bridge.example"},
		{name: "Bridge ALPN", alpn: "nps", serverName: "site.example", bridgeHost: "bridge.example"},
		{name: "other ALPN", alpn: "h2", serverName: "site.example", bridgeHost: "bridge.example"},
		{name: "missing ALPN", alpn: "", serverName: "site.example", bridgeHost: "bridge.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := http3ConnectionIsBrowser(test.alpn, test.serverName, test.bridgeHost); got != test.want {
				t.Fatalf("http3ConnectionIsBrowser(%q, %q, %q) = %t, want %t", test.alpn, test.serverName, test.bridgeHost, got, test.want)
			}
		})
	}
}

func TestBridgeHTTP3NextProtosAvoidsDuplicateH3(t *testing.T) {
	for _, test := range []struct {
		alpn string
		want []string
	}{
		{alpn: "nps", want: []string{"h3", "nps"}},
		{alpn: "h3", want: []string{"h3"}},
		{alpn: "", want: []string{"h3"}},
	} {
		got := bridgeHTTP3NextProtos(test.alpn)
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Fatalf("bridgeHTTP3NextProtos(%q) = %v, want %v", test.alpn, got, test.want)
		}
	}
}
