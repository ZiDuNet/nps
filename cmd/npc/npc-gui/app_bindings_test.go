package main

import "testing"

func TestNormalizeShortcutMigratesLegacyTCP(t *testing.T) {
	shortcut := ShortClient{Name: "legacy", Addr: "bridge.example.com:8024", Key: "vkey"}
	if err := normalizeShortcut(&shortcut); err != nil {
		t.Fatal(err)
	}
	if shortcut.Transport != "tcp" || shortcut.TLS {
		t.Fatalf("legacy shortcut should migrate to plain TCP: %+v", shortcut)
	}
	if got := shortcutID(shortcut); got != "bridge.example.com:8024|vkey" {
		t.Fatalf("plain TCP ID changed and would break saved state: %q", got)
	}
}

func TestShortcutIDSeparatesTransportOptions(t *testing.T) {
	items := []ShortClient{
		{Addr: "bridge.example.com:8024", Key: "vkey", Transport: "tcp"},
		{Addr: "bridge.example.com:8024", Key: "vkey", Transport: "tcp", TLS: true},
		{Addr: "bridge.example.com:8027", Key: "vkey", Transport: "ws", BridgePath: "/ws"},
		{Addr: "bridge.example.com:8029", Key: "vkey", Transport: "quic", QuicALPN: "nps"},
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := normalizeShortcut(&item); err != nil {
			t.Fatal(err)
		}
		id := shortcutID(item)
		if _, exists := seen[id]; exists {
			t.Fatalf("shortcut ID collision: %q", id)
		}
		seen[id] = struct{}{}
	}
}
