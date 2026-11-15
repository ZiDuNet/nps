package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
)

func TestHistoryCollectorSupportsScopedDashboardQueries(t *testing.T) {
	db := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice"})
	db.JsonDb.Users.Store(8, &file.User{Id: 8, UserName: "bob"})
	client := &file.Client{Id: 42, UserId: 7, Remark: "alice-client", Flow: file.NewFlow()}
	other := &file.Client{Id: 43, UserId: 8, Remark: "bob-client", Flow: file.NewFlow()}
	db.JsonDb.Clients.Store(client.Id, client)
	db.JsonDb.Clients.Store(other.Id, other)
	tunnel := &file.Tunnel{Id: 101, Client: client, Mode: "tcp", Flow: file.NewFlow()}
	host := &file.Host{Id: 201, Client: client, Host: "app.example.com", Flow: file.NewFlow()}
	p2p := &file.Tunnel{Id: 102, Client: client, Mode: "p2p", Flow: file.NewFlow()}
	db.JsonDb.Tasks.Store(tunnel.Id, tunnel)
	db.JsonDb.Tasks.Store(p2p.Id, p2p)
	db.JsonDb.Hosts.Store(host.Id, host)
	history := New(Options{})
	history.collect = func() totalsSnapshot { return collectTotalsFromDB(db) }
	now := time.Now().Truncate(5 * time.Minute)
	history.Sample(now)
	if points := history.resources["user:7"].Points1m; len(points) != 0 {
		t.Fatalf("first sample must only establish a baseline: %+v", points)
	}
	client.Flow.Add(1<<20, 2<<20)
	other.Flow.Add(4<<20, 8<<20)
	tunnel.Flow.Add(100, 200)
	host.Flow.Add(300, 400)
	p2p.Flow.Add(16<<20, 32<<20)
	history.Sample(now.Add(time.Minute))

	for _, test := range []struct {
		name     string
		query    Query
		key      string
		upload   int64
		download int64
	}{
		{"admin", Query{Scope: ScopeGlobal}, "global:0", 5 << 20, 10 << 20},
		{"user", Query{Scope: ScopeUser, ID: 7, UserID: 7}, "user:7", 1 << 20, 2 << 20},
		{"vkey", Query{Scope: ScopeClient, ID: 42, Client: 42}, "client:42", 1 << 20, 2 << 20},
		{"user-client", Query{Scope: ScopeClient, ID: 42, UserID: 7, Client: 42}, "client:42", 1 << 20, 2 << 20},
		{"tunnel", Query{Scope: ScopeTunnel, ID: 101, UserID: 7, Client: 42}, "tunnel:101", 100, 200},
		{"host", Query{Scope: ScopeHost, ID: 201, UserID: 7, Client: 42}, "host:201", 300, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, bucket := range []string{"1m", "5m"} {
				series, _ := history.Series(test.query, bucket)
				if len(series) != 1 || series[0].Key != test.key {
					t.Fatalf("%s query returned %+v", bucket, series)
				}
				points := series[0].Points1m
				if bucket == "5m" {
					points = series[0].Points5m
				}
				if len(points) != 1 || points[0].Upload != test.upload || points[0].Download != test.download {
					t.Fatalf("%s points = %+v, want %d/%d bytes", bucket, points, test.upload, test.download)
				}
			}
			if summaries := history.Query(test.query); len(summaries) != 1 || summaries[0].UploadBytes != test.upload {
				t.Fatalf("summary = %+v", summaries)
			}
		})
	}
	for _, query := range []Query{
		{Scope: ScopeUser, ID: 7, UserID: 8},
		{Scope: ScopeClient, ID: 42, UserID: 8},
		{Scope: ScopeClient, ID: 42, Client: 43},
		{Scope: ScopeTunnel, ID: 101, Client: 43},
		{Scope: ScopeTunnel, ID: 102},
	} {
		if series, _ := history.Series(query, "1m"); len(series) != 0 {
			t.Fatalf("mismatched ownership or P2P query %+v returned %+v", query, series)
		}
	}
}

func TestHistoryLoadRepairsLegacyIdentityWithoutChangingOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Now().Truncate(5 * time.Minute)
	points := []Point{{Time: now.Add(-time.Hour).Unix(), Upload: 123, Download: 456}}
	legacy := New(Options{Path: path})
	legacy.resources = map[string]*Resource{
		"user:7":    {Key: "user:7", Scope: ScopeUser, ID: 7, Name: "alice", TotalBytes: 579, Points1m: points, Points5m: points},
		"client:42": {Key: "client:42", Scope: ScopeClient, ID: 42, UserID: 7, TotalBytes: 579, Points1m: points, Points5m: points},
		"client:43": {Key: "client:43", Scope: ScopeClient, ID: 43, UserID: 8, Points1m: points},
		"client:44": {Key: "client:44", Scope: ScopeClient, ID: 44, Points1m: points},
		"user:9":    {Key: "user:9", Scope: ScopeUser, ID: 9, UserID: 8, Points1m: points},
		"tunnel:9":  {Key: "tunnel:9", Scope: ScopeTunnel, ID: 9, UserID: 8, ClientID: 43, Points1m: points},
		"host:9":    {Key: "host:9", Scope: ScopeHost, ID: 9, UserID: 8, ClientID: 43, Points1m: points},
	}
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	loaded := New(Options{Path: path})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []Query{
		{Scope: ScopeUser, ID: 7, UserID: 7},
		{Scope: ScopeClient, ID: 42, UserID: 7, Client: 42},
	} {
		for _, bucket := range []string{"1m", "5m"} {
			series, _ := loaded.Series(query, bucket)
			if len(series) != 1 || series[0].TotalBytes != 579 {
				t.Fatalf("legacy %s query %+v = %+v", bucket, query, series)
			}
			got := series[0].Points1m
			if bucket == "5m" {
				got = series[0].Points5m
			}
			if !reflect.DeepEqual(got, points) {
				t.Fatalf("legacy samples changed: %+v", got)
			}
		}
	}
	for _, query := range []Query{
		{Scope: ScopeClient, ID: 43, UserID: 7},
		{Scope: ScopeClient, ID: 44, UserID: 7},
		{Scope: ScopeUser, ID: 9, UserID: 9},
		{Scope: ScopeTunnel, ID: 9, UserID: 7},
		{Scope: ScopeHost, ID: 9, Client: 42},
	} {
		if series, _ := loaded.Series(query, "1m"); len(series) != 0 {
			t.Fatalf("legacy repair crossed an ownership boundary: %+v", series)
		}
	}
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}
	var saved SnapshotFile
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Resources["user:7"].UserID != 7 || saved.Resources["client:42"].ClientID != 42 {
		t.Fatalf("repaired metadata was not saved in the compatible snapshot format: %+v", saved)
	}
	if saved.Resources["client:44"].UserID != 0 || saved.Resources["user:9"].UserID != 8 {
		t.Fatal("legacy repair must not infer or overwrite an existing owner")
	}
}

func TestHistoryAutomaticallyExpiresDeletedResourcesAndBaselines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	history := New(Options{Path: path, Retention1m: time.Hour, Retention5m: 2 * time.Hour})
	now := time.Now().Truncate(5 * time.Minute)
	upload, download := int64(100), int64(200)
	present := true
	history.collect = func() totalsSnapshot {
		snapshot := totalsSnapshot{resources: make(map[string]*totalResource)}
		if present {
			snapshot.resources["client:1"] = &totalResource{
				Resource: Resource{Key: "client:1", Scope: ScopeClient, ID: 1, ClientID: 1},
				In:       upload, Out: download,
			}
		}
		return snapshot
	}
	history.Sample(now.Add(-time.Minute))
	upload, download = 110, 220
	history.Sample(now)
	present = false
	history.Sample(now.Add(30 * time.Minute))
	if history.resources["client:1"] == nil {
		t.Fatal("deleted resources must retain unexpired history")
	}
	if _, exists := history.previous["client:1"]; exists {
		t.Fatal("deleted resource retained a cumulative baseline")
	}
	history.Sample(now.Add(time.Hour))
	if len(history.resources["client:1"].Points1m) != 1 {
		t.Fatal("a sample exactly on the retention boundary must be retained")
	}
	history.Sample(now.Add(time.Hour + time.Second))
	resource := history.resources["client:1"]
	if resource == nil || len(resource.Points1m) != 0 || len(resource.Points5m) != 1 {
		t.Fatalf("five-minute history should outlive expired minute history: %+v", resource)
	}
	if err := history.Save(); err != nil {
		t.Fatal(err)
	}
	loaded := New(Options{Path: path, Retention1m: time.Hour, Retention5m: 2 * time.Hour})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if loaded.resources["client:1"] == nil {
		t.Fatal("unexpired five-minute history must survive a restart")
	}
	history.Sample(now.Add(2 * time.Hour))
	if history.resources["client:1"] == nil {
		t.Fatal("five-minute retention boundary must be inclusive")
	}
	history.Sample(now.Add(2*time.Hour + time.Second))
	if history.resources["client:1"] != nil {
		t.Fatal("deleted resource with no retained samples must be reclaimed")
	}
	if err := history.Save(); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(loaded.resources) != 0 {
		t.Fatalf("expired records were left on disk: %+v", loaded.resources)
	}

	// An offline but still configured client continues to be sampled. It must
	// not lose its current totals, even when it has not generated new traffic.
	present = true
	history.Sample(now.Add(3 * time.Hour))
	if resource = history.resources["client:1"]; resource == nil || resource.TotalBytes != upload+download || len(resource.Points1m) != 0 {
		t.Fatalf("returning resource must establish a fresh baseline: %+v", resource)
	}
	history.Sample(now.Add(3*time.Hour + time.Minute))
	if resource.Points1m[0].Upload != 0 || resource.Points1m[0].Download != 0 {
		t.Fatalf("stale totals were incorrectly counted as new traffic: %+v", resource.Points1m)
	}
}

func TestHistoryLoadCleansExpiredAndNullResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Now().Truncate(5 * time.Minute)
	old := now.Add(-91 * 24 * time.Hour).Unix()
	legacy := New(Options{Path: path})
	legacy.resources = map[string]*Resource{
		"global:0": {Key: "global:0", Scope: ScopeGlobal, TotalBytes: 999, Points1m: []Point{{Time: old}}, Points5m: []Point{{Time: old}}},
		"client:1": {Key: "client:1", Scope: ScopeClient, ID: 1, Points1m: []Point{{Time: old}}, Points5m: []Point{{Time: old}}},
		"client:2": {Key: "client:2", Scope: ScopeClient, ID: 2, Points5m: []Point{{Time: now.Add(-24 * time.Hour).Unix(), Upload: 123}}},
		"empty":    {Key: "empty", Scope: ScopeHost, ID: 9},
		"null":     nil,
	}
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	loaded := New(Options{Path: path})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(loaded.resources) != 2 || loaded.resources["client:2"] == nil || loaded.resources["global:0"] == nil {
		t.Fatalf("startup cleanup retained expired or null resources: %+v", loaded.resources)
	}
	if resource := loaded.resources["global:0"]; resource.TotalBytes != 999 || len(resource.Points1m) != 0 || len(resource.Points5m) != 0 {
		t.Fatalf("startup retention must preserve current totals but trim samples: %+v", resource)
	}
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(loaded.resources) != 2 {
		t.Fatal("startup cleanup was not persisted")
	}
}

func TestHistoryRetentionPreservesConfiguredOfflineResourceTotals(t *testing.T) {
	history := New(Options{Retention1m: time.Hour, Retention5m: 2 * time.Hour})
	history.collect = func() totalsSnapshot { return testTotals(100, 200) }
	now := time.Now().Truncate(5 * time.Minute)
	history.Sample(now)
	resource := history.resources["client:1"]
	resource.Points1m = []Point{{Time: now.Add(-3 * time.Hour).Unix(), Upload: 10}}
	resource.Points5m = []Point{{Time: now.Add(-3 * time.Hour).Unix(), Upload: 10}}
	history.Sample(now.Add(time.Minute))
	if history.resources["client:1"] != resource || resource.TotalBytes != 300 {
		t.Fatalf("retention must not discard current totals for an offline configured client: %+v", resource)
	}
	if len(resource.Points1m) != 1 || len(resource.Points5m) != 1 || resource.Points1m[0].Upload != 0 {
		t.Fatalf("expired points were not replaced by the idle sample: %+v", resource)
	}
	if _, ok := history.previous["client:1"]; !ok {
		t.Fatal("configured resource lost its baseline")
	}
}

func TestHistoryDeletedResourceReturnDoesNotCountTheSamplingGap(t *testing.T) {
	history := New(Options{})
	upload := int64(100)
	present := true
	history.collect = func() totalsSnapshot {
		if present {
			return testTotals(upload, 0)
		}
		return totalsSnapshot{}
	}
	now := time.Now().Truncate(time.Minute)
	history.Sample(now)
	upload = 110
	history.Sample(now.Add(time.Minute))
	present = false
	history.Sample(now.Add(2 * time.Minute))
	present, upload = true, 1000
	history.Sample(now.Add(3 * time.Minute))
	resource := history.resources["client:1"]
	if len(resource.Points1m) != 1 || resource.Points1m[0].Upload != 10 || resource.TotalBytes != 1000 {
		t.Fatalf("return must preserve retained history but only reset the baseline: %+v", resource)
	}
	upload = 1005
	history.Sample(now.Add(4 * time.Minute))
	if len(resource.Points1m) != 2 || resource.Points1m[1].Upload != 5 {
		t.Fatalf("resumed delta = %+v, want only 5 new bytes", resource.Points1m)
	}
}

func TestHistoryConcurrentSaveAndSampleKeepsAValidSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	history := New(Options{Path: path})
	history.collect = func() totalsSnapshot { return testTotals(100, 200) }
	now := time.Now().Truncate(time.Minute)
	history.Sample(now)
	var workers sync.WaitGroup
	errors := make(chan error, 40)
	for range 4 {
		workers.Go(func() {
			for range 10 {
				if err := history.Save(); err != nil {
					errors <- err
				}
			}
		})
	}
	workers.Go(func() {
		for i := 1; i <= 10; i++ {
			history.Sample(now.Add(time.Duration(i) * time.Minute))
			history.Series(Query{Scope: ScopeClient, ID: 1}, "1m")
			history.Query(Query{Scope: ScopeClient, ID: 1})
		}
	})
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent save: %v", err)
	}
	if err := history.Save(); err != nil {
		t.Fatal(err)
	}
	loaded := New(Options{Path: path})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if resource := loaded.resources["client:1"]; resource == nil || len(resource.Points1m) != 10 || resource.TotalBytes != 300 {
		t.Fatalf("final persisted snapshot lost samples: %+v", resource)
	}
}
