package proxy

import (
	"bytes"
	"crypto/tls"
	"strings"
	"testing"

	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego"
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

func TestHTTP3BridgeSNIUsesBridgeCertificate(t *testing.T) {
	previousHTTP3 := beego.AppConfig.String("bridge_http3")
	previousHost := beego.AppConfig.String("bridge_host")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("bridge_http3", previousHTTP3)
		_ = beego.AppConfig.Set("bridge_host", previousHost)
	})
	_ = beego.AppConfig.Set("bridge_http3", "true")
	_ = beego.AppConfig.Set("bridge_host", "bridge.example")
	crypt.InitTls()

	certificate, err := (&httpServer{}).http3TLSConfig().GetCertificate(&tls.ClientHelloInfo{
		ServerName: "BRIDGE.EXAMPLE.",
	})
	if err != nil {
		t.Fatalf("select Bridge certificate: %v", err)
	}
	if certificate == nil || len(certificate.Certificate) == 0 {
		t.Fatal("HTTP/3 Bridge SNI did not return the Bridge certificate")
	}
	want := crypt.GetCertificateFingerprint(crypt.GetCert())
	got := crypt.GetCertificateFingerprint(*certificate)
	if !bytes.Equal(got, want) {
		t.Fatalf("HTTP/3 Bridge certificate fingerprint = %x, want %x", got, want)
	}
}
