package ipreg

import (
	"net"
	"testing"
	"time"
)

func TestRegisterSupportsClientAndGlobalBuckets(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	ip, ok := ParseIP("203.0.113.7:443")
	if !ok {
		t.Fatal("could not parse IPv4 address with port")
	}
	if Register(7, ip, time.Hour).IsZero() {
		t.Fatal("registration returned zero expiry")
	}
	if !RegisteredString(7, "203.0.113.7") {
		t.Fatal("client registration was not found")
	}
	if RegisteredString(8, "203.0.113.7") {
		t.Fatal("client registration leaked to another client")
	}
	Register(AnyClient, ip, time.Hour)
	if !RegisteredString(8, "203.0.113.7:1") {
		t.Fatal("global registration was not accepted")
	}
}

func TestRegistrationExpiresAndNormalizesIPv4MappedIPv6(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	addr, ok := ParseIP("::ffff:198.51.100.10")
	if !ok {
		t.Fatal("could not parse mapped IPv6 address")
	}
	Register(2, addr, 20*time.Millisecond)
	if !RegisteredAddr(2, &net.TCPAddr{IP: net.ParseIP("198.51.100.10"), Port: 1234}) {
		t.Fatal("mapped IPv6 registration did not normalize to IPv4")
	}
	time.Sleep(30 * time.Millisecond)
	if RegisteredString(2, "198.51.100.10") {
		t.Fatal("expired registration remained active")
	}
}
