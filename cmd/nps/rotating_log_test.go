package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astaxie/beego/logs"
)

func newTestRotatingLogger(t *testing.T, config rotatingLogConfig) (*rotatingFileLogger, string) {
	t.Helper()
	config.Filename = filepath.Join(t.TempDir(), "nps.log")
	if config.Perm == 0 {
		config.Perm = 0600
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	logger := &rotatingFileLogger{}
	if err := logger.Init(string(encoded)); err != nil {
		t.Fatalf("initialize rotating logger: %v", err)
	}
	t.Cleanup(logger.Destroy)
	return logger, config.Filename
}

func TestRotatingFileLoggerRotatesBySizeAndCompresses(t *testing.T) {
	logger, activePath := newTestRotatingLogger(t, rotatingLogConfig{
		MaxSizeMB: 1, MaxBackups: 10, Compress: true, Level: logs.LevelDebug,
	})
	now := time.Now()
	if err := logger.WriteMsg(now, strings.Repeat("a", 1<<20), logs.LevelInformational); err != nil {
		t.Fatalf("write first log entry: %v", err)
	}
	if err := logger.WriteMsg(now, "second entry", logs.LevelInformational); err != nil {
		t.Fatalf("write second log entry: %v", err)
	}
	logger.Destroy()

	entries, err := os.ReadDir(filepath.Dir(activePath))
	if err != nil {
		t.Fatal(err)
	}
	var compressed string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "nps.log.") && strings.HasSuffix(entry.Name(), ".gz") {
			compressed = filepath.Join(filepath.Dir(activePath), entry.Name())
		}
	}
	if compressed == "" {
		t.Fatalf("expected compressed rotated archive; entries: %v", entries)
	}
	archive, err := os.Open(compressed)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(archive)
	if err != nil {
		_ = archive.Close()
		t.Fatalf("open gzip archive: %v", err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip archive: %v", err)
	}
	_ = reader.Close()
	_ = archive.Close()
	if !strings.Contains(string(contents), strings.Repeat("a", 64)) {
		t.Fatal("compressed archive does not contain the original log entry")
	}
	active, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), "second entry") {
		t.Fatalf("active log does not contain post-rotation entry: %q", active)
	}
}

func TestRotatingFileLoggerRotatesByDay(t *testing.T) {
	logger, activePath := newTestRotatingLogger(t, rotatingLogConfig{
		MaxBackups: 10, Level: logs.LevelDebug, RotateDaily: true,
	})
	day := time.Now().In(time.Local).Truncate(24 * time.Hour).Add(12 * time.Hour)
	if err := logger.WriteMsg(day, "first day", logs.LevelInformational); err != nil {
		t.Fatal(err)
	}
	if err := logger.WriteMsg(day.Add(24*time.Hour), "next day", logs.LevelInformational); err != nil {
		t.Fatal(err)
	}
	logger.Destroy()

	entries, err := os.ReadDir(filepath.Dir(activePath))
	if err != nil {
		t.Fatal(err)
	}
	var foundArchive bool
	for _, entry := range entries {
		if rotatedLogName.MatchString(entry.Name()) {
			foundArchive = true
			break
		}
	}
	if !foundArchive {
		t.Fatalf("expected a daily rotated archive; entries: %v", entries)
	}
	active, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), "next day") {
		t.Fatalf("active log does not contain next day's entry: %q", active)
	}
}

func TestRotatingFileLoggerPrunesOnlyItsArchives(t *testing.T) {
	logger, activePath := newTestRotatingLogger(t, rotatingLogConfig{
		MaxBackups: 1, Level: logs.LevelDebug, Compress: false, RotateDaily: true,
	})
	dir := filepath.Dir(activePath)
	oldArchive := filepath.Join(dir, "nps.log.20200101-010203.000000000.000001")
	if err := os.WriteFile(oldArchive, []byte("old archive"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldArchive, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	unrelated := []string{
		filepath.Join(dir, "nps.log.backup"),
		filepath.Join(dir, "nps.log.20200101"),
		filepath.Join(dir, "nps.log.20200101-010203.000000000.000002.gz.tmp"),
	}
	for _, path := range unrelated {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := logger.WriteMsg(now, "first day", logs.LevelInformational); err != nil {
		t.Fatal(err)
	}
	if err := logger.WriteMsg(now.Add(24*time.Hour), "new entry", logs.LevelInformational); err != nil {
		t.Fatal(err)
	}
	logger.Destroy()
	if _, err := os.Stat(oldArchive); !os.IsNotExist(err) {
		t.Fatalf("old rotated archive should be pruned, stat err=%v", err)
	}
	for _, path := range unrelated {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("unrelated file %q should be preserved: %v", path, err)
		}
	}
}

func TestRotatingFileLoggerRejectsOversizedConfiguration(t *testing.T) {
	config := rotatingLogConfig{
		Filename:  filepath.Join(t.TempDir(), "nps.log"),
		MaxSizeMB: int64(^uint64(0)>>1)/(1024*1024) + 1,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&rotatingFileLogger{}).Init(string(encoded)); err == nil {
		t.Fatal("expected oversized log size to be rejected")
	}
}
