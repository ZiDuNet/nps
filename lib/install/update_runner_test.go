package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
)

func TestUpdateHelperSubprocess(t *testing.T) {
	if plan := os.Getenv("NPS_TEST_HELPER_PLAN"); plan != "" {
		os.Exit(RunUpdateHelper(plan))
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake client; Windows SCM is tested separately")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful restart", true: "failed restart rolls back"}[fail], func(t *testing.T) {
			root := t.TempDir()
			stage, err := os.MkdirTemp(root, "nps-npc-update-*")
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "npc")
			source := filepath.Join(stage, "new")
			mark := filepath.Join(root, "started")
			_ = os.WriteFile(target, []byte("#!/bin/sh\necho old > '"+mark+"'\nsleep 3\n"), 0755)
			binary := "#!/bin/sh\necho new > '" + mark + "'\nsleep 3\n"
			if fail {
				binary = "#!/bin/sh\nexit 42\n"
			}
			_ = os.WriteFile(source, []byte(binary), 0755)
			plan := RestartPlan{Target: target, Source: source, Version: "99.0.0", WorkingDir: root, Receipt: filepath.Join(root, "receipt"), Result: common.ClientUpdateProgress{JobID: "test-job"}}
			data, _ := json.Marshal(plan)
			path := filepath.Join(stage, "restart.json")
			if fail {
				path = filepath.Join(stage, "restart-retry-123.json")
			}
			_ = os.WriteFile(path, data, 0600)
			_ = os.WriteFile(filepath.Join(stage, ".nps-update-stage"), []byte(updateStageMarker), 0600)
			_ = ReleaseUpdateHelper(path)
			cmd := exec.Command(os.Args[0], "-test.run=^TestUpdateHelperSubprocess$")
			cmd.Env = append(os.Environ(), "NPS_TEST_HELPER_PLAN="+path)
			err = cmd.Run()
			if fail && err == nil || !fail && err != nil {
				t.Fatalf("helper exit=%v, fail=%v", err, fail)
			}
			started, _ := os.ReadFile(mark)
			want := "new\n"
			if fail {
				want = "old\n"
			}
			if string(started) != want {
				t.Fatalf("started=%q want=%q", started, want)
			}
			payload, err := os.ReadFile(plan.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			var receipt common.ClientUpdateProgress
			_ = json.Unmarshal(payload, &receipt)
			if receipt.JobID != "test-job" || fail && receipt.Stage != "failed" || !fail && receipt.Stage != "restarted" {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func TestUpdateHelperRejectsUnscopedManifestWithoutDeleting(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep")
	_ = os.WriteFile(keep, []byte("safe"), 0600)
	data, _ := json.Marshal(RestartPlan{Target: dir, Source: keep, WorkingDir: dir})
	path := filepath.Join(dir, "restart.json")
	_ = os.WriteFile(path, data, 0600)
	if RunUpdateHelper(path) == 0 {
		t.Fatal("invalid plan accepted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("unscoped directory changed", err)
	}
}

func TestWaitProcessExitUsesActualProcess(t *testing.T) {
	if err := WaitProcessExit(0, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !ProcessRunning(os.Getpid()) {
		t.Fatal("own process should be alive")
	}
	if err := WaitProcessExit(os.Getpid(), time.Millisecond); err == nil {
		t.Fatal("live process reported exited")
	}
}
