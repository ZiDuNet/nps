package server

import (
	"strings"
	"sync/atomic"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestTopologyDataScopesOperationalResourcesToAllowedClients(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	owned := file.NewClient("owned-secret", false, false)
	owned.Id = 901
	owned.Remark = "owned client"
	owned.Status = true
	owned.Addr = "198.51.100.10:12000"
	owned.MaxConn = 8
	owned.Flow = &file.Flow{InletFlow: 120, ExportFlow: 80, FlowLimit: 2}
	other := file.NewClient("other-secret", false, false)
	other.Id = 902
	other.Remark = "other client"
	other.Status = true
	other.Flow = &file.Flow{InletFlow: 900, ExportFlow: 700}
	db.Clients.Store(owned.Id, owned)
	db.Clients.Store(other.Id, other)

	ownedTask := &file.Tunnel{
		Id: 9011, Mode: "tcp", Status: true, Port: 30111, ServerIp: "0.0.0.0",
		Client: owned, Target: &file.Target{TargetStr: "127.0.0.1:8080"},
		Flow: &file.Flow{InletFlow: 40, ExportFlow: 20},
	}
	atomic.StoreInt32(&ownedTask.CurrentConnections, 3)
	db.Tasks.Store(ownedTask.Id, ownedTask)
	db.Tasks.Store(9021, &file.Tunnel{
		Id: 9021, Mode: "udp", Status: true, Port: 30112, Client: other,
		Target: &file.Target{TargetStr: "127.0.0.1:9090"}, Flow: file.NewFlow(),
	})
	db.Hosts.Store(9031, &file.Host{
		Id: 9031, Host: "owned.example", Scheme: "https", Client: owned,
		Target: &file.Target{TargetStr: "127.0.0.1:3000"}, Flow: file.NewFlow(),
	})
	db.Hosts.Store(9041, &file.Host{
		Id: 9041, Host: "other.example", Scheme: "https", Client: other,
		Target: &file.Target{TargetStr: "127.0.0.1:4000"}, Flow: file.NewFlow(),
	})

	dbUtils := file.GetDb()
	oldDB := dbUtils.JsonDb
	oldBridge := Bridge
	dbUtils.JsonDb = db
	Bridge = nil
	t.Cleanup(func() {
		dbUtils.JsonDb = oldDB
		Bridge = oldBridge
	})

	data := GetTopologyData(map[int]struct{}{owned.Id: {}}, false)
	if len(data.Clients) != 1 || data.Clients[0].ID != owned.Id {
		t.Fatalf("visible clients = %#v, want only %d", data.Clients, owned.Id)
	}
	if got := len(data.Resources); got != 2 {
		t.Fatalf("visible resources = %d, want 2", got)
	}
	for _, resource := range data.Resources {
		if resource.ClientID != owned.Id {
			t.Fatalf("resource %d escaped client scope: %#v", resource.ID, resource)
		}
		if strings.Contains(resource.Target, "9090") || strings.Contains(resource.Entry, "30112") {
			t.Fatalf("other client resource leaked into response: %#v", resource)
		}
	}

	var tunnel TopologyResource
	for _, resource := range data.Resources {
		if resource.ID == ownedTask.Id {
			tunnel = resource
			break
		}
	}
	if tunnel.CurrentConnections != 3 {
		t.Fatalf("current connections = %d, want 3", tunnel.CurrentConnections)
	}
	if tunnel.Entry != ":30111" || tunnel.Target != "127.0.0.1:8080" {
		t.Fatalf("tunnel endpoints = %q -> %q", tunnel.Entry, tunnel.Target)
	}

	noScope := GetTopologyData(nil, false)
	if len(noScope.Clients) != 0 || len(noScope.Resources) != 0 {
		t.Fatalf("nil non-admin scope leaked data: %#v", noScope)
	}
}
