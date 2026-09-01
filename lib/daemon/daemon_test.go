package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPIDNormalizesAndValidatesPIDFiles(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "nps.pid")

	if err := os.WriteFile(pidFile, []byte(" 123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pid, err := readPID(dir, "nps")
	if err != nil || pid != "123" {
		t.Fatalf("readPID() = %q, %v; want 123, nil", pid, err)
	}

	for _, invalid := range []string{"0", "-1", "123; touch compromised", "not-a-pid"} {
		if err := os.WriteFile(pidFile, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if pid, err := readPID(dir, "nps"); err == nil || pid != "" {
			t.Fatalf("readPID(%q) = %q, nil; want an error", invalid, pid)
		}
	}
}
