package common

import (
	"context"
	"net"
	"testing"
)

func TestSetCustomDNSRejectsInvalidServer(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	if err := SetCustomDNS("127.0.0.1:5353"); err != nil {
		t.Fatalf("install valid DNS resolver: %v", err)
	}
	if err := SetCustomDNS("127.0.0.1:0"); err == nil {
		t.Fatal("SetCustomDNS accepted port zero")
	}
	if net.DefaultResolver != original {
		t.Fatal("invalid DNS configuration changed the active resolver")
	}
}

func TestSetCustomDNSUsesConfiguredServers(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	if err := SetCustomDNS("127.0.0.1:5353,127.0.0.1:5354"); err != nil {
		t.Fatalf("SetCustomDNS: %v", err)
	}
	resolver := net.DefaultResolver
	if resolver == original || resolver.Dial == nil {
		t.Fatal("custom resolver was not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Dial(ctx, "udp", "ignored:53"); err == nil {
		t.Fatal("cancelled DNS dial unexpectedly succeeded")
	}
	if err := SetCustomDNS(""); err != nil {
		t.Fatalf("restore system DNS resolver: %v", err)
	}
	if net.DefaultResolver != original {
		t.Fatal("empty DNS configuration did not restore system resolver")
	}
}
