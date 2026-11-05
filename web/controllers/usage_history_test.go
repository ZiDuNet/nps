package controllers

import (
	"net/url"
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
