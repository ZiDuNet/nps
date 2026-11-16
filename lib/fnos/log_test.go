package fnos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLogContinuesRotatingRedactsAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.log")
	w, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.limit = 256
	e := entry(t, MainID, "secret-test-key")
	e.Raw += "basic_password=other-secret\n"
	w.SetSecrets(Config{Connections: []Entry{e}})
	for index := range 30 {
		if _, err := fmt.Fprintf(w, "\x1b[31mline %d secret-test-key other-secret %s\x1b[0m\n", index, strings.Repeat(".", 80)); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil || strings.Contains(string(data), "secret-test-key") || strings.Contains(string(data), "other-secret") || strings.ContainsRune(string(data), '\x1b') {
			t.Fatal("rotation missing or unsanitized")
		}
		info, _ := os.Stat(path + suffix)
		if info.Size() > w.limit || info.Mode().Perm() != 0600 {
			t.Fatal("log size or permissions invalid")
		}
	}
	if !strings.Contains(LogTail(path), "line 29") {
		t.Fatal("rotation stopped after filling backups")
	}
	if err := ClearLogs(path); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(w, "after clear"); err != nil || !strings.Contains(LogTail(path), "after clear") {
		t.Fatal("runtime writer did not survive clearing")
	}
	for _, suffix := range []string{".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatal("old backup survived clearing")
		}
	}
}

func TestLogTailBoundAndConcurrentClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.log")
	w, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 30 {
			_ = ClearLogs(path)
		}
	}()
	for index := range 700 {
		if _, err := fmt.Fprintf(w, "line %d %s\n", index, strings.Repeat("-", 400)); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	preview := LogTail(path)
	if len(preview) > LogTailBytes || len(strings.Split(preview, "\n")) > LogTailLines {
		t.Fatal("preview is unbounded")
	}
	other := filepath.Join(t.TempDir(), "safe")
	if err := os.WriteFile(other, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path+".1"); err != nil {
		t.Skip(err)
	}
	if err := ClearLogs(path); err == nil {
		t.Fatal("clear followed a symlink")
	}
	data, _ := os.ReadFile(other)
	if string(data) != "preserve" {
		t.Fatal("unrelated file was modified")
	}
}

func TestRemoveLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.log")
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if err := os.WriteFile(path+suffix, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveLogs(path); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("log %q survived removal", suffix)
		}
	}
	if err := RemoveLogs(path); err != nil {
		t.Fatal("removing an already-missing set should be a no-op")
	}
}
