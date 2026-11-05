package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistorySamplesDeltasAndReportsRates(t *testing.T) {
	history := New(Options{
		SampleInterval: time.Minute,
		Retention1m:    48 * time.Hour,
		Retention5m:    90 * 24 * time.Hour,
		SaveEvery:      5,
	})
	snapshots := []totalsSnapshot{
		testTotals(100, 50),
		testTotals(1060, 650),
	}
	history.collect = func() totalsSnapshot {
		snapshot := snapshots[0]
		snapshots = snapshots[1:]
		return snapshot
	}

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	history.Sample(now)
	history.Sample(now.Add(time.Minute))

	resource := history.resources["client:1"]
	if resource == nil {
		t.Fatal("client resource was not sampled")
	}
	if len(resource.Points1m) != 1 || len(resource.Points5m) != 1 {
		t.Fatalf("points = %d/%d, want 1/1", len(resource.Points1m), len(resource.Points5m))
	}
	if got := resource.Points1m[0]; got.Upload != 960 || got.Download != 600 {
		t.Fatalf("point = %+v, want upload=960 download=600", got)
	}

	summary := history.Query(Query{Scope: ScopeClient, ID: 1})[0]
	if summary.UploadBytes != 960 || summary.DownloadBytes != 600 {
		t.Fatalf("summary bytes = %d/%d", summary.UploadBytes, summary.DownloadBytes)
	}
	if summary.PeakUploadBPS != 16 || summary.PeakDownloadBPS != 10 {
		t.Fatalf("peak rates = %d/%d, want 16/10", summary.PeakUploadBPS, summary.PeakDownloadBPS)
	}
	if summary.EstimatedDays <= 0 {
		t.Fatalf("estimated days = %f, want a positive estimate", summary.EstimatedDays)
	}
}

func TestHistorySeriesFiltersAndDoesNotExposeInternalSlices(t *testing.T) {
	history := New(Options{SampleInterval: time.Minute, Retention1m: 48 * time.Hour, Retention5m: 90 * 24 * time.Hour})
	now := time.Now().Truncate(time.Minute)
	history.resources["client:1"] = &Resource{
		Key:   "client:1",
		Scope: ScopeClient,
		ID:    1,
		Points1m: []Point{
			{Time: now.Add(-2 * time.Minute).Unix(), Upload: 10},
			{Time: now.Add(-time.Minute).Unix(), Upload: 20},
		},
		Points5m: []Point{{Time: now.Unix(), Upload: 30}},
	}
	history.resources["client:2"] = &Resource{Key: "client:2", Scope: ScopeClient, ID: 2}

	series, interval := history.Series(Query{Scope: ScopeClient, ID: 1}, "1m")
	if interval != 60 {
		t.Fatalf("interval = %d, want 60", interval)
	}
	if len(series) != 1 || len(series[0].Points1m) != 2 || series[0].Points5m != nil {
		t.Fatalf("unexpected series: %+v", series)
	}
	series[0].Points1m[0].Upload = 999
	if history.resources["client:1"].Points1m[0].Upload != 10 {
		t.Fatal("Series exposed the internal points slice")
	}
}

func TestHistoryLoadPreservesCumulativeTotalAndAppliesRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	old := time.Now().Add(-72 * time.Hour).Unix()
	fresh := time.Now().Add(-time.Hour).Unix()
	history := New(Options{
		Path:           path,
		SampleInterval: time.Minute,
		Retention1m:    48 * time.Hour,
		Retention5m:    90 * 24 * time.Hour,
	})
	history.resources["client:1"] = &Resource{
		Key:        "client:1",
		Scope:      ScopeClient,
		ID:         1,
		TotalBytes: 123456,
		Points1m: []Point{
			{Time: old, Upload: 1},
			{Time: fresh, Upload: 2},
		},
	}
	if err := history.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := New(Options{Path: path, SampleInterval: time.Minute, Retention1m: 48 * time.Hour, Retention5m: 90 * 24 * time.Hour})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	resource := loaded.resources["client:1"]
	if resource.TotalBytes != 123456 {
		t.Fatalf("TotalBytes = %d, want 123456", resource.TotalBytes)
	}
	if len(resource.Points1m) != 1 || resource.Points1m[0].Time != fresh {
		t.Fatalf("retained points = %+v", resource.Points1m)
	}
}

func TestHistoryStopSavesCurrentSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	history := New(Options{Path: path, SampleInterval: time.Minute, Retention1m: time.Hour, Retention5m: time.Hour})
	history.collect = func() totalsSnapshot { return testTotals(10, 20) }
	history.Sample(time.Now())
	if err := history.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved history is missing: %v", err)
	}
}

func testTotals(upload, download int64) totalsSnapshot {
	return totalsSnapshot{resources: map[string]*totalResource{
		"client:1": {
			Resource: Resource{Key: "client:1", Scope: ScopeClient, ID: 1, Name: "client", LimitBytes: 10 << 20},
			In:       upload,
			Out:      download,
		},
	}}
}
