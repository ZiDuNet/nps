package controllers

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	usagehistory "ehang.io/nps/server/usage"
)

func TestParseUsageHistoryQueryDefaultsAndBucketSelection(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	query, bucket, err := parseUsageHistoryQuery(url.Values{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if query.Scope != usagehistory.ScopeGlobal || bucket != "1m" {
		t.Fatalf("query = %+v bucket=%s", query, bucket)
	}
	if !query.From.Equal(now.Add(-24*time.Hour)) || !query.To.Equal(now) {
		t.Fatalf("range = %s .. %s", query.From, query.To)
	}

	values := url.Values{"from": {now.Add(-72 * time.Hour).Format(time.RFC3339)}}
	_, bucket, err = parseUsageHistoryQuery(values, now)
	if err != nil {
		t.Fatal(err)
	}
	if bucket != "5m" {
		t.Fatalf("bucket = %s, want 5m", bucket)
	}
}

func TestParseUsageHistoryQueryMapsIDByScope(t *testing.T) {
	now := time.Now()
	values := url.Values{"scope": {"client"}, "id": {"42"}}
	query, _, err := parseUsageHistoryQuery(values, now)
	if err != nil {
		t.Fatal(err)
	}
	if query.Scope != usagehistory.ScopeClient || query.Client != 42 {
		t.Fatalf("query = %+v", query)
	}

	values.Set("scope", "user")
	query, _, err = parseUsageHistoryQuery(values, now)
	if err != nil {
		t.Fatal(err)
	}
	if query.Scope != usagehistory.ScopeUser || query.UserID != 42 {
		t.Fatalf("query = %+v", query)
	}
}

func TestParseUsageHistoryQueryRejectsInvalidRanges(t *testing.T) {
	now := time.Now()
	values := url.Values{"from": {now.Format(time.RFC3339)}, "to": {now.Format(time.RFC3339)}}
	if _, _, err := parseUsageHistoryQuery(values, now); err == nil {
		t.Fatal("expected equal from/to to be rejected")
	}
	values = url.Values{"scope": {"unknown"}}
	if _, _, err := parseUsageHistoryQuery(values, now); err == nil {
		t.Fatal("expected unknown scope to be rejected")
	}
}

func TestScopeUsageHistoryResourcesEnforcesUserAndClientBoundaries(t *testing.T) {
	resources := []usagehistory.Resource{
		{Key: "user:1", Scope: usagehistory.ScopeUser, ID: 1},
		{Key: "user:2", Scope: usagehistory.ScopeUser, ID: 2},
		{Key: "client:10", Scope: usagehistory.ScopeClient, ID: 10, ClientID: 10},
		{Key: "client:20", Scope: usagehistory.ScopeClient, ID: 20, ClientID: 20},
		{Key: "host:100", Scope: usagehistory.ScopeHost, ID: 100, ClientID: 10},
	}
	allowed := map[int]struct{}{10: {}}
	filtered := scopeUsageHistoryResources(resources, false, 1, 0, allowed)
	if len(filtered) != 3 || filtered[0].Key != "user:1" || filtered[1].Key != "client:10" || filtered[2].Key != "host:100" {
		t.Fatalf("user-scoped resources = %+v", filtered)
	}

	filtered = scopeUsageHistoryResources(resources, false, 0, 20, allowed)
	if len(filtered) != 0 {
		t.Fatalf("client 20 should not pass an unrelated allowlist: %+v", filtered)
	}
	if got := scopeUsageHistoryResources(resources, true, 0, 0, nil); len(got) != len(resources) {
		t.Fatalf("admin received %d resources, want %d", len(got), len(resources))
	}
}

func TestDashboardHistoryReadsLegacySamplesWithNormalPermissionFilters(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	points := []usagehistory.Point{{Time: now.Add(-time.Minute).Unix(), Upload: 1024, Download: 2048}}
	snapshot := usagehistory.SnapshotFile{
		Version:   1,
		UpdatedAt: now.Unix(),
		Resources: map[string]*usagehistory.Resource{
			"global:0":  {Key: "global:0", Scope: usagehistory.ScopeGlobal, Points1m: points},
			"user:7":    {Key: "user:7", Scope: usagehistory.ScopeUser, ID: 7, Points1m: points},
			"user:8":    {Key: "user:8", Scope: usagehistory.ScopeUser, ID: 8, Points1m: points},
			"client:42": {Key: "client:42", Scope: usagehistory.ScopeClient, ID: 42, UserID: 7, Points1m: points},
			"client:43": {Key: "client:43", Scope: usagehistory.ScopeClient, ID: 43, UserID: 8, Points1m: points},
		},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	history := usagehistory.New(usagehistory.Options{Path: path})
	if err := history.Load(); err != nil {
		t.Fatal(err)
	}
	allowed := map[int]struct{}{42: {}}
	for _, test := range []struct {
		name     string
		scope    string
		id       string
		userID   int
		clientID int
		want     string
	}{
		{"user-dashboard", "user", "7", 7, 0, "user:7"},
		{"vkey-dashboard", "client", "42", 0, 42, "client:42"},
		{"user-owned-client", "client", "42", 7, 0, "client:42"},
		{"other-user", "user", "8", 7, 0, ""},
		{"other-client", "client", "43", 7, 0, ""},
		{"vkey-other-client", "client", "43", 0, 42, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, bucket, err := parseUsageHistoryQuery(url.Values{"scope": {test.scope}, "id": {test.id}}, now)
			if err != nil {
				t.Fatal(err)
			}
			series, _ := history.Series(query, bucket)
			visible := scopeUsageHistoryResources(series, false, test.userID, test.clientID, allowed)
			if test.want == "" {
				if len(visible) != 0 {
					t.Fatalf("foreign history was exposed: %+v", visible)
				}
				return
			}
			if len(visible) != 1 || visible[0].Key != test.want || len(visible[0].Points1m) != 1 || visible[0].Points1m[0].Upload != 1024 {
				t.Fatalf("dashboard history = %+v, want retained samples for %s", visible, test.want)
			}
		})
	}
}
