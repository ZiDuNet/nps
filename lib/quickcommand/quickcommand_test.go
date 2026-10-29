package quickcommand

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLegacyQuickCommandCompatibility(t *testing.T) {
	encoded := EncodeLegacy("测试客户端", "nps.example.com:8025", "abc123", true, "sha256:aa")
	command, err := DecodeBase64(encoded)
	if err != nil {
		t.Fatalf("decode legacy command: %v", err)
	}
	if command.Name != "测试客户端" || command.Server != "nps.example.com:8025" ||
		command.VKey != "abc123" || command.Transport != "tcp" || !command.TLS ||
		command.TLSFingerprint != "sha256:aa" {
		t.Fatalf("unexpected legacy command: %#v", command)
	}
	if command.CommandLine("npc") != "npc -server=nps.example.com:8025 -vkey=abc123 -type=tcp -tls_enable=true -tls_fingerprint=sha256:aa" {
		t.Fatalf("unexpected legacy command line: %s", command.CommandLine("npc"))
	}
}

func TestV2QuickCommandRoundTrip(t *testing.T) {
	original := Command{
		Name:           "WS 客户端",
		Server:         "nps.example.com:8044",
		VKey:           "key",
		Transport:      "ws",
		Endpoint:       "ws",
		BridgePath:     "/bridge",
		TLSServerName:  "nps.example.com",
		TLSFingerprint: "aa",
	}
	encoded, err := EncodeV2(original)
	if err != nil {
		t.Fatalf("encode v2: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.HasPrefix(string(raw), JSONPrefix) {
		t.Fatalf("v2 command should use JSON prefix, raw=%q err=%v", raw, err)
	}
	decoded, err := DecodeBase64(encoded)
	if err != nil {
		t.Fatalf("decode v2: %v", err)
	}
	if decoded.Transport != "ws" || decoded.Endpoint != "ws" || decoded.BridgePath != "/bridge" ||
		decoded.TLSServerName != "nps.example.com" || decoded.V != Version {
		t.Fatalf("unexpected v2 command: %#v", decoded)
	}
}

func TestEncodeKeepsTCPAndTLSPortableByLegacyNPC(t *testing.T) {
	for _, command := range []Command{
		{Name: "TCP", Server: "bridge.example.com:8024", VKey: "key", Transport: "tcp"},
		{Name: "TLS", Server: "bridge.example.com:8025", VKey: "key", Transport: "tcp", TLS: true},
	} {
		encoded, err := Encode(command)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(raw), LegacyPrefix) {
			t.Fatalf("TCP/TLS command must remain v1, got %q", raw)
		}
	}
	encoded, err := Encode(Command{Server: "bridge.example.com:8027", VKey: "key", Transport: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), JSONPrefix) {
		t.Fatalf("WS command must use v2, got %q", raw)
	}
}

func TestQuickCommandTransportAliasesAndValidation(t *testing.T) {
	command, err := Normalize(Command{Server: "127.0.0.1:1", VKey: "key", Transport: "tls"})
	if err != nil || command.Transport != "tcp" || !command.TLS {
		t.Fatalf("TLS alias normalization failed: %#v err=%v", command, err)
	}
	command, err = Normalize(Command{Server: "127.0.0.1:1", VKey: "key", Transport: "wss"})
	if err != nil || command.BridgePath != "/ws" || !command.TLS {
		t.Fatalf("WSS normalization failed: %#v err=%v", command, err)
	}
	if _, err := Normalize(Command{Server: "127.0.0.1:1", VKey: "key", Transport: "unknown"}); err == nil {
		t.Fatal("unknown transport should be rejected")
	}
	if _, err := Normalize(Command{Server: "127.0.0.1:1", VKey: "key", Transport: "ws", BridgePath: "relative"}); err == nil {
		t.Fatal("relative WS path should be rejected")
	}
}

func TestServiceSuffix(t *testing.T) {
	cases := []struct {
		command Command
		want    string
	}{
		{command: Command{Server: "host:1", VKey: "v", Transport: "tcp"}, want: ""},
		{command: Command{Server: "host:1", VKey: "v", Transport: "tcp", TLS: true}, want: "tls"},
		{command: Command{Server: "host:1", VKey: "v", Transport: "kcp"}, want: "kcp"},
		{command: Command{Server: "host:1", VKey: "v", Transport: "ws"}, want: "ws"},
		{command: Command{Server: "host:1", VKey: "v", Transport: "quic"}, want: "quic"},
		{command: Command{Server: "host:1", VKey: "v", Endpoint: "http3", Transport: "quic"}, want: "http3"},
	}
	for _, test := range cases {
		if got := test.command.ServiceSuffix(); got != test.want {
			t.Fatalf("ServiceSuffix(%#v) = %q, want %q", test.command, got, test.want)
		}
	}
}
