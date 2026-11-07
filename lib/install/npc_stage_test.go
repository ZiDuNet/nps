package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/version"
)

type updateRoundTrip func(*http.Request) (*http.Response, error)

func (f updateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareNpcUpdatePinnedAndVerified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses POSIX shell")
	}
	for _, test := range []struct {
		name, checksum, binaryVersion string
		fail                          bool
	}{
		{"valid", "", "v99.0.0", false}, {"checksum rejected", strings.Repeat("0", 64), "v99.0.0", true}, {"version rejected", "", "v98.0.0", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			binary := []byte("#!/bin/sh\necho 'Version: " + test.binaryVersion + "'\n")
			if err := tw.WriteHeader(&tar.Header{Name: "npc", Size: int64(len(binary)), Mode: 0755}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write(binary)
			_ = tw.Close()
			_ = gz.Close()
			digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if test.checksum != "" {
				digest = test.checksum
			}
			assetName := releaseAssetName(runtime.GOOS, runtime.GOARCH, "client")
			seenPinned := false
			client := &http.Client{Transport: updateRoundTrip(func(r *http.Request) (*http.Response, error) {
				var data []byte
				switch r.URL.Path {
				case "/repos/" + version.GitHubRepository + "/releases/tags/v99.0.0":
					seenPinned = true
					data, _ = json.Marshal(release{TagName: "v99.0.0", Assets: []releaseAsset{{Name: assetName, BrowserDownloadURL: "https://fixture.test/pkg"}, {Name: "checksums.txt", BrowserDownloadURL: "https://fixture.test/sums"}}})
				case "/sums":
					data = []byte(digest + "  " + assetName + "\n")
				case "/pkg":
					data = archive.Bytes()
				default:
					return nil, fmt.Errorf("unexpected request: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
			})}
			var stages []string
			pkg, err := PrepareNpcUpdate("99.0.0", false, client, func(stage, _, _ string) { stages = append(stages, stage) })
			if test.fail {
				if err == nil || pkg != nil {
					t.Fatalf("unverified update accepted: %v %v", pkg, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer pkg.Cleanup()
			if !seenPinned || pkg.Version != "v99.0.0" || len(stages) < 4 {
				t.Fatalf("pkg=%+v stages=%v pinned=%v", pkg, stages, seenPinned)
			}
		})
	}
}

func TestInstallBinaryTransactionAndLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "npc")
	source := filepath.Join(dir, "new")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireUpdateLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := AcquireUpdateLock(target); err == nil {
		other()
		t.Fatal("concurrent installation lock accepted")
	}
	release()
	release, err = AcquireUpdateLock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	tx, err := InstallBinary(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old" {
		t.Fatalf("rollback data=%q", data)
	}
	if _, err = os.Stat(tx.Backup); !os.IsNotExist(err) {
		t.Fatalf("backup left after rollback: %v", err)
	}
	if _, err = InstallBinary(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("missing source accepted")
	}
	data, _ = os.ReadFile(target)
	if string(data) != "old" {
		t.Fatal("failed install changed old file")
	}
}

func TestReceiptOnlyFinalizesMatchingScopedBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "npc")
	backup := filepath.Join(dir, ".npc-rollback-test")
	receipt := target + ".update-result.json"
	if err := os.WriteFile(backup, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	result := common.ClientUpdateProgress{JobID: "j", Stage: "restarted", Version: "v99.0.0"}
	if err := WriteExecUpdateReceipt(receipt, result, backup); err != nil {
		t.Fatal(err)
	}
	FinalizeExecUpdateReceipt(receipt, target, "98.0.0")
	if _, err := os.Stat(backup); err != nil {
		t.Fatal("other version removed backup", err)
	}
	FinalizeExecUpdateReceipt(receipt, target, "99.0.0")
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("matching update did not finalize backup")
	}
	outside := filepath.Join(t.TempDir(), ".npc-rollback-outside")
	_ = os.WriteFile(outside, []byte("keep"), 0600)
	_ = WriteExecUpdateReceipt(receipt, result, outside)
	FinalizeExecUpdateReceipt(receipt, target, "99.0.0")
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("unrelated backup removed")
	}
}

func TestInstallBinaryPreservesRestrictedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows access is controlled by ACLs")
	}
	for _, mode := range []os.FileMode{0700, 0750} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dir := t.TempDir()
			target, source := filepath.Join(dir, "npc"), filepath.Join(dir, "new")
			if err := os.WriteFile(target, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("new"), 0755); err != nil {
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
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Fatalf("%s: mode=%v want=%v", step, info.Mode().Perm(), mode)
				}
			}
		})
	}
}
