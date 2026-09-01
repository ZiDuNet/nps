package file

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
