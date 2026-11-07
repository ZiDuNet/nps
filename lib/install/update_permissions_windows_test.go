//go:build windows

package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateUpdateDirectoryRejectsBroaderACL(t *testing.T) {
	dir, err := CreatePrivateUpdateDirectory(t.TempDir(), "nps-npc-update-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateUpdateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	private, err := privateUpdateSecurity(true)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(private.String() + "(A;OICI;FR;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	// Add broad read access only to this empty fixture, then restore for cleanup.
	defer secureUpdatePath(dir, true)
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if checkPrivateUpdateDirectory(dir) == nil {
		t.Fatal("broad ACL accepted for update credentials")
	}
}

func TestWindowsInstallPreservesExplicitACL(t *testing.T) {
	dir := t.TempDir()
	target, source := filepath.Join(dir, "npc.exe"), filepath.Join(dir, "new.exe")
	if err := os.WriteFile(target, []byte("old"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := SecureUpdateFile(target); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := InstallBinary(source, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"installed", "rolled back"} {
		if step == "rolled back" {
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
		after, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		_, want, _ := strings.Cut(before.String(), "(")
		_, actual, _ := strings.Cut(after.String(), "(")
		if actual != want {
			t.Fatal(step, "changed program ACL")
		}
	}
}
