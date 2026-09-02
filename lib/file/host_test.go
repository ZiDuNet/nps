package file

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreviewTargetDoesNotAdvanceRoundRobin(t *testing.T) {
	target := &Target{TargetStr: "127.0.0.1:8080\n127.0.0.1:8081"}

	preview, err := target.PreviewTarget()
	if err != nil {
		t.Fatal(err)
	}
	if preview != "127.0.0.1:8081" {
		t.Fatalf("preview = %q, want the next round-robin target", preview)
	}

	selected, err := target.GetRandomTarget()
	if err != nil {
		t.Fatal(err)
	}
	if selected != preview {
		t.Fatalf("selection after preview = %q, want %q", selected, preview)
	}

	preview, err = target.PreviewTarget()
	if err != nil {
		t.Fatal(err)
	}
	if preview != "127.0.0.1:8080" {
		t.Fatalf("second preview = %q, want the wrapped round-robin target", preview)
	}
}

func TestGetInfoByHostWildcardUsesDNSLabelBoundaries(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Hosts.Store(1, &Host{Id: 1, Host: "*.example.com", Scheme: "http", Location: "/"})
	utils := &DbUtils{JsonDb: db}

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "subdomain", host: "api.example.com", want: true},
		{name: "case insensitive with port", host: "API.EXAMPLE.COM:80", want: true},
		{name: "trailing dot", host: "api.example.com.", want: true},
		{name: "apex is not wildcard", host: "example.com", want: false},
		{name: "suffix lookalike", host: "api.example.com.evil", want: false},
		{name: "label lookalike", host: "api.notexample.com", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://api.example.com/", nil)
			r.RequestURI = "/"
			if _, err := utils.GetInfoByHost(test.host, r); (err == nil) != test.want {
				t.Fatalf("GetInfoByHost(%q) matched = %v, want %v", test.host, err == nil, test.want)
			}
		})
	}
}

func TestGetInfoByHostPrefersExactRuleAndPathBoundary(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Hosts.Store(1, &Host{Id: 1, Host: "*.example.com", Scheme: "http", Location: "/api", Remark: "wildcard"})
	db.Hosts.Store(2, &Host{Id: 2, Host: "api.example.com", Scheme: "http", Location: "/", Remark: "exact"})
	utils := &DbUtils{JsonDb: db}
	request, _ := http.NewRequest(http.MethodGet, "http://other.example.com/apiary", nil)
	if _, err := utils.GetInfoByHost(request.Host, request); err == nil {
		t.Fatal("location /api must not match /apiary")
	}
	request, _ = http.NewRequest(http.MethodGet, "http://api.example.com/api/v1", nil)
	host, err := utils.GetInfoByHost(request.Host, request)
	if err != nil || host == nil || host.Id != 2 {
		t.Fatalf("exact host rule should win over wildcard rule: host=%#v err=%v", host, err)
	}
}

func TestGetHostByAllowedClientsSkipsMalformedRecord(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Hosts.Store(1, &Host{Id: 1})
	utils := &DbUtils{JsonDb: db}

	hosts, count := utils.GetHostByAllowedClients(0, 20, 0, "", nil)
	if count != 0 || len(hosts) != 0 {
		t.Fatalf("malformed host should be skipped, got count=%d hosts=%d", count, len(hosts))
	}
}

func TestGetHostByAllowedClientsFilteredByOwnerAndRemark(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	ownerClient := &Client{Id: 1, UserId: 7, VerifyKey: "owner"}
	otherClient := &Client{Id: 2, UserId: 9, VerifyKey: "other"}
	unassignedClient := &Client{Id: 3, UserId: 0, VerifyKey: "unassigned"}
	staleClient := &Client{Id: 4, UserId: 0, VerifyKey: "stale"}
	db.Clients.Store(ownerClient.Id, ownerClient)
	db.Clients.Store(otherClient.Id, otherClient)
	db.Clients.Store(unassignedClient.Id, unassignedClient)
	db.Clients.Store(staleClient.Id, &Client{Id: 4, UserId: 7, VerifyKey: "stale"})
	db.Hosts.Store(1, &Host{Id: 1, Host: "one.example.com", Remark: "production", Client: ownerClient})
	db.Hosts.Store(2, &Host{Id: 2, Host: "two.example.com", Remark: "staging", Client: ownerClient})
	db.Hosts.Store(3, &Host{Id: 3, Host: "three.example.com", Remark: "production", Client: otherClient})
	db.Hosts.Store(4, &Host{Id: 4, Host: "four.example.com", Remark: "unassigned", Client: unassignedClient})
	db.Hosts.Store(5, &Host{Id: 5, Host: "five.example.com", Remark: "stale-owner", Client: staleClient})
	utils := &DbUtils{JsonDb: db}

	owner := 7
	hosts, total := utils.GetHostByAllowedClientsFiltered(0, 20, 0, "stag", &OwnerFilter{UserID: &owner}, nil)
	if total != 1 || len(hosts) != 1 || hosts[0].Id != 2 {
		t.Fatalf("owner + remark host filter = id=%v total=%d, want [2], 1", hostIDs(hosts), total)
	}
	hosts, total = utils.GetHostByAllowedClientsFiltered(0, 20, 0, "stale", &OwnerFilter{UserID: &owner}, nil)
	if total != 1 || len(hosts) != 1 || hosts[0].Id != 5 {
		t.Fatalf("stale client owner host filter = id=%v total=%d, want [5], 1", hostIDs(hosts), total)
	}
	unassigned := 0
	hosts, total = utils.GetHostByAllowedClientsFiltered(0, 20, 0, "", &OwnerFilter{UserID: &unassigned}, nil)
	if total != 1 || len(hosts) != 1 || hosts[0].Id != 4 {
		t.Fatalf("unassigned host filter = id=%v total=%d, want [4], 1", hostIDs(hosts), total)
	}
}

func hostIDs(hosts []*Host) []int {
	ids := make([]int, 0, len(hosts))
	for _, host := range hosts {
		if host != nil {
			ids = append(ids, host.Id)
		}
	}
	return ids
}

func TestHostRuleMatchesRejectsInvalidWildcards(t *testing.T) {
	for _, rule := range []string{"example*.com", "*example.com", "*.*.example.com", "*."} {
		if hostRuleMatches("api.example.com", rule) {
			t.Fatalf("invalid wildcard rule %q matched", rule)
		}
	}
}

func TestHostRouteConflictIsolatedByOwner(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	ownerA := &Client{Id: 1, UserId: 10}
	ownerB := &Client{Id: 2, UserId: 20}
	db.Hosts.Store(1, &Host{Id: 1, Host: "app.example.com", Location: "/", Scheme: "all", Client: ownerA})
	utils := &DbUtils{JsonDb: db}

	if !utils.IsHostRouteConflict(&Host{Id: 2, Host: "app.example.com", Location: "/admin", Scheme: "https", Client: ownerB}) {
		t.Fatal("cross-tenant overlapping host path must be rejected")
	}
	if utils.IsHostRouteConflict(&Host{Id: 2, Host: "app.example.com", Location: "/admin", Scheme: "https", Client: &Client{Id: 3, UserId: 10}}) {
		t.Fatal("same tenant should be allowed to partition host paths")
	}
	if utils.IsHostRouteConflict(&Host{Id: 2, Host: "other.example.com", Location: "/other", Scheme: "https", Client: ownerB}) {
		t.Fatal("non-overlapping host path should remain available")
	}
}

func TestHostRouteConflictHandlesWildcardAndSchemeOverlap(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Hosts.Store(1, &Host{Id: 1, Host: "*.example.com", Location: "/", Scheme: "http", Client: &Client{Id: 1}})
	utils := &DbUtils{JsonDb: db}

	if !utils.IsHostRouteConflict(&Host{Id: 2, Host: "api.example.com", Location: "/v1", Scheme: "all", Client: &Client{Id: 2}}) {
		t.Fatal("exact host under a wildcard must be rejected across clients")
	}
	if utils.IsHostRouteConflict(&Host{Id: 2, Host: "api.other.example.com", Location: "/v1", Scheme: "https", Client: &Client{Id: 2}}) {
		t.Fatal("disjoint host and scheme should remain available")
	}
}

func TestNewHostRejectsOverlappingCrossTenantRoute(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	first := &Host{Id: 1, Host: "app.example.com", Location: "/", Scheme: "all", Client: &Client{Id: 1, UserId: 1}, Target: &Target{TargetStr: "127.0.0.1:8080"}}
	if err := utils.NewHost(first); err != nil {
		t.Fatal(err)
	}
	second := &Host{Id: 2, Host: "app.example.com", Location: "/admin", Scheme: "https", Client: &Client{Id: 2, UserId: 2}, Target: &Target{TargetStr: "127.0.0.1:8081"}}
	if err := utils.NewHost(second); err == nil {
		t.Fatal("NewHost must reject a cross-tenant overlapping route")
	}
}

func TestLoadHostSkipsPersistedCrossTenantOverlap(t *testing.T) {
	runPath := t.TempDir()
	source := NewJsonDb(runPath)
	source.Clients.Store(1, &Client{Id: 1, UserId: 1})
	source.Clients.Store(2, &Client{Id: 2, UserId: 2})
	source.Hosts.Store(1, &Host{Id: 1, Host: "app.example.com", Location: "/", Scheme: "all", Client: &Client{Id: 1}, Target: &Target{TargetStr: "127.0.0.1:8080"}})
	source.Hosts.Store(2, &Host{Id: 2, Host: "app.example.com", Location: "/admin", Scheme: "https", Client: &Client{Id: 2}, Target: &Target{TargetStr: "127.0.0.1:8081"}})
	storeSyncMapToFile(&source.Clients, source.ClientFilePath)
	storeSyncMapToFile(&source.Hosts, source.HostFilePath)

	loaded := NewJsonDb(runPath)
	loaded.LoadClientFromJsonFile()
	loaded.LoadHostFromJsonFile()
	count := 0
	loaded.Hosts.Range(func(_, value interface{}) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected one non-overlapping persisted host, got %d", count)
	}
}

func TestIsHostExistSupportsEditValidationWithoutClient(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Hosts.Store(1, &Host{Id: 1, Host: "existing.example.com", Location: "/", Scheme: "https", Client: &Client{Id: 1}})
	utils := &DbUtils{JsonDb: db}

	if !utils.IsHostExist(&Host{Id: 2, Host: "existing.example.com", Location: "/", Scheme: "https"}) {
		t.Fatal("host edit validation must reject a duplicate rule without requiring a client pointer")
	}
	if utils.IsHostExist(&Host{Id: 1, Host: "existing.example.com", Location: "/", Scheme: "https"}) {
		t.Fatal("host edit validation must not treat the current record as a duplicate")
	}
}
