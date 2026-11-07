package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestGUIUpdateResumesRunningAndRetryingIndependentlyOfPreference(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("uses platform user config directory; macOS CI covers with isolated account")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	shortcutsMu.Lock()
	oldShortcuts := shortcuts
	shortcutsMu.Unlock()
	runningMu.Lock()
	oldRunning := running
	running = make(map[string]context.CancelFunc)
	runningMu.Unlock()
	t.Cleanup(func() {
		shortcutsMu.Lock()
		shortcuts = oldShortcuts
		shortcutsMu.Unlock()
		runningMu.Lock()
		running = oldRunning
		runningMu.Unlock()
	})
	items := []ShortClient{{Addr: "bridge.test:8024", Key: "a", Transport: "tcp"}, {Addr: "bridge.test:8027", Key: "b", Transport: "ws"}, {Addr: "bridge.test:8024", Key: "stopped", Transport: "tcp"}}
	store := PersistentStore{Shortcuts: items, Settings: GuiSettings{RememberClientState: false}}
	data, _ := json.Marshal(store)
	if err := os.WriteFile(getStoragePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	runningMu.Lock()
	running[shortcutID(items[0])] = func() {}
	running[shortcutID(items[1])] = func() {}
	runningMu.Unlock()
	if err := saveUpdateConnections(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(guiResumePath())
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("resume file permissions too open")
	}
	var started []string
	if err := restoreGUIUpdateConnections(func(id string) error { started = append(started, id); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 {
		t.Fatalf("resumed=%v", started)
	}
	for _, id := range started {
		if id == shortcutID(items[2]) {
			t.Fatal("previously-stopped entry resumed")
		}
	}
	updated, err := loadPersistentStore()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Settings.RememberClientState || len(updated.ClientStates) != 2 {
		t.Fatalf("store=%+v", updated)
	}
}

func TestGUIUpdateIgnoresStaleResumeAndWritesAtomicState(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("isolated account required")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	data, _ := json.Marshal(guiResume{Created: time.Now().Add(-25 * time.Hour), IDs: []string{"old"}})
	if err := writeGUIStateAtomic(guiResumePath(), data); err != nil {
		t.Fatal(err)
	}
	if err := restoreGUIUpdateConnections(func(string) error { t.Fatal("stale connection resumed"); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(guiResumePath()); !os.IsNotExist(err) {
		t.Fatal("stale resume not consumed")
	}
	if entries, _ := filepath.Glob(filepath.Join(filepath.Dir(guiResumePath()), ".npc-state-*")); len(entries) > 0 {
		t.Fatal("partial state files left")
	}
}

func TestGUIStoreMigratesOldPermissionsAndReportsWriteFailure(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("requires isolated macOS account")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	path := getStoragePath()
	data, err := json.Marshal(PersistentStore{Shortcuts: []ShortClient{{Addr: "fixture.test:8024", Key: "fixture-key", Transport: "tcp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	store, err := loadPersistentStore()
	if err != nil || len(store.Shortcuts) != 1 || store.Shortcuts[0].Key != "fixture-key" {
		t.Fatal("could not migrate existing store", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("legacy credential store remains world-readable")
		}
	}
	if err := savePersistentStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // POSIX modes do not simulate Windows ACL denial or root.
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	a := &App{}
	if err := a.SaveClientStates(map[string]string{"fixture": "connecting"}); err == nil {
		t.Fatal("state save silently succeeded in unwritable directory")
	}
	if err := a.SaveGuiSettings(GuiSettings{LogDir: dir}); err == nil {
		t.Fatal("settings save silently succeeded in unwritable directory")
	}
	retained, err := loadPersistentStore()
	if err != nil || len(retained.Shortcuts) != 1 || len(retained.ClientStates) != 0 {
		t.Fatal("failed save changed stored credentials", err)
	}
}
