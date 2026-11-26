package controllers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServerUpdateCapabilityAcceptsWritableInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows refuses in-place replacement by design")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "nps")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	capability := serverUpdateCapabilityFor(exe)
	if !capability.Supported {
		t.Fatalf("writable install should be supported: %+v", capability)
	}
	if capability.Executable != exe {
		t.Fatalf("executable = %q, want %q", capability.Executable, exe)
	}
}

func TestServerUpdateCapabilityRejectsDevelopmentBuild(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "go-build123", "b001", "nps.test")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	capability := serverUpdateCapabilityFor(exe)
	if capability.Supported {
		t.Fatalf("development build must be refused: %+v", capability)
	}
	if !strings.Contains(capability.Reason, "开发") {
		t.Fatalf("reason = %q", capability.Reason)
	}
}

func TestServerUpdateCapabilityRejectsReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root or windows")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "nps")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	capability := serverUpdateCapabilityFor(exe)
	if capability.Supported {
		t.Fatalf("read-only directory must be refused: %+v", capability)
	}
}

func TestServerUpdateJobIsSingleFlight(t *testing.T) {
	serverUpdateState.mu.Lock()
	serverUpdateState.job = serverUpdateJob{}
	serverUpdateState.mu.Unlock()
	t.Cleanup(func() {
		serverUpdateState.mu.Lock()
		serverUpdateState.job = serverUpdateJob{}
		serverUpdateState.mu.Unlock()
	})

	if !beginServerUpdateJob("/tmp/nps") {
		t.Fatal("first job should start")
	}
	if beginServerUpdateJob("/tmp/nps") {
		t.Fatal("a second job must be rejected while one is running")
	}
	setServerUpdateStage("downloading", "下载中")
	job := currentServerUpdateJob()
	if !job.Running || job.Stage != "downloading" || job.Message != "下载中" {
		t.Fatalf("job = %+v", job)
	}
	finishServerUpdateJob("failed", "失败", os.ErrPermission)
	job = currentServerUpdateJob()
	if job.Running || job.Stage != "failed" || job.Error == "" || job.FinishedAt == 0 {
		t.Fatalf("finished job = %+v", job)
	}
	if !beginServerUpdateJob("/tmp/nps") {
		t.Fatal("a new job should start after the previous one finished")
	}
}

func TestServerUpdateJobExpiresStaleRun(t *testing.T) {
	serverUpdateState.mu.Lock()
	serverUpdateState.job = serverUpdateJob{
		Running:   true,
		Stage:     "downloading",
		StartedAt: time.Now().Add(-2 * serverUpdateJobTimeout).Unix(),
	}
	serverUpdateState.mu.Unlock()
	t.Cleanup(func() {
		serverUpdateState.mu.Lock()
		serverUpdateState.job = serverUpdateJob{}
		serverUpdateState.mu.Unlock()
	})
	job := currentServerUpdateJob()
	if job.Running || job.Stage != "failed" || job.Error == "" {
		t.Fatalf("stale job should be reported as failed: %+v", job)
	}
}
