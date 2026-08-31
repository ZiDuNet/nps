package proxy

import (
	"testing"

	"ehang.io/nps/lib/file"
)

func TestHTTPReverseProxyRespectsClientConnectionLimit(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.MaxConn = 1
	client.NowConn = 1
	rp := &HttpReverseProxy{}

	if err := rp.reserveClientConnection(client); err == nil {
		t.Fatal("websocket proxy should reject a client at its connection limit")
	}
	if got := client.NowConn; got != 1 {
		t.Fatalf("rejected websocket connection changed NowConn to %d", got)
	}

	client.AddConn()
	if err := rp.reserveClientConnection(client); err != nil {
		t.Fatalf("available client slot should be reserved: %v", err)
	}
	if got := client.NowConn; got != 1 {
		t.Fatalf("accepted websocket connection should reserve one slot, got %d", got)
	}
	client.AddConn()
}

func TestHTTPReverseProxyRespectsClientFlowLimit(t *testing.T) {
	client := file.NewClient("test-vkey", false, false)
	client.Flow.FlowLimit = 1
	client.Flow.ExportFlow = (1 << 20) + 1
	rp := &HttpReverseProxy{}

	if err := rp.reserveClientConnection(client); err == nil {
		t.Fatal("websocket proxy should reject a client over its flow limit")
	}
	if got := client.NowConn; got != 0 {
		t.Fatalf("flow-limited websocket connection changed NowConn to %d", got)
	}
}
