package server

import (
	"testing"

	"ehang.io/nps/lib/file"
)

func TestGetTunnelByOwnerFilterSupportsRemarkAndUnassigned(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	ownerClient := &file.Client{Id: 1, UserId: 7, VerifyKey: "owner"}
	staleOwnerClient := &file.Client{Id: 4, UserId: 0, VerifyKey: "stale"}
	otherClient := &file.Client{Id: 2, UserId: 9, VerifyKey: "other"}
	unassignedClient := &file.Client{Id: 3, UserId: 0, VerifyKey: "unassigned"}
	db.Clients.Store(ownerClient.Id, ownerClient)
	// The task keeps a pointer from before the client was edited. The current
	// database record below is the source of truth for ownership filtering.
	db.Clients.Store(staleOwnerClient.Id, &file.Client{Id: 4, UserId: 7, VerifyKey: "stale"})
	db.Clients.Store(otherClient.Id, otherClient)
	db.Clients.Store(unassignedClient.Id, unassignedClient)
	db.Tasks.Store(1, &file.Tunnel{Id: 1, Mode: "tcp", Remark: "production", Client: ownerClient, Target: &file.Target{TargetStr: "127.0.0.1:8080"}})
	db.Tasks.Store(2, &file.Tunnel{Id: 2, Mode: "tcp", Remark: "staging", Client: ownerClient, Target: &file.Target{TargetStr: "127.0.0.1:8081"}})
	db.Tasks.Store(3, &file.Tunnel{Id: 3, Mode: "tcp", Remark: "production", Client: otherClient, Target: &file.Target{TargetStr: "127.0.0.1:8082"}})
	db.Tasks.Store(4, &file.Tunnel{Id: 4, Mode: "tcp", Remark: "unassigned", Client: unassignedClient, Target: &file.Target{TargetStr: "127.0.0.1:8083"}})
	db.Tasks.Store(5, &file.Tunnel{Id: 5, Mode: "tcp", Remark: "stale-owner", Client: staleOwnerClient, Target: &file.Target{TargetStr: "127.0.0.1:8084"}})

	dbUtils := file.GetDb()
	oldDB := dbUtils.JsonDb
	oldBridge := Bridge
	dbUtils.JsonDb = db
	Bridge = nil
	t.Cleanup(func() {
		dbUtils.JsonDb = oldDB
		Bridge = oldBridge
	})

	ownerID := 7
	tunnels, total := GetTunnelByOwnerFilter(0, 20, "tcp", 0, "stag", "", "", &file.OwnerFilter{UserID: &ownerID}, nil)
	if total != 1 || len(tunnels) != 1 || tunnels[0].Id != 2 {
		t.Fatalf("owner + remark tunnel filter = ids=%v total=%d, want [2], 1", tunnelIDs(tunnels), total)
	}
	staleTunnels, staleTotal := GetTunnelByOwnerFilter(0, 20, "tcp", 0, "stale", "", "", &file.OwnerFilter{UserID: &ownerID}, nil)
	if staleTotal != 1 || len(staleTunnels) != 1 || staleTunnels[0].Id != 5 {
		t.Fatalf("stale client owner filter = ids=%v total=%d, want [5], 1", tunnelIDs(staleTunnels), staleTotal)
	}

	unassignedID := 0
	tunnels, total = GetTunnelByOwnerFilter(0, 20, "tcp", 0, "", "", "", &file.OwnerFilter{UserID: &unassignedID}, nil)
	if total != 1 || len(tunnels) != 1 || tunnels[0].Id != 4 {
		t.Fatalf("unassigned tunnel filter = ids=%v total=%d, want [4], 1", tunnelIDs(tunnels), total)
	}
}

func tunnelIDs(tunnels []*file.Tunnel) []int {
	ids := make([]int, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel != nil {
			ids = append(ids, tunnel.Id)
		}
	}
	return ids
}
