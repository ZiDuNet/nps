package common

import (
	"net"
	"testing"
)

func TestIsPublicHost(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "example.com:443", want: true},
		{addr: "203.0.113.10:443", want: false},
		{addr: "192.168.1.10:80", want: false},
		{addr: "127.0.0.1:80", want: false},
	}
	for _, test := range tests {
		if got := IsPublicHost(test.addr); got != test.want {
			t.Errorf("IsPublicHost(%q) = %v, want %v", test.addr, got, test.want)
		}
	}
}

func TestBuildBindAddresses(t *testing.T) {
	if got := BuildTCPBindAddr("bad-ip"); got != nil {
		t.Fatalf("BuildTCPBindAddr(invalid) = %v, want nil", got)
	}
	if got := BuildUDPBindAddr("2001:db8::1"); got == nil {
		t.Fatal("BuildUDPBindAddr(valid IPv6) = nil")
	}
	if got := BuildTCPBindAddr("0.0.0.0"); got != nil {
		t.Fatalf("BuildTCPBindAddr(unspecified) = %v, want nil", got)
	}
	if ip := net.ParseIP("198.51.100.10"); IsPublicIPStrict(ip) {
		t.Fatal("documentation IPv4 address classified as public")
	}
}

func TestEffectiveHTTPClientIP(t *testing.T) {
	if got := EffectiveHTTPClientIP("172.18.0.2:43122", "203.0.113.9", "198.51.100.4, 172.18.0.2", false); got != "172.18.0.2" {
		t.Fatalf("forwarded address used while disabled: %q", got)
	}
	if got := EffectiveHTTPClientIP("172.18.0.2:43122", "203.0.113.9", "198.51.100.4, 172.18.0.2", true); got != "203.0.113.9" {
		t.Fatalf("X-Real-IP = %q, want 203.0.113.9", got)
	}
	if got := EffectiveHTTPClientIP("172.18.0.2:43122", "not-an-ip", "198.51.100.4, 172.18.0.2", true); got != "198.51.100.4" {
		t.Fatalf("first X-Forwarded-For = %q, want 198.51.100.4", got)
	}
}
