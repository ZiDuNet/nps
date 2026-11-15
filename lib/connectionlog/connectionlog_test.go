package connectionlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendListAndFilter(t *testing.T) {
	dir := t.TempDir()
	Configure(filepath.Join(dir, "connection.log"))
	t.Cleanup(func() { Configure("") })
	first := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	events := []Event{
		{
			ID: "first", Time: first, Kind: KindBridge, Action: ActionBridgeConnected,
			Result: ResultSuccess, UserID: 7, ClientID: 11, BridgeTransport: "ws",
			BridgeChannel: "main", SourceIP: "198.51.100.8",
			RemoteAddr: "198.51.100.8:4567", ServerAddr: "192.0.2.10:8024",
			ClientLocalAddr:       "10.20.30.40",
			ClientLocalAddrSource: ClientLocalAddrSourceCurrentReport,
			HandshakeStage:        "connected",
		},
		{
			ID: "second", Time: first.Add(time.Minute), Kind: KindTunnel,
			Action: ActionProxyConnection, Result: ResultFailure, UserID: 7,
			ClientID: 12, ProxyProtocol: "tcp", Error: "connection refused",
		},
	}
	for _, event := range events {
		if err := Append(event); err != nil {
			t.Fatal(err)
		}
	}
	Flush()

	owner := 7
	page, err := List(Filter{OwnerUserID: &owner, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Events) != 2 || page.Events[0].ID != "second" {
		t.Fatalf("unexpected page: %+v", page)
	}
	page, err = List(Filter{OwnerUserID: &owner, Search: "10.20.30.40", Limit: 10})
	if err != nil || page.Total != 1 || page.Events[0].ID != "first" {
		t.Fatalf("unexpected detail search page: %+v err=%v", page, err)
	}
	if event := page.Events[0]; event.RemoteAddr != "198.51.100.8:4567" ||
		event.ServerAddr != "192.0.2.10:8024" ||
		event.ClientLocalAddr != "10.20.30.40" ||
		event.ClientLocalAddrSource != ClientLocalAddrSourceCurrentReport ||
		event.BridgeChannel != "main" {
		t.Fatalf("connection detail fields were not persisted: %+v", event)
	}
	page, err = List(Filter{OwnerUserID: &owner, Result: ResultFailure, Limit: 10})
	if err != nil || page.Total != 1 || page.Events[0].ID != "second" {
		t.Fatalf("unexpected filtered page: %+v err=%v", page, err)
	}
}

func TestEmptyPathDisablesLog(t *testing.T) {
	dir := t.TempDir()
	Configure("")
	if err := Append(Event{Action: ActionBridgeConnected}); err != nil {
		t.Fatal(err)
	}
	Flush()
	page, err := List(Filter{})
	if err != nil || page.Total != 0 {
		t.Fatalf("disabled page=%+v err=%v", page, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "connection.log")); !os.IsNotExist(err) {
		t.Fatalf("disabled log unexpectedly created a file: %v", err)
	}
}

func TestListIgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.log")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	Configure(path)
	t.Cleanup(func() { Configure("") })
	if err := Append(Event{Action: ActionBridgeAuthFailed, Result: ResultFailure}); err != nil {
		t.Fatal(err)
	}
	Flush()
	page, err := List(Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("expected valid record after malformed line, page=%+v err=%v", page, err)
	}
}

func TestRecentWindowKeepsUserBoundariesAndFullHistoryQueries(t *testing.T) {
	Configure(filepath.Join(t.TempDir(), "connection.log"))
	t.Cleanup(func() { Configure("") })
	Flush()
	var data strings.Builder
	now := time.Now()
	for i := 0; i < 2005; i++ {
		event := Event{ID: fmt.Sprintf("%04d", i), Time: now.Add(time.Duration(i) * time.Second), Action: ActionBridgeConnected, ClientID: 7, UserID: 1}
		if i < 5 {
			event.ClientID, event.UserID = 9, 2
		}
		encoded, _ := json.Marshal(event)
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(Path(), []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := List(Filter{RecentLimit: 2000, Offset: 1995, Limit: 500, AllowedClientIDs: map[int]struct{}{7: {}}})
	if err != nil || page.Total != 2000 || len(page.Events) != 5 || page.Events[4].ID != "0005" {
		t.Fatalf("recent last page = %+v %v", page, err)
	}
	for _, filter := range []Filter{
		{RecentLimit: 2000, AllowedClientIDs: map[int]struct{}{9: {}}},
		{RecentLimit: 2000, AllowedClientIDs: map[int]struct{}{}},
	} {
		if page, err := List(filter); err != nil || page.Total != 0 {
			t.Fatalf("user query widened recent window: %+v %v", page, err)
		}
	}
	page, err = List(Filter{AllowedClientIDs: map[int]struct{}{9: {}}})
	if err != nil || page.Total != 5 {
		t.Fatalf("full-history API query regressed: %+v %v", page, err)
	}
}

func TestCleanupBeforeDoesNotLoseAsynchronousAppends(t *testing.T) {
	Configure(filepath.Join(t.TempDir(), "connection.log"))
	t.Cleanup(func() { Configure("") })
	cutoff := time.Now().Add(-time.Hour)
	if err := Append(Event{ID: "old", Time: cutoff.Add(-time.Hour), Action: ActionBridgeConnected}); err != nil {
		t.Fatal(err)
	}
	Flush()
	var queued sync.WaitGroup
	queued.Add(1)
	go func() {
		defer queued.Done()
		for i := 0; i < 100; i++ {
			_ = Append(Event{ID: fmt.Sprintf("new-%d", i), Action: ActionBridgeConnected})
		}
	}()
	result, err := CleanupBefore(cutoff)
	queued.Wait()
	Flush()
	if err != nil || result.Removed != 1 {
		t.Fatalf("cleanup = %+v %v", result, err)
	}
	page, err := List(Filter{Limit: 500})
	if err != nil || page.Total != 100 || len(page.Events) != 100 {
		t.Fatalf("cleanup lost asynchronous events: total=%d len=%d err=%v", page.Total, len(page.Events), err)
	}
}
