package connectionlog

import (
	"os"
	"path/filepath"
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
