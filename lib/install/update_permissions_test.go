package install

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrivateUpdateState(t *testing.T) {
	dir, err := CreatePrivateUpdateDirectory(t.TempDir(), "nps-npc-update-*")
	if err != nil {
		t.Fatal(err)
	}
	if err = checkPrivateUpdateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "resume.json")
	if err = os.WriteFile(path, []byte("fixture only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SecureUpdateFile(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private update file did not use mode 0600", err)
		}
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if checkPrivateUpdateDirectory(dir) == nil {
			t.Fatal("non-private stage accepted")
		}
	}
}

func TestPreflightUpdateTargetDoesNotAlterInstalledFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "npc")
	if err := os.WriteFile(target, []byte("original program"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := PreflightUpdateTarget(target, target+".result.json"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original program" {
		t.Fatal("preflight changed installed file", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("preflight left artifacts or changed receipt", err)
	}
	if err := PreflightUpdateTarget("relative-npc", ""); err == nil {
		t.Fatal("relative target accepted")
	}
}

func TestPreflightRefusesUnwritableInstallBeforeReplacement(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX ordinary-user permission test")
	}
	dir := t.TempDir()
	target, source := filepath.Join(dir, "npc"), filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if err := PreflightUpdateTarget(target, ""); err == nil {
		t.Fatal("unwritable installation accepted")
	}
	if _, err := InstallBinary(source, target); err == nil {
		t.Fatal("unwritable installation replaced")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "old" {
		t.Fatal("permission failure changed original program", err)
	}
}

func TestPreflightRefusesSpecialPrivilegeAndReceiptSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file attributes")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "npc")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0700|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	if err := PreflightUpdateTarget(target, ""); err == nil {
		t.Fatal("setuid program accepted for automatic privilege transfer")
	}
	if err := os.Chmod(target, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := target + ".receipt"
	if err := os.Symlink(target, receipt); err != nil {
		t.Fatal(err)
	}
	if err := PreflightUpdateTarget(target, receipt); err == nil {
		t.Fatal("symlink receipt accepted")
	}
}
