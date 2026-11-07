//go:build linux

package install

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// ACL encoding follows linux/posix_acl_xattr.h. Only a disposable fixture is
// changed; no system ACLs or extra privileges are needed.
func fixtureAccessACL(uid uint32) []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 2)
	for _, entry := range [][3]uint32{{1, 7, 0xffffffff}, {2, 4, uid}, {4, 5, 0xffffffff}, {16, 5, 0xffffffff}, {32, 0, 0xffffffff}} {
		item := make([]byte, 8)
		binary.LittleEndian.PutUint16(item, uint16(entry[0]))
		binary.LittleEndian.PutUint16(item[2:], uint16(entry[1]))
		binary.LittleEndian.PutUint32(item[4:], entry[2])
		data = append(data, item...)
	}
	return data
}

func TestInstallBinaryPreservesOwnerGroupAndACL(t *testing.T) {
	dir := t.TempDir()
	target, source := filepath.Join(dir, "npc"), filepath.Join(dir, "new")
	if err := os.WriteFile(target, []byte("old"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	acl := fixtureAccessACL(uint32(os.Getuid() + 1))
	if err := unix.Setxattr(target, "system.posix_acl_access", acl, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem does not support POSIX ACL")
		}
		t.Fatal(err)
	}
	original, _ := os.Stat(target)
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
		actual, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		before, after := original.Sys().(*syscall.Stat_t), actual.Sys().(*syscall.Stat_t)
		if before.Uid != after.Uid || before.Gid != after.Gid {
			t.Fatal(step, "changed owner/group")
		}
		value := make([]byte, 1024)
		n, err := unix.Getxattr(target, "system.posix_acl_access", value)
		if err != nil || !bytes.Equal(acl, value[:n]) {
			t.Fatal(step, "changed access ACL", err)
		}
	}
}

func TestInstallBinaryDoesNotAcquireDirectoryDefaultACL(t *testing.T) {
	dir := t.TempDir()
	acl := fixtureAccessACL(uint32(os.Getuid() + 1))
	if err := unix.Setxattr(dir, "system.posix_acl_default", acl, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem does not support POSIX ACL")
		}
		t.Fatal(err)
	}
	target, source := filepath.Join(dir, "npc"), filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Removexattr(target, "system.posix_acl_access"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	tx, err := InstallBinary(source, target)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Commit()
	if _, err := unix.Getxattr(target, "system.posix_acl_access", nil); !errors.Is(err, unix.ENODATA) {
		t.Fatal("new program acquired default ACL", err)
	}
}
