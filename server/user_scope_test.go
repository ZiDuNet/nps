package server

import (
	"testing"

	"ehang.io/nps/lib/file"
)

func TestFilterClientsByUserId(t *testing.T) {
	alice := &file.Client{Id: 1, UserId: 10, VerifyKey: "alice-a"}
	bob := &file.Client{Id: 2, UserId: 20, VerifyKey: "bob-a"}
	clients := []*file.Client{alice, bob}

	got := FilterClientsByUserId(clients, 10)

	if len(got) != 1 || got[0].Id != alice.Id {
		t.Fatalf("expected only alice client, got %#v", got)
	}
}

func TestFilterTunnelsByAllowedClients(t *testing.T) {
	aliceClient := &file.Client{Id: 1, UserId: 10}
	bobClient := &file.Client{Id: 2, UserId: 20}
	tunnels := []*file.Tunnel{
		{Id: 1, Client: aliceClient},
		{Id: 2, Client: bobClient},
	}

	got := FilterTunnelsByAllowedClients(tunnels, map[int]struct{}{1: {}})

	if len(got) != 1 || got[0].Id != 1 {
		t.Fatalf("expected only alice tunnel, got %#v", got)
	}
}
