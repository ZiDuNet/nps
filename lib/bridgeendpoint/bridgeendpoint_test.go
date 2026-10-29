package bridgeendpoint

import "testing"

func TestResolveKeepsTCPMainAndIncludesAllEnabledProtocols(t *testing.T) {
	endpoints := Resolve(Config{
		RequestHost:    "panel.example.com",
		BridgeType:     "tcp",
		BridgePath:     "/ws",
		BridgePort:     8024,
		TLSEnabled:     true,
		TLSBridgePort:  8025,
		KCPBridgePort:  8026,
		WSBridgePort:   8027,
		WSSBridgePort:  8028,
		QUICBridgePort: 8029,
		HTTP3Bridge:    true,
		HTTP3ProxyPort: 8443,
		QUICALPN:       "nps",
		TCP:            DisplayEndpoint{Show: true, Port: 8024},
		TLS:            DisplayEndpoint{Show: true, Port: 8025},
		KCP:            DisplayEndpoint{Show: true, Port: 8026},
		WS:             DisplayEndpoint{Show: true, Port: 8027},
		WSS:            DisplayEndpoint{Show: true, Port: 8028},
		QUIC:           DisplayEndpoint{Show: true, Port: 8029, ALPN: "nps"},
	})
	if len(endpoints) != 7 {
		t.Fatalf("got %d endpoints, want 7", len(endpoints))
	}
	if !endpoints[0].Enabled || !endpoints[0].Main || endpoints[0].Key != "tcp" {
		t.Fatalf("TCP should be the default main endpoint: %#v", endpoints[0])
	}
	for _, key := range []string{"tcp", "tls", "kcp", "ws", "wss", "quic", "http3"} {
		if endpoint := endpointByKey(endpoints, key); endpoint == nil || !endpoint.Enabled {
			t.Fatalf("endpoint %s should be enabled: %#v", key, endpoint)
		}
	}
	if !endpointByKey(endpoints, "http3").ViaHTTP3 ||
		endpointByKey(endpoints, "quic").Transport != "quic" ||
		endpointByKey(endpoints, "ws").Path != "/ws" {
		t.Fatalf("endpoint details are incomplete: %#v", endpoints)
	}
}

func TestResolveAppliesDisplayOverridesAndHidesCommands(t *testing.T) {
	endpoints := Resolve(Config{
		RequestHost:  "127.0.0.1:30080",
		BridgeAddr:   "fallback.example.com",
		BridgeType:   "tcp",
		BridgePath:   "/ws",
		DisplayPath:  "/show",
		BridgePort:   8024,
		WSBridgePort: 8027,
		TCP:          DisplayEndpoint{Show: false, Port: 18024},
		WS:           DisplayEndpoint{Show: true, Host: "ws.example.com", Port: 443},
	})
	tcp := endpointByKey(endpoints, "tcp")
	if tcp.Host != "fallback.example.com" || tcp.Port != 18024 {
		t.Fatalf("unexpected TCP display override: %#v", tcp)
	}
	if tcp.AvailableForCommands() {
		t.Fatal("show=false endpoint must not generate client commands")
	}
	ws := endpointByKey(endpoints, "ws")
	if !ws.AvailableForCommands() || ws.Host != "ws.example.com" || ws.Port != 443 || ws.Path != "/show" {
		t.Fatalf("unexpected WS endpoint: %#v", ws)
	}
}

func TestResolveMainKCPAndDisabledEndpoints(t *testing.T) {
	endpoints := Resolve(Config{
		BridgeType:    "kcp",
		BridgePort:    9000,
		BridgePath:    "/ws",
		TLSEnabled:    true,
		TLSBridgePort: 9001,
	})
	if tcp := endpointByKey(endpoints, "tcp"); tcp.Enabled {
		t.Fatalf("TCP should be disabled when primary is KCP: %#v", tcp)
	}
	if kcp := endpointByKey(endpoints, "kcp"); !kcp.Enabled || !kcp.Main || kcp.Port != 9000 {
		t.Fatalf("KCP should be the main endpoint: %#v", kcp)
	}
}

func TestResolveKeepsLegacyTLSCommandsAndUsesSNIForQUIC(t *testing.T) {
	endpoints := Resolve(Config{
		RequestHost:    "panel.example.com",
		BridgeAddr:     "bridge.example.com",
		BridgeHost:     "mux.example.com",
		BridgeType:     "tcp",
		BridgePath:     "/ws",
		BridgePort:     8024,
		TLSEnabled:     true,
		TLSBridgePort:  8025,
		WSSBridgePort:  8028,
		QUICBridgePort: 8029,
		QUICALPN:       "nps",
		TCP:            DisplayEndpoint{Show: true, Port: 8024},
		TLS:            DisplayEndpoint{Show: true, Port: 8025},
		WSS:            DisplayEndpoint{Show: true, Port: 8028},
		QUIC:           DisplayEndpoint{Show: true, Port: 8029, ALPN: "nps"},
	})
	if tlsEndpoint := endpointByKey(endpoints, "tls"); tlsEndpoint.TLSServerName != "" {
		t.Fatalf("direct TLS endpoint must not force a server name: %#v", tlsEndpoint)
	}
	if wss := endpointByKey(endpoints, "wss"); wss.TLSServerName != "" {
		t.Fatalf("WSS endpoint must not force a server name: %#v", wss)
	}
	if quic := endpointByKey(endpoints, "quic"); quic.TLSServerName != "mux.example.com" {
		t.Fatalf("QUIC endpoint should use bridge_host as TLS identity: %#v", quic)
	}
}

func TestProtocolNameDistinguishesHTTP3FromDirectQUIC(t *testing.T) {
	if got := ProtocolName("quic"); got != "QUIC" {
		t.Fatalf("ProtocolName(quic) = %q", got)
	}
	if got := ProtocolName("http3"); got != "HTTP/3" {
		t.Fatalf("ProtocolName(http3) = %q", got)
	}
}

func endpointByKey(endpoints []Endpoint, key string) *Endpoint {
	for index := range endpoints {
		if endpoints[index].Key == key {
			return &endpoints[index]
		}
	}
	return nil
}
