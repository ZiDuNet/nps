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

func TestEmptyPathDisablesJournal(t *testing.T) {
	dir := t.TempDir()
	Configure("")
	if Path() != "" {
		t.Fatalf("disabled journal path = %q, want empty", Path())
	}
	if err := Append(Event{Action: "login"}); err != nil {
		t.Fatalf("disabled append returned error: %v", err)
	}
	page, err := List(Filter{})
	if err != nil {
		t.Fatalf("disabled list returned error: %v", err)
	}
	if page.Total != 0 || len(page.Events) != 0 {
		t.Fatalf("disabled journal returned records: %+v", page)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.log")); !os.IsNotExist(err) {
		t.Fatalf("disabled journal unexpectedly created audit.log: %v", err)
	}
}

func TestSearchIncludesLocalizedActionsRequestMetadataAndSafeDetails(t *testing.T) {
	Configure(filepath.Join(t.TempDir(), "audit.log"))
	t.Cleanup(func() { Configure("") })
	if err := Append(Event{Action: "auth.login", ResourceType: "session", ActorID: 7, OwnerUserID: 7, Method: "POST", Path: "/login/verify", RequestID: "correlation-unique", UserAgent: "UniqueBrowser", RemoteAddr: "[2001:db8::7]:123", Details: map[string]string{"remark": "unique-remark"}}); err != nil {
		t.Fatal(err)
	}
	for _, search := range []string{"登录", "Sign in", "登录会话", "correlation-unique", "uniquebrowser", "2001:db8::7", "unique-remark", "7"} {
		page, err := List(Filter{Search: search})
		if err != nil || page.Total != 1 {
			t.Errorf("search %q: %+v %v", search, page, err)
		}
	}
	owner := 8
	page, err := List(Filter{Search: "登录", OwnerUserID: &owner})
	if err != nil || page.Total != 0 {
		t.Fatal("search widened ownership filter")
	}
}

func TestActionCatalogHasUniqueStableCodesAndHistoricalFallback(t *testing.T) {
	seen := map[string]bool{}
	for _, connections := range []bool{false, true} {
		for _, group := range ActionGroups(connections) {
			for _, action := range group.Actions {
				if seen[action.Code] || action.Name == "" || action.NameEN == "" {
					t.Fatalf("invalid action catalog entry: %+v", action)
				}
				seen[action.Code] = true
			}
		}
	}
	if ActionName("auth.login", false) != "登录" || ActionName("future.action", false) != "future.action" {
		t.Fatal("action labels changed stable codes")
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
