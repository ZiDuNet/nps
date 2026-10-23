package instancekey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateAndDeriveKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.key")
	one, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	two, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate(existing) error = %v", err)
	}
	first, err := one.DeriveKey("web-session", 32)
	if err != nil {
		t.Fatalf("DeriveKey() error = %v", err)
	}
	second, err := two.DeriveKey("web-session", 32)
	if err != nil {
		t.Fatalf("DeriveKey(existing) error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("derived key changed after reloading instance key")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("instance key permissions are too broad: %v %v", info, err)
	}
}

func TestLoadOrCreateRejectsCorruptExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.key")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("corrupt existing key was accepted")
	}
}
