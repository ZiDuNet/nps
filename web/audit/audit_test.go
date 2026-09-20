package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndListScopedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	Configure(filepath.Join(dir, "audit.log"))
	first := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	if err := Append(Event{Time: first, ID: "first", ActorType: "user", ActorID: 1, ActorName: "alice", OwnerUserID: 1, Action: "client.create", ResourceType: "client", ResourceID: 10, Result: ResultSuccess, Details: map[string]string{"remark": "safe"}}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Event{Time: second, ID: "second", ActorType: "user", ActorID: 2, ActorName: "bob", OwnerUserID: 2, Action: "client.update", ResourceType: "client", ResourceID: 20, Result: ResultFailure, Error: "validation failed"}); err != nil {
		t.Fatal(err)
	}
	page, err := List(Filter{OwnerUserID: intPtr(1), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Events) != 1 || page.Events[0].ID != "first" {
		t.Fatalf("unexpected scoped page: %+v", page)
	}
	page, err = List(Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Events[0].ID != "second" {
		t.Fatalf("expected newest first: %+v", page)
	}
	if strings.Contains(string(readFile(t, Path())), "password") {
		t.Fatal("audit fixture unexpectedly contains a credential field")
	}
}

func TestAppendRejectsMissingActionAndIgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	Configure(filepath.Join(dir, "audit.log"))
	if err := Append(Event{}); err == nil {
		t.Fatal("expected missing action error")
	}
	if err := os.WriteFile(Path(), []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Append(Event{Action: "login"}); err != nil {
		t.Fatal(err)
	}
	page, err := List(Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("expected valid records after malformed line, page=%+v err=%v", page, err)
	}
}

func intPtr(value int) *int { return &value }

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
