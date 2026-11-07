//go:build !windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixUpdateExecKeepsPIDArgumentsAndWorkingDirectory(t *testing.T) {
	if source := os.Getenv("NPS_EXEC_FIXTURE_SOURCE"); source != "" {
		target, _ := os.Executable()
		release, err := AcquireUpdateLock(target)
		if err != nil {
			os.Exit(10)
		}
		defer release()
		tx, err := InstallBinary(source, target)
		if err != nil {
			os.Exit(11)
		}
		if err = RestartNpcProcessWithArgs([]string{"restored-connection", "--flag=value"}); err != nil {
			_ = tx.Rollback()
			os.Exit(12)
		}
		os.Exit(13)
	}
	root := t.TempDir()
	target := filepath.Join(root, "npc")
	source := filepath.Join(root, "new-npc")
	output := filepath.Join(root, "state")
	current, _ := os.Executable()
	if err := copyFileAtomic(current, target); err != nil {
		t.Fatal(err)
	}
	// The new executable emits only fixture data and exits; no network or real
	// services are involved in this PID-preserving replacement check.
	if err := os.WriteFile(source, []byte("#!/bin/sh\nprintf '%s|%s|%s|%s' \"$$\" \"$PWD\" \"$1\" \"$2\" > \"$NPS_EXEC_FIXTURE_OUTPUT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(target, "-test.run=^TestUnixUpdateExecKeepsPIDArgumentsAndWorkingDirectory$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NPS_EXEC_FIXTURE_SOURCE="+source, "NPS_EXEC_FIXTURE_OUTPUT="+output)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d|%s|restored-connection|--flag=value", pid, root)
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("exec state=%q want=%q", data, want)
	}
	release, err := AcquireUpdateLock(target)
	if err != nil {
		t.Fatal("lock survived exec/process exit", err)
	}
	release()
}
