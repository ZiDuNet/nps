package store

import (
	"path/filepath"
	"testing"
)

func TestReplaceSettingsAppliesUpsertAndDeleteTogether(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertSettings(map[string]string{"keep": "old", "remove": "old"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSettings(map[string]string{"keep": "new", "add": "1"}, []string{"remove"}); err != nil {
		t.Fatal(err)
	}
	values, err := store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if values["keep"] != "new" || values["add"] != "1" {
		t.Fatalf("upserted settings = %#v", values)
	}
	if _, exists := values["remove"]; exists {
		t.Fatalf("deleted setting still present: %#v", values)
	}
	if err := store.CheckSettingsTable(); err != nil {
		t.Fatalf("CheckSettingsTable() error = %v", err)
	}
}

func TestReplaceSettingsRejectsEmptyKeyWithoutPartialWrite(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertSettings(map[string]string{"keep": "old"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSettings(map[string]string{"new": "1", " ": "bad"}, nil); err == nil {
		t.Fatal("empty settings key was accepted")
	}
	values, err := store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if values["keep"] != "old" {
		t.Fatalf("original setting changed: %#v", values)
	}
	if _, exists := values["new"]; exists {
		t.Fatalf("failed transaction left a partial write: %#v", values)
	}
}

func TestCheckSettingsTableRejectsClosedStore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSettingsTable(); err == nil {
		t.Fatal("closed store reported a valid settings table")
	}
}
