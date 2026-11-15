package jsonlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadRecentHandlesBoundariesAndPartialLines(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
		limit            int
	}{
		{"empty", "", "", 2},
		{"newline", "one\ntwo\nthree\n", "two\nthree\n", 2},
		{"no final newline", "one\ntwo\nthree", "two\nthree", 2},
		{"whole file", "one\ntwo\n", "one\ntwo\n", 4},
		{"single line", "one", "one", 1},
		{"blank lines", "one\n\ntwo\n\nthree\n", "two\n\nthree\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.log")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			got, err := ReadRecent(file, tc.limit)
			if err != nil || string(got) != tc.want {
				t.Fatalf("ReadRecent = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReadRecentSkipsHugeOldPrefixAndEnforcesHardLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// A sparse 2 GiB old prefix proves the bounded tail does not scan from
	// the beginning or depend on the full file fitting in memory.
	if _, err := file.Seek(2<<30, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2100; i++ {
		if _, err := fmt.Fprintf(file, "%04d %s\n", i, strings.Repeat("x", 100)); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{RecentLimit, RecentLimit + 10000, 0} {
		got, err := ReadRecent(file, limit)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(got, []byte{'\n'}) != RecentLimit || !bytes.HasPrefix(got, []byte("0100 ")) {
			t.Fatalf("tail window incorrect: bytes=%d prefix=%q", len(got), got[:min(len(got), 20)])
		}
	}
}

func TestReadRecentHasByteBudgetForCorruptFiles(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "corrupt.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(2 * maxRecentBytes); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRecent(file, 2000)
	if err != nil || len(data) != 0 {
		t.Fatalf("corrupt unseparated tail = %d bytes, %v", len(data), err)
	}
}

func TestReadRecentSkipsOversizedRecordWithoutHidingValidNeighbors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.log")
	if err := os.WriteFile(path, []byte("before\n"+strings.Repeat("x", maxRecordBytes)+"\nafter\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := ReadRecent(file, 3)
	if err != nil || string(data) != "before\nafter\n" {
		t.Fatalf("oversized record hid valid neighbors: %q %v", data, err)
	}
}

func TestPruneBeforeKeepsBoundaryMalformedUnknownAndPartialRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.log")
	cutoff := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	old := stampLine(cutoff.Add(-time.Second), "old")
	boundary := stampLine(cutoff, "boundary")
	newer := stampLine(cutoff.Add(time.Hour), "recent")
	unknown := "{\"id\":\"unknown\"}\nnot-json\n"
	oversized := strings.Repeat("x", 2*readBlockSize) + "\n"
	partial := strings.TrimSuffix(stampLine(cutoff.Add(-time.Hour), "partial"), "\n")
	if err := os.WriteFile(path, []byte(old+boundary+newer+unknown+oversized+partial), 0640); err != nil {
		t.Fatal(err)
	}
	var writer sync.Mutex
	result, err := PruneBefore(path, cutoff, &writer)
	if err != nil || result.Removed != 1 || result.RemovedBytes != int64(len(old)) || result.Kept != 6 {
		t.Fatalf("cleanup = %+v, %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != boundary+newer+unknown+oversized+partial {
		t.Fatal("cleanup changed a retained record", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("cleanup did not preserve file permissions", err)
	}
	if temps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".nps-journal-cleanup-*")); len(temps) != 0 {
		t.Fatalf("cleanup left temporary files: %v", temps)
	}
}

type afterSnapshotLock struct {
	sync.Mutex
	after func()
}

func (lock *afterSnapshotLock) Unlock() {
	lock.Mutex.Unlock()
	if lock.after != nil {
		after := lock.after
		lock.after = nil
		after()
	}
}

func TestPruneBeforePreservesConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.log")
	cutoff := time.Now()
	old := stampLine(cutoff.Add(-time.Hour), "old")
	recent := stampLine(cutoff.Add(time.Hour), "recent")
	concurrent := stampLine(cutoff.Add(-2*time.Hour), "newly-appended-backdated")
	if err := os.WriteFile(path, []byte(old+recent), 0600); err != nil {
		t.Fatal(err)
	}
	lock := &afterSnapshotLock{after: func() {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(concurrent); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := PruneBefore(path, cutoff, lock)
	if err != nil || result.Removed != 1 {
		t.Fatalf("cleanup = %+v %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != recent+concurrent {
		t.Fatal("cleanup lost an append made during its scan", err)
	}
}

func TestPruneBeforeAbortsIfExternalRotationReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.log")
	cutoff := time.Now()
	if err := os.WriteFile(path, []byte(stampLine(cutoff.Add(-time.Hour), "old")), 0600); err != nil {
		t.Fatal(err)
	}
	lock := &afterSnapshotLock{after: func() {
		if err := os.Rename(path, path+".rotated"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("external replacement\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := PruneBefore(path, cutoff, lock); err == nil {
		t.Fatal("cleanup replaced an externally rotated journal")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "external replacement\n" {
		t.Fatal("cleanup changed externally replaced journal", err)
	}
}

func TestPruneBeforePreservesSymlinkAndAcceptsMissingFile(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.log"), filepath.Join(dir, "journal.log")
	cutoff := time.Now()
	old := stampLine(cutoff.Add(-time.Hour), "old")
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	var lock sync.Mutex
	result, err := PruneBefore(link, cutoff, &lock)
	if err != nil || result.Removed != 1 {
		t.Fatalf("symlink cleanup = %+v %v", result, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("cleanup replaced the configured symlink", err)
	}
	for _, path := range []string{"", filepath.Join(dir, "missing.log")} {
		if result, err := PruneBefore(path, cutoff, &lock); err != nil || result.Removed != 0 {
			t.Fatalf("missing journal cleanup = %+v %v", result, err)
		}
	}
}

func stampLine(at time.Time, id string) string {
	data, _ := json.Marshal(map[string]interface{}{"time": at, "id": id})
	return string(data) + "\n"
}
